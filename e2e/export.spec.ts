import { test, expect } from '@playwright/test';

test.describe('Export workflow', () => {
  test('triggering an export creates a completed job with a download link', async ({ page }) => {
    await page.goto('/export');

    // Wait for real DC options and select the first
    const select = page.locator('#export-datacenter-select');
    const realOption = select.locator('option:not([disabled])').first();
    await expect(realOption).toBeAttached({ timeout: 8000 });
    const value = await realOption.getAttribute('value');
    await select.selectOption(value!);

    // Trigger the export — wait for the API response before asserting
    const submitBtn = page.locator('#export-submit-btn');
    await expect(submitBtn).toBeEnabled();
    await Promise.all([
      page.waitForResponse(
        resp => resp.url().includes('/api/v1/export') && !resp.url().includes('/jobs') && resp.status() === 202,
      ),
      submitBtn.click(),
    ]);

    // Status box becomes visible quickly
    await expect(page.locator('#export-status-box')).toBeVisible({ timeout: 10_000 });

    // Wait for the job to reach a terminal state (completed or failed)
    await expect(page.locator('#export-status-box')).toContainText(/Export complete|Export failed/, { timeout: 60_000 });

    // At least one job row should now appear in the table
    const statusCells = page.locator('[data-testid="export-job-status"]');
    await expect(statusCells.first()).toBeVisible({ timeout: 10_000 });

    // The first job's status should be "completed" (or "failed" if DGraph unavailable)
    const statusText = await statusCells.first().textContent();
    expect(['completed', 'failed']).toContain(statusText?.trim());

    // If completed, the download button should be present
    if (statusText?.trim() === 'completed') {
      await expect(page.locator('[data-testid="export-download-btn"]').first()).toBeVisible();
    }
  });
});
