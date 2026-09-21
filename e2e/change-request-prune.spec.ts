// Pruning a field from a proposal, from the review page.
//
// The control appears only where the world moved under the author — a satisfied
// or conflicting field — and it is STAGED: clicks change nothing on the server
// until one apply. That matters because removing a field is an amend, and every
// amend re-captures the base and dismisses every approval. Firing per click
// would dismiss the reviewers once per row and could fail halfway, leaving a
// changeset nobody chose.

import { test, expect } from '@playwright/test'

const NS = 'colo'

async function gql(request: any, query: string, variables: any = {}) {
  const res = await request.post('/graphql', { data: { query, variables } })
  return res.json()
}

test('staged pruning sends nothing until applied, then exactly one PATCH', async ({ page, request }) => {
  const suffix = Date.now()
  const server = `${NS}:server-prune-${suffix}`

  // A server whose hostname the proposal will change, and whose `model` someone
  // else has already set to the proposed value → a satisfied field, which is
  // where the control appears.
  const dc = (await gql(request, `{ queryDataCenter(first:1){ orbId } }`)).data?.queryDataCenter?.[0]?.orbId
  test.skip(!dc, 'needs a data center in the seed')

  await gql(request, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
    { input: [{ namespace: NS, orbId: server, version: 1, hostname: 'before', model: 'agreed', dataCenter: { orbId: dc } }] })

  const created = await request.post('/api/v1/change-requests', {
    data: {
      title: `prune-${suffix}`, namespace: NS,
      changes: [{ orbId: server, op: 'update', set: { hostname: 'proposed', model: 'agreed' } }],
    },
  })
  expect(created.status(), await created.text()).toBe(201)
  const id = (await created.json()).id

  try {
    await page.goto(`/change-requests/${encodeURIComponent(id)}`)
    const table = page.locator('[data-testid="cr-fields"]')
    await expect(table).toBeVisible()

    // `model` is satisfied (already at the proposed value) so it is prunable;
    // `hostname` applies, so it is not — pruning an ordinary field is changeset
    // editing, which deliberately lives elsewhere.
    const modelRow = table.locator('tr', { hasText: 'model' })
    const hostRow = table.locator('tr', { hasText: 'hostname' })
    await expect(modelRow.locator('[data-cr-drop]')).toBeVisible()
    await expect(hostRow.locator('[data-cr-drop]')).toHaveCount(0)

    // Staging: a click must not reach the server.
    const calls: string[] = []
    await page.route('**/api/v1/change-requests/**', async (route) => {
      if (route.request().method() === 'PATCH') calls.push(route.request().postData() || '')
      await route.continue()
    })

    await modelRow.locator('[data-cr-drop]').click()
    await expect(page.locator('[data-testid="cr-staging"]')).toContainText('1 field')
    expect(calls, 'a staged click reached the server').toHaveLength(0)

    // And it is reversible for free, right up until apply.
    await modelRow.locator('[data-cr-keep]').click()
    await expect(page.locator('[data-testid="cr-staging"]')).toHaveCount(0)
    expect(calls).toHaveLength(0)

    // Apply: exactly one PATCH for the whole selection.
    await modelRow.locator('[data-cr-drop]').click()
    page.once('dialog', d => d.accept())
    // Wait for the RESPONSE, not the interception: `calls` is appended when the
    // request is routed, which is before the server has handled it — reading the
    // changeset then races the amend.
    const patched = page.waitForResponse(r =>
      r.request().method() === 'PATCH' && r.url().includes('/change-requests/'))
    await page.locator('[data-cr-apply-prune]').click()
    const res = await patched
    expect(res.status(), await res.text()).toBe(200)
    expect(calls, 'more than one amend for one apply').toHaveLength(1)

    // The field is gone from the proposal; the other one survives.
    const after = await (await request.get(`/api/v1/change-requests/${encodeURIComponent(id)}`)).json()
    const set = after.changes[0].set
    expect(set.model, 'the pruned field is still in the changeset').toBeUndefined()
    expect(set.hostname).toBe('proposed')
  } finally {
    await request.post(`/api/v1/change-requests/${encodeURIComponent(id)}/close`).catch(() => {})
    await gql(request, `mutation($orbId:String!){ deleteServer(filter:{orbId:{eq:$orbId}}){ numUids } }`, { orbId: server })
  }
})

// A CLOSED request must show what it PROPOSED, not a live diff.
//
// The live diff is measured against current intent, so for a terminal request
// every row reads "No change" by construction — it says nothing about what the
// request asked for. `effect` is the delta captured when it was opened, and the
// only record of that.
//
// This regressed once: the per-field table's condition was `fields.length`, and
// unlike the `changes`/`satisfied` pair it replaced, `fields` is never empty —
// so it swallowed the terminal branch entirely.
test('a closed request shows what it proposed, not a live diff of no-changes', async ({ page, request }) => {
  const suffix = Date.now()
  const server = `${NS}:server-closed-${suffix}`
  const dc = (await gql(request, `{ queryDataCenter(first:1){ orbId } }`)).data?.queryDataCenter?.[0]?.orbId
  test.skip(!dc, 'needs a data center in the seed')

  await gql(request, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
    { input: [{ namespace: NS, orbId: server, version: 1, hostname: 'before', dataCenter: { orbId: dc } }] })

  const created = await request.post('/api/v1/change-requests', {
    data: {
      title: `closed-${suffix}`, namespace: NS,
      changes: [{ orbId: server, op: 'update', set: { hostname: 'never-applied' } }],
    },
  })
  const id = (await created.json()).id

  try {
    await request.post(`/api/v1/change-requests/${encodeURIComponent(id)}/close`)
    await page.goto(`/change-requests/${encodeURIComponent(id)}`)

    // The label says what this is: a record, not a plan.
    await expect(page.locator('#cr-changes-label')).toHaveText('Proposed')
    // And it is the effect record, NOT the live per-field table.
    await expect(page.locator('[data-testid="cr-record"]')).toBeVisible()
    await expect(page.locator('[data-testid="cr-fields"]')).toHaveCount(0)
    // Nothing prunable on a terminal request.
    await expect(page.locator('[data-cr-drop]')).toHaveCount(0)
  } finally {
    await gql(request, `mutation($orbId:String!){ deleteServer(filter:{orbId:{eq:$orbId}}){ numUids } }`, { orbId: server })
  }
})
