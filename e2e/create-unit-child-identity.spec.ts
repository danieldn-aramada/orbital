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
        d: deleteIPAddress(filter: { orbId: { eq: "colo:192.0.2.21" } }) { numUids }
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

  await modal.getByTestId('create-ref-oobIP').fill('192.0.2.21'); // required on the form
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

// A child whose id comes from its OWN field (IPAddress is `{address}`) used to
// be tested here as `oobIP`, an inline subgraph child. `oobIP` is now a
// reference the form creates or links — see server-create-refs.spec.ts.
