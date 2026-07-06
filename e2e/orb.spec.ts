import { test, expect } from '@playwright/test';

// Orb UI tests — run against the orb server on :8010 as the `orb` Playwright
// project (see playwright.config.ts). Requires orb running locally.
// Run with: make test-e2e (or `npx playwright test --project=orb` for just orb).

test('orb sidebar nav links navigate correctly', async ({ page }) => {
  await page.goto('/');

  await page.click('a.app-menu-link:has-text("Data Center")');
  await expect(page).toHaveURL(/\/datacenter/);

  await page.click('a.app-menu-link:has-text("Import Subgraph")');
  await expect(page).toHaveURL(/\/import/);

  await page.click('a.app-menu-link:has-text("Publish Report")');
  await expect(page).toHaveURL(/\/divergence/);
});

// --- Data center / server tab fragments ---
//
// These guard a specific regression class: HTMX fragment endpoints whose route
// param name (e.g. `:orbId`) must match the c.Param() lookup in the handler.
// A mismatch renders an empty page with status 200 and no log error — invisible
// from the page-load smoke tests above, which only fetch the list page.
// NOTE: fragment render assertions are covered by Go integration tests in
// internal/orbserver/orb_render_integration_test.go. These tests guard the
// HTMX double-click + tab-open interaction flow specifically.

test('datacenter tab fragment renders populated data', async ({ page }) => {
  // Requires orb to have imported a bundle — skips on fresh make seed with no import.
  await page.goto('/datacenter');
  const row = page.locator('#datacenter-table tbody tr', { hasText: 'colo-galleon' });
  if (await row.count() === 0) {
    test.skip(true, 'orb has no imported data — run orbital export+publish+orb import first')
    return
  }
  await row.dblclick();
  await expect(
    page.locator('[id^="tab-content-"] .button.is-loading')
  ).not.toBeVisible({ timeout: 10000 });

  const summary = page.locator('article', { hasText: 'Data Center Summary' });
  await expect(summary.locator('td', { hasText: 'colo-galleon' })).toBeVisible();
  const serverCount = summary.locator('tr', { hasText: 'Servers' }).locator('td').nth(1);
  await expect(serverCount).not.toBeEmpty();
  expect(parseInt((await serverCount.textContent()) ?? '0', 10)).toBeGreaterThan(0);
});

test('cluster tab fragment renders populated data', async ({ page }) => {
  // Requires orb to have imported a bundle — skips on fresh make seed with no import.
  await page.goto('/clusters');
  await expect(page.locator('input[aria-controls="cluster-table"]')).toBeVisible({ timeout: 10_000 })
  const dataRows = page.locator('#cluster-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') })
  if (await dataRows.count() === 0) {
    test.skip(true, 'orb has no imported data — run orbital export+publish+orb import first')
    return
  }
  const row = dataRows.first()
  await row.dblclick();
  await expect(
    page.locator('[id^="tab-content-cluster-"] .button.is-loading')
  ).not.toBeVisible({ timeout: 10000 });

  const summary = page.locator('article', { hasText: 'Cluster Summary' });
  await expect(summary).toBeVisible();
  await expect(summary.locator('tr', { hasText: 'Provider' }).locator('td').nth(1)).not.toBeEmpty();
});

test('orb cluster tab has no Edit / Delete controls', async ({ page }) => {
  await page.goto('/clusters');
  const row = page.locator('#cluster-table tbody tr').first();
  await expect(row).toBeVisible();
  await row.dblclick();
  await expect(
    page.locator('[id^="tab-content-cluster-"] .button.is-loading')
  ).not.toBeVisible({ timeout: 10000 });

  // Verifies the actions-injection: orb passes layout.OrbActions (Edit/Delete=false),
  // so the shared cluster-tab template renders no edit/delete buttons inside the
  // opened tab. Guards the seam at internal/handler/cluster.go::actions(c).
  const tab = page.locator('[id^="tab-content-cluster-"]').last();
  await expect(tab.locator('[data-cluster-edit-id]')).toHaveCount(0);
  await expect(tab.locator('[data-cfg-delete-id]')).toHaveCount(0);
});

test('server tab fragment renders populated data', async ({ page }) => {
  // Requires orb to have imported a bundle — skips on fresh make seed with no import.
  await page.goto('/servers');
  await expect(page.locator('input[aria-controls="server-list-table"]')).toBeVisible({ timeout: 10_000 })
  const dataRows = page.locator('#server-list-table tbody tr').filter({ hasNot: page.locator('td.dt-empty') })
  if (await dataRows.count() === 0) {
    test.skip(true, 'orb has no imported data — run orbital export+publish+orb import first')
    return
  }
  const row = dataRows.first()
  await row.dblclick();
  await expect(
    page.locator('[id^="tab-content-srv-"] .button.is-loading')
  ).not.toBeVisible({ timeout: 10000 });

  const summary = page.locator('article', { hasText: 'Server Summary' });
  await expect(summary).toBeVisible();
  await expect(summary.locator('tr', { hasText: 'Hostname' }).locator('td').nth(1)).not.toBeEmpty();
});

// --- Import page ---

test('import page › refresh button reloads tags table', async ({ page }) => {
  await page.goto('/import')

  const btn = page.locator('#btn-refresh-tags')
  const isPresent = await btn.count() > 0
  if (!isPresent) {
    test.skip(true, 'OCI not configured — tags section not rendered')
    return
  }

  await expect(btn).toBeVisible()

  await Promise.all([
    page.waitForResponse(resp => resp.url().includes('/api/v1/import/tags') && resp.status() === 200),
    btn.click(),
  ])

  // is-loading is removed in the .finally() after the 500ms hold + innerHTML swap.
  await expect(btn).not.toHaveClass(/is-loading/)

  // tbody must be present with real content (not just skeleton spans).
  await expect(page.locator('#orb-tags-tbody')).toBeVisible()
  await expect(page.locator('#orb-tags-tbody .is-skeleton')).toHaveCount(0)
})

// --- Import tags API ---

test('import tags API › response has tags array', async ({ request }) => {
  const resp = await request.get('/api/v1/import/tags');
  expect(resp.ok()).toBeTruthy();
  const body = await resp.json();
  expect(Array.isArray(body.tags)).toBeTruthy();
});

test('import tags API › does not return .sig tags', async ({ request }) => {
  const resp = await request.get('/api/v1/import/tags');
  const body = await resp.json();
  const tags: Array<{ name: string }> = body.tags ?? [];
  const sigTags = tags.filter(t => t.name.endsWith('.sig'));
  expect(sigTags).toHaveLength(0);
});

test('import tags API › tag objects have expected shape', async ({ request }) => {
  const resp = await request.get('/api/v1/import/tags');
  const body = await resp.json();
  const tags: Array<Record<string, unknown>> = body.tags ?? [];
  for (const tag of tags) {
    expect(typeof tag.name).toBe('string');
    expect(typeof tag.verified).toBe('boolean');
    expect(typeof tag.sizeBytes).toBe('number');
    expect(typeof tag.digest).toBe('string');
  }
});

// --- Import history API ---

test('import history API › response is an array', async ({ request }) => {
  const resp = await request.get('/api/v1/import/history');
  expect(resp.ok()).toBeTruthy();
  const records = await resp.json();
  expect(Array.isArray(records)).toBeTruthy();
});

test('import history API › records include verification field', async ({ request }) => {
  const resp = await request.get('/api/v1/import/history');
  const records: Array<Record<string, unknown>> = await resp.json();
  const validValues = new Set(['verified', 'unverified', 'not-applicable']);
  for (const r of records) {
    expect(typeof r.verification).toBe('string');
    expect(validValues.has(r.verification as string)).toBeTruthy();
  }
});
