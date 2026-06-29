# Tier 5 — UI Regression Coverage Expansion

> **Audience:** Sonnet in a new session.
> **Goal:** Close the highest-leverage UI regression gaps remaining after Tier 1–4. Three sub-tiers: edit-modal lifecycle for missing ConfigItem types, DataTable interaction tests, and empty/error state tests.
> **Estimated effort:** ~1.5 days. Each sub-tier is ~half-day.
> **Builds on:** `docs/plans/e2e-test-coverage-improvements.md` (Tiers 1–3), `docs/plans/tier4-smoke-test-migration.md` (Tier 4). Both already landed.
> **Read first:** `CLAUDE.md`, `e2e/configitem-editor.spec.ts` (canonical edit-modal pattern), `e2e/datacenter.spec.ts` (canonical reload/tab pattern).

---

## Context

### Why this work exists

Tier 1–4 closed the regression class Daniel felt during demo prep:
- Reload buttons work across all major pages (Tier 1)
- HTMX afterSwap handlers re-bind after content swap (Tier 2)
- Cross-browser-tab consistency (Tier 3)
- Smoke tests migrated to fast Go integration tests (Tier 4)

But the UI regression surface is bigger than that. Three classes remain genuinely uncovered:

1. **Edit-modal lifecycle for ConfigItem types other than the four already in `configitem-editor.spec.ts`** — DataCenter, Rack, IPAddress, KubernetesNode, ServerConfigurationProfile.
2. **DataTable interactions (sort, filter, expand, pagination)** — recurring footgun per memory `feedback_htmx_datatables_init`. Currently no tests pin these.
3. **Empty and error states** — what the UI shows when there's no data, or when an API fails. Templates that work with data often break with none.

Each is a real bug class Daniel could hit on the next refactor.

### Success criteria

- 5 new edit-modal lifecycle tests added (one per uncovered ConfigItem type)
- DataTable interaction tests for the 5 major tables (sort, filter, IPv4 sort, row expand where applicable)
- Empty-state tests for the major data-rendering pages
- Error-state tests for HTMX endpoints that show user-facing error UI
- All new tests pass against the running stack (`make up`)
- No existing tests broken
- Tests catch their intended regression class when the corresponding code is intentionally broken

---

## Decisions locked in

| Decision | Choice |
|---|---|
| **Test layer** | Playwright (these need real browser + JS execution) |
| **File organization** | 5A extends `configitem-editor.spec.ts`; 5B creates `datatable-interactions.spec.ts`; 5C creates `empty-and-error-states.spec.ts` |
| **Assertion style** | Behavior-focused: check that the user-visible result is correct, not implementation details |
| **Cleanup discipline** | Tests that mutate seed data MUST restore the original value at the end |
| **Selectors** | Prefer `data-testid`, `id`, or stable `data-*` attributes. Avoid CSS-class selectors that move with refactors |

---

## Reference materials

| Why | Path |
|---|---|
| Canonical edit-modal pattern (read first!) | `e2e/configitem-editor.spec.ts` — specifically the `server edit: model change` test |
| Canonical reload + tab pattern | `e2e/datacenter.spec.ts` |
| Existing audit log selectors and assertion shape | Same — search for `'updateServer'` and `audit-row` patterns |
| Seeded data for fixtures | `examples/seed/seattle-galleon.graphql`, `examples/seed/houston-galleon.graphql` |
| HTMX patterns and gotchas | `docs/reference/UI.md` |
| DataTables init pattern (Bulma + DataTables interop) | Memory: `feedback_htmx_datatables_init`, `feedback_datatables_bulma_alignment` |
| Project conventions | `CLAUDE.md` |

---

## Sub-tier 5A — Edit-modal lifecycle (~half-day)

### Pattern (copy from `configitem-editor.spec.ts`)

The existing tests follow this shape:

1. Navigate to the entity's detail page (or wherever the edit modal is triggered from)
2. Open the edit modal
3. Capture the original value of one field (for cleanup)
4. Change that field to a new value
5. Submit
6. Assert: modal closes, page UI updates, audit tab shows the new entry with the right mutation name and a diff containing old + new values
7. Cleanup: re-open modal, restore original value, submit

Mimic this shape exactly. The tests differ only in *which* entity is being edited and *which* field is changed.

### Tests to add to `e2e/configitem-editor.spec.ts`

Add inside the existing `test.describe('configitem-editor module — browser validation', () => { ... })` block.

| Entity | Field to change | Audit mutation expected | Seed data to target |
|---|---|---|---|
| **DataCenter** | `name` or `description` | `updateDataCenter` | `seattle:seattle-galleon` |
| **Rack** | `name` | `updateRack` | `seattle:Rack-5` (or any seeded rack) |
| **IPAddress** | `address` or a metadata field | `updateIPAddress` | One of the seeded IPs (`seattle:10.20.33.49` or similar) |
| **KubernetesNode** | `role` or `ipv4` | `updateKubernetesNode` | Any seeded node (check seed file) |
| **ServerConfigurationProfile** | A field inside the JSON editor — see below | `updateServerConfigurationProfile` | Any seeded profile |

### Test naming convention

Match the existing pattern:
```typescript
test('datacenter edit: name change → updateDataCenter audit row with diff', async ({ page }) => { ... })
test('rack edit: name change → updateRack audit row with diff', async ({ page }) => { ... })
test('ip address edit: address change → updateIPAddress audit row with diff', async ({ page }) => { ... })
test('kubernetes node edit: role change → updateKubernetesNode audit row with diff', async ({ page }) => { ... })
test('server config profile: JSON field change → updateServerConfigurationProfile audit row with diff', async ({ page }) => { ... })
```

### Per-test gotchas

- **DataCenter** — modal is reached from the DC detail page (`/datacenter/<orbId>`). Look for an Edit button.
- **Rack** — modal accessed from… likely the DC detail page's Racks tab, click into a rack. Verify the path.
- **IPAddress** — there may not be an IP detail page yet. Check if IPAddress editing is even reachable from the UI. If not, **flag this test as blocked and skip it** — note it in the PR description. Don't fabricate a UI.
- **KubernetesNode** — accessed from the cluster detail page's Nodes sub-tab.
- **ServerConfigurationProfile** — uses the **vanilla-jsoneditor** component (per memory: per-feature JS library). Interaction is different:
  - You can't just `page.fill()` — the editor manages its own DOM
  - Use the JSON editor's API if exposed, or interact with the underlying `<textarea>` if present
  - Worst case: switch the editor to "Text" / "Code" mode (the editor has a mode toggle), then fill the textarea, then submit
  - **If the JSON editor interaction proves brittle, flag it and skip — don't waste 2 hours debugging** the JSON editor's DOM. We can revisit.

### Cleanup discipline

Each test mutates real seeded data. **Each test MUST restore the original value before completing** so subsequent tests aren't poisoned. Use the existing pattern: capture original at the start, restore at the end inside a `try/finally` or as the last action.

If a test fails mid-flow and doesn't reach cleanup, that's an acceptable risk for these tests since failures will surface in CI. But the *happy path* always restores.

### Validation for 5A

```bash
make test-e2e -- e2e/configitem-editor.spec.ts
```

All new tests pass. Then do the intentional-break sanity check on ONE of them (e.g., temporarily change the audit log to record `updateServer` instead of `updateDataCenter` and verify the DC edit test now fails).

---

## Sub-tier 5B — DataTable interactions (~half-day)

### The bug class

DataTables initialization after HTMX fragment swap is a recurring footgun. Sort, filter, row expansion, pagination — any of these can break silently after a refactor (CSS class rename, JS module reorg, init order change).

### File to create

`e2e/datatable-interactions.spec.ts`. Use the same Playwright config as other specs (auth via `.auth.json`, etc.).

### Tables to cover

| Table | Page | Sort assertion | Filter assertion | Row expand? | Special concern |
|---|---|---|---|---|---|
| `#server-list-table` | `/inventory` | Sort by `serviceTag`, IPv4 column sorts numerically | Filter by serviceTag substring | No | **IPv4 sort regression class** per memory |
| Cluster list table | `/clusters` | Sort by `name` | Filter by name | Yes — expand to show workload children | Recently added (per ROADMAP `2026-06-18`); tree view expansion is a regression risk |
| Audit log table | `/audit` (or wherever audit is) | Sort by timestamp | Filter by operation name | No | Heavy use of `data-related-orb-ids` per memory |
| Divergence reports table | `/divergence-reports` | Sort by DC | Filter by DC | Yes — expand to show entries | Reference for canonical `<colgroup>` pattern per memory |
| DC detail server table | `/datacenter/<orbId>` (Servers tab) | Sort by serviceTag, IPv4 | Filter | No | Per-table `vertical-align: middle` override per memory |

### Test pattern

Template per table:

```typescript
test('inventory table: sorts by serviceTag column', async ({ page }) => {
  await page.goto('/inventory');
  await expect(page.locator('#server-list-table tbody tr').first()).toBeVisible();

  // Click the serviceTag column header
  await page.click('#server-list-table thead th[data-col="serviceTag"]');
  // Find the actual selector by inspecting the page's HTML — adjust if data-col isn't present

  // Collect all serviceTag cell values from the visible rows
  const tags = await page.locator('#server-list-table tbody td[data-col="serviceTag"]').allTextContents();

  // Assert sorted ascending
  const sorted = [...tags].sort();
  expect(tags).toEqual(sorted);
});

test('inventory table: filter narrows results', async ({ page }) => {
  await page.goto('/inventory');
  const initialCount = await page.locator('#server-list-table tbody tr').count();

  // DataTables filter input — selector depends on how DataTables is initialized
  await page.fill('.dataTables_filter input, input[type="search"]', 'JD268');

  // No timeout — DataTables filter is sync
  const filteredCount = await page.locator('#server-list-table tbody tr:visible').count();
  expect(filteredCount).toBeLessThan(initialCount);
  expect(filteredCount).toBeGreaterThan(0);

  // All visible rows contain the filter text
  const visibleTags = await page.locator('#server-list-table tbody tr:visible td[data-col="serviceTag"]').allTextContents();
  for (const tag of visibleTags) {
    expect(tag).toContain('JD268');
  }
});

test('inventory table: IPv4 column sorts numerically', async ({ page }) => {
  await page.goto('/inventory');
  await page.click('#server-list-table thead th[data-col="oobIP"]');

  const ips = await page.locator('#server-list-table tbody td[data-col="oobIP"]').allTextContents();
  // Filter to valid IPv4 strings to skip header / pagination rows
  const valid = ips.filter(s => /^\d+\.\d+\.\d+\.\d+$/.test(s));

  for (let i = 1; i < valid.length; i++) {
    expect(compareIPv4(valid[i - 1], valid[i])).toBeLessThanOrEqual(0);
  }
});

// Helper at top of file or in a shared utils file
function compareIPv4(a: string, b: string): number {
  const aParts = a.split('.').map(n => parseInt(n));
  const bParts = b.split('.').map(n => parseInt(n));
  for (let i = 0; i < 4; i++) {
    if (aParts[i] !== bParts[i]) return aParts[i] - bParts[i];
  }
  return 0;
}
```

### Coverage matrix per table

Aim for these tests per table (skip ones that don't apply):

| Test name template | Inventory | Cluster | Audit | Divergence | DC servers |
|---|---|---|---|---|---|
| `<table>: sorts by <primary-col>` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `<table>: filter narrows results` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `<table>: IPv4 column sorts numerically` | ✅ | — | — | — | ✅ |
| `<table>: row expand reveals child rows` | — | ✅ (workload children) | — | ✅ (entries) | — |
| `<table>: column header sort indicator updates` | optional | optional | optional | optional | optional |

**Approximate total: ~15 tests** across the 5 tables.

### Per-table gotchas

- **Inventory IPv4 sort** — per memory `feedback_table_column_widths` and the IPv4-render helper, this is a specific regression class. The test should use a known-stable IP set from seed data.
- **Cluster tree view** — workload clusters appear as child rows via `row.child().show()`. Test should click the chevron, then assert the child row is now visible.
- **Audit log** — relies on `data-related-orb-ids` attribute for parent audit aggregation. Worth a test asserting that opening a Server detail's audit tab includes events for owned-child IdracSettings (per memory `feedback_parent_audit_aggregation`).
- **Divergence reports** — uses `<colgroup>` + `table-layout: fixed`. Row expansion reveals individual divergence entries.
- **DC servers table** — may have a different DataTables init path than inventory (`vertical-align: middle` per-table override per memory).

### Validation for 5B

```bash
make test-e2e -- e2e/datatable-interactions.spec.ts
```

For the intentional-break check: pick the IPv4 sort test, temporarily remove the `dtIPv4Render`/`ipv4SortKey` helper invocation in the inventory template, run the test, expect failure. Restore.

---

## Sub-tier 5C — Empty + error states (~half-day)

### The bug class

Templates often work when there's data, but break when there's none (missing nil checks, broken `range`, expecting at least one row). Error states (5xx responses, network failures) often have placeholder text but no real graceful UI — and even when they do, they're rarely tested.

### File to create

`e2e/empty-and-error-states.spec.ts`.

### Tests to add

#### Empty states (no-data UI)

The approach: navigate to a page where the data source is genuinely empty. Two strategies:

- **Use an empty namespace.** If you have or can add a namespace with zero servers / zero clusters in the seed (e.g., a fresh DC with no Racks), navigate there.
- **Filter to an impossible value.** Apply a filter that matches nothing, assert the empty UI appears.

The second is simpler and doesn't require seed changes.

| Page | Empty-state trigger | Assertion |
|---|---|---|
| `/inventory` | Filter for a serviceTag that doesn't exist (e.g., `XXXXX-NO-MATCH`) | Empty state visible, OR "No matching records" text, OR `tbody` has 0 rows + visible no-results message |
| `/backups` | Requires DB state with no backup jobs. If seed has backups, fall back to filtering or skip. | Empty state text |
| `/divergence-reports` | If seeded with no divergence reports, navigate directly. Otherwise filter | "No divergence reports" |
| `/audit` | Filter by an orbId that doesn't exist | "No events" |
| `/clusters` | Filter for impossible name | "No matching clusters" |

If any empty state isn't possible to reach without seed changes, **flag and skip**. Don't modify the seed.

#### Error states (API failure UI)

Use Playwright's `page.route()` to intercept network calls and return errors:

```typescript
test('inventory page: shows error UI when API returns 500', async ({ page }) => {
  await page.route('**/api/v1/inventory*', (route) => {
    route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"internal"}' });
  });

  await page.goto('/inventory');

  // Assert error UI appears. Selector depends on the orbital error pattern —
  // search for `.notification.is-danger`, `#error-banner`, etc.
  await expect(page.locator('.notification.is-danger, #error-banner')).toBeVisible();
});
```

| Endpoint to mock | Page | Expected error UI |
|---|---|---|
| `/api/v1/inventory` returns 500 | `/inventory` | Error banner / notification |
| `/api/v1/divergences` returns 500 | `/divergence-reports` | Error banner |
| `/api/v1/audit` returns 500 | Audit page | Error banner |
| `/api/v1/backups` returns 500 | `/backups` | Error banner |
| HTMX endpoint (e.g., `/datacenter/<id>/servers`) returns 4xx | DC detail page | Inline error in fragment area |

### Per-test gotchas

- **The empty-state UI may not exist for every page yet.** If you load an empty page and the table just shows a header with no body, that's a UX bug — not just a test gap. **Flag pages where there's no empty-state UI to add.** Don't fabricate assertions about UI that isn't there.
- **Error-state UI varies.** Some pages may show a Bulma `notification`, others may show a toast (via `bulma-toast`), others may have an inline error in the HTMX target. Inspect each page's error path before asserting.
- **HTMX errors trigger `htmx:responseError` events.** Orbital has handlers for these (search `web/shared/static/shared.js` for `htmx:responseError`). Tests should assert the resulting UI, not the event itself.
- **`page.route()` must be set BEFORE `page.goto()`.** Set up the route first, then navigate. Otherwise the initial request bypasses the intercept.

### Validation for 5C

```bash
make test-e2e -- e2e/empty-and-error-states.spec.ts
```

For the intentional-break check: pick one error-state test, temporarily comment out the error notification rendering in the template, run the test, expect failure. Restore.

---

## Anti-patterns to avoid

- **Don't fabricate UI assertions.** If a page has no empty-state UI when there's no data, don't write a test asserting an empty-state element. Flag the gap, skip the test, note it in the PR.
- **Don't modify seed data to make tests work.** Seed changes affect every other test. If your test needs different data, use filters or `page.route()` mocks. Real seed changes are a separate, deliberate decision.
- **Don't write tests that depend on row counts in seed data exactly.** Seeded server counts change. Assertions like *"there are exactly 7 rows"* are brittle. Prefer *"at least 1 row"* / *"some rows match the filter"* / *"all visible rows contain X"*.
- **Don't use `page.waitForTimeout()`.** Always deterministic waits (`expect.toBeVisible()`, `expect.toHaveCount()`). The one exception is DataTables synchronous filter/sort — even there, a more reliable check is on the resulting state, not a delay.
- **Don't migrate or modify the Tier 1–4 work.** Those are landed and stable. This Tier ADDS coverage; it doesn't refactor existing tests.
- **Don't touch `e2e/configitem-editor.spec.ts`'s existing tests.** Sub-tier 5A *extends* it with new tests; the existing four mutation→audit tests are gold and stay as-is.
- **Don't commit.** Leave the diff for review.

---

## Order of execution

Each sub-tier is independent — Sonnet can stop after any one of them and have shipped value.

1. **5A first** (~half-day). The pattern is the most established (just replicate `configitem-editor.spec.ts`). Lowest risk. Proves you can extend the pattern.
2. **5C second** (~half-day). Uses `page.route()` which is new territory but well-documented in Playwright. The pattern is self-contained.
3. **5B last** (~half-day). DataTable interactions are the trickiest because the selectors depend on how DataTables initializes. Save for after you've warmed up on the simpler patterns.

After each sub-tier, run the per-sub-tier validation before moving on.

---

## Reporting back

When done, summarize:

- **5A**: list of edit-modal tests added (entity + field tested). Note any modal that's not reachable from the current UI (e.g., IPAddress edit modal not yet wired).
- **5B**: list of DataTable interaction tests by table. Note any table where the assertion shape didn't quite work (DataTables init oddity, missing `data-col` attributes, etc.).
- **5C**: list of empty/error tests. **Importantly: list pages where empty-state UI doesn't exist** — that's product feedback, not a test gap. Daniel will want to know.
- **Intentional-break sanity checks** confirmed for at least one test per sub-tier.
- **Wall-clock impact**: `time make test-e2e` before and after. Expect e2e suite to grow by ~30 seconds total (~25 new tests × ~1.5s each amortized).

Don't commit. Leave the diff for review.

---

## Clean-up

This plan lives at `docs/plans/tier5-ui-regression-coverage.md`. Per repo convention, **delete this file after the work is merged.** The durable artifacts are the tests themselves.

When deleting Tier 5's plan, also delete `docs/plans/e2e-test-coverage-improvements.md` and `docs/plans/tier4-smoke-test-migration.md` if they haven't been deleted yet — all three are completed scaffolding.
