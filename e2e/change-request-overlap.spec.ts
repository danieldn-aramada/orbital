// A reviewer is told when ANOTHER active request proposes a field this one touches.
//
// Without it the collision is invisible until one of them merges — at which
// point this request goes stale and its approvals stop counting, because an
// approval is stamped with the graph's hash. Two reviewers can otherwise spend
// attention on proposals where one is guaranteed to be discarded.
//
// The notice names the other requests and links them — deliberately NOT which
// orbIds or fields collide, which can run to any length and is one click away in
// the request it names.
//
// Only the cases a click cannot show are here. That a notice renders and stays
// absent on a terminal request is checked visually; these two are not:
// self-exclusion fails SILENTLY (the endpoint reports our own proposal, so a
// missing filter means a notice that is always present and always wrong), and
// the error path has no visible trigger at all.

import { test, expect, Page } from '@playwright/test'
import { savePolicies, clearPolicies, restorePolicies } from './policy-snapshot'

// A maintenance node this spec owns. Tests here assert "no competitor", which
// only holds while nothing else proposes against the entity — so it must not be
// shared with another spec or with whatever a developer has in flight.
const MAINT = 'colo:server-maintenance-4RK3V64'

async function api(page: Page, method: string, path: string, body?: unknown) {
  return page.request.fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    data: body === undefined ? undefined : JSON.stringify(body),
  })
}

// `reason` is a free String, so a proposal is a real change whatever the seed
// holds — unlike `enabled`, where asserting a value means knowing the current
// one and where a window may be required alongside it.
async function propose(page: Page, reason: string, title: string) {
  const res = await api(page, 'POST', '/api/v1/change-requests', {
    title, namespace: 'colo',
    changes: [{ orbId: MAINT, op: 'update', set: { reason } }],
  })
  expect(res.ok(), `propose ${title}: ${await res.text()}`).toBeTruthy()
  const id = (await res.json()).id as string
  opened.push(id)
  return id
}

// Close only what this spec opened — closing every active request would take out
// whatever a developer had in flight on their own stack.
const opened: string[] = []
test.afterEach(async ({ page }) => {
  for (const id of opened.splice(0)) {
    await api(page, 'POST', `/api/v1/change-requests/${id}/close`)
  }
})

async function openReview(page: Page, id: string) {
  await page.goto(`/change-requests/${encodeURIComponent(id)}`)
  await expect(page.locator('[data-testid="cr-detail"]')).toBeVisible({ timeout: 15_000 })
}

const notice = (page: Page) => page.locator('[data-testid="cr-overlap-notice"]')


// An approval policy is this spec's PRECONDITION, not incidental setup.
//
// With no policy matching, a change request is created already approved: there
// is nothing to approve, so it never enters StatusOpen and `availableActions`
// returns merge/close instead of approve/edit. `make seed` creates no policies,
// so on a fresh stack these tests asserted against a state the app can never be
// in — and passed only where a developer happened to have a `colo` policy
// lying around. The bypass_roles default (["admin"]) is what additionally lets
// the e2e admin, who is also the author, see `approve`: an author never
// approves their own request without bypass.
test.beforeAll(async ({ browser }) => {
  const page = await browser.newPage()
  await savePolicies(page.request)
  await clearPolicies(page.request)
  const res = await page.request.fetch('/api/v1/approval-policies', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    data: JSON.stringify({ namespace: 'colo', requiredApprovals: 1 }),
  })
  if (!res.ok()) throw new Error(`seed approval policy: ${res.status()} ${await res.text()}`)
  await page.close()
})

test.afterAll(async ({ browser }) => {
  const page = await browser.newPage()
  await restorePolicies(page.request)
  await page.close()
})

test('a request with no competitor is never marked against itself', async ({ page }) => {
  const only = await propose(page, 'OVERLAP solo', 'OVERLAP solo')
  await openReview(page, only)

  // The changes table having rendered is what makes the absence below mean
  // "no competitor" rather than "the page never loaded".
  await expect(page.locator('#cr-changes')).toContainText('reason', { timeout: 15_000 })
  await expect(notice(page)).toHaveCount(0)
})

test('a competitor is named and linked, and this request is not named against itself', async ({ page }) => {
  const mine = await propose(page, 'OVERLAP mine', 'OVERLAP competitor A')
  const theirs = await propose(page, 'OVERLAP theirs', 'OVERLAP competitor B')
  await openReview(page, mine)

  await expect(notice(page)).toContainText(theirs, { timeout: 15_000 })
  await expect(notice(page)).not.toContainText(mine)
  await expect(notice(page).locator(`a[href*="${theirs}"]`)).toHaveCount(1)
  // The whole point of the simplification: no per-field detail in the banner.
  await expect(notice(page)).not.toContainText('reason')
})

test('the review page renders fully when the overlap lookup fails', async ({ page }) => {
  await page.route('**/api/v1/proposed-changes**', (route) => route.fulfill({ status: 500, body: '{}' }))
  const mine = await propose(page, 'OVERLAP degraded', 'OVERLAP degraded')
  await propose(page, 'OVERLAP degraded other', 'OVERLAP degraded other')
  await openReview(page, mine)

  // Everything the reviewer acts on is still there; only the notice is missing.
  await expect(page.locator('#cr-changes')).toContainText('reason', { timeout: 15_000 })
  await expect(page.locator('[data-testid="cr-actions"]')).toContainText('Approve')
  await expect(notice(page)).toHaveCount(0)
  await expect(page.locator('#cr-detail-error')).toBeHidden()
})
