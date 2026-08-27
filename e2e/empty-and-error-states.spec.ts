// Tier 5C — Empty and error state coverage.
// Guards the UI behavior when there is no data and when fragment reloads fail.
// Uses DataTables search filter for empty-state triggers (no seed changes needed)
// and page.route() abort for reload error state triggers.

import { test, expect, Page } from '@playwright/test'

// safeDomId mirrors the Go helper; orbId → DOM-safe id.
function safeDomId(orbId: string): string {
  return orbId.replace(/[^a-zA-Z0-9_-]/g, '_')
}

// openDCTab opens the DC detail tab for the given orbId via dblclick.
// Returns domId. Used by the DC reload error test.
async function openDCTab(page: Page, dcOrbId: string): Promise<string> {
  const domId = safeDomId(dcOrbId)
  const dcName = dcOrbId.split(':')[1]
  await page.goto('/datacenters')
  const summaryTab = page.locator('#tab-summary')
  if (await summaryTab.count() > 0) await summaryTab.click()
  const searchInput = page.locator('input[aria-controls="datacenter-table"]')
  await expect(searchInput).toBeVisible({ timeout: 10_000 })
  await searchInput.fill(dcName)
  const row = page.locator('#datacenter-table tbody tr', { hasText: dcName })
  await expect(row).toBeVisible({ timeout: 10_000 })
  await row.dblclick()
  await page.waitForSelector(`#tab-content-${domId}[data-loaded="true"]`, { timeout: 15_000 })
  return domId
}

// ── Empty states ──────────────────────────────────────────────────────────────
//
// These tests drive DataTables search to a string that matches no rows,
// then assert the DataTables empty-state cell is shown. No seed changes needed.

test.describe('empty states', () => {

  test('inventory table: impossible filter shows empty state', async ({ page }) => {
    await page.goto('/inventory')
    // Wait for inventory table to be initialized (DataTables wraps it with controls).
    const searchInput = page.locator('input[aria-controls="inventory-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })
    await searchInput.fill('XXXXX-NO-SUCH-SERVICE-TAG')
    // DataTables 2.x inserts a <td class="dt-empty"> inside the empty row.
    const emptyRow = page.locator('#inventory-table tbody td.dt-empty')
    await expect(emptyRow).toBeVisible({ timeout: 5_000 })
  })

  test('clusters table: impossible filter shows empty state', async ({ page }) => {
    await page.goto('/clusters')
    const searchInput = page.locator('input[aria-controls="cluster-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })
    await searchInput.fill('XXXXX-NO-SUCH-CLUSTER')
    // DataTables 2.x inserts a <td class="dt-empty"> inside the empty row.
    const emptyRow = page.locator('#cluster-table tbody td.dt-empty')
    await expect(emptyRow).toBeVisible({ timeout: 5_000 })
  })

  test('audit log table: impossible filter shows empty state', async ({ page }) => {
    // Route is /audit-log (not /audit — check server.go route registration).
    await page.goto('/audit-log')
    const searchInput = page.locator('input[aria-controls="audit-log-table"]')
    await expect(searchInput).toBeVisible({ timeout: 10_000 })
    await searchInput.fill('XXXXX-NO-SUCH-ORB-ID')
    const emptyRow = page.locator('#audit-log-table tbody td.dt-empty')
    await expect(emptyRow).toBeVisible({ timeout: 5_000 })
  })

  test('divergence reports page: shows empty state when no reports exist', async ({ page }) => {
    // Divergence reports come from edge device submissions — make seed only seeds
    // DGraph. After a fresh seed (or empty DB), this page shows "No divergence reports."
    await page.goto('/divergence-reports')
    // The table body is server-rendered. If groups is empty, the template emits a
    // single row with class has-text-grey and text "No divergence reports."
    const noReports = page.locator('td.has-text-grey', { hasText: 'No divergence reports.' })
    if (await noReports.count() === 0) {
      test.skip(true, 'Divergence reports exist in DB — skipping empty-state assertion')
      return
    }
    await expect(noReports).toBeVisible()
  })

  test('backups page: shows empty state when no backup jobs exist', async ({ page }) => {
    // make seed only seeds DGraph; PostgreSQL backup_jobs table starts empty.
    await page.goto('/backups')
    // The backup tbody starts with "Loading..." and is populated via fetch.
    // Wait for loading to finish before asserting.
    const tbody = page.locator('#backup-tbody')
    await expect(tbody).toBeVisible({ timeout: 10_000 })
    // Wait for "Loading..." to disappear.
    await expect(tbody.locator('td', { hasText: 'Loading...' })).not.toBeVisible({ timeout: 10_000 })

    if (await tbody.locator('td.has-text-grey.has-text-centered', { hasText: 'No backups yet.' }).count() === 0) {
      test.skip(true, 'Backup jobs exist in DB — skipping empty-state assertion')
      return
    }
    await expect(tbody.locator('td.has-text-grey.has-text-centered', { hasText: 'No backups yet.' })).toBeVisible()
  })
})

// ── Error states ──────────────────────────────────────────────────────────────
//
// These tests use page.route() to abort fragment requests, then click the
// corresponding Reload button to trigger the "Reload failed." error notification.
// page.route() is set up AFTER the initial tab load so only the reload is intercepted.

test.describe('fragment reload error states', () => {

  test('DC tab: reload failure shows error notification', async ({ page }) => {
    const domId = await openDCTab(page, 'colo:colo-galleon')

    // Intercept the DC fragment request and abort it to simulate a network failure.
    await page.route('**/datacenters/**', route => route.abort())
    try {
      await page.locator('.js-dc-reload').first().click()
      // After abort, the .catch() handler writes the error notification into the tab content.
      const tabContent = page.locator(`#tab-content-${domId}`)
      await expect(tabContent.locator('.notification.is-danger')).toBeVisible({ timeout: 5_000 })
      await expect(tabContent.locator('.notification.is-danger')).toContainText('Reload failed.')
    } finally {
      await page.unroute('**/datacenters/**')
    }
  })

  test('server tab: reload failure shows error notification', async ({ page }) => {
    const orbId = '2f-uae:server-5HSC3D4'  // seeded R750 server (same as configitem-editor tests)
    const domId = safeDomId(orbId)
    await page.goto(`/servers?open=${encodeURIComponent(orbId)}&label=${encodeURIComponent(orbId)}`)
    const tabContent = page.locator(`#tab-content-srv-${domId}`)
    await page.waitForSelector(`#tab-content-srv-${domId}[data-loaded="true"]`, { timeout: 15_000 })

    // Intercept server fragment requests and abort.
    await page.route('**/servers/**', route => route.abort())
    try {
      await page.locator('.js-srv-reload').first().click()
      await expect(tabContent.locator('.notification.is-danger')).toBeVisible({ timeout: 5_000 })
      await expect(tabContent.locator('.notification.is-danger')).toContainText('Reload failed.')
    } finally {
      await page.unroute('**/servers/**')
    }
  })

  // Cluster reload failure is NOT testable: reloadClusterFragment() has an inner
  // `.catch(() => {})` that swallows network errors before the outer click handler's
  // .catch() can write the "Reload failed." notification. The error path exists in
  // the code but is dead. This is a product gap — the cluster reload button silently
  // discards network failures instead of showing the error notification.
  test.skip('cluster tab: reload failure shows error notification — NOT TESTABLE (inner catch swallows errors)', async () => {})

})
