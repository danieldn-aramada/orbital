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

// listRows is the list page's data rows. Keyed by `data-orb-id` because the
// name cell is PLAIN TEXT — this table has DataTables row selection, so a link
// there would fire selection and opening from one gesture.
export function listRows(page: Page) {
  return page.locator('#generic-table tbody tr[data-orb-id]');
}

// openRow opens the Nth list row as a tab, by DOUBLE click — the gesture the
// product uses. Returns the row's orbId and label so a spec can assert against
// the tab it just opened.
export async function openRow(page: Page, index = 0) {
  const row = listRows(page).nth(index);
  const orbId = (await row.getAttribute('data-orb-id'))!;
  const label = (await row.locator('td').first().innerText()).trim();
  await row.dblclick();
  return { orbId, label };
}

// openDetail opens a ConfigItem's detail view.
//
// There is ONE view model: a detail view is a TAB on its list page, reached by
// `/{slug}?open={orbId}` — the same url the table's rows carry. `/{slug}/{orbId}`
// is the fragment the tab FETCHES and 404s for anyone else, so no spec should
// navigate to it.
export async function openDetail(page: Page, slug: string, orbId: string) {
  await page.goto(`/${slug}?open=${encodeURIComponent(orbId)}`);
  await expect(page.getByTestId('generic-fields')).toBeVisible();
  // Subgraph members, relationship tables and the audit log live in one tab
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
// The detail page puts subgraph members, relationship tables and the audit log
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

// openAuditPanel activates the detail view's audit tab and waits for its rows.
//
// Explicit, and only in specs that read audit content. The tab's activation is
// also remembered across a reload (localStorage), so a spec that reloads and
// then reads the audit panel must call this AFTER the reload — relying on an
// earlier helper having clicked it is a dependency nobody can see.
export async function openAuditPanel(page: Page) {
  await page.locator('[id^="generic-detail-tabs-"] li[data-orb-id]').first().click();
  await expect(page.getByTestId('generic-audit')).not.toBeEmpty({ timeout: 10_000 });
}

// openAllDetailPanels reveals every panel at once.
//
// For specs asserting that content EXISTS rather than that a user can reach it
// — "the schema pins column order", "back-references are dropped" — clicking
// through eight tabs is ceremony that tests the tab strip, not the thing.
export async function openAllDetailPanels(page: Page) {
  // WAIT for the body first. A detail view arrives by HTMX — it is the fragment
  // a tab fetches — so it is not in the DOM when page.goto resolves. Without
  // this the strip lookup below finds nothing, returns having done NOTHING, and
  // the spec then fails on a panel that is still display:none for a reason that
  // has nothing to do with what it was testing.
  await expect(page.getByTestId('generic-fields')).toBeVisible();

  const strip = page.locator('[id^="generic-detail-tabs-"]');
  if (!await strip.count()) return;

  // The audit panel is NOT loaded here. It LAZY-LOADS on activation, so
  // un-hiding it below leaves it empty — a spec asserting audit content calls
  // openAuditPanel, which pays for the fetch only where it is the thing under
  // test. Waiting on it here put a load-dependent round trip inside every spec
  // that uses this helper, and under a busy machine it outran the per-test
  // budget in whichever spec happened to be running.
  await page.evaluate(() => {
    const s = document.querySelector('[id^="generic-detail-tabs-"]');
    if (!s) return;
    for (const li of s.querySelectorAll('li[data-panel]')) {
      const p = document.getElementById((li as HTMLElement).dataset.panel!);
      if (p) p.style.removeProperty('display');
    }
  });
}
