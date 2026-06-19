// Regression guard for orbId-bearing path params. orbIds contain ":" which JS
// percent-encodes; if any of (a) the route receives "%3A", (b) the handler
// reads c.Param() raw, (c) middleware.DecodePathParams is dropped from the
// router, the delete flow silently fails because DGraph never matches the
// encoded string. This test catches the regression end-to-end by creating a
// DC with a ":" in its orbId and exercising the full UI delete flow.

import { test, expect } from '@playwright/test'

test('DataCenter delete works for orbId containing ":"', async ({ page }) => {
  const orbId = `e2e:delete-${Date.now()}`
  const name = `e2e-delete-${Date.now()}`

  // Seed: create namespace + DC via GraphQL.
  await page.goto('http://localhost:8001/')
  const created = await page.evaluate(async ({ orbId, name }) => {
    const r = await fetch('/graphql', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        query: `
          mutation Create($orbId: String!, $name: String!) {
            addNamespace(input: { name: "e2e" }, upsert: true) { numUids }
            addDataCenter(input: [{
              name: $name, orbId: $orbId, version: 1, namespace: "e2e",
              createdBy: "e2e@test", createdAt: "2026-06-18T00:00:00Z"
            }], upsert: true) { numUids }
          }`,
        variables: { orbId, name },
      }),
    })
    return await r.json()
  }, { orbId, name })
  expect(created.errors, JSON.stringify(created.errors)).toBeUndefined()

  // Cleanup if the UI delete doesn't complete — keeps the seed graph clean
  // across runs even when the test fails partway.
  const cleanup = async () => {
    await page.evaluate(async (orbId) => {
      await fetch('/graphql', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          query: `mutation Cleanup($orbId: String!) {
            deleteDataCenter(filter: { orbId: { eq: $orbId } }) { numUids }
          }`,
          variables: { orbId },
        }),
      })
    }, orbId)
  }

  try {
    // Navigate to /datacenters and confirm our DC is in the table.
    await page.goto('http://localhost:8001/datacenters')
    const row = page.locator('#datacenter-table tbody tr', { hasText: name })
    await expect(row).toBeVisible()

    // Open the DC tab (double-click is the standard flow).
    await row.dblclick()
    // Wait until the skeleton resolves — same pattern as datacenter.spec.ts.
    await expect(
      page.locator('[id^="tab-content-"] .button.is-loading'),
    ).not.toBeVisible({ timeout: 10000 })

    // Click Delete in the tab, then Confirm in the modal. The Confirm hits
    // DELETE /api/v1/config-items/DataCenter/<orbId> — the route under test.
    // Match by data attribute — the modal's Confirm button also says "Delete".
    await page.locator(`[data-cfg-delete-id="${orbId}"]`).click()
    await expect(page.locator('#cfg-delete-modal.is-active')).toBeVisible()

    // Wait for the preview API to settle before clicking confirm.
    await expect(page.locator('#cfg-delete-confirm-btn')).toBeEnabled()
    await page.locator('#cfg-delete-confirm-btn').click()

    // Successful delete closes the modal and removes the row from the table.
    await expect(page.locator('#cfg-delete-modal.is-active')).not.toBeVisible({ timeout: 10000 })
    await expect(page.locator('#datacenter-table tbody tr', { hasText: name })).toHaveCount(0)
  } finally {
    await cleanup()
  }
})
