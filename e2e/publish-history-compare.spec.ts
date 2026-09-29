import { test, expect } from '@playwright/test'

// Guards the two Compare-tab behaviours that a click-through would not reveal:
//
//  1. A deep link reproduces a specific diff. The tabs are routes precisely so
//     /publish-history/compare?from=&to= can be pasted into a ticket; if tab
//     state moved back into localStorage that silently stops working.
//  2. The compare result and the audit panel deliberately use different nouns
//     and each carries an explainer. They report different numbers for the same
//     version pair by design (ordered event stream vs net difference), and the
//     explainers are the only thing that stops that reading as a bug.
//
// Skips when fewer than two comparable artifacts exist, since a publish requires
// the OCI registry and cannot be assumed on a fresh stack.

async function comparableArtifacts(request: any): Promise<any[]> {
  const res = await request.get('/api/v1/oci/artifacts')
  if (!res.ok()) return []
  const all = await res.json()
  return (all || []).filter((a: any) => a.status === 'completed' && a.digest)
}

// pickComparablePair returns the oldest/newest pair in one data center that
// orbital can ACTUALLY retrieve, or null.
//
// `status === 'completed' && digest` only means orbital RECORDED a publish. The
// blob lives in the registry, whose lifetime is not Postgres's: recreate the
// registry container, or let it garbage-collect, and the row survives while the
// artifact does not. Comparing then fails with a 502 from ORAS — an accurate
// report about the environment, and nothing at all about the Compare tab these
// tests exist to cover. It failed that way for weeks, on an artifact published
// 2026-09-23, and every run blamed the code.
//
// So the precondition is checked the way the page checks it: ask orbital to do
// the compare. Older pairs are tried first and skipped when unretrievable, so a
// partially garbage-collected registry still exercises the UI instead of
// reporting a false failure.
// pickRetrievableArtifact returns one artifact orbital can still pull, or null.
// Same reason as the pair picker: a recorded publish is not a surviving blob.
async function pickRetrievableArtifact(request: any): Promise<any | null> {
  for (const a of await comparableArtifacts(request)) {
    const probe = await request.get(`/api/v1/export/compare?from=${a.id}&to=${a.id}`)
    if (probe.ok()) return a
  }
  return null
}

async function pickComparablePair(request: any): Promise<{ from: any, to: any } | null> {
  const usable = await comparableArtifacts(request)
  const byDC: Record<string, any[]> = {}
  for (const a of usable) (byDC[a.datacenterName] ||= []).push(a)

  for (const rows of Object.values(byDC)) {
    if (rows.length < 2) continue
    const sorted = [...rows].sort((a, b) => String(a.completedAt).localeCompare(String(b.completedAt)))
    const to = sorted[sorted.length - 1]
    for (const from of sorted.slice(0, -1)) {
      const probe = await request.get(`/api/v1/export/compare?from=${from.id}&to=${to.id}`)
      if (probe.ok()) return { from, to }
    }
  }
  return null
}

test('compare tab: pickers render and diff loads from a deep link', async ({ page, request }) => {
  const pair = await pickComparablePair(request)
  test.skip(!pair, 'no retrievable artifact pair — publish two, or the registry lost the blobs')
  const { from, to } = pair!

  await page.goto(`/publish-history/compare?from=${from.id}&to=${to.id}`)

  // Tab bar present with Compare active.
  await expect(page.locator('.tabs.is-boxed li.is-active', { hasText: 'Compare' })).toBeVisible()

  // The result renders the resolved direction, oldest → newest.
  const result = page.locator('#compare-result')
  await expect(result).toContainText(`${from.tag} → ${to.tag}`, { timeout: 20_000 })

  // Header carries DATA only — the stat row, no standing explainer prose. A
  // regression here means someone reintroduced documentation into a diff header.
  await expect(result).toContainText('Changed')
  await expect(result).toContainText('Unchanged')
  await expect(result).not.toContainText('later undone')
  // The data center name is redundant with the picker above and must not return.
  await expect(result).not.toContainText(from.datacenterName)
})

// The zero-differences case is the one place the audit-log-vs-content-diff
// distinction actually misleads: the Audit Log can show edits for a window while
// the net diff is empty, which reads as "compare is broken". Comparing an
// artifact against itself is a guaranteed way to reach that state.
test('zero-difference compare explains why it can differ from the Audit Log', async ({ page, request }) => {
  const a = await pickRetrievableArtifact(request)
  test.skip(!a, 'no retrievable artifact — the registry lost the blob for every recorded publish')

  await page.goto(`/publish-history/compare?from=${a!.id}&to=${a!.id}`)
  const result = page.locator('#compare-result')
  await expect(result).toContainText('No differences between these versions', { timeout: 20_000 })
  await expect(result).toContainText('later undone')
})

// Tab navigation is a full page load (the tabs are routes, not client-side
// panels), so without session persistence every Artifacts → Compare round trip
// silently discards the selection the user just made. Reported from real use.
test('compare selection survives a round trip through the Artifacts tab', async ({ page, request }) => {
  const pair = await pickComparablePair(request)
  test.skip(!pair, 'no retrievable artifact pair — publish two, or the registry lost the blobs')
  const { from, to } = pair!

  await page.goto(`/publish-history/compare?from=${from.id}&to=${to.id}`)
  await expect(page.locator('#compare-result')).toContainText(`${from.tag} → ${to.tag}`, { timeout: 20_000 })

  // Leave to Artifacts, then come back via the tab — no query params this time.
  await page.locator('.tabs.is-boxed a[href$="/publish-history"]').click()
  await expect(page).toHaveURL(/\/publish-history$/)
  await page.locator('.tabs.is-boxed a[href$="/publish-history/compare"]').click()
  await expect(page).toHaveURL(/\/publish-history\/compare$/)

  // The previous comparison is restored, not reset.
  await expect(page.locator('#compare-result')).toContainText(`${from.tag} → ${to.tag}`, { timeout: 20_000 })
  await expect(page.locator('#compare-from-select')).toHaveValue(String(from.id))
  await expect(page.locator('#compare-to-select')).toHaveValue(String(to.id))
})

// Cold start (no query params, no session memory) must open on a data center
// that actually has artifacts. Defaulting to the alphabetically-first DC means
// the first thing most users see is "Nothing published for this data center yet".
test('compare tab cold start opens on a data center with artifacts', async ({ page, request }) => {
  const pair = await pickComparablePair(request)
  test.skip(!pair, 'no retrievable artifact pair — publish two, or the registry lost the blobs')

  await page.context().clearCookies({ name: 'nothing' }) // no-op; keeps auth state
  await page.goto('/publish-history/compare')
  await page.evaluate(() => sessionStorage.removeItem('orbital.compare.last'))
  await page.reload()

  // Versions must populate — i.e. we did not land on an empty data center.
  const fromSel = page.locator('#compare-from-select')
  await expect(fromSel).toBeEnabled({ timeout: 20_000 })
  await expect(fromSel.locator('option')).not.toHaveCount(0)
  await expect(page.locator('#compare-result')).not.toContainText('Nothing published for this data center yet')
})
