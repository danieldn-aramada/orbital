import { test, expect } from '@playwright/test';

test('menu footer shows app version', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('app-version')).toContainText(/Orbital v\S+/);
});

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
    // Read the expected count from the summary table rather than hardcoding it
    const summaryCountCell = page.locator('article', { hasText: 'Data Center Summary' })
      .locator('tr', { hasText: 'Servers' }).locator('td').nth(1);
    await expect(summaryCountCell).not.toBeEmpty();
    const expected = parseInt((await summaryCountCell.textContent()) ?? '0', 10);

    const serverRows = page.locator('[id^="dc-panel-servers-"] tbody tr');
    await expect(serverRows).toHaveCount(expected);
  });

  test('clicking Racks tab shows rack rows and hides servers', async ({ page }) => {
    // Read expected rack count from the summary table (same pattern as server count)
    const summaryRackCell = page.locator('article', { hasText: 'Data Center Summary' })
      .locator('tr', { hasText: 'Racks' }).locator('td').nth(1);
    await expect(summaryRackCell).not.toBeEmpty();
    const expected = parseInt((await summaryRackCell.textContent()) ?? '0', 10);

    await page.locator('[id^="dc-detail-tabs-"] li', { hasText: 'Racks' }).click();

    const rackRows = page.locator('[id^="dc-panel-racks-"] tbody tr');
    await expect(rackRows.first()).toBeVisible();
    await expect(rackRows).toHaveCount(expected);

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
    const reloadBtn = page.locator('button.js-dc-reload', { hasText: 'Reload' });

    // Wait for the reload request to complete
    await Promise.all([
      page.waitForResponse(resp => resp.url().includes('/datacenters/') && resp.status() === 200),
      reloadBtn.click(),
    ]);

    // Inner tabs should still work after reload
    await page.locator('[id^="dc-detail-tabs-"] li', { hasText: 'Racks' }).click();
    await expect(page.locator('[id^="dc-panel-racks-"] tbody tr').first()).toBeVisible();
  });

  test('data center summary shows correct metadata', async ({ page }) => {
    const summary = page.locator('article', { hasText: 'Data Center Summary' });
    await expect(summary.locator('td', { hasText: 'colo-galleon' })).toBeVisible();
    await expect(summary.locator('tr', { hasText: 'Servers' }).locator('td').nth(1)).not.toBeEmpty();

    // Created By is in the Metadata article; seed data may leave it blank (shows "—")
    const metadata = page.locator('article', { hasText: 'Metadata' });
    await expect(metadata.locator('tr', { hasText: 'Created By' })).toBeVisible();
  });
});
