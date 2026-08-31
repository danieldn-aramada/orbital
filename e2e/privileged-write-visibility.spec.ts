// A privileged write IS a write. It belongs in the audit log like any other,
// visibly marked — not recorded only in the API where the page a person opens
// to ask "who skipped review" cannot show it.
//
// Both directions are asserted. A marker that appears on ordinary writes would
// be worse than none: it trains whoever reads the log to ignore the field.

import { test, expect, Page } from '@playwright/test'
import { savePolicies, clearPolicies, restorePolicies } from './policy-snapshot'

const SERVER_ORB_ID = '2f-uae:server-5HSC3D4'
// A server no other spec touches. The negative assertion has to be scoped to an
// entity this test owns: audit rows are written asynchronously (`go
// h.auditMutation`), so a privileged write from an earlier spec can land in the
// shared log mid-test. Counting a global total was never hermetic — it passed
// alone and failed in combination, which is the worst kind of flake.
const UNSHARED_ORB_ID = '2f-uae:server-9MLN3D4'
const NS = '2f-uae'

async function api(page: Page, method: string, path: string, body?: unknown) {
  return page.request.fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    data: body === undefined ? undefined : JSON.stringify(body),
  })
}


async function writeHostname(page: Page, value: string, orbId = SERVER_ORB_ID) {
  const res = await api(page, 'POST', '/graphql', {
    query: `mutation U($orbId:String!,$set:ServerPatch!){updateServer(input:{filter:{orbId:{eq:$orbId}},set:$set}){numUids}}`,
    variables: { orbId, set: { hostname: value } },
  })
  expect(res.ok()).toBeTruthy()
}

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
test.beforeEach(async ({ page }) => { await clearPolicies(page.request) })
test.afterEach(async ({ page }) => { await clearPolicies(page.request) })

test('a write that bypassed an approval policy is marked in the audit log page', async ({ page }) => {
  await api(page, 'POST', '/api/v1/approval-policies', { namespace: NS, requiredApprovals: 1 })
  // The e2e identity is admin, which the default policy puts in bypass_roles —
  // so this write goes through AND skips review. That is the row under test.
  await writeHostname(page, 'bypassed-' + Date.now())

  await page.goto('/audit-log')
  const marked = page.locator('#audit-log-table tbody tr', { hasText: 'bypassed review' }).first()
  await expect(marked).toBeVisible({ timeout: 15_000 })
  // It names WHICH control was skipped — "a bypass happened" is not actionable.
  await expect(marked).toContainText(NS)
})

test('an ordinary write is NOT marked', async ({ page }) => {
  await writeHostname(page, 'ordinary-' + Date.now(), UNSHARED_ORB_ID)   // no policy exists

  // Wait for this write's audit row to exist before asserting about it.
  await expect
    .poll(async () => {
      const res = await api(page, 'GET', `/api/v1/audit-log?orbId=${encodeURIComponent(UNSHARED_ORB_ID)}`)
      return res.ok() ? ((await res.json()).events || []).length : 0
    }, { timeout: 15_000 })
    .toBeGreaterThan(0)

  await page.goto('/audit-log')
  await expect(page.locator('#audit-log-table tbody tr').first()).toBeVisible({ timeout: 15_000 })

  // Scoped to rows for THIS entity, so nothing another spec did can affect it.
  const rows = page.locator('#audit-log-table tbody tr', { hasText: UNSHARED_ORB_ID })
  await expect(rows.first()).toBeVisible()
  await expect(rows.filter({ hasText: 'bypassed review' })).toHaveCount(0)
})
