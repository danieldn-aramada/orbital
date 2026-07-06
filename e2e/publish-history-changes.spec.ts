import { test, expect } from '@playwright/test';

test.describe('Publish History changes panel', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/publish-history');
    // Wait for the artifacts fragment to load (bodyload triggers via
    // js-artifacts-reload on page load, HTMX populates #artifacts-tbody).
    await expect(page.locator('#artifacts-table')).toBeVisible();
  });

  // Guards the click-reach-handler class of bug on the new chevron. If the
  // delegation is broken or a parent handler swallows the click, the detail
  // row stays hidden and this test fails cleanly. Same shape as the
  // divergence-reports expand test after that regression.
  test('chevron expands sibling detail row', async ({ page }) => {
    // Wait for at least one expandable row.
    const chevron = page.locator('.js-publish-changes-expand').first();
    if (await chevron.count() === 0) {
      test.skip(true, 'no completed publishes in seed');
    }

    // The sibling detail row is the immediate next <tr> after the chevron's row.
    const detailRow = chevron.locator('xpath=ancestor::tr[1]/following-sibling::tr[1]');
    await expect(detailRow).toHaveClass(/js-publish-changes-detail/);
    await expect(detailRow).toHaveClass(/is-hidden/);

    await chevron.click();

    await expect(detailRow).not.toHaveClass(/is-hidden/);
    // Content is either a spinner (HTMX fetch pending), the eventual audit-log
    // fragment, or the "first publish" empty state. All three are legitimate;
    // the assertion is only that the detail row is now visible + populated.
    await expect(detailRow).not.toBeEmpty();
  });

  // Clicking twice re-hides. Guards against the class-toggle flipping to a
  // permanent state or the JS handler mutating something it shouldn't.
  test('chevron toggles closed on second click', async ({ page }) => {
    const chevron = page.locator('.js-publish-changes-expand').first();
    if (await chevron.count() === 0) {
      test.skip(true, 'no completed publishes in seed');
    }
    const detailRow = chevron.locator('xpath=ancestor::tr[1]/following-sibling::tr[1]');
    await chevron.click();
    await expect(detailRow).not.toHaveClass(/is-hidden/);
    await chevron.click();
    await expect(detailRow).toHaveClass(/is-hidden/);
  });
});
