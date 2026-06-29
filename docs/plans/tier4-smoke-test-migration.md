# Tier 4 — Migrate Smoke Tests from Playwright to Go Integration Tests

> **Audience:** Sonnet in a new session.
> **Goal:** Migrate ~31 smoke-level Playwright tests to Go integration tests against real DGraph, freeing Playwright capacity for actual workflow tests and shortening the local test cycle by ~4–7 minutes.
> **Estimated effort:** Half-day (4–6 hours).
> **Builds on:** `docs/plans/e2e-test-coverage-improvements.md` (Tiers 1–3). Read that first for context — this plan is Tier 4 expanded.
> **Read first:** `CLAUDE.md`, `internal/handler/datacenter_test.go`, `internal/handler/backup_integration_test.go`.

---

## Context

### Why this work exists

Daniel's demo-prep pain was post-refactor regressions in basic UI flows. The deeper diagnosis (from the parent plan): Playwright is over-used for assertions that only check *"element X exists in the rendered HTML."* Those are smoke tests. They:

- Cost ~5–15 seconds each (browser startup + page load)
- Don't catch interaction bugs (they only verify initial render)
- Make `make test-e2e` slow enough that engineers skip it locally

Equivalent assertions in Go integration tests:
- Cost ~100–500 ms each (real DGraph query, no browser)
- Catch the same render-time regressions
- Plus catch DGraph-schema-handler-template misalignment (mocked tests miss this; real-DGraph tests catch it)

**The migration is net-positive on coverage AND speed.**

### Success criteria

- All ~31 smoke tests migrated to Go integration tests against real DGraph
- The corresponding Playwright tests deleted (not duplicated)
- `restore.spec.ts` deleted entirely (entire file was smoke)
- `make test-integration` passes
- `make test-e2e` still passes (the remaining workflow tests untouched)
- Wall-clock reduction in `make test-e2e` measurably ~4–7 minutes
- No coverage regressions (each Playwright assertion has a Go equivalent)

### CI gating context

Tests are **not** gated on PRs currently. The success metric is **faster local feedback for UI work**, not faster CI. Frame any commit-message / PR-description language accordingly.

---

## Decisions locked in (don't re-litigate)

| Decision | Choice | Implication |
|---|---|---|
| Granularity | **Consolidated** | One `Test<Page>Page_RendersExpectedElements` per page, multiple `require.Contains` assertions inside |
| Assertion style | **Substring** | `require.Contains(t, html, "...")` — simple, upgrade to goquery later if brittle |
| DB approach | **Real DGraph** | New tests are **integration tests** (`*_integration_test.go`), run via `make test-integration`, assume `make up` is running |
| File naming | **Extend existing** | Add tests to existing `*_integration_test.go` where present; create new `*_integration_test.go` files only when none exists |
| Scope | **Full sweep** | All ~31 candidate tests across 5 spec files |

---

## Reference materials

Read these BEFORE writing any tests:

| Why | Path |
|---|---|
| Project conventions, test commands, run flow | `CLAUDE.md` |
| Existing render-test pattern (HTMX vs non-HTMX, redirect behavior) | `internal/handler/datacenter_test.go` — specifically `TestDataCenterTab_Success`, `TestDataCenterTab_NonHTMX_Redirects` |
| Existing integration-test scaffolding (real DGraph) | `internal/handler/backup_integration_test.go` |
| Existing integration-test auth pattern (`c.Set` for user_id / user_email) | `internal/handler/authz_integration_test.go` |
| Parent plan with Tiers 1–3 | `docs/plans/e2e-test-coverage-improvements.md` |
| Playwright tests being migrated | `e2e/backups.spec.ts`, `e2e/export.spec.ts`, `e2e/restore.spec.ts`, `e2e/datacenter.spec.ts`, `e2e/orb.spec.ts` |

---

## The migration pattern (worked example)

Use this as the template for every migration. Reading `internal/handler/datacenter_test.go` for the actual idioms is mandatory — these snippets are illustrative.

### Source: `e2e/backups.spec.ts` — 6 smoke tests

```typescript
test('shows heading and subtitle', async ({ page }) => {
  await page.goto('/backups');
  await expect(page.locator('h1')).toContainText('Backups');
  await expect(page.locator('.subtitle')).toContainText('object storage');
});
test('backup table is present', async ({ page }) => {
  await page.goto('/backups');
  await expect(page.locator('#backup-history-table')).toBeVisible();
});
// ... etc
```

### Target: `internal/handler/backup_integration_test.go` — one consolidated test

```go
// TestBackupsPage_RendersExpectedElements pins the static surface of the
// backups page: heading, subtitle, table, action buttons, and modal initial
// state. Migrates the 6 smoke tests previously in e2e/backups.spec.ts.
func TestBackupsPage_RendersExpectedElements(t *testing.T) {
    // Use existing integration-test scaffolding — see backup_integration_test.go
    // for the canonical setup. Typically: real DGraph + ent + Echo router.
    srv := newIntegrationServer(t) // existing helper or equivalent

    req := httptest.NewRequest(http.MethodGet, "/backups", nil)
    rec := httptest.NewRecorder()
    c := srv.echo.NewContext(req, rec)

    // Auth: bypass via context values (match existing pattern in authz_integration_test.go)
    c.Set("user_id", srv.adminUserID)
    c.Set("user_email", "admin@armada.ai")

    require.NoError(t, srv.backupHandler.Backups(c))
    require.Equal(t, http.StatusOK, rec.Code)

    html := rec.Body.String()

    // Heading + subtitle
    require.Contains(t, html, "<h1", "h1 element missing")
    require.Contains(t, html, "Backups", "heading text missing")
    require.Contains(t, html, "object storage", "subtitle text missing")

    // Backup history table
    require.Contains(t, html, `id="backup-history-table"`, "backup history table missing")

    // Action buttons (assumes S3 configured in test fixtures)
    require.Contains(t, html, `id="btn-backup-now"`, "Backup Now button missing")
    require.Contains(t, html, `id="btn-test-connection"`, "Test Connection button missing")

    // Storage location section (configured branch)
    require.Contains(t, html, "Location", "storage location field missing")

    // Delete modal: present but NOT active on load
    require.Contains(t, html, `id="delete-backup-modal"`, "delete modal element missing")
    require.NotContains(t, html, `id="delete-backup-modal" class="modal is-active"`,
        "delete modal should not be active on initial load")
}
```

### Things this example shows
- **One test function** asserts the page's entire static surface (decision: consolidated).
- **Substring assertions** with descriptive failure messages (decision: substring).
- **Real DGraph** via `newIntegrationServer` (decision: real DGraph). Sonnet must find the existing helper in `backup_integration_test.go` or equivalent — DO NOT invent a new one.
- **Auth bypass** via `c.Set` (matches existing handler-test pattern).
- **Test placement**: in `backup_integration_test.go` (decision: extend existing).
- **Selector strategy**: prefer ID-based assertions (`id="backup-history-table"`) over text-based — IDs change less often than copy.

### What to do if existing scaffolding helpers don't quite fit

Check `backup_integration_test.go` for whether there's a `newIntegrationServer(t)`, `setupIntegrationTest(t)`, or similar. If yes, use it. If no, the existing integration tests construct things manually — replicate that approach. **Do not invent a new scaffolding pattern.** Match what's there.

---

## Per-file migration tasks

For each spec file: which target Go file, which assertions to migrate, and gotchas specific to that page.

### 1. `e2e/backups.spec.ts` → extend `internal/handler/backup_integration_test.go`

**6 smoke tests → 1 consolidated `TestBackupsPage_RendersExpectedElements`.**

| Playwright assertion | Go equivalent (substring check) |
|---|---|
| `'shows heading and subtitle'` | `html` contains `<h1>...Backups...</h1>` and `object storage` |
| `'shows storage location (configured or not)'` | `html` contains `Location` (or the not-configured warning, depending on test config) |
| `'backup table is present'` | `html` contains `id="backup-history-table"` |
| `'Backup Now button is present when S3 is configured'` | `html` contains `id="btn-backup-now"` (assumes S3 configured in test) |
| `'Test Connection button is present when S3 is configured'` | `html` contains `id="btn-test-connection"` |
| `'delete modal is hidden on load'` | `html` contains delete-modal element but NOT with `is-active` class |

**Gotcha:** Some assertions are conditional on S3 being configured. Either:
- Ensure the test fixture has S3 env vars set (check `backup_integration_test.go` for whether it already does)
- Or add an explicit setup step in the new test

**Keep in Playwright:** `'triggering a backup creates a job entry that reaches a terminal state'` — workflow test.

---

### 2. `e2e/export.spec.ts` → extend `internal/handler/export_integration_test.go`

**6 smoke tests → 1 consolidated `TestExportPage_RendersExpectedElements`.**

| Playwright assertion | Go equivalent |
|---|---|
| `'shows heading and description'` | `html` contains `<h1>...Export...` and the description text |
| `'shows OCI registry (configured or not)'` | `html` contains OCI registry URL or "not configured" warning |
| `'data center select loads with options from seeded data'` | `html` contains `<option value="seattle:seattle-galleon">` (or another seeded DC) — **requires real DGraph with seeded data** |
| `'export button is disabled until a data center is selected'` | `html` contains `<button ... disabled` for the export button (initial state) |
| `'export status box is hidden on load'` | `html` contains the status-box element with `style="display:none"` or `hidden` or `class="... is-hidden"` |
| `'export jobs table is present'` | `html` contains `id="export-jobs-table"` (or equivalent) |

**Gotcha:** The "export button disabled" assertion checks the INITIAL HTML state. JS toggles it on selection — that's not testable in a server-render test. That's fine; the initial-disabled state is what we're verifying.

**Gotcha:** Find the exact selectors by reading `web/templates/orbital/pages/export.gohtml`. Don't guess IDs.

**Keep in Playwright:** `'triggering an export creates a completed job with a download link'` — workflow.

---

### 3. `e2e/restore.spec.ts` → create `internal/handler/restore_integration_test.go`

**5 smoke tests → 1 consolidated `TestRestorePage_RendersExpectedElements`. Entire file migrates — `restore.spec.ts` is deleted after.**

| Playwright assertion | Go equivalent |
|---|---|
| `'shows heading'` | `html` contains `<h1>...Restore...</h1>` |
| `'shows destructive operation warning'` | `html` contains the warning text |
| `'restore history table is present'` | `html` contains `id="restore-history-table"` |
| `'local file runbook section is visible'` | `html` contains the runbook section identifier |
| `'restore log modal is hidden on load'` | `html` contains restore-log-modal element but NOT with `is-active` class |

**File creation:** This is a NEW file. Use `backup_integration_test.go` as template for the scaffolding (real DGraph + integration-test conventions).

**After migration:** `git rm e2e/restore.spec.ts`. The file has zero workflow tests, so it goes away entirely.

---

### 4. `e2e/datacenter.spec.ts` → extend `internal/handler/datacenter_test.go` (or create `datacenter_integration_test.go`)

**2 smoke tests migrate. Don't touch the 4 interactive tests.**

| Playwright assertion | Go equivalent |
|---|---|
| `'menu footer shows app version'` | `html` contains the rendered version string (check what `internal/version.Version` is set to in tests; default may be `"dev"`) |
| `'data center summary shows correct metadata'` | Load DGraph-seeded datacenter; `html` contains DC name + namespace + other expected metadata |

**Naming decision:** `datacenter_test.go` exists but is not `_integration_test.go`. Per the convention chosen, integration tests against real DGraph go in `*_integration_test.go` files. **Create `internal/handler/datacenter_integration_test.go`** for these. Leave `datacenter_test.go` alone.

**Keep in Playwright:** the tab-switching, reload-button, and any other interactive tests in `datacenter.spec.ts`.

---

### 5. `e2e/orb.spec.ts` → extend or create `internal/orbserver/orb_render_integration_test.go`

**Most tests in orb.spec.ts migrate. The API tests do not.**

orb tests live in `internal/orbserver/` (not `internal/handler/`). Check whether there's an existing `*_integration_test.go` in `internal/orbserver/`. If yes, extend it. If no, create `internal/orbserver/orb_render_integration_test.go`.

| Playwright assertion | Go equivalent |
|---|---|
| `${path} loads without error` (parameterized over orb paths) | One test per path OR a table-driven test asserting `http.StatusOK` for each |
| `'orb sidebar shows Orb menu section, not orbital sections'` | `html` contains orb menu items, does NOT contain orbital-only menu items |
| `'orb navbar shows "Orb" brand'` | `html` contains the orb brand string |
| `'orb pages have no edit or delete buttons'` | For each page tested, `html` does NOT contain edit/delete button text or selectors |
| `'orb app version badge is visible'` | `html` contains version badge element |
| `'datacenter tab fragment renders populated data'` | HX-Request fragment: send request with `HX-Request: true` header, assert seeded DC data in response |
| `'cluster tab fragment renders populated data'` | Same pattern for cluster |
| `'server tab fragment renders populated data'` | Same pattern for server |
| `'orb cluster tab has no Edit / Delete controls'` | `html` does NOT contain edit/delete selectors |
| `'import page › tags table has correct column headers'` | `html` contains the column header strings |
| `'import page › courier section has file input and disabled upload button'` | `html` contains file input element AND upload button with `disabled` attribute |
| `'import page › refresh and import latest buttons are present'` | `html` contains both button IDs |

**Gotcha:** orb has its own DGraph instance (per memory `project_orb_dgraph_backend.md`). The integration test scaffolding for orb may differ from orbital's. Check existing orb tests in `internal/orbserver/` to find the pattern.

**Gotcha:** The HX-Request fragment tests need the header set explicitly:
```go
req := httptest.NewRequest(http.MethodGet, "/datacenter/...", nil)
req.Header.Set("HX-Request", "true")
```

**Keep in Playwright:**
- API tests: `'import tags API › response has tags array'`, `'import tags API › does not return .sig tags'`, `'import tags API › tag objects have expected shape'`, `'import history API › response is an array'`, `'import history API › records include verification field'` — these test JSON API contract, which is fine to keep as is (they're fast in Playwright since they're API tests, not browser tests).

---

## Test setup considerations

### Auth bypass

Use the existing pattern (from `authz_integration_test.go`):

```go
c.Set("user_id", userID)
c.Set("user_email", "admin@armada.ai")
```

If a user record needs to exist in Postgres for the handler to work (e.g., RBAC role lookup), seed an admin user in the integration setup. Existing helpers in the integration tests already handle this — find and use them.

### DGraph seeded state

Tests rely on `make seed` having been run. The existing integration tests assume this. Don't re-seed inside individual tests unless that's already the established pattern. If a test needs specific data not in the standard seed, either:

- Add it to the seed scripts (heavyweight — affects all tests)
- Or insert it directly in the test setup and clean up after (preferred for one-off data)

### S3 / OCI registry configuration

Some tests assert "S3 configured" branches. Find how `backup_integration_test.go` handles this — it likely sets env vars at test time or uses a test config struct. Replicate.

### Echo router setup

Don't construct routes by hand. Use whatever helper the existing integration tests use to get a configured Echo instance with all middleware registered. Calling individual handlers directly (as in the worked example above) works for unit tests but may miss middleware effects.

---

## Validation

### Per-file (after each migration)

```bash
# 1. Run the new Go test
go test ./internal/handler/... -run TestBackupsPage_RendersExpectedElements -v

# 2. Run the still-running Playwright workflow tests in that spec
make test-e2e -- --grep "Backup workflow"

# 3. If both pass, delete the migrated Playwright tests
# Edit e2e/backups.spec.ts to remove the 6 smoke `test()` blocks

# 4. Re-run Playwright to verify the spec still works with just workflow tests
make test-e2e -- e2e/backups.spec.ts
```

### After full sweep

```bash
# All integration tests pass
make test-integration

# Remaining Playwright tests still pass
make test-e2e

# Measure wall-clock improvement (capture BEFORE if you can — even rough)
time make test-e2e
```

### Spot-check: did the test actually catch what it should?

For each migrated test, do ONE intentional break to verify the test is real:

```bash
# Example: break the backup history table ID in the template
sed -i.bak 's/id="backup-history-table"/id="backup-history-table-BROKEN"/' web/templates/orbital/pages/backups.gohtml

# Run the test — should FAIL
go test ./internal/handler/... -run TestBackupsPage_RendersExpectedElements -v

# Restore
mv web/templates/orbital/pages/backups.gohtml.bak web/templates/orbital/pages/backups.gohtml
```

Don't do this for ALL tests — just one or two as a sanity check. If the test passes when it shouldn't, the substring is too generic.

---

## Anti-patterns to avoid

- **Don't migrate workflow tests.** Anything that clicks a button, fills a form, or triggers a network mutation stays in Playwright. The migration target is initial-render assertions only.
- **Don't migrate API tests.** The `{ request }` fixture tests in `orb.spec.ts` test JSON contract — they're fast and useful. Leave them.
- **Don't migrate JS-dependent assertions naively.** *"Export button disabled until selection"* tests INITIAL state (testable) AND JS toggling (not testable in Go). Migrate the initial state only. The JS-toggling behavior should remain as a Playwright workflow test if it has one — otherwise add it during a future Tier (not this one).
- **Don't replicate Playwright 1:1.** The whole point of Decision B (consolidated) is fewer test functions. One test per page asserts the whole static surface.
- **Don't invent new test scaffolding.** Use what `backup_integration_test.go` and `datacenter_test.go` already establish. If a pattern doesn't fit, FLAG IT in the PR description — don't silently fork the convention.
- **Don't use `time.Sleep`.** If a test ever needs to wait for something, the scaffolding is wrong.
- **Don't add tests for features the parent plan covers.** The reload-button regression class is Tier 1's job, not Tier 4's. Don't write a "reload button works" Go test here — it belongs in Playwright per the parent plan.
- **Don't commit.** Leave the diff for Daniel to review.

---

## Order of execution

Suggested order (lowest risk first, so a structural problem surfaces early):

1. **`restore.spec.ts` → `restore_integration_test.go`** (NEW file, 5 tests). Entire file migrates and goes away. No mixed-coverage decisions. Smallest, simplest. **Do this first** — proves the pattern.
2. **`backups.spec.ts` → `backup_integration_test.go`** (extend, 6 tests). Existing integration-test file makes scaffolding reuse natural.
3. **`export.spec.ts` → `export_integration_test.go`** (extend, 6 tests). Similar shape to backups.
4. **`datacenter.spec.ts` → `datacenter_integration_test.go`** (NEW file, 2 tests). Small. DGraph-data assertions.
5. **`orb.spec.ts` → `internal/orbserver/orb_render_integration_test.go`** (NEW file or extend, ~12 tests). Different package, different scaffolding. Save for last — most architecturally distinct.

After each file, run the per-file validation (above) before moving on.

---

## What to report back when done

In a PR description / hand-off summary:

- **Migration summary**: list of Go test functions added, Playwright tests deleted, files created/deleted (especially `e2e/restore.spec.ts` removal).
- **Test counts**: before/after for both Playwright and Go integration. Expect ~31 fewer Playwright tests, ~5 new consolidated Go tests.
- **Wall-clock comparison**: rough `time make test-e2e` before and after. Even an approximate "5min → 1min" is useful.
- **Sanity-check verification**: confirm you ran the intentional-break test on at least one migrated test and the Go test caught the break.
- **Anything not migrated and why**: if a Playwright test seemed smoke-y but actually needed JS, flag it. We may want it covered differently in a follow-up.

Don't commit. Leave the diff for review.

---

## Clean-up after execution

This plan file lives at `docs/plans/tier4-smoke-test-migration.md`. Per repo convention, **delete this file (and `docs/plans/e2e-test-coverage-improvements.md`) once the work is merged.** The durable artifact is the tests themselves. The plans were scaffolding for execution.
