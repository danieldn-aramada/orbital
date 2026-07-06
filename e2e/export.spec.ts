import { test, expect } from '@playwright/test';

test.describe('Export workflow (atomic)', () => {
  // OCI is the default destination on load (99% use case). Button is gated
  // on DC selection only (destination is pre-selected). Test the download
  // path — deployment-independent — and assert the atomic flow reaches a
  // terminal status.
  test('Export with destination=download triggers atomic flow', async ({ page }) => {
    await page.goto('/export');

    const submitBtn = page.locator('#export-submit-btn');
    // Initially disabled — no DC selected yet (destination is already OCI).
    await expect(submitBtn).toBeDisabled();

    const dcSelect = page.locator('#export-datacenter-select');
    const dcOption = dcSelect.locator('option[data-name]').first();
    await expect(dcOption).toBeAttached({ timeout: 8000 });
    const dcOrbId = await dcOption.getAttribute('value');
    const dcName = await dcOption.getAttribute('data-name');
    await dcSelect.selectOption(dcOrbId!);

    // OCI destination option's label should now include the DC name (verify
    // the dynamic-label plumbing works).
    const ociOption = page.locator('#export-destination-select option[data-oci="true"]');
    await expect(ociOption).toContainText(dcName!);

    // Switch to download and confirm Test Connection hides, button enables.
    await page.locator('#export-destination-select').selectOption('download');
    await expect(page.locator('#test-connection-row')).toBeHidden();
    await expect(submitBtn).toBeEnabled();

    await Promise.all([
      page.waitForResponse(
        resp =>
          resp.url().includes('/api/v1/export') &&
          !resp.url().includes('/jobs') &&
          resp.request().method() === 'POST' &&
          resp.status() === 202,
      ),
      submitBtn.click(),
    ]);

    await expect(page.locator('#export-status-box')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('#export-status-box')).toContainText(
      /Export complete|Export failed/,
      { timeout: 60_000 },
    );
  });

  // On page load: OCI is pre-selected (when configured), so Test Connection
  // is visible immediately. Switching destinations toggles it.
  test('Test Connection button toggles with destination selection', async ({ page }) => {
    await page.goto('/export');

    const testRow = page.locator('#test-connection-row');
    const destSelect = page.locator('#export-destination-select');

    const ociEnabled = await destSelect.locator('option[value="oci"]:not([disabled])').count();
    if (ociEnabled === 0) {
      test.skip(true, 'OCI destination is disabled in this deployment');
    }

    // OCI pre-selected → Test Connection visible.
    await expect(testRow).toBeVisible();

    // Switch to download → hides.
    await destSelect.selectOption('download');
    await expect(testRow).toBeHidden();

    // Switch back to OCI → visible again.
    await destSelect.selectOption('oci');
    await expect(testRow).toBeVisible();
  });
});
