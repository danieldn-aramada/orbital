// A field with a change in flight says so, on the field.
//
// The banner tells you something is proposed for this server; these marks tell
// you WHICH FIELD, which is what stops someone retyping an edit a colleague
// already proposed. The mark is an index into the proposal, never a second
// value — see docs/reference/CHANGE-CONTROL.md for why competing values in one
// row stop meaning anything the moment there are two.

import { test, expect, Page } from '@playwright/test'
import { openDetail, openAllDetailPanels, openDetailPanel } from './helpers/generic';

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

// Maintenance is a BOX on the server page now, not a tab behind a click — the
// generic renderer stacks owned children rather than tabbing them. Opening the
// page is all that is needed, which also means a mark is visible without
// hunting for the panel holding it.
async function openMaintenanceTab(page: Page) {
  await openDetail(page, 'servers', SERVER)
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
  await expect(page.getByTestId('generic-owned').filter({ hasText: 'Server Maintenance' })
    .locator('table')).toBeVisible()
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
  await expect(page.getByTestId('generic-owned').filter({ hasText: 'Server Maintenance' })
    .locator('table')).toBeVisible()
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

  await openDetail(page, 'servers', SERVER)

  const mark = page.getByTestId('generic-fields').locator('[data-field="manufacturer"] .js-field-mark')
  await expect(mark).toContainText('Dell-proposed', { timeout: 10_000 })

  // Only the proposed field. A mark on every row would be noise indistinguish-
  // able from a mark that means something.
  for (const field of ['hostname', 'model', 'oobMAC', 'serviceTag']) {
    await expect(page.getByTestId('generic-fields').locator(`[data-field="${field}"] .js-field-mark`)).toBeEmpty()
  }

  // The slot-count invariant that used to live here — "every data-field row
  // corresponds to an editable field, and every editable field has a row" —
  // moved to TestFieldMarkSlotsMatchRegistry_AllTypes (internal/handler). It was a
  // hardcoded `toBe(6)` and broke when `serialNumber` and `uHeight` were added
  // correctly, because a magic number cannot tell a properly-added field from a
  // dead slot. The Go test compares the two sets directly, names the offending
  // field in either direction, and needs no browser.
})

// A proposal on a child shows on the PARENT's relationship table.
//
// Field rows, the metadata box and owned-child boxes all carried marks; a child
// rendered as a TABLE did not — so a pending change to a rack was invisible on
// the data centre page listing it. Each ROW is a different entity there, so the
// mark attributes sit on the row rather than the table, and loadFieldMarks
// walks it unchanged because the shape is identical one level down.
test('a proposal on a rack is marked on the data centre that lists it', async ({ page }) => {
  const RACK = 'colo:A.OF.C.09'
  const res = await api(page, 'POST', '/api/v1/change-requests', {
    title: 'MARKS rack row',
    namespace: 'colo',
    changes: [{ orbId: RACK, op: 'update', set: { uHeight: 41 } }],
  })
  expect(res.ok(), 'propose a rack height').toBeTruthy()
  opened.push((await res.json()).id as string)

  await page.goto('/data-centers/' + encodeURIComponent('colo:colo-galleon'))
  await expect(page.getByTestId('generic-fields')).toBeVisible({ timeout: 15_000 })
  // Open the tab holding the racks, as a person would. The mark RENDERS
  // whether or not the panel is visible — loadFieldMarks does not care — but
  // asserting toBeVisible on a collapsed panel tests the tab strip, not the
  // mark. The tab DOT is what says "something is in here" before you click,
  // and it is asserted below.
  await openDetailPanel(page, 'Racks')

  const row = page.locator(`tr[data-field-orbid="${RACK}"]`)
  await expect(row, 'the rack row must carry its own orbId for marks').toHaveCount(1)
  // The mark lands on the proposed FIELD's cell, not merely somewhere in the row.
  await expect(row.locator('td[data-field="uHeight"] .js-field-mark a')).toBeVisible({ timeout: 10_000 })

  // And the negative, on the same page: a field nobody proposed against stays
  // clean. A mark that fires everywhere is a mark nobody reads.
  const otherRow = page.locator('tr[data-field-orbid]').filter({ hasNot: page.locator(`[data-field-orbid="${RACK}"]`) }).first()
  if (await otherRow.count()) {
    await expect(otherRow.locator('.js-field-mark a')).toHaveCount(0)
  }

  // The tab DOT: a proposal inside a collapsed panel must be discoverable
  // without opening it. Tabs hide content, which is the whole reason this
  // exists — a mark nobody can see is the failure we keep finding.
  await page.reload()
  await expect(page.getByTestId('generic-fields')).toBeVisible({ timeout: 15_000 })
  const racksTab = page.locator('[id^="generic-detail-tabs-"] li').filter({ hasText: 'Racks' }).first()
  await expect(racksTab.locator('.js-tab-dot *')).toHaveCount(1, { timeout: 10_000 })
})
