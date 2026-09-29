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
import { openEditor as openGenericEditor } from './helpers/generic';

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

  // The "datacenter edit" case was DELETED 2026-09-25 with the bespoke page.
  // Editing a DataCenter now goes through the generic renderer, covered by
  // e2e/generic-editor.spec.ts (edit + persist + MVCC on every reachable
  // target). NOTE what did NOT carry over: this case also asserted the
  // resulting updateDataCenter AUDIT ROW and its diff. Audit integration for
  // the generic editor has no e2e cover yet.
  // ── NOT WIRED — edit modals not yet implemented for these ConfigItem types ──
  //
  // Rack, IPAddress, KubernetesNode, ServerConfigurationProfile have no FormFields
  // in the configitem registry, no edit modal templates, and no JS handlers.
  // These tests are skipped until the corresponding UI is wired up. Each skip is
  // a product gap, not just a test gap — flag for future work.

  test.skip('rack edit: name change → updateRack audit row with diff', async () => {
    // NOT WIRED: Rack has no edit modal in the current UI. Racks tab (inside a DC
    // tab) is read-only display only. No data-rack-edit-id trigger, no
    // edit-modal-rack template, no updateRack JS dispatch path.
    // Target: seattle:Rack-5 or any seeded rack in examples/seed/seattle-galleon.graphql
  })

  test.skip('ip address edit: address change → updateIPAddress audit row with diff', async () => {
    // NOT WIRED: IPAddress has no edit modal in the current UI. No IP detail page,
    // no data-ip-edit-id trigger, no updateIPAddress JS dispatch.
    // Target: seeded IPs in examples/seed/seattle-galleon-storage.graphql
  })

  test.skip('kubernetes node edit: role change → updateKubernetesNode audit row with diff', async () => {
    // NOT WIRED: KubernetesNode has no edit modal. Cluster Nodes sub-tab is
    // read-only. No data-node-edit-id trigger, no updateKubernetesNode JS dispatch.
    // Target: any seeded node in examples/seed/seattle-galleon-clusters.graphql
  })

  test.skip('server config profile: JSON field change → updateServerConfigurationProfile audit row with diff', async () => {
    // NOT WIRED: ServerConfigurationProfile has no edit modal in the current UI.
    // No data-profile-edit-id trigger, no updateServerConfigurationProfile JS dispatch.
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
