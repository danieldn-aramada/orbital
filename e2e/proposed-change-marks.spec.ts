// A field with a change in flight says so, on the field.
//
// The banner tells you something is proposed for this server; these marks tell
// you WHICH FIELD, which is what stops someone retyping an edit a colleague
// already proposed. The mark is an index into the proposal, never a second
// value — see docs/reference/CHANGE-CONTROL.md for why competing values in one
// row stop meaning anything the moment there are two.

import { test, expect, Page } from '@playwright/test'

// A server this spec owns. It asserts "no field is marked", which can only hold
// if nothing else is proposing against the entity — so it must not share one
// with another spec OR with whatever a developer happens to have in flight.
// CWJHDX3 is the one people reach for by hand; this is not.
const SERVER = 'colo:server-5F206G4'
const MAINT = 'colo:server-maintenance-5F206G4'
const domId = SERVER.replace(/[^a-zA-Z0-9_-]/g, '_')

async function api(page: Page, method: string, path: string, body?: unknown) {
  return page.request.fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    data: body === undefined ? undefined : JSON.stringify(body),
  })
}

async function propose(page: Page, set: Record<string, unknown>, title: string) {
  const res = await api(page, 'POST', '/api/v1/change-requests', {
    title, namespace: 'colo',
    changes: [{ orbId: MAINT, op: 'update', set }],
  })
  expect(res.ok(), `propose ${title}`).toBeTruthy()
  return (await res.json()).id as string
}

// Close only what this spec opened. Closing every active request would take out
// whatever a developer had in flight on their own stack.
const opened: string[] = []
test.afterEach(async ({ page }) => {
  for (const id of opened.splice(0)) {
    await api(page, 'POST', `/api/v1/change-requests/${id}/close`)
  }
})

async function openMaintenanceTab(page: Page) {
  await page.goto(`/servers?open=${encodeURIComponent(SERVER)}&label=${encodeURIComponent(SERVER)}`)
  await page.waitForSelector(`#tab-content-srv-${domId}[data-loaded="true"]`, { timeout: 15_000 })
  await page.locator(`#srv-detail-tabs-${domId} li[data-panel="srv-panel-maintenance-${domId}"]`).click()
}

function markFor(page: Page, field: string) {
  return page.locator(`[data-field="${field}"] .js-field-mark`)
}

test('a single proposal names its value and a way to review it', async ({ page }) => {
  // enabled is currently false, so proposing true is a real change.
  opened.push(await propose(page, { enabled: true }, 'MARKS single'))
  await openMaintenanceTab(page)

  const mark = markFor(page, 'enabled')
  await expect(mark).toContainText('proposed → true', { timeout: 15_000 })
  await expect(mark.locator('a')).toHaveAttribute('href', /\/change-requests\//)
  // Author and age are deliberately NOT here. Two facts fit a table row; four
  // crowded it, and neither of those two is why someone scans this table.
  // They are the first thing on the review page the link goes to.
  await expect(mark).not.toContainText('admin@armada.ai')
  // Info, not danger: one proposal is not a disagreement.
  // The proposed value sits in a tag, in the same register as the current
  // value beside it — not a pale coloured run of text.
  await expect(mark.locator('.tag')).toHaveText('true')

  // Untouched fields stay clean — a mark on everything would say nothing.
  await expect(markFor(page, 'windowStart')).toBeEmpty()
  await expect(markFor(page, 'reason')).toBeEmpty()
})

// The rule that hides the read-skew window, and that stops a proposal someone
// already applied by hand from nagging forever. A regression here is invisible:
// the mark simply appears where it shouldn't, and looks exactly like a real one.
test('a proposal whose value already matches current state is NOT marked', async ({ page }) => {
  // enabled is already false. Proposing false changes nothing.
  const noop = await propose(page, { enabled: false }, 'MARKS no-op')
  opened.push(noop)
  await openMaintenanceTab(page)

  // Give the overlay the same time the positive test allows, so this cannot
  // pass merely by asserting before the fetch resolved.
  await expect(page.locator(`#srv-panel-maintenance-${domId} table`)).toBeVisible({ timeout: 15_000 })
  await page.waitForTimeout(2_000)
  await expect(markFor(page, 'enabled')).toBeEmpty()

  // ...but the request is real and the BANNER still shows it. Suppressing the
  // field mark must not make an open request disappear from the page.
  //
  // Asserted on the ID, which is what the banner links: a title is a stored
  // string that can describe a changeset since amended, so the banner names the
  // one thing that cannot go stale.
  await expect(page.locator(`[data-pending-changes-for="${SERVER}"]`)).toContainText(noop)
})

test('two proposals that disagree show a count and are called out as conflicting', async ({ page }) => {
  opened.push(await propose(page, { reason: 'planned rack move' }, 'MARKS reason A'))
  opened.push(await propose(page, { reason: 'emergency PSU swap' }, 'MARKS reason B'))
  await openMaintenanceTab(page)

  const mark = markFor(page, 'reason')
  await expect(mark).toContainText('2 proposed changes', { timeout: 15_000 })
  await expect(mark).toContainText('conflicting')
  // Neither value appears: with two claims, showing one would assert a fact.
  await expect(mark).not.toContainText('planned rack move')
  await expect(mark).not.toContainText('emergency PSU swap')
  await expect(mark.locator('.tag')).toHaveClass(/is-danger/)
})

test('with nothing proposed, no field is marked', async ({ page }) => {
  await openMaintenanceTab(page)
  await expect(page.locator(`#srv-panel-maintenance-${domId} table`)).toBeVisible({ timeout: 15_000 })
  await page.waitForTimeout(2_000)
  for (const field of ['enabled', 'windowStart', 'windowEnd', 'reason']) {
    await expect(markFor(page, field)).toBeEmpty()
  }
})

// The server's OWN fields, not just its owned children.
//
// Marks shipped wired to the maintenance panel alone, so a proposal against
// `Server.manufacturer` raised the banner and then annotated nothing — the
// summary table had no data-field rows to annotate. The banner says "something
// is proposed for this item"; only the mark says WHICH FIELD, which is the
// whole point of it.
test('a proposal on a server field is marked on the Server Summary table', async ({ page }) => {
  const res = await api(page, 'POST', '/api/v1/change-requests', {
    title: 'MARKS summary field', namespace: 'colo',
    changes: [{ orbId: SERVER, op: 'update', set: { manufacturer: 'Dell-proposed' } }],
  })
  expect(res.ok(), 'propose manufacturer').toBeTruthy()
  opened.push((await res.json()).id)

  await page.goto(`/servers?open=${encodeURIComponent(SERVER)}&label=${encodeURIComponent(SERVER)}`)
  await page.waitForSelector(`#tab-content-srv-${domId}[data-loaded="true"]`, { timeout: 15_000 })

  const mark = page.locator(`#tab-content-srv-${domId} [data-field="manufacturer"] .js-field-mark`)
  await expect(mark).toContainText('Dell-proposed', { timeout: 10_000 })

  // Only the proposed field. A mark on every row would be noise indistinguish-
  // able from a mark that means something.
  for (const field of ['hostname', 'model', 'oobMAC', 'serviceTag']) {
    await expect(page.locator(`#tab-content-srv-${domId} [data-field="${field}"] .js-field-mark`)).toBeEmpty()
  }

  // Exactly the six Server FormFields in configitems/registry.go carry a slot.
  // Data Center, OOB IP and Rack are edge references the editor cannot write,
  // so a mark on them could never fire — and a count is how that stays true
  // when someone adds a row to this table.
  // Scoped by orbId, not position: the maintenance panel on this same tab is
  // also a data-field-orbid table, keyed to the CHILD's orbId.
  const slots = await page.locator(`#tab-content-srv-${domId} table[data-field-orbid="${SERVER}"] tr[data-field]`).count()
  expect(slots).toBe(6)
})
