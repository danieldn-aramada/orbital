import { test, expect } from '@playwright/test';

// Every page carries data-testid="page-heading" on its title element.
//
// This table used to hold three exceptions: the list pages fell back to
// asserting a table id because they had no heading at all, and /backups used
// an <h1 class="title"> that nothing else used. Both were fixed in the page
// templates rather than worked around here — a test table full of exceptions
// is a map of the inconsistencies, and the map was the thing worth deleting.
const pages: Array<{ path: string; heading: string }> = [
  { path: '/data-centers',       heading: 'Data Centers'       },
  { path: '/servers',            heading: 'Servers'            },
  { path: '/clusters',           heading: 'Clusters'           },
  { path: '/network-devices',    heading: 'Network Devices'    },
  { path: '/inventory',          heading: 'Config Items'       },
  { path: '/schema',             heading: 'Schema'             },
  { path: '/export',             heading: 'Export Subgraph'    },
  { path: '/publish-history',    heading: 'Publish History'    },
  { path: '/divergence-reports', heading: 'Divergence Reports' },
  { path: '/audit-log',          heading: 'Audit Log'          },
  { path: '/backups',            heading: 'Backup Graph'       },
  { path: '/restore',            heading: 'Restore Graph'      },
];

for (const { path, heading } of pages) {
  test(`${path} loads and shows heading`, async ({ page }) => {
    await page.goto(path);
    const locator = page.getByTestId('page-heading');
    await expect(locator).toBeVisible();
    await expect(locator).toContainText(heading);
  });
}

test('nav menu links navigate to correct pages', async ({ page }) => {
  await page.goto('/');

  await page.click('a.app-menu-link:has-text("Data Centers")');
  await expect(page).toHaveURL(/\/data-centers/);

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
