import { test, expect } from '@playwright/test';

test.describe('Restore Graph page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/restore');
  });

  test('shows heading', async ({ page }) => {
    await expect(page.locator('text=Restore Graph').first()).toBeVisible();
  });

  test('shows destructive operation warning', async ({ page }) => {
    await expect(page.locator('.notification.is-warning', { hasText: 'destructive' })).toBeVisible();
  });

  test('restore history table is present', async ({ page }) => {
    await expect(page.locator('#restore-tbody')).toBeVisible();
    await expect(page.locator('#restore-tbody td', { hasText: 'Loading...' })).not.toBeVisible({ timeout: 8000 });
  });

  test('local file runbook section is visible', async ({ page }) => {
    await expect(page.locator('text=From a local file').first()).toBeVisible();
    await expect(page.locator('pre', { hasText: 'dgraph live' })).toBeVisible();
  });

  test('restore log modal is hidden on load', async ({ page }) => {
    await expect(page.locator('#restore-log-modal')).not.toHaveClass(/is-active/);
  });
});
