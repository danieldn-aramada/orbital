import { test, expect } from '@playwright/test';

// Row → tab opening on the generic list page.
//
// This is the workflow the hand-written pages have: double-click a row, it
// opens as a tab BELOW the list, and several can be open at once. The generic
// renderer has to match it, or migrating a page onto it is a downgrade.
//
// Uses racks — no bespoke page, so everything here is derived.

test('double-clicking a row opens it as a tab below the list', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();

  const firstRow = page.locator('#generic-table tbody tr').first();
  const label = (await firstRow.locator('td:first-child a').innerText()).trim();
  await firstRow.dblclick();

  // A new tab appears next to Summary, and the detail body loads into it.
  const tab = page.locator('#tablist li.tab a', { hasText: label });
  await expect(tab).toBeVisible();
  await expect(page.locator('[id^="tab-content-generic-racks-"]')).toBeVisible();
  await expect(page.getByTestId('generic-fields')).toBeVisible();

  // The tab body is the FRAGMENT, not a whole page nested in a div.
  await expect(page.locator('[id^="tab-content-generic-racks-"] .app-menu')).toHaveCount(0);
});

test('two rows open as two tabs, which is the point of tabs', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();

  const rows = page.locator('#generic-table tbody tr');
  if (await rows.count() < 2) test.skip(true, 'need two racks');
  await rows.nth(0).dblclick();
  // Opening a tab switches away from Summary, which hides the table — so go
  // back to it before opening the second, exactly as a user would.
  await page.locator('#tab-summary').click();
  await expect(page.locator('#generic-table')).toBeVisible();
  await rows.nth(1).dblclick();

  // Summary + two detail tabs.
  await expect(page.locator('#tablist li.tab')).toHaveCount(3);
});

test('an open tab is restored after a reload', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();
  const firstRow = page.locator('#generic-table tbody tr').first();
  const label = (await firstRow.locator('td:first-child a').innerText()).trim();
  await firstRow.dblclick();
  await expect(page.locator('#tablist li.tab a', { hasText: label })).toBeVisible();

  await page.reload();
  await expect(page.locator('#tablist li.tab a', { hasText: label })).toBeVisible();
});

test('a tab from another slug is not restored onto this page', async ({ page }) => {
  // A rack tab restored onto /storage-devices would fetch a 404 and render an
  // error where a tab should be. Tabs are slug-qualified for that reason.
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();
  await page.locator('#generic-table tbody tr').first().dblclick();
  await expect(page.locator('#tablist li.tab')).toHaveCount(2);

  await page.goto('/storage-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  await expect(page.locator('#tablist li.tab')).toHaveCount(1); // Summary only
});

test('closing a tab removes it and it stays closed after reload', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();
  await page.locator('#generic-table tbody tr').first().dblclick();
  await expect(page.locator('#tablist li.tab')).toHaveCount(2);

  await page.locator('[id^="tab-close-generic-racks-"]').first().click();
  await expect(page.locator('#tablist li.tab')).toHaveCount(1);

  await page.reload();
  await expect(page.locator('#tablist li.tab')).toHaveCount(1);
});
