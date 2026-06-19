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

// safeDomId mirrors web/shared/static/shared.js — orbIds can contain ":"
// (e.g. "colo:9BQS2Z3") which is a CSS pseudo-class operator. The Go handler
// computes the same mapping into the `.DomID` field used by templates.
const safeDomId = (s: string) => s.replace(/[^A-Za-z0-9_-]/g, '_')

test('audit subtab restores after edit-modal submit', async ({ page }) => {
  await page.goto('http://localhost:8001/')
  await page.waitForSelector('#inventory-table tbody tr')

  // Pick any server and open its detail in a tab via htmx.ajax (same path the
  // tab-system would use). Hitting /servers/:orbId directly returns the fragment.
  const orbId = await page.evaluate(async () => {
    const r = await fetch('/graphql', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query: 'query { queryServer(first: 1) { orbId } }' }),
    })
    return (await r.json()).data.queryServer[0].orbId
  })
  const domId = safeDomId(orbId)
  await page.evaluate(({ orbId, domId }) => {
    const main = document.querySelector('.app-main')!
    const wrapper = document.createElement('div')
    wrapper.id = `tab-content-srv-${domId}`
    wrapper.className = 'tab-content'
    main.appendChild(wrapper)
    return (window as any).htmx.ajax('GET', `/servers/${encodeURIComponent(orbId)}`, {
      target: `#tab-content-srv-${domId}`,
      swap: 'innerHTML',
    })
  }, { orbId, domId })
  await page.waitForSelector(`#srv-detail-tabs-${domId}`)
  await page.waitForTimeout(200)

  // Switch to the Audit Log subtab and confirm it activates.
  await page.locator(`#srv-detail-tabs-${domId} li[data-panel="srv-panel-audit-${domId}"]`).click()
  await expect(page.locator(`#srv-panel-audit-${domId}`)).toBeVisible()
  await expect(page.locator(`#srv-panel-idrac-${domId}`)).toBeHidden()

  // Open the edit modal, toggle an iDRAC field, submit.
  await page.locator(`[data-srv-edit-id="${domId}"]`).click()
  await page.waitForSelector(`#edit-modal-srv-${domId}.is-active`)
  await page.waitForTimeout(300)
  await page.evaluate((id) => {
    const editor = (window as any).srvEditors.get(id)
    const data = JSON.parse(editor.get().text)
    data.idracSettings.sshEnabled = !data.idracSettings.sshEnabled
    editor.set({ text: JSON.stringify(data, null, 2) })
  }, domId)
  await page.locator(`#srv-edit-submit-${domId}`).click()
  await page.waitForFunction(
    (id) => !document.getElementById(`edit-modal-srv-${id}`)?.classList.contains('is-active'),
    domId,
    { timeout: 5000 },
  )
  // Allow htmx swap + settle to complete.
  await page.waitForTimeout(800)

  // The actual regression check: audit tab still active, panel still visible.
  await expect(
    page.locator(`#srv-detail-tabs-${domId} li[data-panel="srv-panel-audit-${domId}"]`),
  ).toHaveClass(/is-active/)
  await expect(page.locator(`#srv-panel-audit-${domId}`)).toBeVisible()
  await expect(page.locator(`#srv-panel-idrac-${domId}`)).toBeHidden()
})
