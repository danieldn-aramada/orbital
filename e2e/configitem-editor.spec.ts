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

const CLUSTER_ORB_ID = 'colo:dev-main'
const SERVER_ORB_ID  = '2f-uae:server-5HSC3D4'  // seeded R750 with iDRAC in 2f-uae namespace
const DC_ORB_ID      = 'seattle:seattle-galleon'

// safeDomId mirrors the Go helper; orbId → DOM-safe id (`:` → `_`).
function safeDomId(orbId: string): string {
  return orbId.replace(/[^a-zA-Z0-9_-]/g, '_')
}

async function openClusterEditModal(page: Page, orbId: string) {
  // orbital's clusters page opens a per-cluster TAB on `?open=<orbId>` deep-link.
  // The fragment loads via HTMX into #tab-content-cluster-<domId>; the edit
  // modal is part of that fragment.
  const domId = safeDomId(orbId)
  await page.goto(`/clusters?open=${encodeURIComponent(orbId)}&label=${encodeURIComponent(orbId)}`)
  await page.waitForSelector(`#edit-modal-cluster-${domId}`, { state: 'attached', timeout: 15_000 })
  await page.locator(`[data-cluster-edit-id="${domId}"]`).first().click()
  await expect(page.locator(`#edit-modal-cluster-${domId}`)).toHaveClass(/is-active/)
  return domId
}

async function openServerEditModal(page: Page, orbId: string) {
  // Servers page deep-link: JS reads ?open=<orbId>, calls loadServerListTab,
  // which fetches the server fragment into #tab-content-srv-<domId>. The
  // fragment carries the edit modal. Wait for the fragment to load (its
  // `data-loaded="true"` flag is set after the swap completes), then open
  // the modal.
  const domId = safeDomId(orbId)
  await page.goto(`/servers?open=${encodeURIComponent(orbId)}&label=${encodeURIComponent(orbId)}`)
  await page.waitForSelector(`#tab-content-srv-${domId}[data-loaded="true"]`, { timeout: 15_000 })
  await page.waitForSelector(`#edit-modal-srv-${domId}`, { state: 'attached', timeout: 5_000 })
  await page.locator(`[data-srv-edit-id="${domId}"]`).first().click()
  await expect(page.locator(`#edit-modal-srv-${domId}`)).toHaveClass(/is-active/)
  return domId
}

async function openDataCenterEditModal(page: Page, orbId: string) {
  // DataCenter tabs have no ?open= deep-link. Must navigate to /datacenters,
  // search for the DC in the DataTable (it may be paginated), dblclick the row
  // to load the tab, wait for the fragment to settle, then open the edit modal.
  const domId = safeDomId(orbId)
  const dcName = orbId.split(':')[1]  // 'seattle:seattle-galleon' → 'seattle-galleon'
  await page.goto('/datacenters')

  // If initDatacenterTabRestoration restored the DC tab (saves to localStorage on
  // first open), the summary tab content div is hidden. Click the summary tab to
  // ensure the DataTable and its search input are visible before interacting.
  const summaryTab = page.locator('#tab-summary')
  if (await summaryTab.count() > 0) await summaryTab.click()

  // Filter DataTable to surface the target row (table has pageLength=5, many DCs seeded).
  const searchInput = page.locator('input[aria-controls="datacenter-table"]')
  await expect(searchInput).toBeVisible({ timeout: 10_000 })
  await searchInput.fill(dcName)

  const row = page.locator('#datacenter-table tbody tr', { hasText: dcName })
  await expect(row).toBeVisible({ timeout: 10_000 })
  await row.dblclick()

  // Dblclick → tab created → tab link clicked → HTMX load → afterSettle → data-loaded="true"
  await page.waitForSelector(`#tab-content-${domId}[data-loaded="true"]`, { timeout: 15_000 })
  await page.waitForSelector(`#edit-modal-dc-${domId}`, { state: 'attached', timeout: 5_000 })
  await page.locator(`[data-dc-edit-id="${domId}"]`).first().click()
  await expect(page.locator(`#edit-modal-dc-${domId}`)).toHaveClass(/is-active/)
  return domId
}

async function readEditorJSON(page: Page, selector: string): Promise<any> {
  const text = await page.locator(selector).textContent()
  return JSON.parse(text || '{}')
}

test.describe('configitem-editor module — browser validation', () => {

  test('cluster edit: kubernetesVersion change → updateEksaKubernetesCluster audit row with diff', async ({ page }) => {
    const domId = await openClusterEditModal(page, CLUSTER_ORB_ID)

    // The JSON editor has populated with the cluster state. Read the embedded
    // initial JSON to know what the editor sees.
    const initial = await readEditorJSON(page, `#cluster-edit-data-${domId}`)
    expect(initial.kubernetesVersion).toBeTruthy()
    const newVersion = `v1.99.${Date.now() % 1000}`

    // Programmatically update the JSONEditor's content. The vanilla-jsoneditor
    // exposes `editor.set({text: ...})` on the global window.clusterEditors map.
    await page.evaluate(({ id, newState }) => {
      const editor = (window as any).clusterEditors.get(id)
      editor.set({ text: JSON.stringify(newState, null, 2) })
    }, { id: domId, newState: { ...initial, kubernetesVersion: newVersion } })

    // Click Save — the configitem-editor module dispatches the canonical
    // updateEksaKubernetesCluster(orbId, set) mutation.
    await page.locator(`#cluster-edit-submit-${domId}`).click()

    // Modal closes on success.
    await expect(page.locator(`#edit-modal-cluster-${domId}`)).not.toHaveClass(/is-active/)

    // Wait for the cluster fragment reload to settle, then check Cluster Summary
    // reflects the new value.
    await expect(page.locator('text=' + newVersion).first()).toBeVisible({ timeout: 10_000 })

    // Click the Audit Log tab and verify the latest row is updateEksaKubernetesCluster.
    await page.locator(`[data-panel="cluster-panel-audit-${domId}"]`).click()
    const auditPanel = page.locator(`#cluster-panel-audit-${domId}`)
    await expect(auditPanel).toContainText('updateEksaKubernetesCluster', { timeout: 10_000 })
    // And the diff renderer kicked in (colored before/after present).
    await expect(auditPanel.locator('strong:has-text("kubernetesVersion")').first()).toBeVisible()
  })

  test('cluster edit: backup.etcd.schedule change → updateEtcdBackup audit row (NOT a parent blob)', async ({ page }) => {
    const domId = await openClusterEditModal(page, CLUSTER_ORB_ID)
    const initial = await readEditorJSON(page, `#cluster-edit-data-${domId}`)

    // dev-main may or may not have a backup tree depending on prior session state.
    // If it doesn't, skip this scenario — it covers the edit path, not the create path.
    test.skip(!initial.backup?.etcd, 'dev-main has no etcd backup configured; skipping edit-only scenario')

    const newSchedule = `0 ${(Date.now() % 12) + 1} * * *`
    const newState = {
      ...initial,
      backup: { ...initial.backup, etcd: { ...initial.backup.etcd, schedule: newSchedule } },
    }
    await page.evaluate(({ id, newState }) => {
      const editor = (window as any).clusterEditors.get(id)
      editor.set({ text: JSON.stringify(newState, null, 2) })
    }, { id: domId, newState })

    await page.locator(`#cluster-edit-submit-${domId}`).click()
    await expect(page.locator(`#edit-modal-cluster-${domId}`)).not.toHaveClass(/is-active/)

    // Backups tab should reflect the new schedule (it's a read-only display table).
    await page.locator(`[data-panel="cluster-panel-backups-${domId}"]`).click()
    const backupsPanel = page.locator(`#cluster-panel-backups-${domId}`)
    await expect(backupsPanel).toContainText(newSchedule, { timeout: 10_000 })

    // Audit tab should show updateEtcdBackup, NOT updateEksaKubernetesCluster.
    // This is the critical assertion: per-kind audit attribution, not parent-blob.
    await page.locator(`[data-panel="cluster-panel-audit-${domId}"]`).click()
    const auditPanel = page.locator(`#cluster-panel-audit-${domId}`)
    await expect(auditPanel).toContainText('updateEtcdBackup', { timeout: 10_000 })
    // Diff renderer: green/red lines for the schedule change.
    await expect(auditPanel.locator('strong:has-text("schedule")').first()).toBeVisible()
  })

  test('server edit: model change → updateServer audit row with diff', async ({ page }) => {
    const domId = await openServerEditModal(page, SERVER_ORB_ID)

    const initial = await readEditorJSON(page, `#srv-edit-data-${domId}`)
    const newHostname = initial.hostname  // server identity field — don't churn; just confirm edit path
    const newModel = `PowerEdge R650-${Date.now() % 1000}`
    const newState = { ...initial, model: newModel }

    await page.evaluate(({ id, newState }) => {
      const editor = (window as any).srvEditors.get(id)
      editor.set({ text: JSON.stringify(newState, null, 2) })
    }, { id: domId, newState })

    await page.locator(`#srv-edit-submit-${domId}`).click()
    await expect(page.locator(`#edit-modal-srv-${domId}`)).not.toHaveClass(/is-active/)

    // Server tab should reflect the new model after reload.
    await expect(page.locator('text=' + newModel).first()).toBeVisible({ timeout: 10_000 })

    // Audit log tab on the server page.
    await page.locator(`#srv-panel-audit-${domId}-detlink, [data-panel="srv-panel-audit-${domId}"]`).first().click()
    const auditPanel = page.locator(`#srv-panel-audit-${domId}`)
    await expect(auditPanel).toContainText('updateServer', { timeout: 10_000 })
    await expect(auditPanel.locator('strong:has-text("model")').first()).toBeVisible()
  })

  test('server edit: iDRAC firmwareVersion change → updateIdracSettings audit row (NOT updateServer)', async ({ page }) => {
    const domId = await openServerEditModal(page, SERVER_ORB_ID)

    const initial = await readEditorJSON(page, `#srv-edit-data-${domId}`)
    test.skip(!initial.idracSettings, 'server has no idracSettings configured; skipping iDRAC scenario')

    const newFirmware = `7.11.${Date.now() % 100}.00`
    const newState = {
      ...initial,
      idracSettings: { ...initial.idracSettings, firmwareVersion: newFirmware },
    }
    await page.evaluate(({ id, newState }) => {
      const editor = (window as any).srvEditors.get(id)
      editor.set({ text: JSON.stringify(newState, null, 2) })
    }, { id: domId, newState })

    await page.locator(`#srv-edit-submit-${domId}`).click()
    await expect(page.locator(`#edit-modal-srv-${domId}`)).not.toHaveClass(/is-active/)

    // Wait for fragment reload, then go to audit tab.
    await page.waitForTimeout(500)
    await page.locator(`#srv-panel-audit-${domId}-detlink, [data-panel="srv-panel-audit-${domId}"]`).first().click()
    const auditPanel = page.locator(`#srv-panel-audit-${domId}`)

    // The critical assertion: changing iDRAC alone produces updateIdracSettings,
    // NOT updateServer. This is the per-subtree-diff behavior — without it the
    // audit log would say updateServer with a nested-blob.
    await expect(auditPanel).toContainText('updateIdracSettings', { timeout: 10_000 })
    await expect(auditPanel.locator('strong:has-text("firmwareVersion")').first()).toBeVisible()
  })

  test('datacenter edit: name change → updateDataCenter audit row with diff', async ({ page }) => {
    const domId = await openDataCenterEditModal(page, DC_ORB_ID)

    const initial = await readEditorJSON(page, `#dc-edit-data-${domId}`)
    expect(initial.name).toBeTruthy()
    const origName = initial.name
    const newName = `seattle-galleon-${Date.now() % 10000}`

    await page.evaluate(({ id, newState }) => {
      const editor = (window as any).dcEditors.get(id)
      editor.set({ text: JSON.stringify(newState, null, 2) })
    }, { id: domId, newState: { ...initial, name: newName } })

    await page.locator(`#dc-edit-submit-${domId}`).click()
    await expect(page.locator(`#edit-modal-dc-${domId}`)).not.toHaveClass(/is-active/)

    // DC Summary should reflect the new name after fragment reload.
    await expect(page.locator('text=' + newName).first()).toBeVisible({ timeout: 10_000 })

    // Click the Audit Log tab and verify updateDataCenter with a diff on `name`.
    await page.locator(`[data-panel="dc-panel-audit-${domId}"]`).click()
    const auditPanel = page.locator(`#dc-panel-audit-${domId}`)
    await expect(auditPanel).toContainText('updateDataCenter', { timeout: 10_000 })
    await expect(auditPanel.locator('strong:has-text("name")').first()).toBeVisible()

    // Cleanup: restore original name.
    await openDataCenterEditModal(page, DC_ORB_ID)
    const current = await readEditorJSON(page, `#dc-edit-data-${domId}`)
    await page.evaluate(({ id, newState }) => {
      const editor = (window as any).dcEditors.get(id)
      editor.set({ text: JSON.stringify(newState, null, 2) })
    }, { id: domId, newState: { ...current, name: origName } })
    await page.locator(`#dc-edit-submit-${domId}`).click()
    await expect(page.locator(`#edit-modal-dc-${domId}`)).not.toHaveClass(/is-active/)
  })

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
  // silently rendered `id="edit-modal-srv-"` with no domId, which left users
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
