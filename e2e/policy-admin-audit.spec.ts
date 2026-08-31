// Changing a policy is the most consequential act in change control — it
// decides what needs review at all — so it belongs in the audit log at least as
// visibly as the writes it governs. A bypassed write was already recorded here;
// removing the policy that would have gated it was not, which is backwards.
//
// Asserted through the audit log PAGE, not the API. "An operator can find out
// who turned the gate off" is a claim about what someone sees when they open
// the page they'd actually open, and a passing API test proves only that the
// data exists somewhere.

import { test, expect, Page } from '@playwright/test'

// A namespace no other spec writes policies for, so the assertions below cannot
// see rows another test created. `2f-uae` and `colo` are both taken.
const NS = 'alaska-dot'

async function api(page: Page, method: string, path: string, body?: unknown) {
  return page.request.fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    data: body === undefined ? undefined : JSON.stringify(body),
  })
}

// Deletes only the policies for THIS spec's namespace. Deleting everything the
// API returns is what wiped a developer's own policy off the dev database, and
// the isolation this spec needs never required that.
async function clearOurPolicies(page: Page) {
  const res = await api(page, 'GET', '/api/v1/approval-policies')
  if (!res.ok()) return
  for (const p of await res.json()) {
    if (p.namespace === NS) await api(page, 'DELETE', `/api/v1/approval-policies/${p.id}`)
  }
}

test.beforeEach(async ({ page }) => { await clearOurPolicies(page) })
test.afterEach(async ({ page }) => { await clearOurPolicies(page) })

test('creating, disabling and deleting a policy all appear on the audit log page', async ({ page }) => {
  const created = await api(page, 'POST', '/api/v1/approval-policies', {
    namespace: NS, allTypes: true, requiredApprovals: 2,
  })
  expect(created.ok()).toBeTruthy()
  const id = (await created.json()).id

  const disabled = await api(page, 'PATCH', `/api/v1/approval-policies/${id}`, { enabled: false })
  expect(disabled.ok()).toBeTruthy()

  const deleted = await api(page, 'DELETE', `/api/v1/approval-policies/${id}`)
  expect(deleted.ok()).toBeTruthy()

  await page.goto('/audit-log')
  await expect(page.locator('#audit-log-table tbody tr').first()).toBeVisible({ timeout: 15_000 })

  // All three acts, each naming the namespace whose gate moved — "a policy
  // changed" without saying which is not something anyone can act on.
  for (const op of ['createApprovalPolicy', 'updateApprovalPolicy', 'deleteApprovalPolicy']) {
    const row = page.locator('#audit-log-table tbody tr', { hasText: op }).first()
    await expect(row, `${op} is missing from the audit log page`).toBeVisible({ timeout: 15_000 })
    await expect(row).toContainText(NS)
  }
})

// The delete event is the ONLY surviving record that the policy existed, so it
// has to carry enough to say what protection was removed. A bare "a policy was
// deleted" cannot answer "what were we protecting before?".
test('the delete event records what the policy was', async ({ page }) => {
  const created = await api(page, 'POST', '/api/v1/approval-policies', {
    namespace: NS, allTypes: false, types: ['Server'], requiredApprovals: 3, bypassRoles: [],
  })
  expect(created.ok()).toBeTruthy()
  await api(page, 'DELETE', `/api/v1/approval-policies/${(await created.json()).id}`)

  const res = await api(page, 'GET', '/api/v1/audit-log?operation_name=deleteApprovalPolicy')
  expect(res.ok()).toBeTruthy()
  const events = (await res.json()).events || []
  const ours = events.find((e: any) => e.details?.namespace === NS)
  expect(ours, 'no deleteApprovalPolicy event for this namespace').toBeTruthy()

  expect(ours.details.before.requiredApprovals).toBe(3)
  expect(ours.details.before.types).toEqual(['Server'])
  expect(ours.details.before.allTypes).toBe(false)
  // An explicit "nobody bypasses" has to survive as itself, not as null —
  // reading it back as null would say "default", which is the opposite.
  expect(ours.details.before.bypassRoles).toEqual([])
  expect(ours.actor).toBeTruthy()
})
