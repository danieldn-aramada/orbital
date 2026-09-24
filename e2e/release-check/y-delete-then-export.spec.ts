/**
 * A delete must not break the export of the data centre it happened in.
 *
 * This is the gap that let a real bug ship. orbital's cascade delete is a DQL
 * upsert (bulkDeleteGuarded) — deliberately, because that is the only way to get
 * a version-guarded CAS — but `@hasInverse` is a GraphQL-layer construct DGraph
 * maintains only for mutations through its GraphQL endpoint. A DQL `S * *`
 * delete cleared the child and left the parent's list edge pointing at an empty
 * uid. Any later query walking that edge and selecting a non-nullable field then
 * failed ENTIRELY, because DGraph propagates the error to the root:
 *
 *   Non-nullable field 'orbId' (type String!) was not present in result from Dgraph.
 *
 * The export subgraph query is exactly that shape, so ONE cluster delete
 * permanently broke export for its whole data centre — while the delete returned
 * 200 with a correct audit event. Eight such corpses accumulated on colo-galleon,
 * one per e2e run, and were found only when a human tried to export (2026-09-23).
 *
 * Neither gate would have caught it: the fast e2e suite deletes clusters but
 * never exports, and release-check exported but never deleted. This spec is the
 * seam between them, which is why it belongs here rather than in either.
 *
 * Runs as `y-` so it lands after release-checklist and before z-divergence:
 * workers: 1 and fullyParallel: false mean file order is execution order.
 */

import { test, expect } from '@playwright/test';

// Unique per run. A run that fails mid-test leaves a deleted cluster behind —
// and before the fix that meant a DANGLING edge, which poisons the fixture: the
// next run's sanity query walks it, DGraph propagates the non-nullable error,
// and the test fails at setup for a reason that has nothing to do with the run.
// (That happened while proving this test can fail.) A fresh namespace per run
// makes any leftover inert; cleanup on success keeps it from accumulating.
const NS = `delcheck-${Date.now()}`;
const DC = `${NS}:dc-delete-export`;
const CLUSTER = `${NS}:cluster-delete-export`;

const ORBITAL_DGRAPH = 'http://localhost:8080/graphql';

// Through orbital's proxy — the path a real client uses, and the one whose
// write gate this test wants exercised.
async function gql(request: any, query: string, variables: any = {}) {
  const res = await request.post('/graphql', { data: { query, variables } });
  return res.json();
}

// Straight to DGraph, for fixture plumbing orbital's proxy does not accept.
// Namespace is not a ConfigItem, and the proxy refuses addNamespace — fine,
// since the namespace is scaffolding here, not the thing under test. Asserted
// rather than fire-and-forget: a silently failed fixture surfaces much later
// as a confusing export error ("namespace not found"), which is exactly how
// this test failed the first time it ran.
async function dgraph(request: any, query: string, variables: any = {}) {
  const res = await request.post(ORBITAL_DGRAPH, { data: { query, variables } });
  const body = await res.json();
  expect(body.errors, `fixture mutation failed: ${JSON.stringify(body.errors)}`).toBeFalsy();
  return body;
}

test('a cluster delete leaves its data centre exportable', async ({ page }) => {
  // Own fixture, own namespace: this test deletes, and deleting seeded data
  // would change what every other spec in this file order sees.
  //
  // The Namespace NODE is required, not decorative: export resolves the data
  // centre's namespace against it and fails with `namespace "…" not found in
  // DGraph` if it is missing. A ConfigItem carrying `namespace: "x"` as a
  // string is not the same thing as a Namespace node named "x".
  await dgraph(page.request, `mutation($input:[AddNamespaceInput!]!){
    addNamespace(input:$input, upsert:true){ numUids } }`,
    { input: [{ name: NS }] });
  // Assert EXISTENCE, not numUids: an upsert over a namespace left behind by an
  // interrupted run returns 0 inserted and is perfectly fine.
  const nsCheck = await dgraph(page.request, `query($n:String!){
    queryNamespace(filter:{name:{eq:$n}}){ name } }`, { n: NS });
  expect(nsCheck.data?.queryNamespace?.length,
    `fixture: namespace ${NS} must exist — export resolves the DC's namespace against it`).toBe(1);
  await gql(page.request, `mutation($input:[AddDataCenterInput!]!){
    addDataCenter(input:$input, upsert:true){ numUids } }`,
    { input: [{ namespace: NS, orbId: DC, name: 'delete-export dc', version: 1 }] });
  await gql(page.request, `mutation($input:[AddEksaKubernetesClusterInput!]!){
    addEksaKubernetesCluster(input:$input, upsert:true){ numUids } }`,
    { input: [{ namespace: NS, orbId: CLUSTER, name: 'delete-export cluster', version: 1,
                dataCenter: { orbId: DC } }] });

  // Sanity: the DC lists its cluster before the delete, so an empty list
  // afterwards means "removed", not "never linked".
  const before = await gql(page.request, `query($o:String!){
    queryDataCenter(filter:{orbId:{eq:$o}}){ kubernetesClusters{ ... on ConfigItem { orbId } } } }`,
    { o: DC });
  expect(
    before.data?.queryDataCenter?.[0]?.kubernetesClusters?.length,
    'fixture: the data centre must list its cluster before the delete',
  ).toBe(1);

  const del = await page.request.delete(
    `/api/v1/config-items/KubernetesCluster/${encodeURIComponent(CLUSTER)}`);
  expect(del.status(), `delete cluster: expect 200 (got ${del.status()}: ${await del.text()})`).toBe(200);

  // The failure mode, stated directly: the parent's edge now points at a
  // tombstone and DGraph refuses the whole query.
  const after = await gql(page.request, `query($o:String!){
    queryDataCenter(filter:{orbId:{eq:$o}}){ orbId kubernetesClusters{ ... on ConfigItem { orbId name } } } }`,
    { o: DC });
  const propagated = (after.errors ?? []).filter((e: any) =>
    String(e.message).includes('Non-nullable field'));
  expect(
    propagated,
    'the deleted cluster is still referenced by its data centre — the parent list edge '
    + 'was not cleared, so every query walking it (export included) fails for the whole DC',
  ).toHaveLength(0);
  expect(
    after.data?.queryDataCenter?.[0]?.kubernetesClusters ?? [],
    'the deleted cluster must be gone from the data centre',
  ).toHaveLength(0);

  // And the real thing, not just a query shaped like it: an export of that DC
  // must still complete. This is what actually broke.
  const trigger = await page.request.post('/api/v1/export', { data: { orbId: DC, download: true } });
  expect(trigger.status(), 'export trigger: expect 202').toBe(202);
  const { id: jobId } = await trigger.json();

  const deadline = Date.now() + 180_000;
  let job: any;
  while (Date.now() < deadline) {
    job = await (await page.request.get(`/api/v1/export/jobs/${jobId}`)).json();
    if (['completed', 'failed'].includes(job.status)) break;
    await new Promise(r => setTimeout(r, 3000));
  }
  expect(
    job?.status,
    `export of ${DC} after deleting one of its clusters must complete `
    + `(got "${job?.status}"${job?.error ? ': ' + job.error : ''})`,
  ).toBe('completed');

  await page.request.delete(`/api/v1/config-items/DataCenter/${encodeURIComponent(DC)}`);
  await page.request.post(ORBITAL_DGRAPH, { data: { query: `mutation($n:String!){
    deleteNamespace(filter:{name:{eq:$n}}){ numUids } }`, variables: { n: NS } } });
});
