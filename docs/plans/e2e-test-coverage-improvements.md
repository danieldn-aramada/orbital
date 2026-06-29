# E2E Test Coverage Improvements

> **Audience:** Sonnet in a new session.
> **Goal:** Close the regression-catching gap in Orbital's UI test suite — specifically, post-refactor regressions in reload buttons, HTMX swap handler re-binding, and cross-tab data freshness.
> **Estimated effort:** Tier 1–3 ≈ 2–3 hours. Tier 4 ≈ half-day.
> **Read first:** `CLAUDE.md` (project overview, testing conventions), `e2e/datacenter.spec.ts` (canonical template — mimic this style).

---

## Why this work exists

### The diagnosis

Daniel hit demo-prep pain catching basic regressions: reload buttons silently broken after a refactor, edit-server-then-audit-log flows broken, and sub-tab clicks dead after HTMX content swaps. These are exactly the regression classes e2e tests should catch — but most slipped through.

Survey of the current state:

| Layer | Count | Quality |
|---|---|---|
| **Go unit/integration** (`internal/**/_test.go`) | ~55 files | Genuinely thorough — audit log filtering (8 tests), divergence MVCC, export pipeline, backup pipeline. Not theater. |
| **Playwright e2e** (`e2e/*.spec.ts`) | 13 spec files | Most files = 5 smoke tests + 1 workflow test. Smoke tests assert "page renders" / "button is present" — they don't catch interaction bugs. |

**The asymmetry IS the problem.** Backend invariants are pinned. UI interactions are mostly unverified beyond "page loads."

### Specific gap evidence

| Regression class | Currently covered? | Where |
|---|---|---|
| Reload button works on **datacenter** page | ✅ Yes | `datacenter.spec.ts` — `'reload button refreshes content and inner tabs still work'` |
| Reload button works on **backups** page | ❌ No | `backups.spec.ts` checks button is *present*, never clicks it |
| Reload button works on **divergence reports** | ❌ No | No spec file for divergence reports |
| Reload button works on **cluster detail** | ❌ No | `cluster-delete.spec.ts` tests delete only |
| Reload button works on **orb import history** | ❌ No | `orb.spec.ts` checks button presence only |
| Edit server → audit log shows row | ✅ Yes (good!) | `configitem-editor.spec.ts` — multiple mutation→audit tests |
| Tab restored after edit-modal close | ✅ Narrow | `server-edit-tab-restore.spec.ts` |
| Cross-tab consistency (open 2 browser tabs) | ❌ No | Zero use of `browser.newContext()` / `context.newPage()` anywhere |
| HTMX afterSwap handlers re-bind correctly | ❌ Not directly | No test asserts post-swap interactivity |
| DataTables re-init after fragment swap | ❌ Not directly | Recurring footgun per memory; not pinned |

**1 of ~7 reload buttons has a test.** The rest are an open regression class.

### What success looks like

- Every page with a reload button has a test that clicks it and asserts the swap landed
- One representative test pins the "HTMX afterSwap re-binding" regression class
- One test covers cross-tab freshness
- Smoke-level Playwright tests (asserting only that elements exist on first load) are migrated to faster Go handler tests, freeing Playwright capacity for real flows

---

## Tier 1 — Reload button regression class (~1 hour)

The cheapest, highest-leverage win. Mimic the existing datacenter test for every page that has a reload button.

### Canonical template to read first

**Read `e2e/datacenter.spec.ts` test `'reload button refreshes content and inner tabs still work'` before writing any new tests.** That's the canonical pattern. Match its structure.

### Per-page test additions

| File | New test | What to assert |
|---|---|---|
| `e2e/backups.spec.ts` | `'reload button refreshes backup history table'` | Click reload → backup history table is still present → row count is unchanged (or use HTMX network event to confirm swap completed) |
| `e2e/divergence-reports.spec.ts` *(NEW file)* | `'reload button refreshes divergence report list'` | Same pattern, against `/divergence-reports` |
| `e2e/cluster-detail.spec.ts` *(NEW file)* | `'reload button refreshes content and sub-tabs still work'` | Open a cluster (use a seeded cluster orbId), click reload, then click Nodes sub-tab and assert content appears |
| `e2e/orb.spec.ts` | `'import history reload button refreshes table'` | On orb's `/import` page (port 8010), click reload, assert import history table still present |

### How to find the reload button selectors

For each page, grep the template:

```bash
grep -rn "btn-refresh\|btn-reload\|fa-rotate\|hx-get.*reload" web/templates/orbital/pages/<page>.gohtml
```

Reload buttons typically have IDs like `id="btn-refresh-divergence"`, `id="btn-refresh-backups"`, etc. Find the actual selector before writing the test — don't guess.

### What "swap completed" looks like in HTMX

The button has `hx-get` (or `hx-post`) + `hx-target` + `hx-swap`. After the click, the target element's innerHTML is replaced. Assert:

```typescript
await page.click('#btn-refresh-divergence');
// Wait for HTMX to complete the swap — content present, no spinner
await expect(page.locator('#divergence-content table')).toBeVisible();
await expect(page.locator('.htmx-request')).toHaveCount(0); // no in-flight requests
```

The `.htmx-request` class is added by HTMX during in-flight requests and removed when complete. If the swap fails silently, the table won't appear OR `.htmx-request` will linger.

### Validation for Tier 1

```bash
make test-e2e
```

All new reload tests should pass. To verify they actually catch regressions: temporarily break one (change the button's `hx-get` URL to a non-existent route) and confirm the test fails.

---

## Tier 2 — Post-HTMX-swap interaction (~30 min)

### The bug class

After an HTMX swap replaces content, JavaScript event handlers attached to the original DOM elements no longer exist. If the new content depends on `addEventListener` that was set up at page load, those handlers are dead. This is the *"refactor broke afterSwap handlers"* class.

Orbital's pattern (per memory `feedback_htmx_datatables_init`): there are two `htmx:afterSwap` listeners; handlers must be re-registered after swap.

### Test to add

Add to `e2e/datacenter.spec.ts`:

```typescript
test('sub-tab clicks still work after content reload', async ({ page }) => {
  // Load a seeded datacenter
  await page.goto('/datacenter/seattle:seattle-galleon');
  await expect(page.locator('table')).toBeVisible();

  // Trigger a content reload via the reload button (HTMX swap)
  await page.click('#btn-refresh-datacenter'); // adjust selector to actual ID
  await expect(page.locator('.htmx-request')).toHaveCount(0);

  // Click a sub-tab — handler must have re-bound
  await page.click('[data-tab="racks"]'); // adjust selector
  await expect(page.locator('#racks-tab .table tbody tr').first()).toBeVisible();

  // Click back to default sub-tab — same check
  await page.click('[data-tab="servers"]');
  await expect(page.locator('#servers-tab .table tbody tr').first()).toBeVisible();
});
```

### Why this catches the class

If `htmx:afterSwap` doesn't re-init tab event handlers, the `page.click('[data-tab="racks"]')` will fire but the click handler won't be attached → the test fails because `#racks-tab` never becomes visible.

### Validation for Tier 2

```bash
make test-e2e -- --grep "sub-tab clicks still work"
```

To verify it catches the bug class: temporarily comment out the `htmx:afterSwap` re-binding in `web/shared/static/orbital.js` (look for `document.body.addEventListener('htmx:afterSwap', ...)`) and confirm the test fails.

---

## Tier 3 — Cross-tab consistency (~30 min)

### The bug class

User opens orbital in two browser tabs. Edits a server in tab 2. Clicks reload in tab 1. Tab 1 should show the new value. If browser cache is stale or backend doesn't return fresh data, this breaks silently.

### Test to add

Add to `e2e/configitem-editor.spec.ts` (where related edit tests live):

```typescript
test('edit in one tab is visible after reload in another', async ({ browser }) => {
  // Two contexts sharing auth state (so both are logged in as the same user)
  const ctx = await browser.newContext({ storageState: 'e2e/.auth.json' });

  const tab1 = await ctx.newPage();
  const tab2 = await ctx.newPage();

  // Open inventory in tab 1
  await tab1.goto('/inventory');
  await expect(tab1.locator('table')).toBeVisible();

  // Find a seeded server's row, capture its current value (e.g., model column)
  const serverRowSelector = 'tr[data-server-orbid="seattle:JD268Y3"]';
  const originalModel = await tab1.locator(`${serverRowSelector} td.model`).textContent();

  // In tab 2, open the same server, edit its model, save
  await tab2.goto('/server/seattle:JD268Y3');
  await tab2.click('button:has-text("Edit")');
  await tab2.fill('input[name="model"]', 'PowerEdge R999');
  await tab2.click('button:has-text("Save")');
  await expect(tab2.locator('.modal.is-active')).toHaveCount(0); // modal closed

  // In tab 1, click reload, assert new value appears
  await tab1.click('#btn-refresh-inventory'); // adjust selector
  await expect(tab1.locator(`${serverRowSelector} td.model`)).toHaveText('PowerEdge R999');

  // Restore the original value so other tests aren't affected
  await tab2.click('button:has-text("Edit")');
  await tab2.fill('input[name="model"]', originalModel ?? 'PowerEdge R350');
  await tab2.click('button:has-text("Save")');

  await ctx.close();
});
```

### Notes

- Use a seeded server with a stable `orbId` (check `examples/seed/seattle-galleon.graphql`)
- Adjust selectors to match the actual orbital UI (inspect `web/templates/orbital/pages/inventory.gohtml`)
- The cleanup step is important — without it, this test mutates seeded state for subsequent tests
- If selectors don't match, **fix selectors first** rather than skipping — silent selector mismatches are how this kind of test rots

### Validation for Tier 3

```bash
make test-e2e -- --grep "edit in one tab"
```

To verify: temporarily make the server detail page return a stale cached value (or skip the mutation) and confirm the test fails.

---

## Tier 4 — Migrate smoke tests to Go handler tests (~half-day)

### The principle

Playwright is slow (~5–30 sec per test, depending on browser startup). Go handler tests are fast (~10–100 ms). For assertions that only check "the rendered HTML contains element X," a Go handler test does the same job ~100× faster.

**Smoke tests in Playwright are theater for performance.** Migrate them.

### Identifying smoke tests to migrate

A Playwright test is a "smoke test" if its assertions are only:
- `await expect(page.locator(X)).toBeVisible()`
- `await expect(page.locator(X)).toContainText(Y)`
- `await expect(page.locator(X)).toHaveCount(N)`

…and it doesn't click anything, fill any form, or trigger any network request beyond the initial page load. In other words: it asserts the page rendered correctly on first load, nothing more.

Candidates for migration (verify each by re-reading the test):

| Test file | Smoke tests to migrate |
|---|---|
| `e2e/backups.spec.ts` | `'shows heading and subtitle'`, `'shows storage location (configured or not)'`, `'backup table is present'`, `'Backup Now button is present when S3 is configured'`, `'Test Connection button is present when S3 is configured'`, `'delete modal is hidden on load'` |
| `e2e/export.spec.ts` | `'shows heading and description'`, `'shows OCI registry (configured or not)'`, `'data center select loads with options from seeded data'`, `'export button is disabled until a data center is selected'`, `'export status box is hidden on load'`, `'export jobs table is present'` |
| `e2e/restore.spec.ts` | All 5 tests — entire file is smoke (no workflow tests) |
| `e2e/datacenter.spec.ts` | `'menu footer shows app version'`, `'data center summary shows correct metadata'` |
| `e2e/orb.spec.ts` | `'orb sidebar shows Orb menu section'`, `'orb navbar shows "Orb" brand'`, `'orb pages have no edit or delete buttons'`, `'orb app version badge is visible'`, `'import page › tags table has correct column headers'`, `'import page › courier section has file input...'`, `'import page › refresh and import latest buttons are present'`, `'orb cluster tab has no Edit / Delete controls'` |

**Do NOT migrate** (these are workflow tests):
- `'triggering a backup creates a job entry that reaches a terminal state'` (backups)
- `'triggering an export creates a completed job with a download link'` (export)
- `'reload button refreshes content and inner tabs still work'` (datacenter)
- The 4 mutation→audit tests in `configitem-editor.spec.ts`
- `'audit subtab restores after edit-modal submit'` (server-edit-tab-restore)
- The Tier 1–3 tests you just added
- API tests in `orb.spec.ts` (the ones using `{ request }` fixture)

### Go handler test template

Create handler tests in `internal/handler/*_render_test.go` (new naming convention for HTML rendering tests):

```go
// internal/handler/backups_render_test.go
package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestBackupsPage_RendersExpectedElements(t *testing.T) {
	// Standard test scaffold — see existing _test.go files for setup patterns
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/backups", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Set up auth context (use the test helper that other handler tests use)
	c.Set("user_id", testUserID)
	c.Set("user_email", "admin@armada.ai")

	h := newTestHandler(t) // existing helper — see other handler tests
	require.NoError(t, h.Backups(c))

	require.Equal(t, http.StatusOK, rec.Code)
	html := rec.Body.String()

	// Migrate each Playwright smoke assertion as a substring check:
	require.Contains(t, html, "Backups", "heading missing")
	require.Contains(t, html, `id="backup-history-table"`, "backup table missing")
	require.Contains(t, html, `id="btn-backup-now"`, "backup-now button missing")
	require.Contains(t, html, `id="btn-test-connection"`, "test-connection button missing")
	require.False(t, strings.Contains(html, `class="modal is-active"`), "delete modal should be hidden on load")
}
```

### Migration procedure (per Playwright test being migrated)

1. **Read the existing Playwright test.** Identify each assertion.
2. **Find the existing Go test for the same handler.** (Look in `internal/handler/<feature>_test.go`.)
3. **Add a new test function** named `Test<Page>Page_RendersExpectedElements` (or extend an existing render test).
4. **Translate each assertion** to a `require.Contains(t, html, ...)` check.
5. **Run the new Go test** with `go test ./internal/handler/... -run Test<Page>Page_RendersExpectedElements -v`.
6. **Once Go test passes**, delete the Playwright smoke test from the spec file.
7. **Run** `make test-unit` and `make test-e2e` to confirm parity.

### Don't migrate naively — verify equivalence first

Some Playwright "smoke" tests actually exercise JavaScript:

- `'export button is disabled until a data center is selected'` — this depends on JS form-state logic. A Go handler test can verify the button starts disabled (`disabled` attribute in HTML), but it can't verify the JS toggles it on selection. **Keep the JS-interaction portion in Playwright; migrate only the initial-state assertion.**

When in doubt, leave a smoke test in Playwright. The goal is migrating *clearly-static* HTML assertions, not eviscerating Playwright.

### Validation for Tier 4

```bash
make test-unit            # all migrated Go tests pass
make test-e2e             # remaining Playwright suite is smaller, still passes
```

Compare wall-clock time of `make test-e2e` before and after. Should drop significantly (rough estimate: 30–50% if all candidate smoke tests are migrated).

---

## Anti-patterns to flag for Sonnet

### Don't touch the good stuff

- **Do NOT modify `e2e/configitem-editor.spec.ts`.** Those mutation→audit tests are the example of what good e2e looks like. They include the "updateIdracSettings NOT updateServer" assertion which pins a real regression class. Leave them alone.
- **Do NOT modify `e2e/server-edit-tab-restore.spec.ts`.** Single-purpose regression pin, working as designed.
- **Do NOT modify any of the Go integration tests in `internal/handler/*_integration_test.go`.** Those are the load-bearing backend invariant tests. Out of scope for this plan.

### Don't make these mistakes

- **Don't add smoke tests as Playwright tests.** If your new test only asserts "element exists," it belongs in a Go handler test (Tier 4 pattern).
- **Don't over-mock Go handler tests.** Use real `httptest` recorder, real handler. Mock only the DGraph/Postgres backends (and reuse existing test helpers).
- **Don't add `time.Sleep()` or `page.waitForTimeout()`.** Always use deterministic waits (`expect.toBeVisible()`, `expect.toHaveCount()`, `expect.not.toHaveClass('htmx-request')`).
- **Don't write integration tests you can't run locally.** Per `CLAUDE.md`: if a needed binary isn't on PATH (`dgraph live`, etc.), skip the test and note the gap in the PR description.
- **Don't duplicate coverage.** Before adding a test, grep for the same assertion in existing tests. If it exists, extend; don't add.

---

## Order of execution

Do these in order. Each tier is self-contained — you can stop after any tier and have value.

1. **Tier 1** (~1 hour) — 4 reload-button tests across 4 files. Single biggest win. Closes the regression class Daniel was hitting most.
2. **Tier 2** (~30 min) — 1 post-HTMX-swap interaction test in `datacenter.spec.ts`. Pins a recurring regression class.
3. **Tier 3** (~30 min) — 1 cross-tab consistency test in `configitem-editor.spec.ts`. Covers a real user scenario not currently tested.
4. **Tier 4** (~half-day) — Smoke-test migration. Optional if time runs out; the first three tiers are higher-leverage.

After each tier, run `make test-e2e` (and `make test-unit` after Tier 4) to confirm everything still passes.

---

## What to report back when done

A brief PR description / commit message should cover:

- **Tier 1**: List the 4 reload tests added (file + test name).
- **Tier 2**: Confirm the afterSwap re-binding test is in place.
- **Tier 3**: Confirm the cross-tab test is in place; note any selector adjustments needed.
- **Tier 4** (if done): List of migrated tests (Playwright deleted → Go added), wall-clock comparison of `make test-e2e` before/after.
- **Gaps you couldn't close**: Any tests you started but couldn't finish, with the blocker (selector not found, seed data missing, etc.).

Don't commit. Leave the changes for Daniel to review and commit.

---

## Reference materials Sonnet should consult before starting

| Why | Path |
|---|---|
| Project overview, testing philosophy | `CLAUDE.md` |
| Canonical e2e template — match this style | `e2e/datacenter.spec.ts` (specifically the reload-button test) |
| Good mutation→audit e2e example | `e2e/configitem-editor.spec.ts` |
| HTMX patterns | `docs/reference/UI.md` |
| Auth setup for tests | `e2e/global-setup.ts`, `e2e/.auth.json` |
| Test commands | `Makefile` — `test-unit`, `test-integration`, `test-e2e` |
| Memory notes on test patterns | `~/.claude/projects/-Users-daniel-armada-orbital/memory/feedback_tests_with_changes.md`, `feedback_validate_deployable_not_dev_paths.md`, `feedback_htmx_datatables_init.md` |

Read `CLAUDE.md` and `e2e/datacenter.spec.ts` before writing any tests. Don't skip these.

---

## Clean-up after execution

This plan file lives at `docs/plans/e2e-test-coverage-improvements.md`. Per repo convention (recent ROADMAP entry: *"six done plan docs deleted"*), **delete this file once the work is complete and merged.** It exists to enable Sonnet's execution, not as durable documentation. The durable artifacts are the tests themselves.
