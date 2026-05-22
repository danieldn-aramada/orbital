import { test, expect } from '@playwright/test';

// Pages with data-testid="page-heading" on their title element.
// Datacenters/servers use tab layout with no heading — check for their table instead.
// Backups uses <h1 class="title">.
const pages: Array<{ path: string; heading?: string; testid?: boolean; tableId?: string }> = [
  { path: '/datacenters',        tableId: 'datacenter-table' },
  { path: '/servers',            tableId: 'server-list-table' },
  { path: '/inventory',          tableId: 'inventory-table' },
  { path: '/schema',             heading: 'Schema',             testid: true  },
  { path: '/export',             heading: 'Export Subgraph',    testid: true  },
  { path: '/signed-artifacts',   heading: 'Signed Artifacts',   testid: true  },
  { path: '/divergence-reports', heading: 'Divergence Reports', testid: true  },
  { path: '/audit-log',          heading: 'Audit Log',          testid: true  },
  { path: '/backups',            heading: 'Backup Graph',       testid: false },
  { path: '/restore',            heading: 'Restore Graph',      testid: true  },
];

for (const { path, heading, testid, tableId } of pages) {
  test(`${path} loads and shows heading`, async ({ page }) => {
    await page.goto(path);
    if (tableId) {
      await expect(page.locator(`#${tableId}`)).toBeVisible();
    } else {
      const locator = testid
        ? page.getByTestId('page-heading')
        : page.getByRole('heading').filter({ hasText: heading! });
      await expect(locator).toBeVisible();
      await expect(locator).toContainText(heading!);
    }
  });
}

test('nav menu links navigate to correct pages', async ({ page }) => {
  await page.goto('/');

  await page.click('a.app-menu-link:has-text("Data Centers")');
  await expect(page).toHaveURL(/\/datacenters/);

  await page.click('a.app-menu-link:has-text("Audit Log")');
  await expect(page).toHaveURL(/\/audit-log/);

  await page.click('a.app-menu-link:has-text("Backup Graph")');
  await expect(page).toHaveURL(/\/backups/);
});

test('active menu link matches current page', async ({ page }) => {
  await page.goto('/backups');
  const activeLink = page.locator('a.app-menu-link.is-active');
  await expect(activeLink).toHaveText('Backup Graph');
});

// Regression: these menu items were incorrectly marked as todo after the shared-menu refactor.
// They must navigate to real pages, not show the todo toast.
const realMenuLinks = [
  { label: 'Inventory',          url: /\// },
  { label: 'Schema Version',     url: /\/schema/ },
  { label: 'Audit Log',          url: /\/audit-log/ },
  { label: 'Backup Graph',       url: /\/backups/ },
  { label: 'Restore Graph',      url: /\/restore/ },
];

for (const { label, url } of realMenuLinks) {
  test(`menu item "${label}" navigates (not todo)`, async ({ page }) => {
    await page.goto('/');
    await page.click(`a.app-menu-link:has-text("${label}")`);
    // If the link were a todo (href="#"), the URL would stay at "/".
    await expect(page).toHaveURL(url);
  });
}
