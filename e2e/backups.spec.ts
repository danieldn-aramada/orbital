import { test, expect } from '@playwright/test';

test.describe('Backup workflow', () => {
  test('triggering a backup creates a job entry that reaches a terminal state', async ({ page }) => {
    await page.goto('/backups');

    // Skip if S3 is not configured (no Backup Now button rendered)
    const backupBtn = page.locator('#btn-backup');
    const isPresent = await backupBtn.count() > 0;
    if (!isPresent) {
      test.skip(true, 'Object store not configured — backup workflow unavailable');
      return;
    }

    await expect(backupBtn).toBeEnabled();

    // Trigger the backup and wait for the API call to return
    await Promise.all([
      page.waitForResponse(
        resp => resp.url().includes('/api/v1/backup') && !resp.url().includes('/jobs') && resp.request().method() === 'POST' && resp.status() === 202,
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
