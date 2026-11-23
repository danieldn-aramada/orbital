import { test, expect, Page, APIRequestContext } from '@playwright/test';

// Adding and deleting list members from the parent's edit form: a cluster's
// nodes. Its own namespace, created and removed here.

const NS = 'e2e-members';
const DC = `${NS}:datacenter-${NS}`;
const H1 = `${NS}:server-h1`;
const H2 = `${NS}:server-h2`;
const CLUSTER = `${NS}:azurelocalkubernetescluster-c1`;
const N1 = `${NS}:n1`;
const N2 = `${NS}:n2`;

async function gql(request: APIRequestContext, query: string, variables: any = {}) {
  const res = await request.post('/graphql', { data: { query, variables } });
  const body = await res.json();
  expect(body.errors, JSON.stringify(body.errors)).toBeUndefined();
  return body.data;
}

const node = async (request: APIRequestContext, orbId: string) =>
  (await gql(request, `query($id: String!) { n: queryKubernetesNode(filter: {orbId: {eq: $id}}) { orbId version server { orbId } } }`,
    { id: orbId })).n[0] ?? null;

async function clean(request: APIRequestContext) {
  for (const [op, ids] of [
    ['deleteKubernetesNode', [N1, N2]], ['deleteAzureLocalKubernetesCluster', [CLUSTER]],
    ['deleteServer', [H1, H2]], ['deleteDataCenter', [DC]],
  ] as const) {
    for (const orbId of ids) {
      await request.post('/graphql', { data: {
        query: `mutation C($orbId: String!) { ${op}(filter: { orbId: { eq: $orbId } }) { numUids } }`, variables: { orbId } } });
    }
  }
  await request.post('/graphql', { data: {
    query: `mutation C($name: String!) { deleteNamespace(filter: { name: { eq: $name } }) { numUids } }`, variables: { name: NS } } });
}

test.beforeEach(async ({ request }) => {
  await clean(request);
  await gql(request, `mutation S($ns: [AddNamespaceInput!]!, $dc: [AddDataCenterInput!]!) {
      addNamespace(input: $ns, upsert: true) { numUids }
      addDataCenter(input: $dc, upsert: true) { numUids } }`, {
    ns: [{ name: NS }],
    dc: [{ namespace: NS, orbId: DC, name: NS, version: 1, servers: [
      { namespace: NS, orbId: H1, name: 'h1', version: 1 },
      { namespace: NS, orbId: H2, name: 'h2', version: 1 }] }],
  });
  await gql(request, `mutation S($c: [AddAzureLocalKubernetesClusterInput!]!) {
      addAzureLocalKubernetesCluster(input: $c, upsert: true) { numUids } }`, {
    c: [{ namespace: NS, orbId: CLUSTER, name: 'c1', version: 1, dataCenter: { orbId: DC },
      nodes: [{ namespace: NS, orbId: N1, name: 'n1', version: 1, server: { orbId: H1 } }] }],
  });
});
test.afterEach(async ({ request }) => clean(request));

async function openEdit(page: Page) {
  await page.goto('/clusters?open=' + encodeURIComponent(CLUSTER));
  await page.locator('[data-generic-edit-id]').first().click();
  const modal = page.locator('.modal.is-active[id^="edit-modal-generic-"]');
  await expect(modal.locator('[data-edit-member-list="nodes"]')).toBeVisible();
  return modal;
}

async function save(page: Page, modal: any) {
  await modal.locator('[id^="generic-edit-submit-"]').click();
}

test('a node added in the edit form is created on the server chosen for it', async ({ page, request }) => {
  const modal = await openEdit(page);
  await modal.getByTestId('edit-add-row').click();
  const row = modal.locator('[data-edit-new-row="nodes"]').last();
  await row.locator('[data-create-row-field="name"]').fill('n2');
  await expect(row.getByTestId('edit-new-row-orbid')).toHaveText(N2);
  await row.locator('[data-create-row-rel="server"]').selectOption(H2);
  await save(page, modal);
  await expect(page.locator('.modal.is-active')).toHaveCount(0, { timeout: 10_000 });

  expect((await node(request, N2))?.server.orbId).toBe(H2);
});

test('Delete previews before Save, deletes only the node, and is audited against it', async ({ page, request }) => {
  const modal = await openEdit(page);
  const row = modal.locator(`[data-edit-member-row][data-row-orb-id="${N1}"]`);
  await row.getByTestId('edit-delete-row').check();
  await expect(row.getByTestId('edit-delete-preview'), 'the delete is named before it happens')
    .toContainText('Will not be deleted');
  const since = new Date().toISOString();
  await save(page, modal);
  await expect(page.locator('.modal.is-active')).toHaveCount(0, { timeout: 10_000 });

  expect(await node(request, N1), 'the node is deleted').toBeNull();
  const h1 = (await gql(request, `query($id: String!) { s: queryServer(filter: {orbId: {eq: $id}}) { orbId kubernetesNodes { orbId } } }`,
    { id: H1 })).s[0];
  expect(h1, 'its server is kept').toBeTruthy();
  expect(h1.kubernetesNodes, 'and no longer lists it').toEqual([]);

  // Read back through the consumer API: the delete is recorded against the node, not the server.
  const res = await request.get('/api/v1/audit-log?namespace=' + NS + '&createdAt_gte=' + encodeURIComponent(since));
  const events = (await res.json()).events as any[];
  expect(events.some(e => e.operations.includes('deleteKubernetesNode') && e.resourceIds.includes(N1))).toBe(true);
  expect(events.some(e => e.operations.includes('deleteServer') && e.resourceIds.includes(H1))).toBe(false);
});

test('a node changed by someone else after the form opened is not deleted', async ({ page, request }) => {
  const modal = await openEdit(page);
  const row = modal.locator(`[data-edit-member-row][data-row-orb-id="${N1}"]`);
  await row.getByTestId('edit-delete-row').check();
  await expect(row.getByTestId('edit-delete-preview')).toContainText('Total:');
  await gql(request, `mutation U($orbId: String!, $set: KubernetesNodePatch!) {
      updateKubernetesNode(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`,
  { orbId: N1, set: { notes: 'edited elsewhere' } });
  await save(page, modal);

  await expect(modal.locator('[id^="generic-edit-error-"]')).toContainText('changed by someone else');
  expect(await node(request, N1), 'the node survives').not.toBeNull();
});
