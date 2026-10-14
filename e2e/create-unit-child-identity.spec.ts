import { test, expect } from '@playwright/test';

// A unit child's identity, pinned BEFORE the placeholder scoping is unified.
//
// `idracSettings` is `{server.serviceTag}-idrac` — every placeholder references
// the PARENT, so it works under the old rule where `fill()` resolves from the
// parent's inputs. It must keep working after bare `{x}` starts meaning "the
// child's own field".
//
// This is the regression the unification risks, so it is written first.

const TAG = 'E2EUNITID';
const ORB_ID = `colo:server-${TAG}`;
const IDRAC_ID = `colo:${TAG}-idrac`;

async function clean(request: any) {
  await request.post('/graphql', {
    data: {
      query: `mutation {
        a: deleteIdracSettings(filter: { orbId: { eq: "${IDRAC_ID}" } }) { numUids }
        b: deleteServer(filter: { orbId: { eq: "${ORB_ID}" } }) { numUids }
        c: deleteServerMaintenance(filter: { orbId: { eq: "colo:servermaintenance-${TAG}" } }) { numUids }
      }`,
    },
  });
}
test.beforeEach(async ({ request }) => clean(request));
test.afterEach(async ({ request }) => clean(request));

test('a unit child whose id comes from the PARENT still resolves', async ({ page, request }) => {
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: 'colo:datacenter-colo-galleon' });
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-unit-id');
  await modal.locator('[data-create-field="serviceTag"]:not([data-create-child])').fill(TAG);
  await modal.locator('[data-create-child="idracSettings"][data-create-field="firmwareVersion"]').fill('7.10.00.00');

  await modal.getByTestId('create-submit').click();
  await expect(page.locator(`#generic-table tr[data-orb-id="${ORB_ID}"]`)).toHaveCount(1, { timeout: 15_000 });

  const res = await request.post('/graphql', {
    data: { query: `{ getServer(orbId: "${ORB_ID}") { idracSettings { orbId firmwareVersion } } }` },
  });
  const idrac = (await res.json()).data.getServer.idracSettings;
  expect(idrac, 'the unit child must be created').toBeTruthy();
  expect(idrac.orbId, "its id is built from the PARENT's serviceTag").toBe(IDRAC_ID);
  expect(idrac.firmwareVersion).toBe('7.10.00.00');
});

test('a unit child whose id comes from its OWN field is offered and created', async ({ page, request }) => {
  // IPAddress is `{address}` — a Server has no `address`, so under the old rule
  // the placeholder resolved to nothing and the child was SILENTLY dropped.
  // Adding oobIP to the subgraph appeared to do nothing at all.
  const IP = 'colo:10.99.99.99';
  const clean = () => request.post('/graphql', {
    data: {
      query: `mutation {
        a: deleteIPAddress(filter: { orbId: { eq: "${IP}" } }) { numUids }
        b: deleteIdracSettings(filter: { orbId: { eq: "${IDRAC_ID}" } }) { numUids }
        c: deleteServer(filter: { orbId: { eq: "${ORB_ID}" } }) { numUids }
        d: deleteServerMaintenance(filter: { orbId: { eq: "colo:servermaintenance-${TAG}" } }) { numUids }
      }`,
    },
  });
  await clean();

  try {
    await page.goto('/servers');
    await page.getByTestId('create-open').click();
    const modal = page.locator('.modal.is-active');

    const addr = modal.locator('[data-create-child="oobIP"][data-create-field="address"]');
    await expect(addr, 'a child whose id is its own field must be OFFERED').toHaveCount(1);

    await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: 'colo:datacenter-colo-galleon' });
    await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-own-id');
    await modal.locator('[data-create-field="serviceTag"]:not([data-create-child])').fill(TAG);
    await addr.fill('10.99.99.99');
    await modal.getByTestId('create-submit').click();
    await expect(page.locator(`#generic-table tr[data-orb-id="${ORB_ID}"]`)).toHaveCount(1, { timeout: 15_000 });

    const res = await request.post('/graphql', {
      data: { query: `{ getServer(orbId: "${ORB_ID}") { oobIP { orbId address } } }` },
    });
    const ip = (await res.json()).data.getServer.oobIP;
    expect(ip, 'the IP must be created with the server, in one mutation').toBeTruthy();
    expect(ip.orbId, "its id comes from its OWN address, not the parent").toBe(IP);
    expect(ip.address).toBe('10.99.99.99');
  } finally {
    await clean();
  }
});

test('an identity field is settable at create and frozen afterwards', async ({ page }) => {
  // `createOnly`. The orbId is `server-{serviceTag}` and orbIds are immutable,
  // so editing the tag later leaves the id encoding the OLD one — and a scan
  // rebuilding that id mints a SECOND server for the same machine, breaking the
  // upsert idempotence a derivable orbId exists to give.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  await expect(page.locator('.modal.is-active [data-create-field="serviceTag"]'),
    'settable when the server is created').toHaveCount(1);
  await page.locator('.modal.is-active [data-modal-close]').first().click();

  await page.locator('#generic-table tbody tr[data-orb-id]').first().dblclick();
  const panel = page.locator('[id^="tab-content-generic-servers-"]').locator('visible=true');
  await expect(panel.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });
  const domId = await panel.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  const tree = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);

  expect(Object.keys(tree), 'and frozen once it exists').not.toContain('serviceTag');
  expect(Object.keys(tree), 'while ordinary fields stay editable').toContain('model');
});
