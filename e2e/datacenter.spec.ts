import { test, expect } from '@playwright/test';

test.describe('Data center tab', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/datacenters');

    // Wait for the datacenter table to load colo-galleon
    const row = page.locator('#datacenter-table tbody tr', { hasText: 'colo-galleon' });
    await expect(row).toBeVisible();

    // Double-click to open the tab
    await row.dblclick();

    // Wait for the tab link to appear and click it to activate
    const tabLink = page.locator('[id^="tab-"][id$="colo-galleon"]').or(
      page.locator('#tablist li.tab a', { hasText: 'colo-galleon' })
    );
    // Tab is auto-activated on dblclick; wait for skeleton to resolve
    // Skeleton is gone when the reload button is no longer is-loading
    await expect(
      page.locator('[id^="tab-content-"] .button.is-loading')
    ).not.toBeVisible({ timeout: 10000 });
  });

  test('servers tab is active by default and shows server rows', async ({ page }) => {
    const serverRows = page.locator('[id^="dc-panel-servers-"] tbody tr');
    await expect(serverRows.first()).toBeVisible();
    await expect(serverRows).toHaveCount(50);
  });

  test('clicking Racks tab shows rack rows and hides servers', async ({ page }) => {
    await page.locator('[id^="dc-detail-tabs-"] li', { hasText: 'Racks' }).click();

    const rackRows = page.locator('[id^="dc-panel-racks-"] tbody tr');
    await expect(rackRows.first()).toBeVisible();
    await expect(rackRows).toHaveCount(4);

    const serversPanel = page.locator('[id^="dc-panel-servers-"]');
    await expect(serversPanel).toBeHidden();
  });

  test('clicking Servers tab after Racks switches back', async ({ page }) => {
    await page.locator('[id^="dc-detail-tabs-"] li', { hasText: 'Racks' }).click();
    await page.locator('[id^="dc-detail-tabs-"] li', { hasText: 'Servers' }).click();

    const serversPanel = page.locator('[id^="dc-panel-servers-"]');
    await expect(serversPanel).toBeVisible();

    const racksPanel = page.locator('[id^="dc-panel-racks-"]');
    await expect(racksPanel).toBeHidden();
  });

  test('reload button refreshes content and inner tabs still work', async ({ page }) => {
    const reloadBtn = page.locator('button[hx-get^="/datacenters/"]', { hasText: 'Reload' });
    await reloadBtn.click();

    // Skeleton should appear
    await expect(
      page.locator('[id^="tab-content-"] .button.is-loading')
    ).toBeVisible();

    // Wait for real data to come back
    await expect(
      page.locator('[id^="tab-content-"] .button.is-loading')
    ).not.toBeVisible({ timeout: 10000 });

    // Inner tabs should still work after reload
    await page.locator('[id^="dc-detail-tabs-"] li', { hasText: 'Racks' }).click();
    await expect(page.locator('[id^="dc-panel-racks-"] tbody tr').first()).toBeVisible();
  });

  test('data center summary shows correct metadata', async ({ page }) => {
    const summary = page.locator('article', { hasText: 'Data Center Summary' });
    await expect(summary.locator('td', { hasText: 'colo-galleon' })).toBeVisible();
    await expect(summary.locator('td', { hasText: '50' })).toBeVisible();
    await expect(summary.locator('td', { hasText: 'admin@armada.ai' })).toBeVisible();
  });
});
