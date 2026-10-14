import { test, expect } from '@playwright/test';
import { listRows } from './helpers/generic';

// A tab whose entity no longer exists.
//
// Found by a dead-selector sweep 2026-10-13. Orb's `clearStaleTabState` kept
// its OWN list of localStorage keys and `genericTabs` was never added to it —
// so after an import (drop_all + live load) orb restored tabs for entities the
// new bundle did not contain. Orbital reaches the same state whenever another
// session deletes something you have open.
//
// The fetch 404s, htmx RESOLVES but does not swap, and the panel was then
// flagged `loaded` — so the blank was permanent and every later click returned
// early at the `loaded` short-circuit.

test('a tab whose entity is gone says so, and stays retryable', async ({ page, request }) => {
  const ORB = 'colo:network-device-E2ESTALE';
  await request.post('/graphql', {
    data: {
      query: `mutation { addNetworkDevice(input: [{ orbId: "${ORB}", namespace: "colo", version: 1,
        name: "e2e-stale", serial: "E2ESTALE", dataCenter: { orbId: "colo:colo-galleon" } }]) { numUids } }`,
    },
  });

  try {
    await page.goto('/network-devices');
    await expect(page.locator('#generic-table')).toBeVisible();
    await page.locator(`#generic-table tbody tr[data-orb-id="${ORB}"]`).dblclick();
    await expect(page.locator(`#tablist li.tab a[data-orb-id="${ORB}"]`)).toBeVisible();

    // Delete it out from under the open tab — what another session, or an
    // import, does.
    await request.post('/graphql', {
      data: { query: `mutation { deleteNetworkDevice(filter: { orbId: { eq: "${ORB}" } }) { numUids } }` },
    });

    // Arrive fresh so the tab is RESTORED from storage and re-fetches.
    await page.goto('/network-devices');
    const tab = page.locator(`#tablist li.tab a[data-orb-id="${ORB}"]`);
    await expect(tab).toBeVisible();
    await tab.click();

    const panel = page.locator('[id^="tab-content-generic-network-devices-"]').locator('visible=true');
    await expect(panel.getByTestId('tab-load-failed'),
      'a panel that could not load must SAY so, not sit blank').toBeVisible({ timeout: 15_000 });

    // And it must stay retryable: a panel flagged `loaded` short-circuits every
    // later click, which is what made the blank permanent.
    await expect(panel, 'an empty panel must not be flagged loaded').not.toHaveAttribute('data-loaded', '1');
  } finally {
    await request.post('/graphql', {
      data: { query: `mutation { deleteNetworkDevice(filter: { orbId: { eq: "${ORB}" } }) { numUids } }` },
    });
    await page.evaluate(() => localStorage.removeItem('genericTabs'));
  }
});
