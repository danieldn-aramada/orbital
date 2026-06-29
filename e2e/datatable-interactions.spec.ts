// Tier 5B — DataTable interaction tests.
// Regression class: DataTables sort, filter, and row-expand silently break
// after JS refactors (init-order changes, module renames, column-index drift).
//
// Five tables covered:
//   • #server-list-table (/servers) — sort, filter, IPv4 numeric sort
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

  // ── Server list (#server-list-table at /servers) ─────────────────────────

  test('servers table: sorts by Service Tag column', async ({ page }) => {
    // Guards: clicking a column header in a scrollX DataTable updates aria-sort
    // and reorders rows. Regression: JS refactor breaks initServerListTable().
    await page.goto('/servers')
    await expect(page.locator('input[aria-controls="server-list-table"]')).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'server-list-table')

    const header = visibleHeader(page, 'Service Tag')
    await expect(header).toBeVisible({ timeout: 5_000 })
    await header.click()
    await expect(header).toHaveAttribute('aria-sort', 'ascending', { timeout: 5_000 })
  })

  test('servers table: filter narrows results to matching rows', async ({ page }) => {
    await page.goto('/servers')
    const searchInput = page.locator('input[aria-controls="server-list-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'server-list-table')
    const initialCount = await page.locator('#server-list-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') }).count()

    // 5HSC3D4 is a seeded server in 2f-uae (configitem-editor fixture).
    await searchInput.fill('5HSC3D4')
    await expect(page.locator('#server-list-table tbody td.dt-empty')).not.toBeVisible({ timeout: 5_000 })
    const filteredCount = await page.locator('#server-list-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') }).count()
    expect(filteredCount).toBeGreaterThan(0)
    expect(filteredCount).toBeLessThan(initialCount)
  })

  test('servers table: OOB IP column sorts numerically not lexicographically', async ({ page }) => {
    // Regression class: removing dtIPv4Render reverts OOB IP sort to lexicographic,
    // putting "10.20.21.100" before "10.20.21.41" (because "1" < "4" as strings).
    // colo-galleon seed has servers at both .41–.55 AND .100 — a concrete pair
    // where lex and numeric order differ. If dtIPv4Render is broken, the test fails.
    await page.goto('/servers')
    await expect(page.locator('input[aria-controls="server-list-table"]')).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'server-list-table')

    // Sort by OOB IP ascending (default is by Data Center; this is first OOB IP click).
    const oobIPHeader = visibleHeader(page, 'OOB IP')
    await expect(oobIPHeader).toBeVisible({ timeout: 5_000 })
    await oobIPHeader.click()
    await expect(oobIPHeader).toHaveAttribute('aria-sort', 'ascending', { timeout: 5_000 })

    // Read visible cell text in column 2 (OOB IP, 1-based) from the scroll body.
    // With scrollX, tbody is in the original table (dt-scroll-body).
    const oobIPs = await domCells(page, '#server-list-table', 2)
    const validIPs = oobIPs.filter(v => /^\d+\.\d+\.\d+\.\d+$/.test(v))
    expect(validIPs.length).toBeGreaterThan(1)

    for (let i = 1; i < validIPs.length; i++) {
      expect(compareIPv4(validIPs[i - 1], validIPs[i])).toBeLessThanOrEqual(0)
    }
  })

  // ── Cluster list (#cluster-table at /clusters) ───────────────────────────

  test('clusters table: sorts by Name column', async ({ page }) => {
    await page.goto('/clusters')
    await expect(page.locator('input[aria-controls="cluster-table"]')).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'cluster-table')

    const header = visibleHeader(page, 'Name')
    await expect(header).toBeVisible({ timeout: 5_000 })
    await header.click()
    await expect(header).toHaveAttribute('aria-sort', 'ascending', { timeout: 5_000 })
  })

  test('clusters table: filter narrows results to matching rows', async ({ page }) => {
    await page.goto('/clusters')
    const searchInput = page.locator('input[aria-controls="cluster-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })
    await waitForDataRows(page, 'cluster-table')
    const initialCount = await page.locator('#cluster-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') }).count()

    // g2-m is the seeded houston management cluster.
    await searchInput.fill('g2-m')
    await expect(page.locator('#cluster-table tbody td.dt-empty')).not.toBeVisible({ timeout: 5_000 })
    const filteredCount = await page.locator('#cluster-table tbody tr').count()
    expect(filteredCount).toBeGreaterThan(0)
    expect(filteredCount).toBeLessThan(initialCount)
  })

  test('clusters table: workload child rows expand and collapse on toggle click', async ({ page }) => {
    // Regression class: cluster expand breaks after JS refactor. Management
    // cluster houston:g2-m has one workload child houston:g2-w1. initComplete
    // auto-expands all management rows with workloads.
    await page.goto('/clusters')
    const searchInput = page.locator('input[aria-controls="cluster-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })

    // Filter to the management cluster so only g2-m and its child g2-w1 are visible.
    await searchInput.fill('g2-m')
    const parentRow = page.locator('#cluster-table tbody tr:not(.cluster-child-row)', { hasText: 'g2-m' })
    await expect(parentRow).toBeVisible({ timeout: 10_000 })

    // initComplete's expandAllClusterChildren auto-expands g2-m on page load.
    const childRow = page.locator('#cluster-table tbody tr.cluster-child-row')
    await expect(childRow.first()).toBeVisible({ timeout: 5_000 })

    // Click toggle cell to collapse — child becomes hidden.
    const toggleCell = parentRow.locator('td.cluster-toggle-cell')
    await toggleCell.click()
    await expect(childRow.first()).not.toBeVisible({ timeout: 5_000 })

    // Click again to re-expand — child reappears.
    await toggleCell.click()
    await expect(childRow.first()).toBeVisible({ timeout: 5_000 })
  })

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

  test('DC detail servers table: OOB IP column sorts numerically (default init sort)', async ({ page }) => {
    // Regression class: dtIPv4Render on dc-servers-table (initialized in the
    // htmx:afterSettle handler) breaks, reverting to lexicographic sort.
    // colo-galleon has servers at 10.20.21.100 AND 10.20.21.41–.55. Lex sort
    // puts "10.20.21.100" before "10.20.21.41" (because "1" < "4" as strings);
    // numeric sort (dtIPv4Render) correctly places it after. The DataTables
    // default order [[0,'asc']] is applied on init — check it without clicking.
    await page.goto('/datacenters')
    const summaryTab = page.locator('#tab-summary')
    if (await summaryTab.count() > 0) await summaryTab.click()
    const dcSearchInput = page.locator('input[aria-controls="datacenter-table"]')
    await expect(dcSearchInput).toBeVisible({ timeout: 10_000 })
    await dcSearchInput.fill('colo-galleon')
    const row = page.locator('#datacenter-table tbody tr', { hasText: 'colo-galleon' })
    await expect(row).toBeVisible({ timeout: 10_000 })
    await row.dblclick()

    // safeDomId('colo:colo-galleon') → 'colo_colo-galleon'
    const domId = 'colo_colo-galleon'
    await page.waitForSelector(`#tab-content-${domId}[data-loaded="true"]`, { timeout: 15_000 })

    const serversTable = page.locator(`#dc-servers-table-${domId}`)
    await expect(serversTable).toBeVisible({ timeout: 5_000 })

    // The dc-servers-table uses no scrollX; thead is directly inside the table.
    // Default sort is [[0,'asc']] with dtIPv4Render → numeric ascending on init.
    // Verify by reading visible OOB IP cells (column 1, 1-based nth-child).
    const oobIPCells = await domCells(page, `#dc-servers-table-${domId}`, 1)
    const validIPs = oobIPCells.filter(v => /^\d+\.\d+\.\d+\.\d+$/.test(v))

    if (validIPs.length < 2) {
      test.skip(true, 'Not enough IP addresses in colo-galleon to verify numeric sort')
      return
    }

    for (let i = 1; i < validIPs.length; i++) {
      expect(compareIPv4(validIPs[i - 1], validIPs[i])).toBeLessThanOrEqual(0)
    }
  })

})
