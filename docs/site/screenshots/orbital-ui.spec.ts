import { test, expect, type Page } from '@playwright/test';
import { openDetailPanel } from '../../../e2e/helpers/generic';

// Regenerates the Orbital UI screenshots the docs site uses. Every shot is of
// the committed docs-example namespace (examples/docs/), so anyone can rebuild
// them and none of them shows a real deployment's data.
//
// A selector that stops matching fails the run — that is the signal a
// screenshot, and probably the page that describes it, has gone stale.

const OUT = 'docs/site/static/img/ui';
const SERVER = 'docs-example:server-DOCS001';
const RACK = 'docs-example:rack-R01';

async function openDetailView(page: Page, slug: string, orbId: string) {
  await page.goto(`/${slug}?open=${encodeURIComponent(orbId)}`);
  await expect(page.getByTestId('generic-fields')).toBeVisible();
  await page.locator('[id^="generic-detail-tabs-"][data-wired="true"]').first().waitFor();
}

test('list page', async ({ page }) => {
  await page.goto('/servers');
  await expect(page.getByTestId('page-heading')).toHaveText('Servers');
  await page.locator('#generic-table_wrapper input[type="search"]').fill('docs-example');
  await expect(page.locator('#generic-table tbody tr[data-orb-id]')).toHaveCount(2);
  await page.screenshot({ path: `${OUT}/list-page.png` });
});

test('detail page', async ({ page }) => {
  await openDetailView(page, 'servers', SERVER);
  await page.screenshot({ path: `${OUT}/detail-page.png`, fullPage: true });
});

test('single relationship renders as a panel', async ({ page }) => {
  await openDetailView(page, 'servers', SERVER);
  await openDetailPanel(page, 'idracSettings');
  await page.getByTestId('generic-detail-tabs').screenshot({ path: `${OUT}/relationship-panel.png` });
});

test('list relationship renders as a table', async ({ page }) => {
  await openDetailView(page, 'servers', SERVER);
  await openDetailPanel(page, 'networkInterfaces');
  await page.getByTestId('generic-detail-tabs').screenshot({ path: `${OUT}/relationship-table.png` });
});

test('editor', async ({ page }) => {
  await openDetailView(page, 'servers', SERVER);
  const btn = page.locator('[data-generic-edit-id]').first();
  const domId = await btn.getAttribute('data-generic-edit-id');
  await btn.click();
  const modal = page.locator(`#edit-modal-generic-${domId}`);
  await expect(modal).toHaveClass(/is-active/);
  await modal.locator('.modal-card').screenshot({ path: `${OUT}/editor.png` });
});

test('a page added by the views overlay', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.getByTestId('page-heading'),
    'no Racks page — start orbital with ORBITAL_VIEWS_OVERLAY_PATH=examples/docs/views-overlay.yaml').toHaveText('Racks');
  await openDetailView(page, 'racks', RACK);
  await page.screenshot({ path: `${OUT}/overlay-rack-page.png`, fullPage: true });
});
