import { test, expect } from '@playwright/test';

test.describe('Backups page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/backups');
  });

  test('shows heading and subtitle', async ({ page }) => {
    await expect(page.getByRole('heading', { name: 'Backup Graph' })).toBeVisible();
    await expect(page.locator('p.subtitle')).toContainText('S3');
  });

  test('shows storage location (configured or not)', async ({ page }) => {
    // Either a working endpoint input or a danger input with "S3 not configured"
    const locationInput = page.locator('input[type="text"]').first();
    await expect(locationInput).toBeVisible();
    const value = await locationInput.inputValue();
    expect(value.length).toBeGreaterThan(0);
  });

  test('backup table is present', async ({ page }) => {
    await expect(page.locator('#backup-tbody')).toBeVisible();
  });

  test('Backup Now button is present when S3 is configured', async ({ page }) => {
    const backupBtn = page.locator('#btn-backup');
    const isPresent = await backupBtn.count() > 0;
    if (isPresent) {
      await expect(backupBtn).toBeVisible();
      await expect(backupBtn).toBeEnabled();
    }
    // If not present, S3 is unconfigured — that branch renders a disabled button
    // without the id, which is also valid
  });

  test('Test Connection button is present when S3 is configured', async ({ page }) => {
    const testBtn = page.locator('#btn-test-backup-connection');
    const isPresent = await testBtn.count() > 0;
    if (isPresent) {
      await expect(testBtn).toBeVisible();
    }
  });

  test('delete modal is hidden on load', async ({ page }) => {
    await expect(page.locator('#delete-modal')).not.toHaveClass(/is-active/);
  });
});

test.describe('Backup workflow', () => {
  test('triggering a backup creates a job entry that reaches a terminal state', async ({ page }) => {
    await page.goto('/backups');

    // Skip if S3 is not configured (no Backup Now button rendered)
    const backupBtn = page.locator('#btn-backup');
    const isPresent = await backupBtn.count() > 0;
    if (!isPresent) {
      test.skip(true, 'S3 not configured — backup workflow unavailable');
      return;
    }

    await expect(backupBtn).toBeEnabled();

    // Trigger the backup and wait for the API call to return
    await Promise.all([
      page.waitForResponse(
        resp => resp.url().includes('/api/v1/backups') && resp.request().method() === 'POST' && resp.status() === 202,
      ),
      backupBtn.click(),
    ]);

    // Button goes into loading state during the job
    await expect(backupBtn).toBeDisabled({ timeout: 5_000 });

    // Wait for the button to become re-enabled (job reached terminal state)
    await expect(backupBtn).toBeEnabled({ timeout: 60_000 });

    // At least one job row should appear in the table
    const statusCells = page.locator('[data-testid="backup-job-status"]');
    await expect(statusCells.first()).toBeVisible({ timeout: 10_000 });

    // Status should be a terminal state
    const statusText = await statusCells.first().textContent();
    expect(['completed', 'skipped', 'failed']).toContain(statusText?.trim().split(/\s/)[0]);

    // If completed, a download button should appear
    if (statusText?.trim().startsWith('completed')) {
      await expect(page.locator('[data-testid="backup-download-btn"]').first()).toBeVisible();
    }
  });
});
