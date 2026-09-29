import { test, expect } from '@playwright/test';

// The Views page: orbital's own UI reading GET /api/v1/views.
//
// It exists so the view list can be validated in the UI rather than by curling
// the API, and it is READ-ONLY — editing a view is the P1 override layer.

test('the views page lists every derived view', async ({ page }) => {
  await page.goto('/views');

  await expect(page.getByTestId('page-heading')).toHaveText('Views');
  await expect(page.locator('#views-table')).toBeVisible();

  // Rows come from the API, so a non-empty table proves the endpoint round-trip.
  await expect(page.locator('#views-table tbody tr').first()).toBeVisible();
  const rows = await page.locator('#views-table tbody tr').count();
  expect(rows).toBeGreaterThan(10);

  // No error banner: the page must not be reporting a failure it recovered from.
  await expect(page.locator('#views-error')).toBeHidden();
});

test('a row expands to show the fields and relationships behind the counts', async ({ page }) => {
  await page.goto('/views');
  await expect(page.locator('#views-table tbody tr').first()).toBeVisible();

  // The Server row specifically. Matching on the text "Server" is not enough:
  // rows sort by slug, so server-configuration-profiles comes first and has no
  // fields at all — the test would then assert against the wrong view.
  const serverRow = page.locator('#views-table tbody tr').filter({
    has: page.locator('td:nth-child(2) code', { hasText: /^servers$/ }),
  }).first();
  await expect(serverRow).toBeVisible();
  await serverRow.locator('td.dt-control').click();

  const child = page.locator('#views-table tbody tr.shown + tr');
  await expect(child).toBeVisible();
  await expect(child).toContainText('Fields');
  await expect(child).toContainText('Relationships');

  // A count says a view has N fields; only this says WHICH.
  await expect(child).toContainText('hostname');

  // Relationships link through by the target type's slug.
  await expect(child.locator('a[href*="/idrac-settings"]').first()).toBeVisible();
});

// 'the page states it is read-only' was DELETED 2026-09-28.
//
// It pinned a caption saying the page was read-only and that overrides "are not
// built yet". No other orbital page captions what it cannot do, and no
// comparable product does either — the ABSENCE of edit controls is the
// statement, and a roadmap note in product chrome dates the moment it ships.
// What the page still asserts is the thing that matters: no mutation controls.
test('the views page offers no mutation controls', async ({ page }) => {
  await page.goto('/views');
  await expect(page.locator('#views-table')).toBeVisible();
  await expect(page.locator('[data-generic-edit-id]')).toHaveCount(0);
  await expect(page.locator('[data-cfg-delete-id]')).toHaveCount(0);
});

test('Views is in the Config Items menu, next to Schema Version', async ({ page }) => {
  await page.goto('/');
  const link = page.locator('.app-menu a[href$="/views"]');
  await expect(link).toBeVisible();
  await expect(link).toHaveText(/Views/);
});

test('the expanded panel contains no nested table', async ({ page }) => {
  await page.goto('/views');
  await expect(page.locator('#views-table tbody tr').first()).toBeVisible();

  const serverRow = page.locator('#views-table tbody tr').filter({
    has: page.locator('td:nth-child(2) code', { hasText: /^servers$/ }),
  }).first();
  await serverRow.locator('td.dt-control').click();

  const child = page.locator('#views-table tbody tr.shown + tr');
  await expect(child).toBeVisible();

  // Bulma scopes .is-striped and .is-hoverable with DESCENDANT selectors, so a
  // <table> nested in a child row inherits both from the parent table: its rows
  // alternate white/grey and light up on hover, reading as stray selection in
  // the parent. A grid avoids it; this pins that nobody reintroduces the table.
  await expect(child.locator('table')).toHaveCount(0);

  // ...and the content is still there, so the fix is not "delete the panel".
  await expect(child).toContainText('dataCenter');
  await expect(child.locator('a[href*="/data-centers"]').first()).toBeVisible();
});
