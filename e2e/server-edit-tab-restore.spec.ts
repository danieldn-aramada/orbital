// Regression: after edit-modal submit, the server's active sub-tab must
// remain active AND its panel must remain visible. Two classes of bug landed
// here historically:
//   1. shared.js loaded twice (head.gohtml standalone script + module import
//      with no version query) — doubled every listener and module-level state,
//      tab restore raced itself.
//   2. afterSwap ran restore before htmx's settle phase reapplied template
//      attributes from the swap response — restore set audit panel
//      display='', settle reverted it to display:none.
// Switched to listening on htmx:afterSettle. This test would fail if either
// regression returned.

import { test, expect } from '@playwright/test'

test('audit subtab restores after edit-modal submit', async ({ page }) => {
  await page.goto('http://localhost:8001/')
  await page.waitForSelector('#inventory-table tbody tr')

  // Pick any server and open its detail in a tab via htmx.ajax (same path the
  // tab-system would use). Hitting /servers/:id directly returns the fragment.
  const srvId = await page.evaluate(async () => {
    const r = await fetch('/graphql', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query: 'query { queryServer(first: 1) { id } }' }),
    })
    return (await r.json()).data.queryServer[0].id
  })
  await page.evaluate((id) => {
    const main = document.querySelector('.app-main')!
    const wrapper = document.createElement('div')
    wrapper.id = `tab-content-srv-${id}`
    wrapper.className = 'tab-content'
    main.appendChild(wrapper)
    return (window as any).htmx.ajax('GET', `/servers/${id}`, {
      target: `#tab-content-srv-${id}`,
      swap: 'innerHTML',
    })
  }, srvId)
  await page.waitForSelector(`#srv-detail-tabs-${srvId}`)
  await page.waitForTimeout(200)

  // Switch to the Audit Log subtab and confirm it activates.
  await page.locator(`#srv-detail-tabs-${srvId} li[data-panel="srv-panel-audit-${srvId}"]`).click()
  await expect(page.locator(`#srv-panel-audit-${srvId}`)).toBeVisible()
  await expect(page.locator(`#srv-panel-idrac-${srvId}`)).toBeHidden()

  // Open the edit modal, toggle an iDRAC field, submit.
  await page.locator(`[data-srv-edit-id="${srvId}"]`).click()
  await page.waitForSelector(`#edit-modal-srv-${srvId}.is-active`)
  await page.waitForTimeout(300)
  await page.evaluate((id) => {
    const editor = (window as any).srvEditors.get(id)
    const data = JSON.parse(editor.get().text)
    data.idracSettings.sshEnabled = !data.idracSettings.sshEnabled
    editor.set({ text: JSON.stringify(data, null, 2) })
  }, srvId)
  await page.locator(`#srv-edit-submit-${srvId}`).click()
  await page.waitForFunction(
    (id) => !document.getElementById(`edit-modal-srv-${id}`)?.classList.contains('is-active'),
    srvId,
    { timeout: 5000 },
  )
  // Allow htmx swap + settle to complete.
  await page.waitForTimeout(800)

  // The actual regression check: audit tab still active, panel still visible.
  await expect(
    page.locator(`#srv-detail-tabs-${srvId} li[data-panel="srv-panel-audit-${srvId}"]`),
  ).toHaveClass(/is-active/)
  await expect(page.locator(`#srv-panel-audit-${srvId}`)).toBeVisible()
  await expect(page.locator(`#srv-panel-idrac-${srvId}`)).toBeHidden()
})
