// A reviewer is told when ANOTHER active request proposes a field this one touches.
//
// Without it the collision is invisible until one of them merges — at which
// point this request goes stale and its approvals stop counting, because an
// approval is stamped with the graph's hash. Two reviewers can otherwise spend
// attention on proposals where one is guaranteed to be discarded.
//
// Only the cases a click cannot show are here. That a notice renders, names the
// other request, and stays absent on a terminal request is checked visually;
// these three are not: self-exclusion fails SILENTLY (the endpoint reports our
// own proposal, so a missing filter means a notice that is always present and
// always wrong), the conflicting/agreeing branch is invertible and an inverted
// one tells a reviewer "same value" when the values differ, and the error path
// has no visible trigger at all.

import { test, expect, Page } from '@playwright/test'

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

test('a request with no competitor is never marked against itself', async ({ page }) => {
  const only = await propose(page, 'OVERLAP solo', 'OVERLAP solo')
  await openReview(page, only)

  // The changes table having rendered is what makes the absence below mean
  // "no competitor" rather than "the page never loaded".
  await expect(page.locator('#cr-changes')).toContainText('reason', { timeout: 15_000 })
  await expect(notice(page)).toHaveCount(0)
})

test('a competitor proposing a different value is named and flagged conflicting', async ({ page }) => {
  const mine = await propose(page, 'OVERLAP mine', 'OVERLAP conflicting A')
  const theirs = await propose(page, 'OVERLAP theirs', 'OVERLAP conflicting B')
  await openReview(page, mine)

  await expect(notice(page)).toContainText('different value', { timeout: 15_000 })
  await expect(notice(page)).toContainText(theirs)
  await expect(notice(page)).toContainText('reason')
  await expect(notice(page)).not.toContainText(mine)
  // Conflict decides an outcome — whichever merges first wins the field — so it
  // is the one state here worth interrupting for.
  await expect(page.locator('[data-testid="cr-overlap-notice"].is-danger')).toHaveCount(1)
})

test('a competitor proposing the same value is named as agreeing, not conflicting', async ({ page }) => {
  const mine = await propose(page, 'OVERLAP identical', 'OVERLAP agreeing A')
  const theirs = await propose(page, 'OVERLAP identical', 'OVERLAP agreeing B')
  await openReview(page, mine)

  await expect(notice(page)).toContainText('same value', { timeout: 15_000 })
  await expect(notice(page)).toContainText(theirs)
  await expect(notice(page)).not.toContainText('different value')
  // Agreeing overlap costs only duplicated review, so it stays informational.
  await expect(page.locator('[data-testid="cr-overlap-notice"].is-info')).toHaveCount(1)
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
