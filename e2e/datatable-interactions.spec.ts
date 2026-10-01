// Tier 5B — DataTable interaction tests.
// Regression class: DataTables sort, filter, and row-expand silently break
// after JS refactors (init-order changes, module renames, column-index drift).
//
// Five tables covered:
//   • #generic-table (/servers) — sort, filter, IPv4 numeric sort
//   • #cluster-table (/clusters)    — sort, filter, workload child expand/collapse
//   • #audit-log-table (/audit-log) — sort, row detail expand
//   • #inventory-table (/inventory) — sort, filter
//   • dc-servers-table-{id} (DC tab) — IPv4 numeric sort after HTMX fragment load
//
// DataTables scrollX note: tables with scrollX:true have their original <thead>
// MOVED to a .dt-scroll-head container (the visible header). A sizing clone is
// left inside the original <table>. Headers inside the original <table>
// (hidden clone) are not clickable. Use .dt-scroll-head to find clickable headers.

import { test, expect, Page } from '@playwright/test'

// compareIPv4 mirrors the ipv4SortKey logic: numeric comparison by octet.
function compareIPv4(a: string, b: string): number {
  const pa = a.split('.').map(Number)
  const pb = b.split('.').map(Number)
  for (let i = 0; i < 4; i++) {
    if (pa[i] !== pb[i]) return pa[i] - pb[i]
  }
  return 0
}

// visibleHeader returns the visible (clickable) column header <th> for a DataTables
// table. For scrollX tables, the original <thead> is moved to .dt-scroll-head;
// for non-scrollX tables the original <thead> remains in the table element.
function visibleHeader(page: Page, thText: string) {
  // Matches both scroll-head (scrollX tables) and direct thead (non-scrollX tables).
  return page.locator(`.dt-scroll-head thead th:has-text("${thText}"), table:not(.dt-scroll-head table) thead th:has-text("${thText}")`)
    .filter({ visible: true })
    .first()
}

// domCells returns the visible text content of cells in a column by 1-based CSS
// nth-child index, from the tbody of the given table. Reads the DOM directly
// (independent of DataTables internal API ordering).
async function domCells(page: Page, tableSelector: string, nthChild: number): Promise<string[]> {
  return page.locator(`${tableSelector} tbody tr td:nth-child(${nthChild})`).allTextContents()
}

// waitForDataRows waits until the table has at least one non-empty-state row.
async function waitForDataRows(page: Page, tableId: string) {
  await page.waitForFunction(
    (id) => {
      const tbody = document.querySelector(`#${id} tbody`)
      if (!tbody) return false
      const rows = [...tbody.querySelectorAll('tr')].filter(r => !r.querySelector('td.dt-empty'))
      return rows.length > 0
    },
    tableId,
    { timeout: 15_000 },
  )
}

test.describe('DataTable interactions', () => {

  // ── Server list (#generic-table at /servers) ─────────────────────────

  test('servers table: sorts by Service Tag column', async ({ page }) => {
    // Guards: clicking a column header in a scrollX DataTable updates aria-sort
    // and reorders rows. Regression: a JS refactor breaks initGenericTable().
    await page.goto('/servers')
    await expect(page.locator('input[aria-controls="generic-table"]')).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'generic-table')

    const header = visibleHeader(page, 'Service Tag')
    await expect(header).toBeVisible({ timeout: 5_000 })
    await header.click()
    await expect(header).toHaveAttribute('aria-sort', 'ascending', { timeout: 5_000 })
  })

  test('servers table: filter narrows results to matching rows', async ({ page }) => {
    await page.goto('/servers')
    const searchInput = page.locator('input[aria-controls="generic-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'generic-table')
    const initialCount = await page.locator('#generic-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') }).count()

    // 5HSC3D4 is a seeded server in 2f-uae (configitem-editor fixture).
    await searchInput.fill('5HSC3D4')
    await expect(page.locator('#generic-table tbody td.dt-empty')).not.toBeVisible({ timeout: 5_000 })
    const filteredCount = await page.locator('#generic-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') }).count()
    expect(filteredCount).toBeGreaterThan(0)
    expect(filteredCount).toBeLessThan(initialCount)
  })

  test('servers table: OOB IP column sorts numerically not lexicographically', async ({ page }) => {
    // Regression class: removing dtIPv4Render reverts OOB IP sort to lexicographic,
    // putting "10.20.21.100" before "10.20.21.41" (because "1" < "4" as strings).
    // colo-galleon seed has servers at both .41–.55 AND .100 — a concrete pair
    // where lex and numeric order differ. If dtIPv4Render is broken, the test fails.
    await page.goto('/servers')
    await expect(page.locator('input[aria-controls="generic-table"]')).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'generic-table')

    // Sort by OOB IP ascending (default is by Data Center; this is first OOB IP click).
    const oobIPHeader = visibleHeader(page, 'OOB IP')
    await expect(oobIPHeader).toBeVisible({ timeout: 5_000 })
    await oobIPHeader.click()
    await expect(oobIPHeader).toHaveAttribute('aria-sort', 'ascending', { timeout: 5_000 })

    // Read the OOB IP column by finding its position rather than hardcoding it:
    // generic pages derive their columns from the schema, so an index pinned
    // here would silently start reading a different column the next time a
    // field is added.
    const headers = await page.locator('#generic-table thead th').allInnerTexts()
    const oobIPCol = headers.findIndex((h) => h.trim() === 'OOB IP') + 1
    expect(oobIPCol, 'no OOB IP column').toBeGreaterThan(0)
    const oobIPs = await domCells(page, '#generic-table', oobIPCol)
    const validIPs = oobIPs.filter(v => /^\d+\.\d+\.\d+\.\d+$/.test(v))
    expect(validIPs.length).toBeGreaterThan(1)

    for (let i = 1; i < validIPs.length; i++) {
      expect(compareIPv4(validIPs[i - 1], validIPs[i])).toBeLessThanOrEqual(0)
    }
  })

  // ── Cluster list (#generic-table at /clusters) ───────────────────────────

  test('clusters table: sorts by Name column', async ({ page }) => {
    await page.goto('/clusters')
    await expect(page.locator('input[aria-controls="generic-table"]')).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'generic-table')

    const header = visibleHeader(page, 'Name')
    await expect(header).toBeVisible({ timeout: 5_000 })
    await header.click()
    await expect(header).toHaveAttribute('aria-sort', 'ascending', { timeout: 5_000 })
  })

  test('clusters table: filter narrows results to matching rows', async ({ page }) => {
    await page.goto('/clusters')
    const searchInput = page.locator('input[aria-controls="generic-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'generic-table')
    const initialCount = await page.locator('#generic-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') }).count()

    // g2-m is the seeded houston management cluster.
    await searchInput.fill('g2-m')
    await expect(page.locator('#generic-table tbody td.dt-empty')).not.toBeVisible({ timeout: 5_000 })
    const filteredCount = await page.locator('#generic-table tbody tr').count()
    expect(filteredCount).toBeGreaterThan(0)
    expect(filteredCount).toBeLessThan(initialCount)
  })

  // 'clusters table: workload child rows expand and collapse on toggle click'
  // was DELETED 2026-09-25 with the bespoke cluster table.
  //
  // The capability did not disappear, it MOVED: a management cluster's workload
  // clusters are a relationship table on its detail page, asserted by
  // e2e/clusters-generic.spec.ts. Nested rows in a LIST were a per-page feature
  // of that table; the generic list expands owned children only, and a workload
  // cluster is a peer, not a child.


  // ── Audit log table (#audit-log-table at /audit-log) ─────────────────────

  test('audit log table: sorts by Timestamp column', async ({ page }) => {
    // DataTables 2.x sort cycle: asc → desc → none → asc. Initialized at desc.
    // Clicking once removes the sort (→ none); clicking twice gives ascending.
    // Regression: initAuditLogTable() breaks → header click has no effect.
    await page.goto('/audit-log')
    await expect(page.locator('input[aria-controls="audit-log-table"]')).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'audit-log-table')

    const header = visibleHeader(page, 'Timestamp')
    await expect(header).toBeVisible({ timeout: 5_000 })
    // Click twice: from any starting state, two clicks always reach a sort direction.
    await header.click()
    await header.click()
    await expect(header).toHaveAttribute('aria-sort', /ascending|descending/, { timeout: 5_000 })
  })

  test('audit log table: row detail expands on dt-control cell click', async ({ page }) => {
    // Regression class: dt-control click handler breaks after DataTables refactor.
    // Only mutation rows have diff details; skip if audit log is empty.
    await page.goto('/audit-log')
    await waitForDataRows(page, 'audit-log-table')

    const controlCell = page.locator('#audit-log-table tbody td.dt-control').first()
    if (await controlCell.count() === 0) {
      test.skip(true, 'No expandable audit rows — run configitem-editor tests first')
      return
    }
    await expect(controlCell).toBeVisible()
    await controlCell.click()
    // DataTables adds dt-hasChild to the parent <tr> when a child row is shown.
    await expect(page.locator('#audit-log-table tbody tr.dt-hasChild').first()).toBeVisible({ timeout: 3_000 })
  })

  // ── Inventory table (#inventory-table at /inventory) ─────────────────────

  test('inventory table: sorts by Name column', async ({ page }) => {
    await page.goto('/inventory')
    await expect(page.locator('input[aria-controls="inventory-table"]')).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'inventory-table')

    const header = visibleHeader(page, 'Name')
    await expect(header).toBeVisible({ timeout: 5_000 })
    await header.click()
    await expect(header).toHaveAttribute('aria-sort', 'ascending', { timeout: 5_000 })
  })

  test('inventory table: filter narrows results to matching rows', async ({ page }) => {
    await page.goto('/inventory')
    const searchInput = page.locator('input[aria-controls="inventory-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'inventory-table')
    const dataRows = page.locator('#inventory-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') })
    const initialCount = await dataRows.count()

    // Many config items are namespaced seattle:* in the seed data.
    await searchInput.fill('seattle')
    await expect(page.locator('#inventory-table tbody td.dt-empty')).not.toBeVisible({ timeout: 5_000 })
    const filteredCount = await page.locator('#inventory-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') }).count()
    expect(filteredCount).toBeGreaterThan(0)
    expect(filteredCount).toBeLessThan(initialCount)
  })

  // ── DC detail servers table (dc-servers-table-{domId}) ───────────────────

  // The "DC detail servers table" case was DELETED 2026-09-25. That page moved
  // to the generic renderer, where a relationship tab renders a PLAIN table —
  // DataTables is initialised on the list page only, so sorting inside a detail
  // tab is a capability the generic renderer does not yet have.
})

// The toolbar-sizing rule in main.scss is a hand-maintained list of table ids
// (`div.dt-container:has(#inventory-table), …`). A table whose id is not on it
// renders its info line and paging buttons at DataTables' default size, which
// is visibly larger than the 12px table content beside it — and nothing fails.
// Exactly how /clusters and /data-centers ended up oversized.
//
// Asserted COMPARATIVELY against the inventory page rather than against fixed
// pixel values: the requirement is "the same as the other tables", and a test
// pinning 12px would need editing every time the house size changed.
test('generic list pages size their table chrome like the inventory page', async ({ page }) => {
  const chrome = async (url: string, tableId: string) => {
    await page.goto(url);
    // The inventory table has stateSave:true, so a filter left by an earlier
    // test in this file persists in localStorage — the page then loads showing
    // one row, the paging control is not rendered at all, and this test fails
    // on a null reference rather than on a size. Cleared so the comparison is
    // against an unfiltered table whichever order the file runs in.
    await page.evaluate(() => {
      for (const k of Object.keys(localStorage)) {
        if (k.startsWith('DataTables_')) localStorage.removeItem(k);
      }
    });
    await page.reload();
    // Wait for the TOOLBAR, not the table.
    //
    // The <table> is server-rendered, so waiting on its id returns before
    // DataTables has built the info line and paging control — measuring then
    // reads a half-constructed node and getComputedStyle returns "". The info
    // line showing a row count is the signal that the draw finished.
    await page.locator(`#${tableId}`).waitFor();
    await expect(page.locator('.dt-info').first()).toHaveText(/\d/, { timeout: 15_000 });
    await page.locator('.dt-paging .pagination-link').first().waitFor({ state: 'visible' });
    const read = async (sel: string) => {
      const el = page.locator(sel).first();
      if (await el.count() === 0) return null;
      return await el.evaluate((e: HTMLElement) => {
        const s = getComputedStyle(e);
        return { fontSize: s.fontSize, height: Math.round(e.getBoundingClientRect().height) };
      });
    };
    return {
      info: await read('.dt-info'),
      page: await read('.dt-paging .pagination-link'),
      search: await read('.dt-search .input'),
    };
  };

  const reference = await chrome('/inventory', 'inventory-table');
  // Named explicitly: if the reference page stops rendering one of these the
  // test would silently compare nothing and pass.
  expect(reference.info, 'no info line on the reference page').not.toBeNull();
  expect(reference.page, 'no paging control on the reference page').not.toBeNull();
  expect(reference.search, 'no search box on the reference page').not.toBeNull();

  // Every page with a DataTable, not a sample: the sizing rule is global now,
  // and the failure mode it replaced was always a table nobody remembered to
  // add to a hand-maintained list.
  for (const [path, tableId] of [
    ['/clusters', 'generic-table'],
    ['/data-centers', 'generic-table'],
    ['/servers', 'generic-table'],
    ['/network-devices', 'generic-table'],
    ['/views', 'views-table'],
  ]) {
    const got = await chrome(path, tableId);
    expect(got.info!.fontSize, `${path} info line font`).toBe(reference.info!.fontSize);
    expect(got.info!.height, `${path} info line height`).toBe(reference.info!.height);
    expect(got.page!.fontSize, `${path} paging button font`).toBe(reference.page!.fontSize);
    expect(got.page!.height, `${path} paging button height`).toBe(reference.page!.height);
    expect(got.search!.fontSize, `${path} search box font`).toBe(reference.search!.fontSize);
    expect(got.search!.height, `${path} search box height`).toBe(reference.search!.height);
  }
});
