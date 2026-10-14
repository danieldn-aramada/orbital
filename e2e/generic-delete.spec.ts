import { test, expect } from '@playwright/test';
import { listRows } from './helpers/generic';

// Deleting a ConfigItem from a generic list page.
//
// The delete SUCCEEDED and the tab stayed open, so the page looked exactly as
// it had a moment earlier — the row still listed, the tab still there. "Nothing
// happened" is the one outcome nobody can debug from the outside.
//
// Cause: every post-delete branch was keyed to a hand-written page's ids
// (`tab-close-srv-…`, `btn-reload-servers`, `tab-content-<dcDomId>`). A generic
// page has none of them, so no branch matched and the success path did nothing.

const ORB = 'colo:network-device-E2EDELETE';

async function seed(request: any) {
  await request.post('/graphql', {
    data: {
      query: `mutation { addNetworkDevice(input: [{ orbId: "${ORB}", namespace: "colo", version: 1,
        name: "e2e-delete-me", serial: "E2EDELETE", dataCenter: { orbId: "colo:colo-galleon" } }]) { numUids } }`,
    },
  });
}
async function clean(request: any) {
  await request.post('/graphql', {
    data: { query: `mutation { deleteNetworkDevice(filter: { orbId: { eq: "${ORB}" } }) { numUids } }` },
  });
}

test.beforeEach(async ({ request }) => { await clean(request); await seed(request); });
test.afterEach(async ({ request }) => clean(request));

test('deleting from a tab closes the tab and drops the row', async ({ page, request }) => {
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();

  const row = page.locator(`#generic-table tbody tr[data-orb-id="${ORB}"]`);
  await expect(row, 'precondition: the row to delete is on the page').toHaveCount(1);
  await row.dblclick();
  await expect(page.locator(`#tablist li.tab a[data-orb-id="${ORB}"]`)).toBeVisible();

  await page.locator('[data-cfg-delete-id]').first().click();
  const modal = page.locator('#cfg-delete-modal');
  await expect(modal).toHaveClass(/is-active/);
  await page.locator('#cfg-delete-confirm-btn').click();

  // The three things that must change, none of which happened before.
  await expect(page.locator(`#tablist li.tab a[data-orb-id="${ORB}"]`),
    'the tab for a deleted entity must close').toHaveCount(0, { timeout: 15_000 });
  await expect(page.locator(`#generic-table tbody tr[data-orb-id="${ORB}"]`),
    'and its row must leave the list').toHaveCount(0);

  const res = await request.post('/graphql', {
    data: { query: `{ getNetworkDevice(orbId: "${ORB}") { orbId } }` },
  });
  expect((await res.json()).data.getNetworkDevice, 'and it must actually be gone').toBeNull();
});

test('the closed tab does not come back on reload', async ({ page }) => {
  // Closing is what removes the tab from localStorage. Reloading without that
  // restores a tab whose entity no longer exists, which renders an error where
  // a tab should be — a worse outcome than the bug being fixed.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  await page.locator(`#generic-table tbody tr[data-orb-id="${ORB}"]`).dblclick();
  await expect(page.locator(`#tablist li.tab a[data-orb-id="${ORB}"]`)).toBeVisible();

  await page.locator('[data-cfg-delete-id]').first().click();
  await page.locator('#cfg-delete-confirm-btn').click();
  await expect(page.locator(`#tablist li.tab a[data-orb-id="${ORB}"]`)).toHaveCount(0, { timeout: 15_000 });

  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  await expect(page.locator(`#tablist li.tab a[data-orb-id="${ORB}"]`),
    'a deleted entity must not be restored as a tab').toHaveCount(0);
});
