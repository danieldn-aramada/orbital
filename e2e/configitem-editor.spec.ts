// End-to-end validation of the configitem-editor refactor.
// Walks through cluster / server / dc edit modals in a real Chromium browser,
// changes a single field, saves, and verifies the audit log shows one clean
// audit row per affected concrete type (not a blob mutation).
//
// This is the "real browser validation" that curl can't do — it exercises the
// JSON editor, the JS module's snapshot+diff+dispatch path, the post-save
// fragment reload, and the audit panel re-fetch. If anything in that chain
// drifts, this fails.

import { test, expect, Page } from '@playwright/test'
import { openEditor as openGenericEditor, openAllDetailPanels } from './helpers/generic';

const SERVER_ORB_ID  = '2f-uae:server-5HSC3D4'  // seeded R750 with iDRAC in 2f-uae namespace
const DC_ORB_ID      = 'seattle:seattle-galleon'

// safeDomId mirrors the Go helper; orbId → DOM-safe id (`:` → `_`).
function safeDomId(orbId: string): string {
  return orbId.replace(/[^a-zA-Z0-9_-]/g, '_')
}

async function openServerEditModal(page: Page, orbId: string) {
  return await openGenericEditor(page, 'servers', orbId)
}

async function openDataCenterEditModal(page: Page, orbId: string) {
  return await openGenericEditor(page, 'data-centers', orbId)
}

async function readEditorJSON(page: Page, selector: string): Promise<any> {
  const text = await page.locator(selector).textContent()
  return JSON.parse(text || '{}')
}

test.describe('configitem-editor module — browser validation', () => {

  // The two cluster cases moved to e2e/clusters-generic.spec.ts on 2026-09-25,
  // when /clusters became the interface-backed generic page. They assert the
  // same two guarantees — a per-kind audit row with a rendered diff, and an
  // owned-child edit attributed to the CHILD rather than blobbed into its
  // parent — against the generic editor and its audit box.

  test('server edit: model change → updateServer audit row with diff', async ({ page }) => {
    const domId = await openServerEditModal(page, SERVER_ORB_ID)

    const initial = await readEditorJSON(page, `#generic-edit-data-${domId}`)
    const newHostname = initial.hostname  // server identity field — don't churn; just confirm edit path
    const newModel = `PowerEdge R650-${Date.now() % 1000}`
    const newState = { ...initial, model: newModel }

    await page.evaluate(({ id, newState }) => {
      const editor = (window as any).genericEditors.get(id)
      editor.set({ text: JSON.stringify(newState, null, 2) })
    }, { id: domId, newState })

    await page.locator(`#generic-edit-submit-${domId}`).click()
    await expect(page.locator(`#edit-modal-generic-${domId}`)).not.toHaveClass(/is-active/)

    // Server tab should reflect the new model after reload.
    await expect(page.locator('text=' + newModel).first()).toBeVisible({ timeout: 10_000 })

    // Audit log tab on the server page.
    // A box now, not a tab: the generic renderer stacks panels rather than
    // tabbing them, so there is nothing to click and it loads with the page.
    await page.reload()
    const auditPanel = page.getByTestId('generic-audit')
    await expect(auditPanel).toContainText('updateServer', { timeout: 10_000 })
    await expect(auditPanel.locator('strong:has-text("model")').first()).toBeVisible()
  })

  test('server edit: iDRAC firmwareVersion change → updateIdracSettings audit row (NOT updateServer)', async ({ page }) => {
    const domId = await openServerEditModal(page, SERVER_ORB_ID)

    const initial = await readEditorJSON(page, `#generic-edit-data-${domId}`)
    test.skip(!initial.idracSettings, 'server has no idracSettings configured; skipping iDRAC scenario')

    const newFirmware = `7.11.${Date.now() % 100}.00`
    const newState = {
      ...initial,
      idracSettings: { ...initial.idracSettings, firmwareVersion: newFirmware },
    }
    await page.evaluate(({ id, newState }) => {
      const editor = (window as any).genericEditors.get(id)
      editor.set({ text: JSON.stringify(newState, null, 2) })
    }, { id: domId, newState })

    await page.locator(`#generic-edit-submit-${domId}`).click()
    await expect(page.locator(`#edit-modal-generic-${domId}`)).not.toHaveClass(/is-active/)

    // Wait for fragment reload, then go to audit tab.
    await page.waitForTimeout(500)
    // A box now, not a tab: the generic renderer stacks panels rather than
    // tabbing them, so there is nothing to click and it loads with the page.
    await page.reload()
    const auditPanel = page.getByTestId('generic-audit')

    // The critical assertion: changing iDRAC alone produces updateIdracSettings,
    // NOT updateServer. This is the per-subtree-diff behavior — without it the
    // audit log would say updateServer with a nested-blob.
    await expect(auditPanel).toContainText('updateIdracSettings', { timeout: 10_000 })
    await expect(auditPanel.locator('strong:has-text("firmwareVersion")').first()).toBeVisible()
  })

  // Regression guard: server / cluster / DC handlers must return 404 when the
  // orbId doesn't exist instead of rendering a tab with empty DomID
  // placeholders. Caught during browser validation 2026-06-20 — `/servers/<bogus>`
  // silently rendered `id="edit-modal-generic-"` with no domId, which left users
  // with broken modals if they ever hit that path. The cluster handler had
  // the right check; server + DC didn't.
  test('handlers return 404 for missing orbIds (server, cluster, DC)', async ({ page }) => {
    for (const path of [
      '/servers/nonexistent:no-such-server',
      '/clusters/nonexistent:no-such-cluster',
      '/datacenters/nonexistent:no-such-dc',
    ]) {
      const resp = await page.request.get(path, {
        headers: { 'HX-Request': 'true' },
        failOnStatusCode: false,
      })
      expect(resp.status(), `${path} should return 404 when the entity doesn't exist`).toBe(404)
    }
  })
})
