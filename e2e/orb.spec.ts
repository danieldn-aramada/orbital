import { test, expect } from '@playwright/test';

// Orb UI smoke tests — run against the orb server (default :8010).
// Requires: make up-orb-deps && make run-orb
// Run with: npx playwright test --config=playwright.orb.config.ts

const pages: Array<{ path: string; heading?: string; testid?: boolean; tableId?: string }> = [
  { path: '/',               heading: 'Status',              testid: true  },
  { path: '/status',         heading: 'Status',              testid: true  },
  { path: '/inventory',      tableId: 'inventory-table'                    },
  { path: '/schema',         heading: 'Schema',              testid: true  },
  { path: '/datacenter',     tableId: 'datacenter-table'                   },
  { path: '/servers',        tableId: 'server-list-table'                  },
  { path: '/import',         heading: 'Import Subgraph',     testid: true  },
  { path: '/import-history', heading: 'Import History',      testid: false }, // p.is-size-4, no testid
  { path: '/divergence',     heading: 'Divergence Report',   testid: false }, // h1.title
];

for (const { path, heading, testid, tableId } of pages) {
  test(`${path} loads without error`, async ({ page }) => {
    const response = await page.goto(path);
    expect(response?.status()).toBeLessThan(500);

    if (tableId) {
      await expect(page.locator(`#${tableId}`)).toBeVisible();
    } else if (heading) {
      const locator = testid
        ? page.getByTestId('page-heading')
        : page.locator('p.is-size-4, h1.title').filter({ hasText: heading });
      await expect(locator.first()).toBeVisible();
    }
  });
}

test('orb sidebar shows Orb menu section, not orbital sections', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('.app-menu-section-heading').filter({ hasText: 'Orb' })).toBeVisible();
  await expect(page.locator('.app-menu-section-heading').filter({ hasText: 'Sync' })).toBeVisible();
  // Orbital-only sections must not appear
  await expect(page.locator('.app-menu-section-heading').filter({ hasText: 'Edge' })).not.toBeVisible();
  await expect(page.locator('.app-menu-section-heading').filter({ hasText: 'Operations' })).not.toBeVisible();
});

test('orb navbar shows "Orb" brand', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('.navbar-brand span').first()).toContainText('Orb');
});

test('orb pages have no edit or delete buttons', async ({ page }) => {
  await page.goto('/datacenter');
  await expect(page.locator('button:has-text("Edit"), a:has-text("Edit")')).toHaveCount(0);
  await expect(page.locator('button:has-text("Delete"), a:has-text("Delete")')).toHaveCount(0);
});

test('orb sidebar nav links navigate correctly', async ({ page }) => {
  await page.goto('/');

  await page.click('a.app-menu-link:has-text("Data Center")');
  await expect(page).toHaveURL(/\/datacenter/);

  await page.click('a.app-menu-link:has-text("Import Subgraph")');
  await expect(page).toHaveURL(/\/import/);

  await page.click('a.app-menu-link:has-text("Divergence Report")');
  await expect(page).toHaveURL(/\/divergence/);
});

test('orb app version badge is visible', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('app-version')).toBeVisible();
  await expect(page.getByTestId('app-version')).toContainText('Orb');
});

// --- Import page ---

test('import page › tags table has correct column headers', async ({ page }) => {
  await page.goto('/import');
  const ths = page.locator('#orb-tags-table thead th');
  await expect(ths.nth(0)).toHaveText('Tag');
  await expect(ths.nth(1)).toHaveText('Signature');
  await expect(ths.nth(2)).toHaveText('Digest');
  await expect(ths.nth(3)).toHaveText('Size');
});

test('import page › courier section has file input and disabled upload button', async ({ page }) => {
  await page.goto('/import');
  await expect(page.locator('#orb-courier-file')).toBeAttached();
  await expect(page.locator('#orb-courier-upload-btn')).toBeVisible();
  await expect(page.locator('#orb-courier-upload-btn')).toBeDisabled();
});

test('import page › refresh and import latest buttons are present', async ({ page }) => {
  await page.goto('/import');
  await expect(page.locator('#btn-refresh-tags')).toBeVisible();
  await expect(page.locator('#btn-import-latest')).toBeVisible();
});

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

test('import history API › records include verified field', async ({ request }) => {
  const resp = await request.get('/api/v1/import/history');
  const records: Array<Record<string, unknown>> = await resp.json();
  for (const r of records) {
    expect(typeof r.verified).toBe('boolean');
  }
});
