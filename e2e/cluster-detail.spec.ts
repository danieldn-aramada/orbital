import { test, expect } from '@playwright/test';

// safeDomId mirrors the Go helper: non-alphanumeric chars → '_'
function safeDomId(orbId: string): string {
  return orbId.replace(/[^a-zA-Z0-9_-]/g, '_')
}

const CLUSTER_ORB_ID = 'colo:dev-main'

test.describe('Cluster detail tab', () => {
  test.beforeEach(async ({ page }) => {
    const domId = safeDomId(CLUSTER_ORB_ID)
    await page.goto(`/clusters?open=${encodeURIComponent(CLUSTER_ORB_ID)}&label=${encodeURIComponent(CLUSTER_ORB_ID)}`)

    // Wait for the cluster tab content to finish loading (skeleton clears)
    await expect(
      page.locator(`#tab-content-cluster-${domId} .button.is-loading`)
    ).not.toBeVisible({ timeout: 10_000 })

    // Summary article must be present before proceeding
    await expect(page.locator('article', { hasText: 'Cluster Summary' })).toBeVisible()
  })

  test('reload button refreshes content and sub-tabs still work', async ({ page }) => {
    const domId = safeDomId(CLUSTER_ORB_ID)
    const reloadBtn = page.locator('.js-cluster-reload')

    // Confirm the fetch fires — mirrors the datacenter.spec.ts pattern.
    await Promise.all([
      page.waitForResponse(
        resp => resp.url().includes('/clusters/') && resp.status() === 200
      ),
      reloadBtn.click(),
    ])

    // Reload paints a skeleton then swaps real content; wait for skeleton to clear.
    await expect(page.locator(`#tab-content-cluster-${domId} .is-skeleton`)).toHaveCount(0, { timeout: 10_000 })

    // Cluster Summary must still be present after the swap.
    await expect(page.locator('article', { hasText: 'Cluster Summary' })).toBeVisible()

    // Sub-tab click handlers must be re-bound after the reload
    // (guarded by initClusterDetailTabs call inside reloadClusterFragment).
    // Use the Backups tab — always rendered regardless of cluster type.
    await page.locator(`[data-panel="cluster-panel-backups-${domId}"]`).click()
    const backupsPanel = page.locator(`#cluster-panel-backups-${domId}`)
    await expect(backupsPanel).toBeVisible()

    const nodesPanel = page.locator(`#cluster-panel-nodes-${domId}`)
    await expect(nodesPanel).toBeHidden()

    // Switch back to Nodes — handler still live.
    await page.locator(`[data-panel="cluster-panel-nodes-${domId}"]`).click()
    await expect(nodesPanel).toBeVisible()
    await expect(backupsPanel).toBeHidden()
  })
})
