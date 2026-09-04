// The config editor sends `ifVersion`, so a concurrent edit is refused rather
// than silently overwritten.
//
// This exists because it was silently lost once already. `b1157ac "Fix orb ui"`
// (2026-06-20) replaced the per-page edit modals — each of which passed
// `ifVersion` — with the shared configitem-editor module, which never carried it
// forward. MVCC was off on every UI edit for two and a half months and nothing
// failed, because `ifVersion` is opt-in server-side: a client that omits it is
// indistinguishable from one that declined to use it.
//
// So the regression class is precisely "a refactor drops it from the client
// again", and the only way to catch that is to assert on the REQUEST the browser
// sends. The server-side tests in graphql_handler_test.go already prove orbital
// honours `ifVersion` when supplied; nothing proved anyone supplied it.

import { test, expect, Page } from '@playwright/test'

const SERVER = 'colo:server-6CVD664'
const domId = SERVER.replace(/[^a-zA-Z0-9_-]/g, '_')

async function openEditor(page: Page) {
  await page.goto(`/servers?open=${encodeURIComponent(SERVER)}&label=${encodeURIComponent(SERVER)}`)
  await page.waitForSelector(`#tab-content-srv-${domId}[data-loaded="true"]`, { timeout: 15_000 })
  await page.locator(`[data-srv-edit-id="${domId}"]`).click()
  await expect(page.locator(`#edit-modal-srv-${domId}`)).toHaveClass(/is-active/, { timeout: 10_000 })
}

test('the page hands the editor an OCC version for every entity it can edit', async ({ page }) => {
  await page.goto(`/servers?open=${encodeURIComponent(SERVER)}&label=${encodeURIComponent(SERVER)}`)
  await page.waitForSelector(`#tab-content-srv-${domId}[data-loaded="true"]`, { timeout: 15_000 })

  const targets = await page.locator(`#srv-edit-targets-${domId}`).textContent()
  const parsed = JSON.parse(targets || '[]')

  // The root and its owned children exist, so each must carry a version. A
  // target whose entity does not exist yet legitimately has none — a create has
  // nothing to assert — so this asserts only the ones that do.
  const byKind = Object.fromEntries(parsed.map((t: any) => [t.kind, t]))
  for (const kind of ['Server', 'IdracSettings', 'ServerMaintenance']) {
    expect(byKind[kind], `${kind} target missing`).toBeTruthy()
    expect(byKind[kind].version, `${kind} has no version — the editor cannot send ifVersion`).toBeGreaterThan(0)
  }
})

test('a save sends ifVersion as a top-level variable, and never inside set', async ({ page }) => {
  await openEditor(page)

  const bodies: any[] = []
  await page.route('**/graphql', async (route) => {
    const post = route.request().postData()
    if (post) { try { bodies.push(JSON.parse(post)) } catch (_) { /* ignore */ } }
    await route.continue()
  })

  // Drive the JSONEditor through its instance, the way configitem-editor.spec
  // does — clicking into the tree is brittle and tests the widget, not us.
  const initial = JSON.parse(
    (await page.locator(`#srv-edit-data-${domId}`).textContent()) || '{}')
  await page.evaluate(({ id, next }) => {
    const editor = (window as any).srvEditors.get(id)
    editor.set({ text: JSON.stringify(next, null, 2) })
  }, { id: domId, next: { ...initial, hostname: 'mvcc-e2e-' + Date.now() } })

  await page.locator(`#srv-edit-submit-${domId}`).click()

  await expect.poll(() => bodies.length, { timeout: 15_000 }).toBeGreaterThan(0)
  const update = bodies.find(b => /mutation Update/.test(b.query || ''))
  expect(update, 'no update mutation was sent').toBeTruthy()

  // The guard itself.
  expect(Number.isInteger(update.variables.ifVersion)).toBeTruthy()
  expect(update.variables.ifVersion).toBeGreaterThan(0)

  // `version` in `set` and `ifVersion` as a variable are two different things
  // wearing one name — the counter the server stamps and clients must not
  // write, versus the precondition clients must send. Sending the first breaks
  // the auto-increment; omitting the second is the regression above.
  expect(update.variables.set).not.toHaveProperty('version')

  // Not declared in the query: orbital consumes ifVersion and strips it before
  // the body reaches DGraph, so declaring it would make DGraph reject an
  // undeclared-then-removed variable.
  expect(update.query).not.toContain('$ifVersion')
})
