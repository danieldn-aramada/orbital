import { test, expect } from '@playwright/test';
import { listRows } from './helpers/generic';

// Row → tab opening on the generic list page.
//
// This is the workflow the hand-written pages have: double-click a row, it
// opens as a tab BELOW the list, and several can be open at once. The generic
// renderer has to match it, or migrating a page onto it is a downgrade.
//
// Uses network-devices: every page is generic now, and Rack is no longer a page.

test('a single click SELECTS the row; it does not open anything', async ({ page }) => {
  // Single click is DataTables row selection (`select: { style: 'os' }`), and
  // the Copy/Excel/CSV buttons export the selection. Binding "open" to it fires
  // both actions from one gesture and makes selecting a row impossible — which
  // is exactly why the hand-written tables opened on DOUBLE click. A
  // single-click opener was added 2026-10-04 without checking what the gesture
  // already meant, and removed the next day.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();

  const row = listRows(page).first();
  await row.click();

  await expect(row, 'a single click must select the row').toHaveClass(/selected/);
  await expect(page.locator('#tablist li.tab'), 'and must not open a tab').toHaveCount(1);
  await expect(page).toHaveURL(/\/network-devices$/);
});

test('the name cell is PLAIN TEXT — no row carries a link to itself', async ({ page }) => {
  // A link here would claim a navigation that does not happen: the tab opens on
  // the page you are already on. It would also hand the detail url — which is
  // the tab's FETCH, not an address — to ⌘-click, copy-link-address and the
  // status bar. Reference cells still link, because those really do leave.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();

  const row = listRows(page).first();
  await expect(row.locator('td').first().locator('a'),
    'the name must not be a link').toHaveCount(0);
  expect(await row.getAttribute('data-orb-id'),
    'the ROW carries the entity, since there is no href to read it from').toBeTruthy();

  // Nothing anywhere in the table points at the detail path.
  const hrefs = await page.locator('#generic-table tbody a').evaluateAll(
    (as) => as.map((a) => a.getAttribute('href') || ''));
  for (const h of hrefs) {
    expect(h, `a row links at the detail path: ${h}`).not.toMatch(/\/[^/?]+\/[^/?]+$/);
  }
});

test('the detail path is a fragment endpoint, not a page', async ({ page }) => {
  // The url a tab fetches must not be reachable as a page: that is what made a
  // per-entity "page" exist at all, which nobody designed and which left two
  // layouts for one thing. 404 for a human, 200 for the tab.
  const res = await page.goto('/network-devices/' + encodeURIComponent('colo:network-device-JX3623130496'));
  expect(res?.status(), 'a human navigating to the fragment url gets 404').toBe(404);

  const asTab = await page.request.get(
    '/network-devices/' + encodeURIComponent('colo:network-device-JX3623130496'),
    { headers: { 'HX-Request': 'true' } });
  expect(asTab.status(), 'the same url with HX-Request is the tab body').toBe(200);
  expect(await asTab.text()).toContain('generic-fields');
});

test('switching back and forth between tabs keeps their contents', async ({ page }) => {
  // The bug this pins: `displayTabContent` hid EVERY `.tab-content` whose id did
  // not match — and `.tab-content` is also the house layout wrapper, so the
  // detail fragment swapped into a tab opens with its own class="tab-content"
  // and no id. Hiding that one was permanent: the function can only un-hide by
  // matching an id, so the panel stayed display:block around a child that was
  // display:none and every tab went blank the moment a second one opened.
  //
  // Asserting the PANEL is visible is what catches it: an element whose only
  // child is display:none has no box, so :visible is false even though the
  // panel's own display is block. Asserting the tab STRIP — which is what the
  // other tests here do — passes throughout.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  const rows = listRows(page);
  await rows.nth(0).dblclick();
  await page.locator('#tab-summary').click();
  await rows.nth(1).dblclick();
  await expect(page.locator('#tablist li.tab')).toHaveCount(3);

  const panels = page.locator('[id^="tab-content-generic-network-devices-"]');
  const tabs = page.locator('#tablist li.tab a');

  // Round trip twice: the first switch is what used to break it, and the second
  // proves coming BACK works too.
  for (const i of [1, 2, 1, 2]) {
    await tabs.nth(i).click();
    const active = panels.filter({ has: page.getByTestId('generic-fields') }).locator('visible=true');
    await expect(active).toHaveCount(1);
    await expect(active.getByTestId('generic-fields')).toBeVisible();
    expect((await active.innerText()).trim().length).toBeGreaterThan(0);
  }

  // Summary still comes back, and no detail panel is left showing behind it.
  await page.locator('#tab-summary').click();
  await expect(page.locator('#generic-table')).toBeVisible();
  await expect(panels.locator('visible=true')).toHaveCount(0);
});

test('Reload inside a tab refreshes that tab and leaves the others open', async ({ page }) => {
  // One button, two meanings: on a full page it reloads the window; inside a
  // tab it must re-fetch THAT panel only. The branch is resolved at click time
  // from the DOM, and getting it wrong is silent in the worst way — a full
  // reload inside a tab looks like it worked while closing every other tab the
  // user had open. Two tabs is the smallest case that can tell them apart.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  const rows = page.locator('#generic-table tbody tr');
  await rows.nth(0).dblclick();
  await expect(page.locator('#tablist li.tab')).toHaveCount(2);
  // Back to Summary first: opening a tab hides the list, so the second row is
  // not clickable until the list panel is showing again.
  await page.locator('#tab-summary').click();
  await rows.nth(1).dblclick();
  await expect(page.locator('#tablist li.tab')).toHaveCount(3);

  const panel = page.locator('[id^="tab-content-generic-network-devices-"]').last();
  await expect(panel.getByTestId('generic-fields')).toBeVisible();
  await panel.locator('.js-generic-reload').click();

  // The panel's body came back...
  await expect(panel.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });
  // ...and nothing navigated, so both tabs survive.
  await expect(page).toHaveURL(/\/network-devices$/);
  await expect(page.locator('#tablist li.tab')).toHaveCount(3);
});

test('double-clicking a row opens it as a tab below the list', async ({ page }) => {
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();

  const firstRow = page.locator('#generic-table tbody tr').first();
  const label = (await firstRow.locator('td').first().innerText()).trim();
  await firstRow.dblclick();

  // A new tab appears next to Summary, and the detail body loads into it.
  const tab = page.locator('#tablist li.tab a', { hasText: label });
  await expect(tab).toBeVisible();
  await expect(page.locator('[id^="tab-content-generic-network-devices-"]')).toBeVisible();
  await expect(page.getByTestId('generic-fields')).toBeVisible();

  // The tab body is the FRAGMENT, not a whole page nested in a div.
  await expect(page.locator('[id^="tab-content-generic-network-devices-"] .app-menu')).toHaveCount(0);
});

test('two rows open as two tabs, which is the point of tabs', async ({ page }) => {
  await page.goto('/network-devices');
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
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  const firstRow = page.locator('#generic-table tbody tr').first();
  const label = (await firstRow.locator('td').first().innerText()).trim();
  await firstRow.dblclick();
  await expect(page.locator('#tablist li.tab a', { hasText: label })).toBeVisible();

  await page.reload();
  await expect(page.locator('#tablist li.tab a', { hasText: label })).toBeVisible();
});

test('a tab from another slug is not restored onto this page', async ({ page }) => {
  // A tab restored onto a different page would fetch the wrong entity and
  // render an error where a tab should be. Tabs are slug-qualified for that.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  await page.locator('#generic-table tbody tr').first().dblclick();
  await expect(page.locator('#tablist li.tab')).toHaveCount(2);

  await page.goto('/servers');
  await expect(page.locator('#generic-table')).toBeVisible();
  await expect(page.locator('#tablist li.tab')).toHaveCount(1); // Summary only
});

test('closing the active tab lands on the one to its LEFT, not on Summary', async ({ page }) => {
  // It clicked Summary unconditionally, so closing the last of five tabs threw
  // you back to the list every time. Left-neighbour terminates naturally: the
  // leftmost detail tab's neighbour IS Summary, so that case needs no branch.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  const rows = listRows(page);
  const labels: string[] = [];
  for (const i of [0, 1, 2]) {
    await page.locator('#tab-summary').click();
    labels.push((await rows.nth(i).locator('td').first().innerText()).trim());
    await rows.nth(i).dblclick();
  }
  await expect(page.locator('#tablist li.tab')).toHaveCount(4);

  const esc = (t: string) => new RegExp(t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  // The third is active; closing it must land on the SECOND.
  await page.locator('#tablist li.tab', { hasText: labels[2] }).locator('[id^="tab-close-generic-"]').click();
  await expect(page.locator('#tablist li.tab.is-active a')).toHaveText(esc(labels[1]));

  // ...and again, landing on the first.
  await page.locator('#tablist li.tab', { hasText: labels[1] }).locator('[id^="tab-close-generic-"]').click();
  await expect(page.locator('#tablist li.tab.is-active a')).toHaveText(esc(labels[0]));

  // The leftmost detail tab's neighbour is Summary — same rule, no special case.
  await page.locator('#tablist li.tab', { hasText: labels[0] }).locator('[id^="tab-close-generic-"]').click();
  await expect(page.locator('#tablist li.tab.is-active a')).toHaveText(/Summary/);
  await expect(page.locator('#generic-table')).toBeVisible();
});

test('closing a BACKGROUND tab leaves you where you were', async ({ page }) => {
  // The other half of the same handler, and the reason it needed a decision:
  // tidying away a tab you are not looking at must not yank you out of the one
  // you are. Closing Summary-adjacent tabs used to send you to Summary whichever
  // tab you were reading.
  await page.goto('/network-devices');
  const rows = listRows(page);
  const labels: string[] = [];
  for (const i of [0, 1, 2]) {
    await page.locator('#tab-summary').click();
    labels.push((await rows.nth(i).locator('td').first().innerText()).trim());
    await rows.nth(i).dblclick();
  }
  const esc = (t: string) => new RegExp(t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  await expect(page.locator('#tablist li.tab.is-active a')).toHaveText(esc(labels[2]));

  // Close the FIRST one while sitting on the third.
  await page.locator('#tablist li.tab', { hasText: labels[0] }).locator('[id^="tab-close-generic-"]').click();
  await expect(page.locator('#tablist li.tab')).toHaveCount(3);
  await expect(page.locator('#tablist li.tab.is-active a'),
    'closing a background tab must not move focus').toHaveText(esc(labels[2]));
  // Still showing its content, not an empty panel.
  const panel = page.locator('[id^="tab-content-generic-network-devices-"]').locator('visible=true');
  await expect(panel.getByTestId('generic-fields')).toBeVisible();
});

test('closing a tab removes it and it stays closed after reload', async ({ page }) => {
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  await page.locator('#generic-table tbody tr').first().dblclick();
  await expect(page.locator('#tablist li.tab')).toHaveCount(2);

  await page.locator('[id^="tab-close-generic-network-devices-"]').first().click();
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
  await page.goto('/data-centers?open=' + encodeURIComponent('colo:colo-galleon'));
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

// The is-boxed underline is what makes a tab look attached to its panel, and
// a strip with more tabs than fit WRAPS rather than clipping. Bulma's default
// is nowrap + overflow-x:auto, which cut the last tab off mid-word with no
// sign that more existed — a tab nobody finds.
test('the tab strip keeps its underline and never clips a tab', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/servers?open=' + encodeURIComponent('colo:server-7MP6K74'));
  await expect(page.getByTestId('generic-fields')).toBeVisible({ timeout: 15_000 });

  const ul = page.locator('[id^="generic-detail-tabs-"] ul');
  const width = await ul.evaluate(el => getComputedStyle(el).borderBottomWidth);
  expect(width, 'the is-boxed underline must not be removed to make tabs wrap').not.toBe('0px');

  const overflow = await ul.evaluate(el => el.scrollWidth - el.clientWidth);
  expect(overflow, 'the strip overflows sideways — a tab is clipped instead of wrapping').toBeLessThanOrEqual(1);
  const right = (await ul.boundingBox())!.x + (await ul.boundingBox())!.width;
  const lis = page.locator('[id^="generic-detail-tabs-"] li');
  for (let i = 0; i < await lis.count(); i++) {
    const b = (await lis.nth(i).boundingBox())!;
    expect(b.x + b.width, `tab ${i} extends past the strip`).toBeLessThanOrEqual(right + 1);
  }
});

// ── Acceptance 1–3: lazy restoration ────────────────────────────────────────

test('arriving at a list page fetches ONLY the restored tab, not every saved one', async ({ page }) => {
  // Acceptance 1. Restoration used to CLICK every rebuilt tab, which fired one
  // detail request per saved tab on arrival — for panels nobody had opened. The
  // strip must still come back in full, and the tab you were ON is selected and
  // loaded; the rest wait until you open them. One, not N, is the whole claim:
  // asserting ZERO would pass equally well for a restore that silently does
  // nothing, which is the bug that hid here for a month.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  const rows = listRows(page);
  for (const i of [0, 1, 2]) {
    await page.locator('#tab-summary').click();
    await rows.nth(i).dblclick();
  }
  await expect(page.locator('#tablist li.tab')).toHaveCount(4);

  let detailFetches = 0;
  page.on('request', (r) => {
    const t = r.resourceType();
    if ((t === 'xhr' || t === 'fetch') && /\/network-devices\/[^/]+$/.test(new URL(r.url()).pathname)) detailFetches++;
  });
  await page.goto('/network-devices');          // a fresh arrival, not a soft nav
  await expect(page.locator('#tablist li.tab')).toHaveCount(4);
  await page.waitForTimeout(1500);
  expect(detailFetches,
    'only the restored tab loads; the others wait to be opened').toBe(1);
});

test('the tab you were on comes back selected', async ({ page }) => {
  // The hand-written pages did this and the generic page lost it: the writer
  // went through getTabStorageKey() and the reader read a literal `tabCurrent`
  // that nothing writes, so every visit landed on Summary. Persisting the strip
  // without the selection is half a feature.
  await page.goto('/network-devices');
  const rows = listRows(page);
  await rows.nth(0).dblclick();
  await page.locator('#tab-summary').click();
  const label = (await rows.nth(1).locator('td').first().innerText()).trim();
  await rows.nth(1).dblclick();

  await page.goto('/network-devices');
  const active = page.locator('#tablist li.tab.is-active a');
  await expect(active).toHaveText(new RegExp(label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  // ...and it is LOADED, not an empty selected panel.
  const panel = page.locator('[id^="tab-content-generic-network-devices-"]').locator('visible=true');
  await expect(panel.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });
});

test('the selected tab is per SLUG — one list page does not steal another\'s', async ({ page }) => {
  // Every generic list page shared one `dcTabCurrent` key, so the tab left open
  // on /clusters was looked up on /network-devices, found nothing, and the
  // restore quietly did nothing. The key is slug-qualified now, the same way the
  // tab LIST already was.
  await page.goto('/network-devices');
  const ndRows = listRows(page);
  const ndLabel = (await ndRows.nth(0).locator('td').first().innerText()).trim();
  await ndRows.nth(0).dblclick();

  await page.goto('/clusters');
  const cRows = listRows(page);
  const cLabel = (await cRows.nth(0).locator('td').first().innerText()).trim();
  await cRows.nth(0).dblclick();

  // Each page comes back on ITS own tab, not on the other's.
  await page.goto('/network-devices');
  await expect(page.locator('#tablist li.tab.is-active a'))
    .toHaveText(new RegExp(ndLabel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));

  await page.goto('/clusters');
  await expect(page.locator('#tablist li.tab.is-active a'))
    .toHaveText(new RegExp(cLabel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
});

test('a deferred tab fetches once when selected, and not again on return', async ({ page }) => {
  // Acceptance 2. The deferral must not become "never": selecting a tab that
  // was restored-but-not-loaded has to load it, and the existing `loaded` guard
  // still has to hold afterwards.
  //
  // THREE tabs, because the third is the one restored on arrival and is already
  // loaded — with only two, the test cannot tell "deferred then loaded" apart
  // from "loaded by the restore".
  await page.goto('/network-devices');
  const rows = listRows(page);
  for (const i of [0, 1, 2]) {
    await page.locator('#tab-summary').click();
    await rows.nth(i).dblclick();
  }
  await page.goto('/network-devices');
  await expect(page.locator('#tablist li.tab')).toHaveCount(4);

  let fetches = 0;
  page.on('request', (r) => {
    const t = r.resourceType();
    if ((t === 'xhr' || t === 'fetch') && /\/network-devices\/[^/]+$/.test(new URL(r.url()).pathname)) fetches++;
  });

  const tabs = page.locator('#tablist li.tab a');
  const active = page.locator('[id^="tab-content-generic-network-devices-"]').locator('visible=true');

  await tabs.nth(1).click();
  await expect(active.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });
  expect(fetches, 'selecting a deferred tab loads it').toBe(1);

  await tabs.nth(2).click();
  await expect(active.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });
  expect(fetches, 'and the next deferred tab loads once too').toBe(2);

  await tabs.nth(1).click();
  await expect(active.getByTestId('generic-fields')).toBeVisible();
  await tabs.nth(3).click();   // the one the arrival restored — loaded already
  await expect(active.getByTestId('generic-fields')).toBeVisible();
  expect(fetches, 'returning to an already-loaded tab must not refetch').toBe(2);
});

test('every restored tab has content — none is left permanently blank', async ({ page }) => {
  // Acceptance 3. Eager restoration fired its requests concurrently, one was
  // dropped, and `loaded` had already been set OPTIMISTICALLY — so that tab
  // stayed empty forever with no error anywhere. Measured 2 fetches for 3 tabs.
  // The flag is now set only after the swap, and the requests are no longer
  // concurrent, so every tab must fill.
  await page.goto('/network-devices');
  const rows = listRows(page);
  for (const i of [0, 1, 2]) {
    await page.locator('#tab-summary').click();
    await rows.nth(i).dblclick();
  }
  await page.goto('/network-devices');

  const tabs = page.locator('#tablist li.tab a');
  for (let i = 1; i < 4; i++) {
    await tabs.nth(i).click();
    const active = page.locator('[id^="tab-content-generic-network-devices-"]').locator('visible=true');
    await expect(active.getByTestId('generic-fields'),
      `restored tab ${i} came back blank`).toBeVisible({ timeout: 10_000 });
  }
});

// ── Acceptance 4: the editor works inside a tab ─────────────────────────────

test('Edit inside a tab opens the editor and saves', async ({ page }) => {
  // Acceptance 4. The modal was rendered in the PAGE block, outside the shared
  // body, so it shipped with the full page and not with the tab fragment: the
  // Edit button was present, resolved no modal, and did nothing — silently, with
  // no console error. The old hand-written fragment rendered its own modal for
  // exactly this reason.
  await page.goto('/network-devices');
  await listRows(page).first().dblclick();
  const panel = page.locator('[id^="tab-content-generic-network-devices-"]').locator('visible=true');
  await expect(panel.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });

  const domId = await panel.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await panel.locator('[data-generic-edit-id]').click();
  await expect(page.locator('.modal.is-active')).toBeVisible();

  const initial = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);
  const marker = 40 + (Math.floor(Date.now() / 1000) % 9);
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, rackPosition: marker } });
  await page.locator('[id^="generic-edit-submit-"]').click();

  // The save re-renders THIS panel, not the whole window — a blanket reload
  // would throw the reader back to the list and (lazily) unload the tab.
  await expect(panel.getByTestId('generic-fields')).toContainText(String(marker), { timeout: 15_000 });
  await expect(page).toHaveURL(/\/network-devices$/);
  await expect(page.locator('#tablist li.tab')).toHaveCount(2);
});

// ── Acceptance 5–6: the cap ─────────────────────────────────────────────────

// Uses /clusters, not /network-devices: the cap tests need SIX rows to open and
// the seeded graph has exactly five network devices — at which point the test
// would pass for the wrong reason, never reaching the cap at all.
test('a sixth tab evicts the least recently USED one and says so', async ({ page }) => {
  // Acceptance 5 and 6 together. Six opens, five survive — and the one evicted
  // must be the one not looked at, NOT the one opened first. Open-order would
  // discard the tab the reader keeps returning to, which is the opposite of
  // what they want kept.
  await page.goto('/clusters');
  await expect(page.locator('#generic-table')).toBeVisible();
  const rows = listRows(page);

  const labels: string[] = [];
  for (let i = 0; i < 5; i++) {
    await page.locator('#tab-summary').click();
    labels.push((await rows.nth(i).locator('td').first().innerText()).trim());
    await rows.nth(i).dblclick();
  }
  await expect(page.locator('#tablist li.tab')).toHaveCount(6);   // Summary + 5

  // Re-visit the FIRST tab, making the second the least recently used.
  await page.locator('#tablist li.tab a', { hasText: labels[0] }).click();

  await page.locator('#tab-summary').click();
  const sixth = (await rows.nth(5).locator('td').first().innerText()).trim();
  await rows.nth(5).dblclick();

  await expect(page.locator('#tablist li.tab')).toHaveCount(6);   // still capped
  await expect(page.locator('#tablist li.tab a', { hasText: sixth })).toBeVisible();
  await expect(page.locator('#tablist li.tab a', { hasText: labels[1] })).toHaveCount(0);
  await expect(page.locator('#tablist li.tab a', { hasText: labels[0] }),
    'the re-visited tab must survive — eviction is least recently USED').toBeVisible();

  // Said out loud, naming what went.
  await expect(page.locator('.toast, .notification').filter({ hasText: labels[1] })).toBeVisible();
});

test('the cap survives a reload — restoration cannot exceed it', async ({ page }) => {
  await page.goto('/clusters');
  const rows = listRows(page);
  for (let i = 0; i < 6; i++) {
    await page.locator('#tab-summary').click();
    await rows.nth(i).dblclick();
  }
  await page.goto('/clusters');
  await expect(page.locator('#tablist li.tab')).toHaveCount(6);   // Summary + 5
});
