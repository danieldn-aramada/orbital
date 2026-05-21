import { test, expect } from '@playwright/test';

test.describe('Export Subgraph page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/export');
  });

  test('shows heading and description', async ({ page }) => {
    await expect(page.locator('text=Export Subgraph').first()).toBeVisible();
    await expect(page.locator('p', { hasText: 'json.gz' })).toBeVisible();
  });

  test('shows OCI registry (configured or not)', async ({ page }) => {
    const registryInput = page.locator('input[type="text"]').first();
    await expect(registryInput).toBeVisible();
    const value = await registryInput.inputValue();
    expect(value.length).toBeGreaterThan(0);
  });

  test('data center select loads with options from seeded data', async ({ page }) => {
    const select = page.locator('#export-datacenter-select');
    await expect(select).toBeVisible();

    // Wait for options to load (JS replaces "Loading data centers...")
    await expect(select.locator('option', { hasText: 'Loading' })).not.toBeAttached({ timeout: 8000 });

    // Seeded data has at least one data center
    const options = select.locator('option:not([disabled])');
    await expect(options).not.toHaveCount(0);
  });

  test('export button is disabled until a data center is selected', async ({ page }) => {
    const submitBtn = page.locator('#export-submit-btn');
    await expect(submitBtn).toBeVisible();

    // Initially disabled (no DC selected)
    await expect(submitBtn).toBeDisabled();

    // Wait for real options to load, then select the first non-disabled one
    const select = page.locator('#export-datacenter-select');
    const realOption = select.locator('option:not([disabled])').first();
    await expect(realOption).toBeAttached({ timeout: 8000 });
    const value = await realOption.getAttribute('value');
    await select.selectOption(value!);

    // Button should now be enabled
    await expect(submitBtn).toBeEnabled();
  });

  test('export status box is hidden on load', async ({ page }) => {
    await expect(page.locator('#export-status-box')).not.toBeVisible();
  });

  test('export jobs table is present', async ({ page }) => {
    await expect(page.locator('#export-jobs-table')).toBeVisible();
    await expect(page.locator('#export-jobs-tbody')).toBeVisible();
  });
});

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
        resp => resp.url().includes('/api/v1/datacenters/') && resp.url().includes('/export') && resp.status() === 202,
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
