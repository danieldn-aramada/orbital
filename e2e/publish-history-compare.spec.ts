import { test, expect } from '@playwright/test'

// Guards the Compare tab's two non-obvious behaviours, neither visible from a
// single click-through:
//
//  1. Direction is derived from publish time, not click order. The artifacts
//     table sorts newest-first, so selecting rows top-down yields newer-then-
//     older; if the button ever used click order the diff would silently invert
//     (before/after swapped) for roughly half of all comparisons.
//  2. The compare result and the audit panel deliberately use different nouns
//     and each carries an explainer. They report different numbers for the same
//     version pair by design (ordered event stream vs net difference), and the
//     explainers are the only thing that stops that reading as a bug.
//
// Both specs skip when fewer than two comparable artifacts exist, since a
// publish requires the OCI registry and cannot be assumed on a fresh stack.

async function comparableArtifacts(request: any): Promise<any[]> {
  const res = await request.get('/api/v1/oci/artifacts')
  if (!res.ok()) return []
  const all = await res.json()
  return (all || []).filter((a: any) => a.status === 'completed' && a.digest)
}

test('compare tab: pickers render and diff loads from a deep link', async ({ page, request }) => {
  const usable = await comparableArtifacts(request)
  test.skip(usable.length < 2, 'needs two published artifacts')

  // Same data center, oldest and newest.
  const dc = usable[0].datacenterName
  const inDC = usable
    .filter(a => a.datacenterName === dc)
    .sort((a, b) => String(a.completedAt).localeCompare(String(b.completedAt)))
  test.skip(inDC.length < 2, 'needs two artifacts in one data center')
  const from = inDC[0], to = inDC[inDC.length - 1]

  await page.goto(`/publish-history/compare?from=${from.id}&to=${to.id}`)

  // Tab bar present with Compare active.
  await expect(page.locator('.tabs.is-boxed li.is-active', { hasText: 'Compare' })).toBeVisible()

  // The result renders the resolved direction, oldest → newest.
  const result = page.locator('#compare-result')
  await expect(result).toContainText(`${from.tag} → ${to.tag}`, { timeout: 20_000 })

  // The explainer that distinguishes this from the audit panel must be present —
  // without it the two surfaces look like they contradict each other.
  await expect(result).toContainText('Net difference between the two published versions')
})

test('artifacts tab: selecting two rows enables Compare with chronological direction', async ({ page, request }) => {
  const usable = await comparableArtifacts(request)
  test.skip(usable.length < 2, 'needs two published artifacts')

  await page.goto('/publish-history')
  const boxes = page.locator('.js-compare-select')
  await expect(boxes.first()).toBeVisible({ timeout: 15_000 })
  test.skip(await boxes.count() < 2, 'needs two selectable rows')

  const btn = page.locator('#btn-compare-selected')
  await expect(btn).toBeDisabled()

  // Table is newest-first, so this ticks newer THEN older.
  const newer = boxes.nth(0), older = boxes.nth(1)
  const newerTag = await newer.getAttribute('data-tag')
  const olderTag = await older.getAttribute('data-tag')
  await newer.check()
  await older.check()

  await expect(btn).toBeEnabled()
  // Despite the click order, the label must read older → newer.
  await expect(page.locator('#btn-compare-selected-label'))
    .toHaveText(`Compare ${olderTag} → ${newerTag}`)
})
