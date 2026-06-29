import { test, expect } from '@playwright/test';

test.describe('Divergence Reports page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/divergence-reports');
    await expect(page.locator('#divergence-content table')).toBeVisible();
  });

  test('reload button refreshes divergence report list', async ({ page }) => {
    const btn = page.locator('#btn-refresh-divergence');

    // Confirm the fetch fires — matches the datacenter.spec.ts pattern.
    await Promise.all([
      page.waitForResponse(resp => resp.url().includes('/divergence-reports') && resp.status() === 200),
      btn.click(),
    ]);

    // is-loading is removed in the .finally() after the 500ms hold + innerHTML swap.
    await expect(btn).not.toHaveClass(/is-loading/);

    // Table structure must survive the swap (data rows or the empty-state row).
    await expect(page.locator('#divergence-content table')).toBeVisible();

    // No skeleton cells left — real content is in place.
    await expect(page.locator('#divergence-content .is-skeleton')).toHaveCount(0);
  });
});
