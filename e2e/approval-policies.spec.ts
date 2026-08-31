// The approval-policies admin page. Acceptance items from the design agreed
// 2026-08-29: a segmented Enabled|Disabled control (Users-page pattern), no
// Status column, the enforcement anomaly rendered ON the control, and an
// asymmetric confirm — free to protect, deliberate to unprotect.
//
// Policies are GLOBAL state: one row changes how every editor in the app
// behaves. Each test clears them first and after.

import { test, expect, Page } from '@playwright/test'
import { savePolicies, clearPolicies, restorePolicies } from './policy-snapshot'

async function api(page: Page, method: string, path: string, body?: unknown) {
  return page.request.fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json' },
    data: body === undefined ? undefined : JSON.stringify(body),
  })
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

test('the enable state is a segmented control, not an action button, and there is no Status column', async ({ page }) => {
  await api(page, 'POST', '/api/v1/approval-policies', { namespace: 'colo', requiredApprovals: 1 })
  await page.goto('/approval-policies')

  const row = page.locator('#ap-tbody tr').first()
  await expect(row).toBeVisible()

  // Both options are present, so nothing is implied about what a click does —
  // which is the defect a lone "Disable" button (or a toggle) has.
  await expect(row.locator('.js-ap-enable')).toHaveText('Active')
  await expect(row.locator('.js-ap-disable')).toHaveText('Disabled')

  // The active one is highlighted AND disabled, matching the Users page.
  await expect(row.locator('.js-ap-enable')).toBeDisabled()
  await expect(row.locator('.js-ap-enable')).toHaveClass(/is-selected/)
  // Green while it is actually enforcing. There is no third state: with
  // enforcement per-policy and no global switch, `enabled` IS enforced.
  await expect(row.locator('.js-ap-enable')).toHaveClass(/is-success/)
  await expect(row.locator('.js-ap-disable')).toBeEnabled()
  await expect(row.locator('.js-ap-disable')).not.toHaveClass(/is-danger/)

  // The old action button is gone, and so is the column that duplicated it.
  await expect(row.locator('.js-ap-toggle')).toHaveCount(0)
  const headers = await page.locator('#ap-table thead th').allTextContents()
  expect(headers).not.toContain('Status')
  expect(headers).toContain('Enforcement')
})

test('the Add policy button sits above the table it adds to', async ({ page }) => {
  await page.goto('/approval-policies')
  const order = await page.evaluate(() => {
    const btn = document.getElementById('ap-add')!
    const table = document.getElementById('ap-table')!
    // Node.DOCUMENT_POSITION_FOLLOWING === 4 when table comes after btn.
    return btn.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING ? 'above' : 'below'
  })
  expect(order).toBe('above')
})

test('disabling asks first; enabling does not', async ({ page }) => {
  await api(page, 'POST', '/api/v1/approval-policies', { namespace: 'colo', requiredApprovals: 1 })
  await page.goto('/approval-policies')
  const row = page.locator('#ap-tbody tr').first()
  await expect(row.locator('.js-ap-enable')).toBeDisabled()

  // Turning protection OFF is the consequential direction, so it confirms.
  let asked = ''
  page.once('dialog', d => { asked = d.message(); d.accept() })
  await row.locator('.js-ap-disable').click()
  await expect(page.locator('#ap-tbody tr').first().locator('.js-ap-disable')).toBeDisabled()
  expect(asked, 'disabling a policy did not confirm').toContain('no review')

  // Turning it back ON is free — no dialog. If one appeared, this would hang
  // and fail rather than silently pass.
  let dialogs = 0
  page.on('dialog', d => { dialogs++; d.accept() })
  await page.locator('#ap-tbody tr').first().locator('.js-ap-enable').click()
  await expect(page.locator('#ap-tbody tr').first().locator('.js-ap-enable')).toBeDisabled()
  expect(dialogs, 'enabling a policy should not confirm').toBe(0)

  const after = await (await api(page, 'GET', '/api/v1/approval-policies')).json()
  expect(after[0].enabled).toBe(true)
})

test('a disabled policy shows Disabled as the active side', async ({ page }) => {
  const created = await api(page, 'POST', '/api/v1/approval-policies', {
    namespace: 'colo', requiredApprovals: 1, enabled: false,
  })
  expect(created.ok()).toBeTruthy()
  await page.goto('/approval-policies')

  const row = page.locator('#ap-tbody tr').first()
  await expect(row.locator('.js-ap-disable')).toBeDisabled()
  await expect(row.locator('.js-ap-disable')).toHaveClass(/is-selected/)
  // Red, matching the Users page's severity ramp — a disabled policy sits in
  // the table looking like protection while gating nothing, so a scan has to
  // catch it. Vocabulary follows the category: enforcement is per-policy in
  // GitHub rulesets, Kyverno, Gatekeeper and Sentinel alike.
  await expect(row.locator('.js-ap-disable')).toHaveClass(/is-danger/)
  await expect(row.locator('.js-ap-enable')).toBeEnabled()
  await expect(row.locator('.js-ap-enable')).not.toHaveClass(/is-danger/)
})

test('the create modal uses pickers, not free text', async ({ page }) => {
  await page.goto('/approval-policies')
  await page.locator('#ap-add').click()
  await expect(page.locator('#ap-modal')).toHaveClass(/is-active/)

  // A typo'd namespace would create a policy that reports itself enforced and
  // gates nothing, so the field must not accept arbitrary text.
  await expect(page.locator('select#ap-namespace')).toBeVisible()
  await expect(page.locator('select#ap-type')).toBeVisible()
  // Scope is a deliberate MODE, defaulting to the whole namespace — not an
  // empty selection standing in for "everything", which needed a paragraph of
  // help text to explain and was a hidden mode.
  await expect(page.locator('#ap-all-types')).toBeChecked()
  await expect(page.locator('select#ap-type')).toBeDisabled()
  await expect(page.locator('select#ap-type')).toHaveAttribute('multiple', '')
  await expect(page.locator('#ap-type option[value=""]')).toHaveCount(0)

  // Unticking it is what opens the list.
  await page.locator('#ap-all-types').uncheck()
  await expect(page.locator('select#ap-type')).toBeEnabled()
  const namespaces = await page.locator('#ap-namespace option').allTextContents()
  expect(namespaces.length).toBeGreaterThan(0)
  expect(namespaces).toContain('colo')

  // bypassRoles is settable at all — it defaulted silently to ["admin"] before.
  await expect(page.locator('.ap-bypass')).toHaveCount(2)
  await expect(page.locator('.ap-bypass[value="admin"]')).toBeChecked()
})

test('selecting several types creates ONE policy holding the list', async ({ page }) => {
  await page.goto('/approval-policies')
  await page.locator('#ap-add').click()
  await expect(page.locator('#ap-modal')).toHaveClass(/is-active/)

  await page.locator('#ap-namespace').selectOption('colo')
  await page.locator('#ap-all-types').uncheck()
  await page.locator('#ap-type').selectOption(['Server', 'IdracSettings', 'Rack'])
  await page.locator('#ap-modal-save').click()

  await expect(page.locator('#ap-modal')).not.toHaveClass(/is-active/)

  // One row, not three. Three rows in one namespace would each be separately
  // switchable, and "why was my change gated?" would have three candidate
  // answers with no written rule for which one applied.
  await expect(page.locator('#ap-tbody tr')).toHaveCount(1)

  const policies = await (await api(page, 'GET', '/api/v1/approval-policies')).json()
  expect(policies).toHaveLength(1)
  expect(policies[0].allTypes).toBe(false)
  expect([...policies[0].types].sort()).toEqual(['IdracSettings', 'Rack', 'Server'])
  expect(policies[0].namespace).toBe('colo')
  expect(policies[0].enabled).toBe(true)
})

// The default scope is the whole namespace, and that is a BOOLEAN rather than
// a list of every type on purpose: a ConfigItem added to the schema later is
// covered the day it lands. Nineteen enumerated types cover nineteen types and
// let the twentieth arrive ungoverned.
test('the default scope is all types, including ones added later', async ({ page }) => {
  await page.goto('/approval-policies')
  await page.locator('#ap-add').click()
  await page.locator('#ap-namespace').selectOption('colo')
  await page.locator('#ap-modal-save').click()   // scope left at its default

  await expect(page.locator('#ap-tbody tr')).toHaveCount(1)
  await expect(page.locator('#ap-tbody tr').first()).toContainText('All types')

  const policies = await (await api(page, 'GET', '/api/v1/approval-policies')).json()
  expect(policies).toHaveLength(1)
  expect(policies[0].allTypes).toBe(true)
  expect(policies[0].types ?? []).toEqual([])
})

test('choosing specific types but picking none is refused, not silently treated as all', async ({ page }) => {
  await page.goto('/approval-policies')
  await page.locator('#ap-add').click()
  await page.locator('#ap-namespace').selectOption('colo')
  await page.locator('#ap-all-types').uncheck()
  await page.locator('#ap-modal-save').click()

  await expect(page.locator('#ap-modal-error')).toBeVisible()
  await expect(page.locator('#ap-modal')).toHaveClass(/is-active/)
  const policies = await (await api(page, 'GET', '/api/v1/approval-policies')).json()
  expect(policies).toHaveLength(0)
})

// One policy per namespace, so a second submit for the same namespace collides.
// The modal has to stay open saying so — a create that vanishes leaves the
// operator believing their scope change landed when the namespace still holds
// the old policy.
test('a second policy for the same namespace is refused in the modal', async ({ page }) => {
  await api(page, 'POST', '/api/v1/approval-policies', {
    namespace: 'colo', allTypes: false, types: ['Server'], requiredApprovals: 1,
  })

  await page.goto('/approval-policies')
  await page.locator('#ap-add').click()
  await page.locator('#ap-namespace').selectOption('colo')
  await page.locator('#ap-all-types').uncheck()
  await page.locator('#ap-type').selectOption(['Server', 'Rack'])
  await page.locator('#ap-modal-save').click()

  const err = page.locator('#ap-modal-error')
  await expect(err).toBeVisible()
  await expect(err).toContainText('namespace')
  await expect(page.locator('#ap-modal')).toHaveClass(/is-active/)   // stays open to fix

  // The existing policy is untouched — a refused create must not half-apply.
  const policies = await (await api(page, 'GET', '/api/v1/approval-policies')).json()
  expect(policies).toHaveLength(1)
  expect(policies[0].types).toEqual(['Server'])
})

// One policy per namespace means "also protect Rack" has to be an EDIT. Without
// it the operator's only route is delete-and-rebuild from memory, and the API's
// own 409 tells them to PATCH — advice a UI with no edit path cannot follow.
test('an existing policy can be edited in place, and its namespace cannot move', async ({ page }) => {
  await api(page, 'POST', '/api/v1/approval-policies', {
    namespace: 'colo', allTypes: false, types: ['Server'], requiredApprovals: 1,
  })

  await page.goto('/approval-policies')
  await page.locator('.js-ap-edit').first().click()
  await expect(page.locator('#ap-modal')).toHaveClass(/is-active/)

  // Prefilled from the row, so an edit starts from what is actually stored
  // rather than from the form's defaults.
  await expect(page.locator('#ap-namespace')).toHaveValue('colo')
  await expect(page.locator('#ap-all-types')).not.toBeChecked()
  await expect(page.locator('#ap-type')).toHaveValues(['Server'])

  // The namespace is the policy's identity — moving it would be deleting one
  // policy and creating another, which is not what "Edit" says.
  await expect(page.locator('#ap-namespace')).toBeDisabled()

  await page.locator('#ap-type').selectOption(['Server', 'Rack'])
  await page.locator('#ap-required').fill('2')
  await page.locator('#ap-modal-save').click()

  await expect(page.locator('#ap-modal')).not.toHaveClass(/is-active/)
  await expect(page.locator('#ap-tbody tr')).toHaveCount(1)

  const policies = await (await api(page, 'GET', '/api/v1/approval-policies')).json()
  expect(policies).toHaveLength(1)
  expect([...policies[0].types].sort()).toEqual(['Rack', 'Server'])
  expect(policies[0].requiredApprovals).toBe(2)
})

test('editing to a contradictory scope is refused and the stored policy is untouched', async ({ page }) => {
  await api(page, 'POST', '/api/v1/approval-policies', {
    namespace: 'colo', allTypes: false, types: ['Server'], requiredApprovals: 1,
  })

  await page.goto('/approval-policies')
  await page.locator('.js-ap-edit').first().click()
  await expect(page.locator('#ap-all-types')).not.toBeChecked()   // prefilled
  await page.locator('#ap-type').selectOption([])                 // clear the selection
  await page.locator('#ap-modal-save').click()

  await expect(page.locator('#ap-modal-error')).toBeVisible()
  await expect(page.locator('#ap-modal')).toHaveClass(/is-active/)

  const policies = await (await api(page, 'GET', '/api/v1/approval-policies')).json()
  expect(policies[0].types).toEqual(['Server'])
  expect(policies[0].allTypes).toBe(false)
})
