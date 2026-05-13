import { test, expect } from '@playwright/test';

const DC_NAME = 'colo-galleon';
const EDITED_NAME = 'colo-galleon-smoke';

test.describe('Data center edit flow', () => {
  let dcId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/datacenters');

    const row = page.locator('#datacenter-table tbody tr', { hasText: DC_NAME });
    await expect(row).toBeVisible({ timeout: 10000 });
    await row.dblclick();

    // Wait for tab content to finish loading
    await expect(
      page.locator('[id^="tab-content-"] .button.is-loading')
    ).not.toBeVisible({ timeout: 15000 });

    // Grab the DC ID from the Edit button
    const editBtn = page.locator('[data-dc-edit-id]').first();
    await expect(editBtn).toBeVisible();
    dcId = await editBtn.getAttribute('data-dc-edit-id') ?? '';
    expect(dcId).not.toBe('');
  });

  test('can edit data center name and change is persisted', async ({ page }) => {
    // Open the edit modal
    await page.locator(`[data-dc-edit-id="${dcId}"]`).click();
    await expect(page.locator(`#edit-modal-dc-${dcId}`)).toBeVisible();

    // Wait for JSONEditor to initialise (lazily on first open)
    await page.waitForFunction(
      (id) => (window as any).dcEditors?.has(id),
      dcId,
      { timeout: 5000 }
    );

    // Set the modified name via the editor API
    await page.evaluate(([ id, name ]) => {
      const editor = (window as any).dcEditors.get(id);
      const current = JSON.parse(editor.get().text);
      current.name = name;
      editor.set({ text: JSON.stringify(current, null, 2) });
    }, [dcId, EDITED_NAME]);

    // Save and wait for modal to close
    await Promise.all([
      page.waitForResponse(r => r.url().includes('/graphql') && r.status() === 200),
      page.locator(`#dc-edit-submit-${dcId}`).click(),
    ]);
    await expect(page.locator(`#edit-modal-dc-${dcId}`)).not.toBeVisible({ timeout: 10000 });

    // Reload the tab content and assert the new name appears in the summary
    await page.waitForSelector(`[id^="tab-content-"] article`, { timeout: 10000 });
    await expect(
      page.locator('[id^="tab-content-"]').getByText(EDITED_NAME)
    ).toBeVisible();

    // ── Restore original name ──────────────────────────────────────────────────
    await page.locator(`[data-dc-edit-id="${dcId}"]`).click();
    await expect(page.locator(`#edit-modal-dc-${dcId}`)).toBeVisible();

    await page.evaluate(([ id, name ]) => {
      const editor = (window as any).dcEditors.get(id);
      const current = JSON.parse(editor.get().text);
      current.name = name;
      editor.set({ text: JSON.stringify(current, null, 2) });
    }, [dcId, DC_NAME]);

    await Promise.all([
      page.waitForResponse(r => r.url().includes('/graphql') && r.status() === 200),
      page.locator(`#dc-edit-submit-${dcId}`).click(),
    ]);
    await expect(page.locator(`#edit-modal-dc-${dcId}`)).not.toBeVisible({ timeout: 10000 });
  });
});
