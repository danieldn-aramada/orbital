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

// Switching tabs must not move the tab strip.
//
// Panels differ enormously in height — a data centre's Servers tab is 190 rows
// and its Racks tab is 24 — so swapping one for the other collapsed the page
// by 1,400px, the browser clamped scrollY, and the strip you just clicked flew
// 569px down the screen. Measured, then fixed by pinning the strip and
// reserving the shortfall when the page is too short to scroll back.
//
// Asserted while SCROLLED, because that is the only state where it happens.
test('the tab strip stays put when switching between panels', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/data-centers/' + encodeURIComponent('colo:colo-galleon'));
  await expect(page.getByTestId('generic-fields')).toBeVisible({ timeout: 15_000 });

  const strip = page.locator('[id^="generic-detail-tabs-"]');
  await page.evaluate(() => {
    const s = document.querySelector('[id^="generic-detail-tabs-"]')!;
    window.scrollTo(0, s.getBoundingClientRect().top + window.scrollY - 10);
  });

  const labels = (await strip.locator('li').allInnerTexts()).map(t => t.trim());
  // Round trip, so both the shrink and the grow direction are covered.
  for (const label of [...labels, ...labels.slice(0, 2)]) {
    const before = (await strip.boundingBox())!.y;
    await strip.locator('li', { hasText: label }).first().click();
    await page.waitForTimeout(400);   // the audit panel fills in async
    const after = (await strip.boundingBox())!.y;
    expect(Math.abs(after - before),
      `switching to "${label}" moved the tab strip`).toBeLessThanOrEqual(20);
  }
});

// The is-boxed underline is what makes a tab look attached to its panel.
test('the tab strip keeps its underline and fits on one row', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/servers/' + encodeURIComponent('colo:server-7MP6K74'));
  await expect(page.getByTestId('generic-fields')).toBeVisible({ timeout: 15_000 });

  const ul = page.locator('[id^="generic-detail-tabs-"] ul');
  const width = await ul.evaluate(el => getComputedStyle(el).borderBottomWidth);
  expect(width, 'the is-boxed underline must not be removed to make tabs wrap').not.toBe('0px');

  // One row at this width: a wrapped strip puts the active tab above a line
  // drawn under the row below it, and the boxed look falls apart.
  const lis = page.locator('[id^="generic-detail-tabs-"] li');
  const tops = new Set<number>();
  for (let i = 0; i < await lis.count(); i++) {
    tops.add(Math.round((await lis.nth(i).boundingBox())!.y));
  }
  expect(tops.size, 'eight tabs should fit on one row at 1440px').toBe(1);
});
