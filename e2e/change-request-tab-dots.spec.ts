// A tab whose panel has something proposed inside it says so on the tab.
//
// Field marks are invisible until you open the tab holding them, so a proposal
// on a server's maintenance window was discoverable only by clicking through
// every panel.
//
// Only the two properties a click cannot show are pinned here. That a dot
// appears at all, and that Summary / Audit / the parent tab do not get one, is
// checked visually. These are not: a dot that fires for a proposal already equal
// to current state is ALWAYS on, which is indistinguishable from a working
// feature until someone notices it never turns off; and every panel renders
// eagerly today, so an implementation that scraped rendered marks instead of
// reading the tab's declared orbIds would pass every visual check and break the
// first time a panel goes lazy.

import { test, expect, Page } from '@playwright/test'

// A server this spec owns — it asserts "no dot", which only holds while nothing
// else proposes against the entity.
const SERVER = 'colo:server-2RK3V64'
const MAINT = 'colo:server-maintenance-2RK3V64'
const domId = SERVER.replace(/[^a-zA-Z0-9_-]/g, '_')

async function api(page: Page, method: string, path: string, body?: unknown) {
  return page.request.fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    data: body === undefined ? undefined : JSON.stringify(body),
  })
}

const opened: string[] = []
async function propose(page: Page, set: Record<string, unknown>, title: string) {
  const res = await api(page, 'POST', '/api/v1/change-requests', {
    title, namespace: 'colo',
    changes: [{ orbId: MAINT, op: 'update', set }],
  })
  expect(res.ok(), `propose ${title}: ${await res.text()}`).toBeTruthy()
  const id = (await res.json()).id as string
  opened.push(id)
  return id
}

// Close only what this spec opened — closing every active request would take out
// whatever a developer had in flight on their own stack.
test.afterEach(async ({ page }) => {
  for (const id of opened.splice(0)) {
    await api(page, 'POST', `/api/v1/change-requests/${id}/close`)
  }
})

async function openServerTab(page: Page) {
  await page.goto(`/servers?open=${encodeURIComponent(SERVER)}&label=${encodeURIComponent(SERVER)}`)
  await page.waitForSelector(`#tab-content-srv-${domId}[data-loaded="true"]`, { timeout: 15_000 })
}

const maintTab = (page: Page) =>
  page.locator(`#srv-detail-tabs-${domId} li[data-panel="srv-panel-maintenance-${domId}"]`)

test('a proposal lights the tab of a panel that was never opened', async ({ page }) => {
  // enabled is false, so proposing true is a real change.
  await propose(page, { enabled: true }, 'TABDOT unopened')
  await openServerTab(page)

  const tab = maintTab(page)
  await expect(tab.locator('[data-testid="tab-dot"]')).toBeVisible({ timeout: 15_000 })
  await expect(tab.locator('[data-testid="tab-dot"]')).toHaveAttribute('aria-label', '1 proposed change')

  // The point of the feature: Maintenance is NOT the active tab — iDRAC is — so
  // the dot is reporting on a panel the user has not looked at. An
  // implementation that read rendered marks instead of the tab's declared
  // orbIds could still pass this today, which is what the next assertion is for.
  await expect(tab).not.toHaveClass(/is-active/)
  await expect(tab).toHaveAttribute('data-panel-orbids', MAINT)
})

test('a proposal equal to the current value does not light the tab', async ({ page }) => {
  // enabled is already false. The request is legal and shows in the queue; it
  // simply would not change anything, and the rows suppress it for that reason.
  await propose(page, { enabled: false }, 'TABDOT no-op')
  await openServerTab(page)

  const tab = maintTab(page)
  // Wait for the marks pass to have run rather than asserting on an empty DOM:
  // the iDRAC panel is active and its table is present, so once the page is
  // settled the absence below means suppression, not "nothing has loaded yet".
  await expect(page.locator(`#srv-panel-idrac-${domId} table`)).toBeVisible({ timeout: 15_000 })
  await page.waitForTimeout(1_000)
  await expect(tab.locator('[data-testid="tab-dot"]')).toHaveCount(0)
})

test('two requests disagreeing light the tab as conflicting', async ({ page }) => {
  // `reason` is unset, so both proposals are live and they disagree.
  await propose(page, { reason: 'TABDOT-A' }, 'TABDOT conflict A')
  await propose(page, { reason: 'TABDOT-B' }, 'TABDOT conflict B')
  await openServerTab(page)

  const dot = maintTab(page).locator('[data-testid="tab-dot"]')
  await expect(dot).toBeVisible({ timeout: 15_000 })
  await expect(dot).toHaveClass(/has-text-danger/)
  await expect(dot).toHaveAttribute('aria-label', 'conflicting proposals')
})
