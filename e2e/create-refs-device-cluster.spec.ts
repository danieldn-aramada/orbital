import { test, expect, Page } from '@playwright/test';

// The same `editable: true` references Server got, on the other create forms —
// all optional:
//
//   NetworkDevice          rack, virtualChassis       selects, scoped to the data centre
//   EksaKubernetesCluster  managementCluster          select, scoped to the data centre
//                          controlPlaneEndpoint       create-or-link IP (single back-edge: refused when owned)
//                          tinkerbellIP               create-or-link IP (list back-edge: shared, links)
//
// TEST-NET-1 (192.0.2.0/24) for new addresses, so nothing collides with seed data.

const DC = 'colo:datacenter-colo-galleon';
const DEVICE = 'colo:networkdevice-E2EREFSW1';
const VC_MASTER = 'colo:networkdevice-XH3123090344';
const CLUSTER = 'colo:e2e-ref-cluster';
const NEW_CPE = '192.0.2.30';
const SHARED_TINKERBELL = '10.20.22.222'; // cluster-upgrade-sim's — a list back-edge, so shareable
const OWNED_CPE = '10.20.22.157';          // aramco-m's control-plane endpoint — single, so refused

async function cleanup(request: any) {
  // A cluster created through the form brings its backup children with it.
  const r = await request.post('/graphql', {
    data: { query: `{ c: getEksaKubernetesCluster(orbId: "${CLUSTER}") {
      backup { orbId etcd { orbId } velero { orbId } s3Sync { orbId } } } }` },
  });
  const b = (await r.json()).data?.c?.backup;
  const ids = b ? [b.orbId, b.etcd?.orbId, b.velero?.orbId, b.s3Sync?.orbId].filter(Boolean) : [];
  // Only when there ARE ids: DGraph refuses an empty `in: []` and then skips
  // every later mutation in the request, which left the cluster behind.
  const backups = ids.length ? `
        a: deleteEtcdBackup(filter: { orbId: { in: $ids } }) { numUids }
        b: deleteVeleroBackup(filter: { orbId: { in: $ids } }) { numUids }
        c: deleteS3Sync(filter: { orbId: { in: $ids } }) { numUids }
        d: deleteClusterBackup(filter: { orbId: { in: $ids } }) { numUids }` : '';
  await request.post('/graphql', {
    data: {
      query: `mutation${ids.length ? '($ids: [String!])' : ''} {${backups}
        e: deleteEksaKubernetesCluster(filter: { orbId: { eq: "${CLUSTER}" } }) { numUids }
        f: deleteNetworkDevice(filter: { orbId: { eq: "${DEVICE}" } }) { numUids }
        g: deleteIPAddress(filter: { orbId: { eq: "colo:${NEW_CPE}" } }) { numUids }
      }`,
      variables: ids.length ? { ids } : {},
    },
  });
}
test.beforeEach(async ({ request }) => cleanup(request));
test.afterEach(async ({ request }) => cleanup(request));

async function openForm(page: Page, slug: string) {
  await page.goto('/' + slug);
  await page.getByTestId('create-open').click();
  return page.locator('.modal.is-active');
}

async function visibleNamespaces(modal: any, field: string) {
  return modal.getByTestId('create-rel-' + field).evaluate((sel: HTMLSelectElement) =>
    [...sel.options].filter((o) => o.value && !o.hidden).map((o) => o.dataset.namespace));
}

test('a network device form offers rack and virtual chassis, optional and scoped, and links both', async ({ page, request }) => {
  const modal = await openForm(page, 'network-devices');
  for (const f of ['rack', 'virtualChassis']) {
    await expect(modal.getByTestId('create-rel-' + f), `${f} waits for a data centre`).toBeDisabled();
    await expect(modal.getByTestId('create-rel-' + f), `${f} is optional`).not.toHaveAttribute('required', '');
  }
  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: DC });
  for (const f of ['rack', 'virtualChassis']) {
    expect(new Set(await visibleNamespaces(modal, f)), `${f}: only the chosen data centre's`).toEqual(new Set(['colo']));
  }
  const rack = await modal.getByTestId('create-rel-rack').evaluate((s: HTMLSelectElement) =>
    [...s.options].find((o) => o.value && !o.hidden)!.value);
  await modal.getByTestId('create-rel-rack').selectOption(rack);
  await modal.getByTestId('create-rel-virtualChassis').selectOption(VC_MASTER);
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-ref-switch');
  await modal.locator('[data-create-field="serialNumber"]:not([data-create-child])').fill('E2EREFSW1');
  await modal.getByTestId('create-submit').click();
  await expect(page.locator(`#generic-table tr[data-orb-id="${DEVICE}"]`)).toHaveCount(1, { timeout: 15_000 });

  const res = await request.post('/graphql', {
    data: { query: `{ d: getNetworkDevice(orbId: "${DEVICE}") { rack { orbId } virtualChassis { orbId } } }` },
  });
  const d = (await res.json()).data.d;
  expect(d.rack.orbId).toBe(rack);
  expect(d.virtualChassis.orbId).toBe(VC_MASTER);
});

test('a cluster form creates its control-plane IP, links a shared tinkerbell IP, and links its management cluster', async ({ page, request }) => {
  const modal = await openForm(page, 'clusters');
  await expect(modal.getByTestId('create-rel-managementCluster')).toBeDisabled();
  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: DC });
  expect(new Set(await visibleNamespaces(modal, 'managementCluster'))).toEqual(new Set(['colo']));
  const mgmt = await modal.getByTestId('create-rel-managementCluster').evaluate((s: HTMLSelectElement) =>
    [...s.options].find((o) => o.value && !o.hidden)!.value);
  await modal.getByTestId('create-rel-managementCluster').selectOption(mgmt);

  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-ref-cluster');
  await modal.locator('[data-create-field="clusterType"]:not([data-create-child])').fill('workload');
  await modal.getByTestId('create-ref-controlPlaneEndpoint').fill(NEW_CPE);
  await modal.getByTestId('create-ref-tinkerbellIP').fill(SHARED_TINKERBELL);
  await modal.getByTestId('create-submit').click();
  await expect(page.locator(`#generic-table tr[data-orb-id="${CLUSTER}"]`)).toHaveCount(1, { timeout: 15_000 });

  const res = await request.post('/graphql', {
    data: { query: `{
      c: getEksaKubernetesCluster(orbId: "${CLUSTER}") {
        managementCluster { orbId }
        controlPlaneEndpoint { orbId address role }
        tinkerbellIP { orbId role }
      }
      shared: getIPAddress(orbId: "colo:${SHARED_TINKERBELL}") { eksaKubernetesClusterTinkerbellIP { orbId } }
    }` },
  });
  const d = (await res.json()).data;
  expect(d.c.managementCluster.orbId).toBe(mgmt);
  expect(d.c.controlPlaneEndpoint).toMatchObject({ orbId: `colo:${NEW_CPE}`, address: NEW_CPE, role: 'k8s_api' });
  expect(d.c.tinkerbellIP).toMatchObject({ orbId: `colo:${SHARED_TINKERBELL}`, role: 'tinkerbell' });
  const users = d.shared.eksaKubernetesClusterTinkerbellIP.map((c: any) => c.orbId);
  expect(users, 'a shared tinkerbell IP keeps its other cluster').toEqual(expect.arrayContaining([CLUSTER, 'colo:cluster-upgrade-sim']));
});

test("a control-plane IP that is another cluster's is refused, naming that cluster", async ({ page, request }) => {
  let mutations = 0;
  page.on('request', (r) => {
    if (r.url().includes('/graphql') && (r.postData() || '').includes('addEksaKubernetesCluster')) mutations++;
  });
  const modal = await openForm(page, 'clusters');
  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: DC });
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-ref-cluster');
  await modal.getByTestId('create-ref-controlPlaneEndpoint').fill(OWNED_CPE);
  await modal.getByTestId('create-submit').click();

  await expect(modal.getByTestId('create-error')).toContainText('colo:aramco-m');
  expect(mutations, 'nothing may be sent').toBe(0);
  const res = await request.post('/graphql', {
    data: { query: `{ ip: getIPAddress(orbId: "colo:${OWNED_CPE}") { kubernetesClusterControlPlaneEndpoint { ... on ConfigItem { orbId } } } }` },
  });
  expect((await res.json()).data.ip.kubernetesClusterControlPlaneEndpoint.orbId, 'the IP stays with its cluster').toBe('colo:aramco-m');
});
