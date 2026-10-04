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
  // Asserted against the GENERIC detail markup. This used to look for an
  // `article` headed "Data Center Summary" with a Servers count row — that was
  // datacenter-tab.gohtml, deleted when the pages became schema-derived. The
  // test never failed over it because it skipped itself whenever orb's graph
  // was empty, which `make up` always left it. `make seed-orb` fixes that, and
  // this now describes the page that exists.
  await page.goto('/datacenter');
  const row = page.locator('#datacenter-table tbody tr', { hasText: 'colo-galleon' });
  await expect(row).toBeVisible({ timeout: 15_000 });
  await row.dblclick();
  await expect(
    page.locator('[id^="tab-content-"] .button.is-loading')
  ).not.toBeVisible({ timeout: 10_000 });

  const tab = page.locator('[id^="tab-content-"]').last();
  await expect(tab.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });
  // The name, not the page heading: a tab renders the detail FRAGMENT, and the
  // heading belongs to the page wrapper the fragment does not include.
  await expect(tab).toContainText('colo-galleon');
  // The relationship tables the detail page derives — a data centre's racks and
  // servers — rather than a hand-written count that no longer exists.
  await expect(tab.getByTestId('generic-tab').first()).toBeVisible();
});

test('cluster tab fragment renders populated data', async ({ page }) => {
  // Requires orb to have imported a bundle — skips on fresh make seed with no import.
  // Orb derives its views from the schema it IMPORTED, so with nothing imported
  // there is no /clusters at all — a different state from "no clusters", and
  // one a fresh `make up` is always in.
  await page.goto('/clusters');
  const table = page.locator('#generic-table')
  if (await table.count() === 0) {
    test.skip(true, 'orb has no imported schema — run orbital export+publish+orb import first')
    return
  }
  const dataRows = table.locator('tbody tr').filter({ hasNot: page.locator('td.dt-empty') })
  if (await dataRows.count() === 0) {
    test.skip(true, 'orb has no imported data — run orbital export+publish+orb import first')
    return
  }
  const row = dataRows.first()
  await row.dblclick();
  const tab = page.locator('[id^="tab-content-generic-"]').last();
  await expect(tab.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });
  await expect(tab.getByTestId('generic-fields')).toContainText('Provider');
});

test('orb generic detail pages carry no mutation controls', async ({ page }) => {
  // Replaces TestOrbClusterTab_NoEditDeleteControls, deleted with
  // handler.NewClusterHandler. The property moved to the SHARED renderer, which
  // gates Edit and Delete on CanMutate — and orb never sets it. Asserted here
  // rather than in a handler test because it depends on orb's middleware and
  // the shared template together, and a handler test exercises neither.
  await page.goto('/clusters');
  const table = page.locator('#generic-table');
  if (await table.count() === 0) {
    test.skip(true, 'orb has no imported schema — run orbital export+publish+orb import first');
    return;
  }
  const dataRows = table.locator('tbody tr').filter({ hasNot: page.locator('td.dt-empty') });
  if (await dataRows.count() === 0) {
    test.skip(true, 'orb has no imported data — run orbital export+publish+orb import first');
    return;
  }
  await dataRows.first().dblclick();
  const tab = page.locator('[id^="tab-content-generic-"]').last();
  await expect(tab.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });

  await expect(tab.locator('[data-generic-edit-id]')).toHaveCount(0);
  await expect(tab.locator('[data-cfg-delete-id]')).toHaveCount(0);
  // Orb has no audit log at all — the box must not merely be empty, it must be absent.
  await expect(tab.getByTestId('generic-audit')).toHaveCount(0);
});

test('server tab fragment renders populated data', async ({ page }) => {
  // Orb derives its views from the schema it IMPORTED, so with nothing imported
  // there is no /servers at all — a different state from "no servers".
  await page.goto('/servers');
  const table = page.locator('#generic-table');
  if (await table.count() === 0) {
    test.skip(true, 'orb has no imported schema — run orbital export+publish+orb import first');
    return;
  }
  const dataRows = table.locator('tbody tr').filter({ hasNot: page.locator('td.dt-empty') });
  if (await dataRows.count() === 0) {
    test.skip(true, 'orb has no imported data — run orbital export+publish+orb import first');
    return;
  }
  await dataRows.first().dblclick();
  const tab = page.locator('[id^="tab-content-generic-"]').last();
  await expect(tab.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });
  await expect(tab.getByTestId('generic-fields')).toContainText('Hostname');
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
