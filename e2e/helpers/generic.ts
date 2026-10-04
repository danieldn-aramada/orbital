import { expect, type Page } from '@playwright/test';

// Shared helpers for the schema-derived pages.
//
// Every ConfigItem page is the SAME page now, so the way a spec opens one is
// the same too. These used to be six near-identical `openServerEditModal`
// copies, which is why deleting the bespoke Server page broke ten spec files
// at once.

// safeDomId mirrors the Go helper and shared.js: orbIds contain ":" and DOM ids
// may not.
export function safeDomId(orbId: string): string {
  return orbId.replace(/[^A-Za-z0-9_-]/g, '_');
}

// genericDomId is the id a generic detail tab is keyed by. It includes the SLUG
// because one list page can hold tabs for several types, and two types could
// otherwise collide on the same orbId.
export function genericDomId(slug: string, orbId: string): string {
  return `${slug}-${safeDomId(orbId)}`;
}

// openDetail navigates straight to a detail page. Prefer this: it is one
// navigation, and the page and the tab render the same block.
export async function openDetail(page: Page, slug: string, orbId: string) {
  await page.goto(`/${slug}/${encodeURIComponent(orbId)}`);
  await expect(page.getByTestId('generic-fields')).toBeVisible();
  // Owned children, relationship tables and the audit log live in one tab
  // strip, so all but the first start hidden. Specs that assert on their
  // CONTENT want them reachable; a spec about the tab strip ITSELF should
  // navigate with page.goto directly and use openDetailPanel.
  await openAllDetailPanels(page);
}

// openDetailTab opens a detail view as a TAB on the list page, via the
// ?open= deep link. Use it only when the test is ABOUT the tab — restoration,
// tab dots, closing — otherwise openDetail is cheaper and less fragile.
export async function openDetailTab(page: Page, slug: string, orbId: string, label = orbId) {
  await page.goto(`/${slug}?open=${encodeURIComponent(orbId)}&label=${encodeURIComponent(label)}`);
  const domId = genericDomId(slug, orbId);
  await page.waitForSelector(`#tab-content-generic-${domId}`);
  await expect(page.locator(`#tab-content-generic-${domId}`).getByTestId('generic-fields')).toBeVisible();
  return domId;
}

// openEditor opens the detail page and its edit modal, returning the modal's
// dom id — which is the id every generic-edit-* element is suffixed with.
export async function openEditor(page: Page, slug: string, orbId: string) {
  await openDetail(page, slug, orbId);
  const btn = page.locator('[data-generic-edit-id]').first();
  await expect(btn).toBeVisible();
  const domId = await btn.getAttribute('data-generic-edit-id');
  await btn.click();
  await expect(page.locator(`#edit-modal-generic-${domId}`)).toHaveClass(/is-active/);
  return domId!;
}

// editorState reads the JSON the editor was seeded with.
export async function editorState(page: Page, domId: string): Promise<any> {
  return await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);
}

// editorTargets reads the edit targets the page handed the editor.
export async function editorTargets(page: Page, domId: string): Promise<any[]> {
  return await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-targets-' + id)!.textContent!.trim()), domId);
}

// setEditorState replaces the editor's content.
export async function setEditorState(page: Page, domId: string, next: any) {
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next });
}

// saveEditor clicks Save.
export async function saveEditor(page: Page, domId: string) {
  await page.locator(`#generic-edit-submit-${domId}`).click();
}

// openDetailPanel activates a detail-page tab by its visible label.
//
// The detail page puts owned children, relationship tables and the audit log
// in ONE tab strip, so all but the first panel start `display:none`. A spec
// that asserts on content inside one has to open it first — which is also what
// a person does.
//
// Returns the panel element, so a spec can scope its assertions to it rather
// than to the page: two panels can hold a table with the same testid.
export async function openDetailPanel(page: Page, label: string | RegExp) {
  const strip = page.locator('[id^="generic-detail-tabs-"]');
  await expect(strip, 'the detail page should have a tab strip').toBeVisible();
  const tab = strip.locator('li').filter({ hasText: label }).first();
  await expect(tab, `no tab labelled ${label}`).toBeVisible();
  await tab.click();
  const panelId = await tab.getAttribute('data-panel');
  // An attribute selector, not `#` + CSS.escape: CSS.escape is a BROWSER
  // global and this runs in Node. Panel ids come from SafeDomID so they need
  // no escaping anyway.
  const panel = page.locator(`[id="${panelId}"]`);
  await expect(panel).toBeVisible();
  return panel;
}

// openAllDetailPanels reveals every panel at once.
//
// For specs asserting that content EXISTS rather than that a user can reach it
// — "the schema pins column order", "back-references are dropped" — clicking
// through eight tabs is ceremony that tests the tab strip, not the thing.
export async function openAllDetailPanels(page: Page) {
  const strip = page.locator('[id^="generic-detail-tabs-"]');
  if (!await strip.count()) return;

  // Click the audit tab first. It is the one panel that LAZY-LOADS — that is
  // the point of tabbing it — so merely un-hiding it leaves an empty div, and
  // a spec asserting on audit content fails for a reason that has nothing to
  // do with what it is testing.
  const audit = strip.locator('li[data-orb-id]');
  if (await audit.count()) {
    await audit.first().click();
    await expect(page.getByTestId('generic-audit')).not.toBeEmpty({ timeout: 10_000 });
  }

  await page.evaluate(() => {
    const s = document.querySelector('[id^="generic-detail-tabs-"]');
    if (!s) return;
    for (const li of s.querySelectorAll('li[data-panel]')) {
      const p = document.getElementById((li as HTMLElement).dataset.panel!);
      if (p) p.style.removeProperty('display');
    }
  });
}
