// Cluster delete end-to-end:
//   - Creates an EKSA cluster with one node attached to a real seed server
//   - Asserts the preview shows the node in the cascade list AND the server
//     in the preserved list
//   - Confirms the delete, asserts the cluster + node are gone from DGraph
//   - Asserts the server still exists (the invariant: cluster delete does NOT
//     touch server inventory)
//
// Regression guard for the cluster cascade scope.

import { test, expect } from '@playwright/test'

test('Cluster delete cascades nodes but preserves servers', async ({ page }) => {
  const t = Date.now()
  const clusterOrbId = `e2e:cluster-${t}`
  const nodeOrbId = `e2e:node-${t}`

  await page.goto('http://localhost:8001/')

  // Seed: pick an existing server, then create a cluster + node pointing to it.
  const setup = await page.evaluate(async ({ clusterOrbId, nodeOrbId }) => {
    const r1 = await fetch('/graphql', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query: 'query { queryServer(first: 1) { orbId hostname serviceTag } }' }),
    })
    const j1 = await r1.json()
    const server = j1.data.queryServer[0]
    if (!server) throw new Error('no seed server available')

    // Nodes attach to cluster via the cluster's `nodes` field — DGraph omits
    // the `cluster` field from AddKubernetesNodeInput because cluster is an
    // interface type. Creating nodes inline under the cluster is the only way
    // to wire up the @hasInverse edge in one round-trip.
    const r2 = await fetch('/graphql', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        query: `mutation Seed($clusterOrbId: String!, $nodeOrbId: String!, $serverOrbId: String!) {
          addNamespace(input: { name: "e2e" }, upsert: true) { numUids }
          addEksaKubernetesCluster(input: [{
            name: "e2e-cluster", orbId: $clusterOrbId, namespace: "e2e", version: 1,
            createdBy: "e2e@test", createdAt: "2026-06-18T00:00:00Z",
            kubernetesVersion: "v1.31.0", cni: "cilium", environment: "dev",
            clusterType: "workload",
            dataCenter: { orbId: "colo:colo-galleon", version: 1 },
            nodes: [{
              orbId: $nodeOrbId, namespace: "e2e", version: 1,
              createdBy: "e2e@test", createdAt: "2026-06-18T00:00:00Z",
              role: "control_plane",
              server: { orbId: $serverOrbId, version: 1 }
            }]
          }], upsert: true) { numUids }
        }`,
        variables: { clusterOrbId, nodeOrbId, serverOrbId: server.orbId },
      }),
    })
    const j2 = await r2.json()
    if (j2.errors) throw new Error(JSON.stringify(j2.errors))
    return server
  }, { clusterOrbId, nodeOrbId })

  const cleanup = async () => {
    await page.evaluate(async ({ clusterOrbId, nodeOrbId }) => {
      await fetch('/graphql', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          query: `mutation Cleanup($clusterOrbId: String!, $nodeOrbId: String!) {
            deleteKubernetesNode(filter: { orbId: { eq: $nodeOrbId } }) { numUids }
            deleteEksaKubernetesCluster(filter: { orbId: { eq: $clusterOrbId } }) { numUids }
          }`,
          variables: { clusterOrbId, nodeOrbId },
        }),
      })
    }, { clusterOrbId, nodeOrbId })
  }

  try {
    await page.goto('http://localhost:8001/clusters')
    const row = page.locator('#cluster-table tbody tr', { hasText: 'e2e-cluster' })
    await expect(row).toBeVisible()
    await row.dblclick()

    // Wait for the cluster tab content to settle.
    await expect(page.locator('text=Cluster Summary')).toBeVisible({ timeout: 10000 })

    // Open the delete modal.
    await page.locator(`[data-cfg-delete-id="${clusterOrbId}"]`).click()
    await expect(page.locator('#cfg-delete-modal.is-active')).toBeVisible()

    // Preview must mention: 1 Kubernetes Node deleted, server in the preserved
    // list. The server name is from the seed pick — use a generic match on
    // "Servers" header + the row count to assert the cascade scope.
    const modalBody = page.locator('#cfg-delete-modal-body')
    await expect(modalBody).toContainText('Kubernetes Nodes')
    await expect(modalBody).toContainText('not be deleted')
    await expect(modalBody).toContainText('Servers')

    // Confirm. Wait for the DELETE response explicitly — modal close transition
    // races with the verify-query otherwise.
    await expect(page.locator('#cfg-delete-confirm-btn')).toBeEnabled()
    const deleteDone = page.waitForResponse(r =>
      r.url().includes('/api/v1/config-items/KubernetesCluster/') && r.request().method() === 'DELETE',
    )
    await page.locator('#cfg-delete-confirm-btn').click()
    const resp = await deleteDone
    expect(resp.status()).toBe(200)
    await expect(page.locator('#cfg-delete-modal.is-active')).not.toBeVisible({ timeout: 10000 })

    // Invariant: cluster gone, node gone, server preserved.
    const after = await page.evaluate(async ({ clusterOrbId, nodeOrbId, serverOrbId }) => {
      const r = await fetch('/graphql', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          query: `query Check($clusterOrbId: String!, $nodeOrbId: String!, $serverOrbId: String!) {
            cluster: queryConfigItem(filter: { orbId: { eq: $clusterOrbId } }) { __typename }
            node: queryConfigItem(filter: { orbId: { eq: $nodeOrbId } }) { __typename }
            server: queryConfigItem(filter: { orbId: { eq: $serverOrbId } }) { __typename }
          }`,
          variables: { clusterOrbId, nodeOrbId, serverOrbId },
        }),
      })
      return await r.json()
    }, { clusterOrbId, nodeOrbId, serverOrbId: setup.orbId })

    expect(after.data.cluster, 'cluster should be deleted').toEqual([])
    expect(after.data.node, 'node should be deleted (cascade)').toEqual([])
    expect(after.data.server.length, 'server must NOT be deleted').toBeGreaterThan(0)
  } finally {
    await cleanup()
  }
})
