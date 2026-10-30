import { test, expect, Page } from '@playwright/test';

// The audit log is its own section below Relationships, not the last tab in
// it. Names transcribed from the acceptance list:
//
//   1, 2  the strip has no audit tab, and nothing is fetched until the section is expanded
//   3     expanding shows the audit rows; collapse and re-expand do not fetch again
//   4     an expanded section stays expanded across a reload
//   5     two detail tabs on a list page each have their own section
//   6     a stale saved "audit tab" falls back to the first relationship tab

const SERVER = 'colo:server-CFRHDX3';
const OTHER = 'colo:server-GD206G4';

function countAuditRequests(page: Page) {
  const seen = { n: 0 };
  page.on('request', (r) => { if (r.url().includes('/api/v1/audit-log')) seen.n++; });
  return seen;
}

async function openServer(page: Page, orbId: string) {
  await page.goto('/servers?open=' + encodeURIComponent(orbId));
  await expect(page.getByTestId('generic-fields')).toBeVisible();
  await page.locator('[data-audit-section][data-wired="true"]').first().waitFor();
}

test('the strip has no audit tab, and nothing is fetched until the section is expanded', async ({ page }) => {
  const audits = countAuditRequests(page);
  await openServer(page, SERVER);

  const strip = page.locator('[id^="generic-detail-tabs-"]');
  await expect(strip.locator('li', { hasText: 'Audit Log' }), 'no audit tab in Relationships').toHaveCount(0);
  const section = page.getByTestId('generic-audit-section');
  await expect(section).toContainText('Audit Log');
  await expect(section.getByTestId('audit-toggle')).toHaveAttribute('aria-expanded', 'false');
  await expect(page.getByTestId('generic-audit')).toBeHidden();
  await page.waitForTimeout(300);
  expect(audits.n, 'a collapsed section must not query the audit log').toBe(0);
});

test('expanding shows the audit rows; collapse and re-expand do not fetch again', async ({ page }) => {
  const audits = countAuditRequests(page);
  await openServer(page, SERVER);
  const toggle = page.getByTestId('audit-toggle');

  await toggle.click();
  await expect(page.getByTestId('generic-audit')).toBeVisible();
  await expect(page.getByTestId('generic-audit')).not.toBeEmpty({ timeout: 10_000 });
  expect(audits.n).toBe(1);

  await toggle.click();
  await expect(page.getByTestId('generic-audit')).toBeHidden();
  await toggle.click();
  await expect(page.getByTestId('generic-audit')).toBeVisible();
  await page.waitForTimeout(300);
  expect(audits.n, 'loaded once per render').toBe(1);
});

test('an expanded section stays expanded across a reload', async ({ page }) => {
  await openServer(page, SERVER);
  await page.getByTestId('audit-toggle').click();
  await expect(page.getByTestId('generic-audit')).not.toBeEmpty({ timeout: 10_000 });

  await page.reload();
  await page.locator('[data-audit-section][data-wired="true"]').first().waitFor();
  await expect(page.getByTestId('audit-toggle')).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByTestId('generic-audit'), 'remembered open, and loaded again').not.toBeEmpty({ timeout: 10_000 });

  // And closing is remembered too.
  await page.getByTestId('audit-toggle').click();
  await page.reload();
  await page.locator('[data-audit-section][data-wired="true"]').first().waitFor();
  await expect(page.getByTestId('audit-toggle')).toHaveAttribute('aria-expanded', 'false');
});

test('two detail tabs on a list page each have their own section', async ({ page }) => {
  await page.goto('/servers?open=' + encodeURIComponent(SERVER) + '&open=' + encodeURIComponent(OTHER));
  // Only the active tab loads at first; the other loads when it is clicked.
  await page.locator(`#tablist li.tab a[data-orb-id="${SERVER}"]`).click();
  const sections = page.locator('[data-audit-section]');
  await expect(sections).toHaveCount(2, { timeout: 10_000 });
  const ids = await sections.evaluateAll((els) => els.map((e) => e.getAttribute('data-orb-id')));
  expect(new Set(ids)).toEqual(new Set([SERVER, OTHER]));
  const panels = await page.getByTestId('generic-audit').evaluateAll((els) => els.map((e) => e.id));
  expect(new Set(panels).size, 'each section has its own panel id').toBe(2);
});

test('a stale saved "audit tab" falls back to the first relationship tab', async ({ page }) => {
  await openServer(page, SERVER);
  const stripId = await page.locator('[id^="generic-detail-tabs-"]').first().getAttribute('id');
  const auditPanel = await page.getByTestId('generic-audit').getAttribute('id');
  // What the old layout saved when the audit TAB was the active one.
  await page.evaluate(([k, v]) => localStorage.setItem('tab-active:' + k, v!), [stripId!, auditPanel]);

  await page.reload();
  const strip = page.locator('[id^="generic-detail-tabs-"]').first();
  await expect(strip).toHaveAttribute('data-wired', 'true');
  await expect(strip.locator('li').first()).toHaveClass(/is-active/);
  await expect(strip.locator('li.is-active')).toHaveCount(1);
});
