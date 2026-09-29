import { test, expect } from '@playwright/test';

// The generic renderer: /{slug} serves every ConfigItem type from one template.
//
// These use `racks` deliberately — Rack has NO bespoke page, so anything that
// works here works because it was derived from the deployed schema, not because
// someone hand-wrote it. A type with its own page would prove nothing.

test('a type with no bespoke page gets a working list page', async ({ page }) => {
  await page.goto('/racks');

  // No page heading, deliberately: this mirrors /servers, which has none. The
  // page is identified by the active nav item and the URL.
  await expect(page.getByTestId('page-heading')).toHaveCount(0);
  await expect(page.locator('#generic-table')).toBeVisible();
  await expect(page.locator('#generic-table')).toHaveAttribute('data-slug', 'racks');
});

test('the generic page gets the house DataTable toolbar, not a bare table', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();

  // DataTables replaces the plain <table> with its own wrapper and controls.
  // Without this the page renders but behaves like a static table — which is
  // how it shipped first, and is the regression this pins.
  await expect(page.locator('#generic-table_wrapper')).toBeVisible();
  await expect(page.locator('#generic-table_wrapper input[type="search"]')).toBeVisible();
  await expect(page.locator('#generic-table_wrapper button:has-text("Select")')).toBeVisible();
});

test('the tab bar and tabpanel match the hand-written list pages', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.locator('nav.tabs.is-boxed')).toBeVisible();
  await expect(page.locator('#tab-content-generic[role="tabpanel"]')).toBeVisible();
});

test('a row links through to the derived detail page', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();

  const first = page.locator('#generic-table tbody tr td:first-child a').first();
  await expect(first).toBeVisible();
  await first.click();

  await expect(page).toHaveURL(/\/racks\/.+/);
  await expect(page.getByTestId('generic-fields')).toBeVisible();
});

test('an unknown slug is a 404, not an empty page', async ({ page }) => {
  const res = await page.goto('/definitely-not-a-kind');
  expect(res?.status()).toBe(404);
});

test('a relationship tab shows the child type\'s own fields, not just names', async ({ page }) => {
  // A DataCenter's servers table carries model, service tag, rack position —
  // the child's data. Selecting only orbId+name made every relationship tab a
  // bare list of names, which is a downgrade from the hand-written pages the
  // generic renderer replaces.
  await page.goto('/data-centers');
  await expect(page.locator('#generic-table')).toBeVisible();
  const href = await page.locator('#generic-table tbody tr td:first-child a').first().getAttribute('href');
  if (!href) test.skip(true, 'no data centers seeded');
  await page.goto(href!);

  const serversTab = page.locator('[data-testid="generic-tab"][data-field="servers"]');
  await expect(serversTab).toBeVisible();
  for (const col of ['Hostname', 'Model', 'Service Tag', 'Rack Position']) {
    await expect(serversTab).toContainText(col);
  }
  // And actual values, or the columns are headers over an empty table.
  await expect(serversTab.locator('tbody tr').first()).toBeVisible();
});
