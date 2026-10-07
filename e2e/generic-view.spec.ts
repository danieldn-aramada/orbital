import { openAllDetailPanels, listRows } from './helpers/generic';
import { test, expect } from '@playwright/test';

// The generic renderer: /{slug} serves every ConfigItem type from one template.
//
// These use `network-devices` — every page is rendered by the generic renderer
// works here works because it was derived from the deployed schema, not because
// someone hand-wrote it. A type with its own page would prove nothing.

test('a type with no bespoke page gets a working list page', async ({ page }) => {
  await page.goto('/network-devices');

  // The page heading every other page carries, naming the view. This used to
  // assert the OPPOSITE — `toHaveCount(0)` — because the bespoke list pages
  // had no heading and the generic page copied them. The test encoded the
  // quirk as a requirement, which is how a quirk survives a rewrite.
  await expect(page.getByTestId('page-heading')).toHaveText('Network Devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  await expect(page.locator('#generic-table')).toHaveAttribute('data-slug', 'network-devices');
});

test('the generic page gets the house DataTable toolbar, not a bare table', async ({ page }) => {
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();

  // DataTables replaces the plain <table> with its own wrapper and controls.
  // Without this the page renders but behaves like a static table — which is
  // how it shipped first, and is the regression this pins.
  await expect(page.locator('#generic-table_wrapper')).toBeVisible();
  await expect(page.locator('#generic-table_wrapper input[type="search"]')).toBeVisible();
  await expect(page.locator('#generic-table_wrapper button:has-text("Select")')).toBeVisible();
});

test('the tab bar and tabpanel match the hand-written list pages', async ({ page }) => {
  await page.goto('/network-devices');
  await expect(page.locator('nav.tabs.is-boxed')).toBeVisible();
  await expect(page.locator('#tab-content-generic[role="tabpanel"]')).toBeVisible();
});

test('a row opens a working derived detail view', async ({ page }) => {
  // The row carries its entity on `data-orb-id` — there is no href, because the
  // name is plain text (single click is row selection). `?open=` is how a url
  // addresses one, and it must land on a rendered view rather than a 404 or an
  // empty list.
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();

  const first = listRows(page).first();
  await expect(first).toBeVisible();
  const orbId = (await first.getAttribute('data-orb-id'))!;
  await page.goto('/network-devices?open=' + encodeURIComponent(orbId));

  await expect(page.getByTestId('generic-fields')).toBeVisible();
  await expect(page.locator('#tablist li.tab')).toHaveCount(2);
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
  const orbId = (await listRows(page).first().getAttribute('data-orb-id'))!;
  if (!orbId) test.skip(true, 'no data centers seeded');
  await page.goto('/data-centers?open=' + encodeURIComponent(orbId));

  const serversTab = page.locator('[data-testid="generic-tab"][data-field="servers"]');
  await expect(serversTab).toBeVisible();
  for (const col of ['Hostname', 'Model', 'Service Tag', 'Rack Position']) {
    await expect(serversTab).toContainText(col);
  }
  // And actual values, or the columns are headers over an empty table.
  await expect(serversTab.locator('tbody tr').first()).toBeVisible();
});

// Every list page is the SAME template, so one page proving it is weak — the
// value is that the heading tracks the view rather than being hardcoded.
test('each list page heading names its own view', async ({ page }) => {
  for (const [slug, label] of [['servers', 'Servers'], ['clusters', 'Clusters'], ['network-devices', 'Network Devices']]) {
    await page.goto(`/${slug}`);
    await expect(page.getByTestId('page-heading'), `/${slug} heading`).toHaveText(label);
  }
});

// The heading is ADDED ABOVE the tab bar, not swapped for it. The bar is the
// mount point for detail tabs and #tab-summary is the way back to the list, so
// a change that removes it breaks opening a row — this pins both surviving.
test('the heading does not replace the tab bar', async ({ page }) => {
  await page.goto('/network-devices');
  await expect(page.getByTestId('page-heading')).toBeVisible();
  await expect(page.locator('#tab-summary')).toBeVisible();
});

// The row cap is STATED when it bites, and silent when it does not.
//
// Only the negative is assertable against the dev graph — 190 servers under a
// cap of 500 — and it is the half worth pinning here: a notice that fires when
// nothing was dropped trains people to ignore the one that matters. The
// positive half is covered by TestIsTruncated, which exercises the boundary
// (exactly at the cap, with and without more behind it) without needing 500
// rows of fixture.
test('no truncation notice when everything fits', async ({ page }) => {
  await page.goto('/servers');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId('list-truncated')).toHaveCount(0);
});

// Computed columns — acceptance items 4 and 5.
//
// Two columns were lost when the bespoke pages went: a rack table that counted
// its servers, and a network device's server list that named each server's
// cluster and node role. Both come from one `columns:` entry per view,
// which is why the SAME columns appear on the type's own list page and inside
// any relationship table showing it.
// The rack LIST page is gone — Rack has no page now, so `servers.count` only
// renders where a Rack renders: inside a data centre's Racks tab. That is
// asserted below in "a relationship table shows the child type's computed
// columns", which also checks the VALUE rather than just the header.

test('path columns render on the server list', async ({ page }) => {
  await page.goto('/servers');
  await expect(page.locator('#generic-table tbody tr').first()).toBeVisible({ timeout: 15_000 });
  const headers = await page.locator('#generic-table thead th').allInnerTexts();
  // "Cluster", not "Name": an identity leaf labels by its hop, which also
  // avoids colliding with the row's own Name column.
  expect(headers).toContain('Cluster');
  expect(headers).toContain('Role');
  // "GPU", not "Gpu" — the label is declared on KubernetesNode's view, so the
  // label must come from the type the field belongs to.
  expect(headers).toContain('GPU');
});

test('a relationship table shows the child type\'s computed columns', async ({ page }) => {
  await page.goto('/data-centers?open=' + encodeURIComponent('colo:colo-galleon'));
  await expect(page.getByTestId('generic-fields')).toBeVisible({ timeout: 15_000 });
  await openAllDetailPanels(page);

  const racks = page.getByTestId('generic-tab').filter({ hasText: 'Racks' }).first();
  await expect(racks.locator('thead th').filter({ hasText: 'Servers' })).toHaveCount(1);
  // Subgraph members take a different branch of the detail query, and that
  // branch originally skipped the computed columns — header present, cells
  // empty. This asserts the VALUE, which is what caught it.
  const rackHeaders = await racks.locator('thead th').allInnerTexts();
  const cell = racks.locator('tbody tr').first().locator('td').nth(rackHeaders.indexOf('Servers'));
  expect(parseInt((await cell.innerText()).trim(), 10)).toBeGreaterThan(0);

  const servers = page.getByTestId('generic-tab').filter({ hasText: 'Servers' }).first();
  const srvHeaders = await servers.locator('thead th').allInnerTexts();
  expect(srvHeaders, 'a Server row carries its computed columns wherever it renders').toContain('Cluster');
});

// The case the whole feature was filed for, and the one this spec originally
// did NOT cover: a network device's connected ports, showing what each cabled
// server does in its Kubernetes cluster.
//
// The bespoke page emitted one row per SERVER. The generic page emits one row
// per NetworkInterface, with the server as a link — so the declaration lives on
// NetworkInterface and reaches through `server.kubernetesNode.…`, which is why
// the path limit had to be four segments rather than the three I first set.
//
// Asserted on the NETWORK DEVICE page specifically. The earlier version of this
// coverage asserted on the data centre page instead, which passes without
// testing the thing that was lost.
test('a network device shows what its connected servers do', async ({ page }) => {
  await page.goto('/network-devices?open=' + encodeURIComponent('colo:network-device-JX3623130496'));
  await expect(page.getByTestId('generic-fields')).toBeVisible({ timeout: 15_000 });
  await openAllDetailPanels(page);

  const conns = page.getByTestId('generic-tab').filter({ hasText: 'Connections' }).first();
  const headers = await conns.locator('thead th').allInnerTexts();
  for (const h of ['Server', 'Cluster', 'Role', 'GPU']) {
    expect(headers, `Connections must carry ${h}`).toContain(h);
  }

  // At least one row has a real cluster — headers over universally empty cells
  // is exactly how the subgraph-member branch bug presented.
  const idx = headers.indexOf('Cluster');
  const texts = await conns.locator('tbody tr').allInnerTexts();
  expect(texts.length).toBeGreaterThan(0);
  const values = await Promise.all(
    (await conns.locator('tbody tr').all()).map(r => r.locator('td').nth(idx).innerText()));
  expect(values.some(v => v.trim() !== '' && v.trim() !== '—'),
    'no connected port resolved a cluster — the path is not being fetched').toBeTruthy();
});
