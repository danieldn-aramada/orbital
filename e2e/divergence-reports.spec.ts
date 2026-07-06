import { test, expect } from '@playwright/test';

test.describe('Divergence Reports page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/divergence-reports');
    await expect(page.locator('#divergence-content table.is-striped')).toBeVisible();
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
    await expect(page.locator('#divergence-content table.is-striped')).toBeVisible();

    // No skeleton cells left — real content is in place.
    await expect(page.locator('#divergence-content .is-skeleton')).toHaveCount(0);
  });

  // Row click must expand the paired detail row. Guards the class of bug where
  // a click delegation ordering or stopPropagation trap silently eats clicks
  // — the exact shape that broke Publish after the delegation refactor.
  test('row click expands the detail row', async ({ page }) => {
    const rows = page.locator('.divergence-group-row.js-divergence-group-toggle');
    if ((await rows.count()) === 0) test.skip(true, 'no divergence rows in current state');
    const row = rows.first();
    const dcId = await row.getAttribute('data-dc');
    expect(dcId).toBeTruthy();

    const detail = page.locator(`tr.divergence-group-detail[data-dc="${dcId}"]`);
    await expect(detail).toBeHidden();

    await row.click();
    await expect(detail).toBeVisible();
  });

  // Publish click must reach the export API. Skip when no DC row is in the
  // enabled-Publish state (pending decisions, already-published, ignore-only
  // all suppress the enabled button). Passing this test proves the click
  // survived event bubbling and reached the delegated handler.
  test('publish button click triggers the export API', async ({ page }) => {
    const btn = page.locator('.divergence-publish-btn:not([disabled])').first();
    if (await btn.count() === 0) test.skip(true, 'no enabled Publish button in current state');

    await Promise.all([
      page.waitForResponse(
        resp => resp.url().includes('/api/v1/export')
             && !resp.url().includes('/jobs')
             && resp.request().method() === 'POST',
        { timeout: 10_000 },
      ),
      btn.click(),
    ]);
  });

  // Delete-report click must reach the handler, which prompts window.confirm.
  // We dismiss the dialog — the assertion is that it appeared at all, proving
  // the click delegation is intact for this button.
  test('delete report button reaches its confirm dialog', async ({ page }) => {
    const btn = page.locator('.divergence-delete-btn').first();
    if (await btn.count() === 0) test.skip(true, 'no delete button in current state');

    let dialogSeen = false;
    page.once('dialog', dialog => {
      dialogSeen = true;
      dialog.dismiss();
    });

    await btn.click();
    await expect.poll(() => dialogSeen, { timeout: 2_000 }).toBe(true);
  });

  // Action buttons (Accept/Reject/Ignore) inside an expanded detail row must
  // toggle the .is-selected class on click. Guards both the delegation reach
  // and the toggleDivergenceAction visible effect.
  test('action button click toggles is-selected', async ({ page }) => {
    const rows = page.locator('.divergence-group-row.js-divergence-group-toggle');
    if ((await rows.count()) === 0) test.skip(true, 'no divergence rows in current state');
    const row = rows.first();
    const dcId = await row.getAttribute('data-dc');
    if (!dcId) test.skip(true, 'no divergence rows');
    await row.click();
    await expect(page.locator(`tr.divergence-group-detail[data-dc="${dcId}"]`)).toBeVisible();

    // Action buttons only render for pending rows (undecided entries). Skip if
    // every entry in this DC is already resolved.
    const actionBtn = page.locator('.divergence-action-btn').first();
    if (await actionBtn.count() === 0) test.skip(true, 'no pending rows with action buttons');

    await expect(actionBtn).not.toHaveClass(/is-selected/);
    await actionBtn.click();
    await expect(actionBtn).toHaveClass(/is-selected/);
  });
});
