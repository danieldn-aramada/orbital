import { test, expect, type Page } from '@playwright/test';

// The view's `filterBy:`, end to end.
//
// The bespoke Servers and Clusters pages each carried a hand-built "All Data
// Centers" select. Both died with their templates and NOBODY NOTICED for a
// week — the JavaScript that built them survived and simply returns early when
// its table is absent, so the code read as present the whole time. These specs
// exist because a missing control is invisible in a way a broken one is not.

const FILTER_BY = '[data-testid="generic-filter-by"]';

async function rowCount(page: Page): Promise<number> {
  return await page.locator('#generic-table tbody tr').count();
}

// Acceptance item 4.
test('servers page has a data center filterBy', async ({ page }) => {
  await page.goto('/servers');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });

  const filterBy = page.locator(FILTER_BY);
  await expect(filterBy).toBeVisible();
  // Named after the column it filters, not after the type.
  await expect(filterBy.locator('option').first()).toHaveText('All Data Centers');
  // At least two real choices — one option that selects everything is not a
  // filter, and the server drops the control rather than render it.
  expect(await filterBy.locator('option').count()).toBeGreaterThan(2);
});

// Acceptance item 5's visible half: /clusters is an INTERFACE view, and the
// declaration sits on the interface view. A per-concrete-type one would have
// left this page with no control at all.
test('clusters page inherits the filterBy from the interface', async ({ page }) => {
  await page.goto('/clusters');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(FILTER_BY)).toBeVisible();
});

// Acceptance item 1's negative. Asserted on a page that certainly has rows, so
// a passing result cannot be "the table was empty".
test('a page with no filterBy declared renders no dropdown', async ({ page }) => {
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(FILTER_BY)).toHaveCount(0);
});

// Acceptance item 3.
test('filterBy filters the table and All restores it', async ({ page }) => {
  await page.goto('/servers');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });

  const filterBy = page.locator(FILTER_BY);
  const all = await rowCount(page);
  const pick = await filterBy.locator('option').nth(1).getAttribute('value');

  await filterBy.selectOption(pick!);
  await expect.poll(() => rowCount(page), { timeout: 5_000 }).toBeLessThan(all);

  // Every surviving row really is that data center — a filter that merely
  // changes the count could be filtering the wrong column, which is exactly
  // what an off-by-one column index looks like.
  const cells = await page.locator('#generic-table tbody tr').allInnerTexts();
  for (const text of cells) {
    expect(text, `a row survived the ${pick} filter without containing it`).toContain(pick!);
  }

  await filterBy.selectOption('');
  await expect.poll(() => rowCount(page), { timeout: 5_000 }).toBe(all);
});

// Acceptance item 6. The selection is persisted as part of the DataTables
// state, keyed by SLUG — the table id `generic-table` is shared by every
// generic page, so a shared key would carry /servers' filter onto /racks.
test('filterBy selection survives a reload', async ({ page }) => {
  await page.goto('/servers');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });

  const filterBy = page.locator(FILTER_BY);
  const pick = await filterBy.locator('option').nth(1).getAttribute('value');
  await filterBy.selectOption(pick!);
  await expect.poll(() => rowCount(page), { timeout: 5_000 }).toBeGreaterThan(0);
  const filtered = await rowCount(page);

  await page.reload();
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(FILTER_BY)).toHaveValue(pick!);
  expect(await rowCount(page), 'the restored state must filter, not just set the select').toBe(filtered);

  // Restore, so the persisted filter does not leak into any spec that runs
  // after this one on the same storage state.
  await page.locator(FILTER_BY).selectOption('');
});

// The per-slug key, asserted directly: this is the bug that made stateSave
// unusable on the generic page in the first place.
test('a filter on one page does not leak onto another', async ({ page }) => {
  await page.goto('/servers');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  const filterBy = page.locator(FILTER_BY);
  const pick = await filterBy.locator('option').nth(1).getAttribute('value');
  await filterBy.selectOption(pick!);
  await expect.poll(() => rowCount(page), { timeout: 5_000 }).toBeGreaterThan(0);

  await page.goto('/network-devices');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  const racks = await rowCount(page);

  await page.goto('/servers');
  await page.locator(FILTER_BY).selectOption('');
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  expect(await rowCount(page), '/racks changed when /servers was filtered').toBe(racks);
});
