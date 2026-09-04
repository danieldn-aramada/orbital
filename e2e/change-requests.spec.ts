// Session 3 acceptance criteria that need a browser, transcribed from
// the session-3 acceptance list (see docs/reference/CHANGE-CONTROL.md) — names first, bodies after.
//
// AC 7 and 8 are pure-function assertions on the changeset translation. They
// run here rather than in a JS unit framework because none exists, and adding
// one is a dependency decision this session did not ask for. page.evaluate()
// imports the module and calls it directly — no DOM interaction, so they stay
// fast and isolated despite living in the e2e harness.
//
// Every test that installs a policy removes it again: the gate is global, and a
// leaked policy would silently gate every later spec in the run.

import { test, expect, Page } from '@playwright/test'
import { savePolicies, clearPolicies, restorePolicies } from './policy-snapshot'

const SERVER_ORB_ID = '2f-uae:server-5HSC3D4'
const NS = '2f-uae'

function safeDomId(orbId: string): string {
  return orbId.replace(/[^a-zA-Z0-9_-]/g, '_')
}

async function api(page: Page, method: string, path: string, body?: unknown) {
  return page.request.fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    data: body === undefined ? undefined : JSON.stringify(body),
  })
}

// The approval gate is GLOBAL state: one policy row changes how every editor in
// the app behaves. So each test establishes the state it needs rather than
// assuming it, and clears up after itself. A leaked policy does not fail the
// test that leaked it — it fails an unrelated one later, which is the worst
// kind of flake to chase.

test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await savePolicies(page.request)
  await page.close()
})
test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await restorePolicies(page.request)
  await page.close()
})

// bypassRoles defaults to ["admin"], and the e2e identity IS admin — so the
// default policy makes this browser a privileged writer, not a proposer. Tests
// that need the gated path pass a role nobody holds, which is a real
// configuration (a class nobody may bypass) rather than a test-only trick.
async function protect(page: Page, namespace: string, bypassRoles?: string[]): Promise<string> {
  await clearPolicies(page.request)
  const res = await api(page, 'POST', '/api/v1/approval-policies', {
    namespace, requiredApprovals: 1,
    ...(bypassRoles ? { bypassRoles } : {}),
  })
  expect(res.ok(), 'could not install the policy this test depends on').toBeTruthy()
  return (await res.json()).id
}

// NOBODY_BYPASSES gates every caller, including admin.
const NOBODY_BYPASSES = ['no-such-role']

// Change requests are global state too: one left in flight on a shared fixture
// entity changes what every later editor shows. Tests that assert on the
// absence of a pending change must therefore ESTABLISH that absence rather than
// hope for it — the alternative is a test that passes or fails depending on
// what someone did in a browser an hour earlier.
async function closeAllInFlight(page: Page, orbId: string) {
  const res = await api(page, 'GET', `/api/v1/change-requests?status=active&orbId=${encodeURIComponent(orbId)}`)
  if (!res.ok()) return
  for (const cr of (await res.json()).items || []) {
    await api(page, 'POST', `/api/v1/change-requests/${cr.id}/close`)
  }
}

async function unprotect(page: Page, id: string) {
  await api(page, 'DELETE', `/api/v1/approval-policies/${id}`)
}

async function openServerEditModal(page: Page, orbId: string) {
  const domId = safeDomId(orbId)
  await page.goto(`/servers?open=${encodeURIComponent(orbId)}&label=${encodeURIComponent(orbId)}`)
  await page.waitForSelector(`#tab-content-srv-${domId}[data-loaded="true"]`, { timeout: 15_000 })
  await page.waitForSelector(`#edit-modal-srv-${domId}`, { state: 'attached', timeout: 5_000 })
  await page.locator(`[data-srv-edit-id="${domId}"]`).first().click()
  await expect(page.locator(`#edit-modal-srv-${domId}`)).toHaveClass(/is-active/)
  return domId
}

// AC 3
test('the queue lists requests and each filter tab changes only the query param', async ({ page }) => {
  const policyId = await protect(page, NS)
  const created = await api(page, 'POST', '/api/v1/change-requests', {
    title: 'queue fixture', namespace: NS,
    changes: [{ orbId: SERVER_ORB_ID, op: 'update', set: { hostname: 'queued' } }],
  })
  expect(created.ok()).toBeTruthy()
  const crId = (await created.json()).id

  try {
    await page.goto('/change-requests')
    await expect(page.locator(`[data-cr-row="${crId}"]`)).toBeVisible()
    // The row carries derived state the client must not recompute.
    const row = page.locator(`[data-cr-row="${crId}"]`)
    // The row carries BOTH: the proposer's title, which is what a reviewer
    // scans for, and a narrow Change column with the size of the change,
    // derived from the API's `effect` on every read.
    //
    // This reverses an earlier assertion that the title must NOT appear. That
    // rested on titles being machine-generated once at creation; proposers now
    // write them, so the title is the most actionable thing in the row and the
    // count moved to its own column rather than being appended to the title.
    await expect(row).toContainText('queue fixture')
    await expect(row).toContainText('1 field')
    await expect(row).toContainText(NS)
    await expect(row).toContainText('0 of 1')

    // Status is the only coloured column, and staleness rides inside it rather
    // than in a second one competing for the eye. The count grew to seven when
    // the ID column was added — two requests on one entity were otherwise
    // identical in every visible column — and to eight when proposers began
    // writing titles, which made the title worth its own wide column with the
    // change size beside it rather than appended to it.
    await expect(page.locator('#cr-table thead th')).toHaveCount(8)
    await expect(page.locator('#cr-table thead th').first()).toHaveText('ID')
    await expect(page.locator('#cr-table thead th').nth(1)).toHaveText('Title')
    await expect(page.locator('#cr-table thead th').nth(2)).toHaveText('Change')
    // The id is the row's link, so a row can always be reached by the thing
    // people quote.
    await expect(row.locator('td').first()).toHaveText(/^[a-z0-9-]+-\d+$/)
    const headers = await page.locator('#cr-table thead th').allTextContents()
    expect(headers).not.toContain('Stale')

    // "Closed" must exclude a request that has not finished — otherwise the
    // tabs are decoration. It is three statuses OR-ed, which is also the
    // coverage for the repeatable ?status= filter: read as a single value it
    // would answer about `merged` alone.
    await page.locator('a[data-cr-filter="status=merged&status=rejected&status=closed"]').click()
    await expect(page.locator(`[data-cr-row="${crId}"]`)).toHaveCount(0)

    await page.locator('a[data-cr-filter="status=active"]').click()
    await expect(page.locator(`[data-cr-row="${crId}"]`)).toBeVisible()
  } finally {
    await api(page, 'POST', `/api/v1/change-requests/${crId}/close`)
    await unprotect(page, policyId)
  }
})

// AC 6 + AC 12
test('a gated class relabels Save to "Propose change", writes nothing, and lands on the review page', async ({ page }) => {
  const policyId = await protect(page, NS, NOBODY_BYPASSES)
  try {
    const before = await (await api(page, 'GET', `/api/v1/change-requests?orbId=${encodeURIComponent(SERVER_ORB_ID)}`)).json()

    const domId = await openServerEditModal(page, SERVER_ORB_ID)
    const btn = page.locator(`#srv-edit-submit-${domId}`)

    // AC 6 — the button says what it will actually do, before the user commits
    // effort to an edit that would be refused.
    await expect(btn).toHaveText('Propose change', { timeout: 10_000 })
    await expect(page.locator(`#edit-modal-srv-${domId}`)).toContainText('Needs approval')

    // ONE button, not two. A caller who cannot bypass gets no direct-write
    // control: a second button that always 403s trains people to click through
    // errors, and the extra action here would be a dead one.
    await expect(page.locator(`#edit-modal-srv-${domId} [data-testid="propose-change"]`)).toHaveCount(0)

    // The notice sits in the FOOTER, as a sibling of the button row.
    //
    // That is what makes its left edge the Save button's left edge: both are
    // laid out inside the footer's padding. Earlier placements each satisfied
    // some rule and still looked wrong — inside `.buttons` it became a flex
    // sibling of the actions and shoved them sideways; in the body it scrolled
    // away with the editor; between body and footer it was flush to the card
    // while the editor sat inside the body's padding, giving three left edges
    // in one dialog.
    //
    // Asserted structurally rather than by measuring pixels: the modal animates
    // with a 3D transform (modal-fx-3dRotateFromBottom), so getBoundingClientRect
    // returns projected coordinates that differ per child until it settles —
    // a measurement here reports a phantom offset. Position is the cause,
    // alignment is the effect, and only the cause is stable to assert.
    const misplaced = await page.evaluate(({ id }) => {
      const card = document.querySelector(`#edit-modal-srv-${id} .modal-card`)
      // `.js-gate-notice`, not `.notification`: these are text lines, not
      // tinted panels — colour in this footer belongs to the three buttons.
      const notice = card?.querySelector('.js-gate-notice')
      if (!notice) return 'no notice found'
      if (notice.closest('.buttons')) return 'notice is inside the button row'
      if (notice.closest('.modal-card-body')) return 'notice is inside the scrolling body'
      const foot = notice.closest('.modal-card-foot')
      if (!foot) return 'notice is outside the footer, so it does not share the buttons inset'
      if (notice.parentElement !== foot) return 'notice is nested inside the footer rather than a sibling of the button row'
      if (!foot.classList.contains('has-gate-notice')) return 'footer is missing has-gate-notice, so it will not wrap the notice onto its own row'
      return ''
    }, { id: domId })
    expect(misplaced).toBe('')

    // Change one field through the real editor instance, the same way the
    // existing editor spec drives it.
    await page.evaluate(({ id }) => {
      const ed = (window as any).srvEditors.get(id)
      const cur = JSON.parse(ed.get().text)
      cur.hostname = 'proposed-by-e2e'
      ed.set({ text: JSON.stringify(cur, null, 2) })
    }, { id: domId })

    await btn.click()

    // AC 12 — proposing is a SAVE: it leaves you on the thing you were editing.
    //
    // It used to navigate to the new request's review page. That teleported the
    // user out of the entity mid-flow and made the gated path behave unlike the
    // ordinary one. The banner is what confirms the proposal landed, and it is
    // also the way through to the review.
    await expect(page.locator(`#edit-modal-srv-${domId}`)).not.toHaveClass(/is-active/, { timeout: 15_000 })
    expect(page.url()).not.toMatch(/\/change-requests\//)
    const banner = page.locator(`[data-pending-changes-for="${SERVER_ORB_ID}"]`)
    await expect(banner).toContainText('in review', { timeout: 15_000 })
    await expect(banner.locator('a')).toHaveAttribute('href', /\/change-requests\/[a-z0-9-]+-\d+$/)

    // AC 6 — nothing reached the graph.
    const srv = await (await api(page, 'POST', '/graphql', {
      query: `{ getServer(orbId: "${SERVER_ORB_ID}") { hostname } }`,
    })).json()
    expect(srv.data.getServer.hostname).not.toBe('proposed-by-e2e')

    const after = await (await api(page, 'GET', `/api/v1/change-requests?orbId=${encodeURIComponent(SERVER_ORB_ID)}`)).json()
    expect(after.total).toBe(before.total + 1)
    for (const cr of after.items) {
      if (!before.items.some((b: any) => b.id === cr.id)) {
        await api(page, 'POST', `/api/v1/change-requests/${cr.id}/close`)
      }
    }
  } finally {
    await unprotect(page, policyId)
  }
})

// AC 7
test('an edit that changes one field and clears another produces the right set and clear', async ({ page }) => {
  await page.goto('/servers')
  const result = await page.evaluate(async () => {
    const mod: any = await import('/static/configitem-editor.js')
    const target = {
      path: [], kind: 'Server', orbId: 'ns:server-1',
      fields: ['hostname', 'model', 'oobMAC'], payloadField: 'server',
    }
    return mod.buildChangeset({
      namespace: 'ns',
      rootTarget: target,
      rootOrbId: 'ns:server-1',
      rootScalars: { hostname: 'renamed' },
      rootRemove: { oobMAC: 'aa:bb:cc:dd:ee:ff' },
      changes: [],
      wrappersNeeded: new Map(),
      foldedOrbIds: new Set(),
    })
  })
  expect(result.namespace).toBe('ns')
  expect(result.changes).toHaveLength(1)
  expect(result.changes[0]).toMatchObject({
    orbId: 'ns:server-1', type: 'Server', op: 'update', set: { hostname: 'renamed' },
  })
  // `clear` carries the field NAME; the mutation path's `remove` carries its
  // prior VALUE. Losing that distinction would send DGraph dialect to an API
  // that rejects it.
  expect(result.changes[0].clear).toEqual(['oobMAC'])
  expect(result.changes[0].set).not.toHaveProperty('oobMAC')
})

// A changeset must record what the author CHANGED, not the whole entity.
//
// The mutation path sends every scalar on an update, which is harmless for
// DGraph and wrong for a proposal: merging writes every field named, so a
// six-field payload for a one-field edit claims authority over five fields
// nobody touched — and every count derived from it reads six.
test('a changeset carries only the fields whose value actually changed', async ({ page }) => {
  await page.goto('/servers')
  const result = await page.evaluate(async () => {
    const mod: any = await import('/static/configitem-editor.js')
    const target = {
      path: [], kind: 'Server', orbId: 'ns:server-1',
      fields: ['hostname', 'model', 'manufacturer'], payloadField: 'server',
    }
    return mod.buildChangeset({
      namespace: 'ns', rootTarget: target, rootOrbId: 'ns:server-1',
      // What the editor computes: the entity's whole scalar payload.
      rootScalars: { hostname: 'unchanged', model: 'unchanged', manufacturer: 'Dell-edit' },
      // What it looked like when the modal opened.
      rootBefore: { hostname: 'unchanged', model: 'unchanged', manufacturer: 'Dell' },
      rootRemove: {}, changes: [], wrappersNeeded: new Map(), foldedOrbIds: new Set(),
    })
  })
  expect(result.changes).toHaveLength(1)
  expect(result.changes[0].set).toEqual({ manufacturer: 'Dell-edit' })
})

// AC 8
test('a nested wrapper create is FLATTENED into ordered items, never a nested object', async ({ page }) => {
  await page.goto('/servers')
  const result = await page.evaluate(async () => {
    const mod: any = await import('/static/configitem-editor.js')
    const wrapper = { orbId: 'ns:backup-1', kind: 'ClusterBackup', name: 'backup', namespace: 'ns', parentField: 'backup' }
    const child = {
      path: ['backup', 'etcd'], kind: 'EtcdBackup', orbId: 'ns:etcd-1',
      fields: ['schedule'], payloadField: 'etcdBackup',
      parentInverseField: 'clusterBackupEtcd', parentOrbId: 'ns:backup-1', namespace: 'ns',
    }
    return mod.buildChangeset({
      namespace: 'ns',
      rootTarget: { path: [], kind: 'EksaKubernetesCluster', orbId: 'ns:cluster-1', fields: [] },
      rootOrbId: 'ns:cluster-1',
      rootScalars: {},
      rootRemove: {},
      changes: [{ target: child, existed: false, currentSub: { schedule: '0 2 * * *' }, before: null }],
      wrappersNeeded: new Map([['ns:backup-1', wrapper]]),
      foldedOrbIds: new Set(['ns:etcd-1']),
    })
  })

  const ids = result.changes.map((c: any) => c.orbId)
  // Order is the contract: merge applies items in sequence, so anything
  // referencing the wrapper must come after it.
  expect(ids.indexOf('ns:backup-1')).toBeLessThan(ids.indexOf('ns:etcd-1'))
  expect(ids).toContain('ns:cluster-1')

  // No item may carry a nested entity — DGraph links on an edge and silently
  // discards nested values, so the API refuses this shape outright.
  for (const item of result.changes) {
    for (const [field, value] of Object.entries(item.set || {})) {
      if (value && typeof value === 'object' && !Array.isArray(value)) {
        expect(Object.keys(value as object), `${item.orbId}.${field} must be a reference, not a nested entity`).toEqual(['orbId'])
      }
    }
  }
  // The root points at the wrapper by reference.
  const root = result.changes.find((c: any) => c.orbId === 'ns:cluster-1')
  expect(root.set.backup).toEqual({ orbId: 'ns:backup-1' })
})

// AC 9 — being ALLOWED to skip review is not the same as wanting to.
//
// Holding the bypass role used to mean losing access to review entirely: the
// footer offered one button, it wrote directly, and the only way for an admin to
// get a second pair of eyes on a production edit was to give up the role. Both
// destinations are now one click away, and review is the primary one.
//
// The direct-write half (it still writes, and the audit row still names the
// policy it bypassed) is asserted in privileged-write-visibility.spec.ts —
// asserting it again here would duplicate that without adding a guarantee.
test('an admin on a protected class gets both actions, with Propose change as the primary', async ({ page }) => {
  const policyId = await protect(page, NS)
  try {
    const domId = await openServerEditModal(page, SERVER_ORB_ID)
    const modal = page.locator(`#edit-modal-srv-${domId}`)
    const propose = modal.locator('[data-testid="propose-change"]')
    const save = page.locator(`#srv-edit-submit-${domId}`)
    // The policy resolve is async, and the second button appearing IS the
    // resolution — the labels are what tell a privileged writer where each
    // click goes, so there is no notice to wait on.
    await expect(propose).toBeVisible({ timeout: 10_000 })
    await expect(propose).toHaveText('Propose change')
    // Relabelled: with two write destinations in one footer, "Save" no longer
    // says which one it is.
    await expect(save).toHaveText('Save directly')

    // Order and emphasis are the point, not merely the presence of two buttons —
    // a footer that offered both but made bypassing the default click would
    // satisfy "both exist" and still push every admin past review.
    const order = await page.evaluate(({ id }) => {
      const p = document.querySelector(`#edit-modal-srv-${id} [data-testid="propose-change"]`)!
      const b = document.getElementById(`srv-edit-submit-${id}`)!
      if (p.parentElement !== b.parentElement) return 'propose is not in the same button row as save'
      // DOCUMENT_POSITION_FOLLOWING === 4 when save comes after propose.
      return p.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? 'propose-first' : 'save-first'
    }, { id: domId })
    expect(order).toBe('propose-first')
    await expect(propose).toHaveClass(/is-success/)
    await expect(save).not.toHaveClass(/is-success/)
    // Amber, matching the notice above it: the colour says "this skips review"
    // without disabling a control the role is entitled to use.
    await expect(save).toHaveClass(/is-warning/)

    // A no-op Propose says so and stays open. Save closes silently on an empty
    // diff because there is nothing to persist, but a modal that closes on
    // "Propose change" reads as "a request was created" — and sends the reader
    // to the queue looking for one that was never made.
    await propose.click()
    await expect(modal).toContainText('Nothing to propose', { timeout: 10_000 })
    await expect(modal).toHaveClass(/is-active/)
  } finally {
    await unprotect(page, policyId)
  }
})

// AC 9b — the admin's Propose button must actually PROPOSE.
//
// This is the regression that would be invisible. The two buttons share one
// handler and differ by a single flag; if that flag were dropped, Propose would
// fall through to the ordinary dispatch and write the edit straight to the
// graph — producing the audit row of a perfectly legitimate bypass, with no
// trace that the user asked for review. The button would look like it worked.
test("an admin's Propose change opens a request and writes nothing", async ({ page }) => {
  const policyId = await protect(page, NS)
  await closeAllInFlight(page, SERVER_ORB_ID)
  const before = await (await api(page, 'GET', `/api/v1/change-requests?orbId=${encodeURIComponent(SERVER_ORB_ID)}`)).json()
  const newIds: string[] = []
  try {
    const domId = await openServerEditModal(page, SERVER_ORB_ID)
    const modal = page.locator(`#edit-modal-srv-${domId}`)
    await expect(modal.locator('[data-testid="propose-change"]')).toBeVisible({ timeout: 10_000 })

    await page.evaluate(({ id }) => {
      const ed = (window as any).srvEditors.get(id)
      const cur = JSON.parse(ed.get().text)
      cur.hostname = 'proposed-by-admin-e2e'
      ed.set({ text: JSON.stringify(cur, null, 2) })
    }, { id: domId })

    await modal.locator('[data-testid="propose-change"]').click()

    // Same completion behaviour as Save: the modal closes and you stay on the
    // entity you were editing.
    await expect(modal).not.toHaveClass(/is-active/, { timeout: 15_000 })
    expect(page.url()).not.toMatch(/\/change-requests\//)

    const after = await (await api(page, 'GET', `/api/v1/change-requests?orbId=${encodeURIComponent(SERVER_ORB_ID)}`)).json()
    expect(after.total).toBe(before.total + 1)
    for (const cr of after.items) {
      if (!before.items.some((b: any) => b.id === cr.id)) newIds.push(cr.id)
    }
    expect(newIds).toHaveLength(1)

    // The request carries the edit that was on screen — not an empty shell.
    const detail = await (await api(page, 'GET', `/api/v1/change-requests/${newIds[0]}`)).json()
    expect(JSON.stringify(detail.changes)).toContain('proposed-by-admin-e2e')

    // And the graph is untouched, which is the whole difference between the two
    // buttons.
    const srv = await (await api(page, 'POST', '/graphql', {
      query: `{ getServer(orbId: "${SERVER_ORB_ID}") { hostname } }`,
    })).json()
    expect(srv.data.getServer.hostname).not.toBe('proposed-by-admin-e2e')
  } finally {
    for (const id of newIds) await api(page, 'POST', `/api/v1/change-requests/${id}/close`)
    await unprotect(page, policyId)
  }
})

// The same guarantee, through the REAL path — and this is the one that matters.
//
// The pure-function test above passed while this was broken: `buildChangeset`
// narrowed correctly, but `proposeChange` sits between the editor and it,
// destructures its parameters, and forwarded a fixed list that had dropped
// `rootBefore` on the floor. Calling the helper directly cannot see a caller
// that never reaches it.
test('a proposal from the editor carries only the edited field', async ({ page }) => {
  const policyId = await protect(page, NS)          // admin bypasses, so both buttons show
  await closeAllInFlight(page, SERVER_ORB_ID)
  const before = await (await api(page, 'GET', `/api/v1/change-requests?orbId=${encodeURIComponent(SERVER_ORB_ID)}`)).json()
  const created: string[] = []
  try {
    const domId = await openServerEditModal(page, SERVER_ORB_ID)
    const modal = page.locator(`#edit-modal-srv-${domId}`)
    await expect(modal.locator('[data-testid="propose-change"]')).toBeVisible({ timeout: 10_000 })

    // One of the six editable scalars. The other five go untouched and must not
    // appear in the payload.
    await page.evaluate(({ id }) => {
      const ed = (window as any).srvEditors.get(id)
      const cur = JSON.parse(ed.get().text)
      cur.manufacturer = 'Dell-narrowed'
      ed.set({ text: JSON.stringify(cur, null, 2) })
    }, { id: domId })
    await modal.locator('[data-testid="propose-change"]').click()
    await expect(modal).not.toHaveClass(/is-active/, { timeout: 15_000 })

    const after = await (await api(page, 'GET', `/api/v1/change-requests?orbId=${encodeURIComponent(SERVER_ORB_ID)}`)).json()
    for (const cr of after.items) if (!before.items.some((b: any) => b.id === cr.id)) created.push(cr.id)
    expect(created).toHaveLength(1)

    const d = await (await api(page, 'GET', `/api/v1/change-requests/${created[0]}`)).json()
    expect(d.changes[0].set).toEqual({ manufacturer: 'Dell-narrowed' })
    // The count and the payload are the same fact, so they cannot disagree.
    // No `· 1 field` tail: the queue has a Change column for the count, and
    // carrying it in the title too printed the same fact twice in one row.
    expect(d.title).toBe('server-5HSC3D4')
    expect(d.effect).toMatchObject({ entities: 1, fields: 1, field: 'manufacturer', value: 'Dell-narrowed' })
  } finally {
    for (const id of created) await api(page, 'POST', `/api/v1/change-requests/${id}/close`)
    await unprotect(page, policyId)
  }
})

// AC 10
test('with no policy the editor behaves exactly as it does today', async ({ page }) => {
  await clearPolicies(page.request)
  const domId = await openServerEditModal(page, SERVER_ORB_ID)
  const modal = page.locator(`#edit-modal-srv-${domId}`)
  await expect(page.locator(`#srv-edit-submit-${domId}`)).toHaveText('Save')
  // Give the async policy resolve time to have landed if it were going to.
  await page.waitForTimeout(1000)
  await expect(modal).not.toContainText('Needs approval')
  // The opt-in promise reaches the footer too: no policy means no second button
  // and no relabelling, so an installation that never configures change control
  // sees the pre-approval-engine UI exactly.
  await expect(modal.locator('[data-testid="propose-change"]')).toHaveCount(0)
  await expect(page.locator(`#srv-edit-submit-${domId}`)).toHaveClass(/is-success/)
})

// AC 14 (browser half — the notice a person actually sees)
test('an entity with a change in flight says so in the editor, and links to it', async ({ page }) => {
  const policyId = await protect(page, NS)
  await closeAllInFlight(page, SERVER_ORB_ID)
  const created = await api(page, 'POST', '/api/v1/change-requests', {
    title: 'in-flight fixture', namespace: NS,
    changes: [{ orbId: SERVER_ORB_ID, op: 'update', set: { hostname: 'already-proposed' } }],
  })
  const crId = (await created.json()).id
  try {
    const domId = await openServerEditModal(page, SERVER_ORB_ID)
    const notice = page.locator('[data-testid="pending-change-notice"]')
    await expect(notice.first()).toBeVisible({ timeout: 10_000 })
    await expect(notice.first()).toContainText('already proposed')
    await expect(notice.first().locator(`a[href$="/change-requests/${crId}"]`)).toHaveCount(1)
    expect(domId).toBeTruthy()
  } finally {
    await api(page, 'POST', `/api/v1/change-requests/${crId}/close`)
    await unprotect(page, policyId)
  }
})

// AC 4 — the review page renders buttons STRAIGHT from availableActions.
//
// This lives in the browser rather than in Go because the page is
// client-rendered: orbital's UI is a consumer of the same public API any other
// client uses, so there is no server-side fragment to assert against. The API
// half — that availableActions is correct per caller — is pinned separately by
// TestCR_AvailableActions_AreCallerRelative.
test('the review page shows exactly the actions the API allows, and no others', async ({ page }) => {
  const policyId = await protect(page, NS, NOBODY_BYPASSES)
  const created = await api(page, 'POST', '/api/v1/change-requests', {
    title: 'actions fixture', namespace: NS,
    changes: [{ orbId: SERVER_ORB_ID, op: 'update', set: { hostname: 'action-check' } }],
  })
  const cr = await created.json()
  try {
    await page.goto(`/change-requests/${cr.id}`)
    const actions = page.locator('[data-testid="cr-actions"] button')
    await expect(actions.first()).toBeVisible({ timeout: 10_000 })

    // The ACTION, not the label: `edit` renders as "Rename" (the UI implements
    // only amend's rename half), so comparing button text to action names
    // compares two different vocabularies. Reading data-cr-action asserts the
    // actual invariant — the UI renders exactly what the API allows — and needs
    // no per-action exemption.
    const rendered = (await actions.evaluateAll(
      els => els.map(e => e.getAttribute('data-cr-action') || ''))).sort()
    const allowed = (await (await api(page, 'GET', `/api/v1/change-requests/${cr.id}`)).json())
      .availableActions.sort()

    expect(rendered).toEqual(allowed)
    // The author opened it, so approve must NOT be offered — if the client were
    // re-deriving eligibility instead of reading the API, this is where it
    // would disagree.
    expect(rendered).not.toContain('approve')
  } finally {
    await api(page, 'POST', `/api/v1/change-requests/${cr.id}/close`)
    await unprotect(page, policyId)
  }
})

// AC 5 — an approval cast against an earlier version is SHOWN and labelled, not
// silently dropped. The alternative — approvals vanishing when the base moves —
// looks like the system lost someone's review.
test('an approval cast against an earlier version is shown as such, not hidden', async ({ page }) => {
  const policyId = await protect(page, NS)   // admin bypasses, so admin may approve
  const created = await api(page, 'POST', '/api/v1/change-requests', {
    title: 'stale approval fixture', namespace: NS,
    changes: [{ orbId: SERVER_ORB_ID, op: 'update', set: { hostname: 'stale-check' } }],
  })
  const cr = await created.json()
  try {
    const approved = await api(page, 'POST', `/api/v1/change-requests/${cr.id}/approve`, { comment: 'looks fine' })
    expect(approved.ok()).toBeTruthy()

    await page.goto(`/change-requests/${cr.id}`)
    await expect(page.locator('[data-testid="cr-reviews"]')).toContainText('looks fine')
    await expect(page.locator('[data-testid="cr-reviews"]')).not.toContainText('approved an earlier version')

    // Move the base out from under it, exactly as a third party would.
    // The value must be unique per run: writing the value a field already holds
    // is a no-op, the content hash does not move, and the test would silently
    // stop testing anything the second time it ran.
    const moved = await api(page, 'POST', '/graphql', {
      query: `mutation($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
      variables: { orbId: SERVER_ORB_ID, set: { model: 'moved-by-someone-else-' + Date.now() } },
    })
    expect(moved.ok()).toBeTruthy()

    await page.reload()
    await expect(page.locator('[data-testid="cr-reviews"]')).toContainText('approved an earlier version')
    // The banner names the CONSEQUENCE, not just the fact: the diff looks
    // identical stale or not, so "merge is blocked and here is the way through"
    // is the part a reader cannot see for themselves.
    //
    // Asserts the STATE and the way out, not the exact sentence — the wording has
    // been revised twice and pinning prose makes copy edits look like failures.
    // This fixture sends no `version`, so what moved is the reviewed SCOPE, not
    // a change object — `subtreeChanged`, which the reviewer clears. Asserting
    // the way out rather than the sentence: the wording has been revised twice
    // and pinning prose makes copy edits look like failures.
    const banner = page.locator('[data-testid="cr-detail"]')
    await expect(banner).toContainText(/re-approve/i)
  } finally {
    await api(page, 'POST', `/api/v1/change-requests/${cr.id}/close`)
    await unprotect(page, policyId)
  }
})

// AC 15 — the negative. A badge that fires when nothing is pending trains
// people to ignore it, which is worse than having no badge at all.
test('an entity whose only requests are closed shows no pending notice', async ({ page }) => {
  const policyId = await protect(page, NS)
  await closeAllInFlight(page, SERVER_ORB_ID)
  const created = await api(page, 'POST', '/api/v1/change-requests', {
    title: 'closed fixture', namespace: NS,
    changes: [{ orbId: SERVER_ORB_ID, op: 'update', set: { hostname: 'will-be-closed' } }],
  })
  const cr = await created.json()
  await api(page, 'POST', `/api/v1/change-requests/${cr.id}/close`)
  try {
    await openServerEditModal(page, SERVER_ORB_ID)
    // Give the async lookup time to have rendered a notice if it were going to.
    await page.waitForTimeout(1500)
    await expect(page.locator('[data-testid="pending-change-notice"]')).toHaveCount(0)
  } finally {
    await unprotect(page, policyId)
  }
})

// AC 11 — a refusal must never dead-end.
//
// The trigger looked like a race (a policy appearing between opening a modal
// and saving) but it is fully stageable: `applyGateState` resolves mode ONCE at
// init, so installing the policy after the modal is open leaves the editor in
// `save` mode with a gate now in force. No timing luck involved — the ordering
// is the test.
//
// This is the path where the old code showed `Server error (403) — try again.`,
// which is the worst available outcome: the remedy exists, it is one click
// away, and the browser is holding the exact edit it needs.
test('a save refused as APPROVAL_REQUIRED offers to open a change request with that edit', async ({ page }) => {
  await clearPolicies(page.request)
  await closeAllInFlight(page, SERVER_ORB_ID)

  // Modal opens with NO policy, so the button resolves to plain Save.
  const domId = await openServerEditModal(page, SERVER_ORB_ID)
  const btn = page.locator(`#srv-edit-submit-${domId}`)
  await expect(btn).toHaveText('Save')
  await page.waitForTimeout(500)   // let the (empty) policy resolve settle

  // NOW the class becomes protected — after the editor decided how to write.
  const policyId = await protect(page, NS, NOBODY_BYPASSES)

  try {
    await page.evaluate(({ id }) => {
      const ed = (window as any).srvEditors.get(id)
      const cur = JSON.parse(ed.get().text)
      cur.hostname = 'refused-then-proposed'
      ed.set({ text: JSON.stringify(cur, null, 2) })
    }, { id: domId })

    let prompt = ''
    page.once('dialog', d => { prompt = d.message(); d.accept() })
    await btn.click()

    // The offer names the reason and what accepting does — a bare "403" would
    // leave the operator to work out both.
    await expect(page.locator(`#edit-modal-srv-${domId}`)).not.toHaveClass(/is-active/, { timeout: 15_000 })
    expect(prompt).toContain('approval')
    expect(prompt).toContain('change request')

    // Accepting the offer proposes and returns you to the entity, same as a
    // save — not to the review page.
    expect(page.url()).not.toMatch(/\/change-requests\//)
    await expect(page.locator(`[data-pending-changes-for="${SERVER_ORB_ID}"]`))
      .toContainText('in review', { timeout: 15_000 })

    // The edit survived the refusal — the whole point is not retyping it.
    const proposed = await (await api(page, 'GET',
      `/api/v1/change-requests?status=active&orbId=${encodeURIComponent(SERVER_ORB_ID)}`)).json()
    expect(JSON.stringify(proposed.items)).toContain('refused-then-proposed')

    // And it is a proposal, not a write.
    const srv = await (await api(page, 'POST', '/graphql', {
      query: `{ getServer(orbId: "${SERVER_ORB_ID}") { hostname } }`,
    })).json()
    expect(srv.data.getServer.hostname).not.toBe('refused-then-proposed')
  } finally {
    await closeAllInFlight(page, SERVER_ORB_ID)
    await unprotect(page, policyId)
  }
})

// The other half: declining the offer must leave the user where they were, with
// the reason visible and their edit intact. An offer that proposes anyway when
// you say no is worse than no offer.
test('declining the offer leaves the edit in place and explains the refusal', async ({ page }) => {
  await clearPolicies(page.request)
  await closeAllInFlight(page, SERVER_ORB_ID)

  const domId = await openServerEditModal(page, SERVER_ORB_ID)
  await expect(page.locator(`#srv-edit-submit-${domId}`)).toHaveText('Save')
  await page.waitForTimeout(500)

  const policyId = await protect(page, NS, NOBODY_BYPASSES)
  try {
    await page.evaluate(({ id }) => {
      const ed = (window as any).srvEditors.get(id)
      const cur = JSON.parse(ed.get().text)
      cur.hostname = 'declined-edit'
      ed.set({ text: JSON.stringify(cur, null, 2) })
    }, { id: domId })

    page.once('dialog', d => d.dismiss())
    await page.locator(`#srv-edit-submit-${domId}`).click()

    // Still on the servers page, modal still open, edit still there.
    await expect(page.locator(`#edit-modal-srv-${domId}`)).toHaveClass(/is-active/)
    expect(page.url()).not.toMatch(/\/change-requests\//)
    await expect(page.locator(`#edit-modal-srv-${domId}`)).toContainText('approval')

    const stillThere = await page.evaluate(({ id }) => {
      const ed = (window as any).srvEditors.get(id)
      return JSON.parse(ed.get().text).hostname
    }, { id: domId })
    expect(stillThere).toBe('declined-edit')

    // Nothing proposed, nothing written.
    const inFlight = await (await api(page, 'GET', `/api/v1/change-requests?status=active&orbId=${encodeURIComponent(SERVER_ORB_ID)}`)).json()
    expect(inFlight.total).toBe(0)
  } finally {
    await closeAllInFlight(page, SERVER_ORB_ID)
    await unprotect(page, policyId)
  }
})
