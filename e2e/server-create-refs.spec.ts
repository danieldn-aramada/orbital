import { test, expect, Page } from '@playwright/test';

// References on the Server create form: `rack` (a select of existing racks, in
// the chosen data centre's namespace) and `oobIP` (create-or-link by address,
// required). Names transcribed from the acceptance list:
//
//   2      the rack list follows the chosen data centre
//   9      an OOB IP that is not an address is refused, and nothing is sent
//   3, 6   a new OOB IP is created with the server, and the chosen rack is linked
//   7      an existing IP no server uses is linked, not duplicated
//   8      an IP that is already another server's OOB IP is refused, naming that server
//
// Addresses are TEST-NET-1 (192.0.2.0/24), reserved for documentation, so they
// cannot collide with a seeded IP.

const DC = 'colo:datacenter-colo-galleon';
const OTHER_DC = '2f-uae:datacenter-2f-uae';
const TAG1 = 'E2EREF1';
const TAG2 = 'E2EREF2';
const IP_NEW = '192.0.2.10';
const IP_FREE = '192.0.2.11';

async function cleanup(request: any) {
  await request.post('/graphql', {
    data: {
      query: `mutation {
        a: deleteServer(filter: { orbId: { in: ["colo:server-${TAG1}", "colo:server-${TAG2}"] } }) { numUids }
        b: deleteIPAddress(filter: { orbId: { in: ["colo:${IP_NEW}", "colo:${IP_FREE}"] } }) { numUids }
        c: deleteServerMaintenance(filter: { orbId: { in: ["colo:servermaintenance-${TAG1}", "colo:servermaintenance-${TAG2}"] } }) { numUids }
        d: deleteIdracSettings(filter: { orbId: { in: ["colo:${TAG1}-idrac", "colo:${TAG2}-idrac"] } }) { numUids }
      }`,
    },
  });
}
test.beforeEach(async ({ request }) => cleanup(request));
test.afterEach(async ({ request }) => cleanup(request));

async function openServerForm(page: Page) {
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  return page.locator('.modal.is-active');
}

// The namespaces of the rack options a user can actually pick.
async function visibleRackNamespaces(page: Page) {
  return page.getByTestId('create-rel-rack').evaluate((sel: HTMLSelectElement) =>
    [...sel.options].filter((o) => o.value && !o.hidden).map((o) => o.dataset.namespace));
}

async function fillServer(modal: any, tag: string, ip: string) {
  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: DC });
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-' + tag.toLowerCase());
  await modal.locator('[data-create-field="serviceTag"]:not([data-create-child])').fill(tag);
  await modal.getByTestId('create-ref-oobIP').fill(ip);
}

test('the rack list follows the chosen data centre', async ({ page }) => {
  const modal = await openServerForm(page);
  const rack = modal.getByTestId('create-rel-rack');
  await expect(rack, 'no data centre yet, so no rack can be chosen').toBeDisabled();

  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: DC });
  await expect(rack).toBeEnabled();
  const colo = await visibleRackNamespaces(page);
  expect(colo.length).toBeGreaterThan(0);
  expect(new Set(colo), 'only racks in the chosen data centre').toEqual(new Set(['colo']));

  const first = await rack.evaluate((sel: HTMLSelectElement) =>
    [...sel.options].find((o) => o.value && !o.hidden)!.value);
  await rack.selectOption(first);

  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: OTHER_DC });
  await expect(rack, 'a rack chosen for one data centre is cleared for another').toHaveValue('');
  expect(new Set(await visibleRackNamespaces(page))).toEqual(new Set(['2f-uae']));
});

test('an OOB IP that is not an address is refused, and nothing is sent', async ({ page }) => {
  let mutations = 0;
  page.on('request', (r) => {
    if (r.url().includes('/graphql') && (r.postData() || '').includes('addServer')) mutations++;
  });
  const modal = await openServerForm(page);
  await fillServer(modal, TAG1, 'not-an-ip');
  await modal.getByTestId('create-submit').click();

  await expect(modal.getByTestId('create-error')).toContainText('not a valid address');
  expect(mutations).toBe(0);
});

test('a new OOB IP is created with the server, and the chosen rack is linked', async ({ page, request }) => {
  const modal = await openServerForm(page);
  await fillServer(modal, TAG1, IP_NEW);
  const rack = await modal.getByTestId('create-rel-rack').evaluate((sel: HTMLSelectElement) =>
    [...sel.options].find((o) => o.value && !o.hidden)!.value);
  await modal.getByTestId('create-rel-rack').selectOption(rack);
  await modal.getByTestId('create-submit').click();
  await expect(page.locator(`#generic-table tr[data-orb-id="colo:server-${TAG1}"]`)).toHaveCount(1, { timeout: 15_000 });

  const res = await request.post('/graphql', {
    data: { query: `{ getServer(orbId: "colo:server-${TAG1}") {
      rack { orbId name }
      oobIP { orbId address role namespace version }
    } }` },
  });
  const s = (await res.json()).data.getServer;
  expect(s.rack.orbId).toBe(rack);
  expect(s.oobIP).toMatchObject({ orbId: `colo:${IP_NEW}`, address: IP_NEW, role: 'oob', namespace: 'colo', version: 1 });

  // And the server's page names the rack.
  await page.goto('/servers?open=' + encodeURIComponent(`colo:server-${TAG1}`));
  const panel = page.locator('[data-testid="generic-owned"][data-field="rack"]');
  await expect(panel).toContainText(s.rack.name);
});

test('an existing IP no server uses is linked, not duplicated', async ({ page, request }) => {
  // An IP that exists for another reason: a node's address, with its own role.
  await request.post('/graphql', {
    data: {
      query: `mutation($input: [AddIPAddressInput!]!) { addIPAddress(input: $input) { numUids } }`,
      variables: { input: [{ orbId: `colo:${IP_FREE}`, namespace: 'colo', address: IP_FREE, role: 'k8s_node', version: 1 }] },
    },
  });

  const modal = await openServerForm(page);
  await fillServer(modal, TAG2, IP_FREE);
  await modal.getByTestId('create-submit').click();
  await expect(page.locator(`#generic-table tr[data-orb-id="colo:server-${TAG2}"]`)).toHaveCount(1, { timeout: 15_000 });

  const res = await request.post('/graphql', {
    data: { query: `{
      getServer(orbId: "colo:server-${TAG2}") { oobIP { orbId } }
      queryIPAddress(filter: { address: { eq: "${IP_FREE}" } }) { orbId role }
    }` },
  });
  const d = (await res.json()).data;
  expect(d.getServer.oobIP.orbId).toBe(`colo:${IP_FREE}`);
  expect(d.queryIPAddress, 'linked, not created a second time').toHaveLength(1);
  expect(d.queryIPAddress[0].role, 'a linked IP keeps its own values').toBe('k8s_node');
});

test("an IP that is already another server's OOB IP is refused, naming that server", async ({ page, request }) => {
  await request.post('/graphql', {
    data: {
      query: `mutation($input: [AddServerInput!]!) { addServer(input: $input) { numUids } }`,
      variables: { input: [{
        orbId: `colo:server-${TAG1}`, namespace: 'colo', name: 'e2e-owner', serviceTag: TAG1, version: 1,
        dataCenter: { orbId: DC },
        oobIP: { orbId: `colo:${IP_NEW}`, namespace: 'colo', address: IP_NEW, version: 1 },
      }] },
    },
  });
  let mutations = 0;
  page.on('request', (r) => {
    if (r.url().includes('/graphql') && (r.postData() || '').includes('addServer')) mutations++;
  });

  const modal = await openServerForm(page);
  await fillServer(modal, TAG2, IP_NEW);
  await modal.getByTestId('create-submit').click();

  await expect(modal.getByTestId('create-error')).toContainText(`colo:server-${TAG1}`);
  expect(mutations, 'nothing may be sent').toBe(0);
  const res = await request.post('/graphql', {
    data: { query: `{ getIPAddress(orbId: "colo:${IP_NEW}") { serverOobIP { orbId } } }` },
  });
  expect((await res.json()).data.getIPAddress.serverOobIP.orbId, 'the IP stays with its server').toBe(`colo:server-${TAG1}`);
});
