import { test, expect } from '@playwright/test';

// Editing through the GENERIC detail page.
//
// Uses racks — Rack has no bespoke page, so a successful edit here goes through
// the derived field list, derived edit targets and the shared editor, with no
// per-type code anywhere in the path.

test('a type with no bespoke page can be edited, and the change persists', async ({ page }) => {
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();
  await page.locator('#generic-table tbody tr td:first-child a').first().click();
  await expect(page).toHaveURL(/\/racks\/.+/);
  const detailUrl = page.url();

  await expect(page.getByTestId('generic-fields')).toBeVisible();
  await page.locator('[data-generic-edit-id]').click();

  const modal = page.locator('.modal.is-active');
  await expect(modal).toBeVisible();

  // Drive the editor through the window bridge, as the bespoke specs do —
  // vanilla-jsoneditor exposes set({text}) and typing into CodeMirror is
  // brittle. window.genericEditors is keyed by the same DOM id as the modal.
  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  const initial = await page.evaluate((id) => {
    const el = document.getElementById('generic-edit-data-' + id);
    return JSON.parse(el!.textContent!.trim());
  }, domId);

  // The editor renders the DERIVED field set, not a hand-written form.
  expect(Object.keys(initial)).toContain('uHeight');

  const marker = 30 + (Math.floor(Date.now() / 1000) % 9);
  await page.evaluate(({ id, next }) => {
    const editor = (window as any).genericEditors.get(id);
    editor.set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, uHeight: marker } });

  await modal.locator('[id^="generic-edit-submit-"]').click();

  // The page reloads after a successful save, and the new value is on it.
  await expect(page).toHaveURL(detailUrl);
  await expect(page.getByTestId('generic-fields')).toContainText(String(marker), { timeout: 15_000 });
});

test('the edit button is absent for a type with no editable fields', async ({ page }) => {
  // StorageDevice's fields are all editorIgnored, so there is nothing to edit —
  // an Edit button opening an empty editor would be a lie.
  await page.goto('/storage-devices');
  await expect(page.locator('#generic-table')).toBeVisible();

  // Navigate by href rather than clicking: DataTables clones the table under
  // scrollX, so "the first row link" can resolve to a hidden clone.
  const href = await page.locator('#generic-table tbody tr td:first-child a').first().getAttribute('href');
  if (!href) test.skip(true, 'no storage devices seeded');
  await page.goto(href!);

  // It still SHOWS its data. editorIgnored is editor-scoped, not
  // display-scoped: a scanned hardware fact is exactly what someone opens this
  // page to read, even though nobody should type into it. Rendering the
  // editable set made this page show nothing but a name.
  await expect(page.getByTestId('generic-fields')).toBeVisible();
  await expect(page.getByTestId('generic-fields')).toContainText('Capacity Bytes');
  await expect(page.getByTestId('generic-fields')).toContainText('Serial Number');

  // But there is nothing to edit, so no button and no modal — a hidden modal
  // with no opener is dead markup.
  await expect(page.locator('[data-generic-edit-id]')).toHaveCount(0);
  await expect(page.locator('[id^="edit-modal-generic-"]')).toHaveCount(0);
});

test('every reachable edit target on a generic page carries an OCC version', async ({ page }) => {
  // The same invariant edit_targets_invariant_test pins for the bespoke pages.
  // "Reachable" means the edit-data tree actually contains the target's path,
  // so the editor renders it and a user can change it. A reachable target with
  // no version is written UNGUARDED: a concurrent edit is overwritten silently
  // instead of refused. MVCC was off for every UI edit for two and a half
  // months once, and nothing failed.
  //
  // ClusterBackup is the demanding case: a wrapper with no scalars of its own
  // whose GRANDchildren are the edit targets.
  await page.goto('/cluster-backups');
  await expect(page.locator('#generic-table')).toBeVisible();
  const href = await page.locator('#generic-table tbody tr td:first-child a').first().getAttribute('href');
  if (!href) test.skip(true, 'no cluster backups seeded');
  await page.goto(href!);

  const id = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  expect(id, 'a wrapper with editable children must offer an Edit button').toBeTruthy();

  const targets = await page.evaluate((i) => {
    const data = JSON.parse(document.getElementById('generic-edit-data-' + i)!.textContent!.trim());
    const list = JSON.parse(document.getElementById('generic-edit-targets-' + i)!.textContent!.trim());
    const reachable = (path: string[]) => {
      let cur: any = data;
      for (const seg of path) { if (!cur || typeof cur !== 'object') return false; cur = cur[seg]; }
      return cur !== undefined;
    };
    return list.map((t: any) => ({ kind: t.kind, path: t.path, version: t.version ?? null, reachable: reachable(t.path) }));
  }, id);

  expect(targets.length, 'no targets rendered — the assertion below would be vacuous').toBeGreaterThan(0);
  const unguarded = targets.filter((t: any) => t.reachable && !t.version);
  expect(unguarded, `reachable targets with no OCC version: ${JSON.stringify(unguarded)}`).toEqual([]);

  // And at least one owned child IS reachable, or this passes for the wrong
  // reason — an all-inert tree satisfies the check while testing nothing.
  expect(targets.some((t: any) => t.reachable && t.path.length > 0)).toBe(true);
});

test('an edit through the generic page produces an audit row with a diff', async ({ page }) => {
  // The gap left when the bespoke datacenter edit spec was deleted: that case
  // asserted the resulting audit row and its diff, and nothing covered it for
  // the generic editor.
  //
  // Asserted through GET /api/v1/audit-log rather than an on-page panel — the
  // generic renderer has no audit panel yet, and the API is the contract an
  // operator actually queries. Reading it back through the consumer API is also
  // the house rule for anything written for someone to read later.
  await page.goto('/racks');
  await expect(page.locator('#generic-table')).toBeVisible();
  const href = await page.locator('#generic-table tbody tr td:first-child a').first().getAttribute('href');
  await page.goto(href!);

  const orbId = decodeURIComponent(href!.split('/').pop()!);
  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await page.locator('[data-generic-edit-id]').click();

  const initial = await page.evaluate((id) => {
    const el = document.getElementById('generic-edit-data-' + id);
    return JSON.parse(el!.textContent!.trim());
  }, domId);

  const marker = 20 + (Math.floor(Date.now() / 1000) % 9);
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, uHeight: marker } });

  await page.locator('[id^="generic-edit-submit-"]').click();
  await expect(page.getByTestId('generic-fields')).toContainText(String(marker), { timeout: 15_000 });

  // The audit row must name the concrete type AND carry the field that changed.
  // "Something was updated" is a different and much weaker claim.
  const events = await page.evaluate(async (orbId) => {
    const r = await fetch(`/api/v1/audit-log?orbId=${encodeURIComponent(orbId)}&limit=20`);
    return await r.json();
  }, orbId);

  const rows = (events.events ?? []).filter((e: any) =>
    (e.operations ?? []).some((op: string) => op === 'updateRack'));
  expect(rows.length, `no updateRack audit row for ${orbId}: ${JSON.stringify(events).slice(0, 400)}`).toBeGreaterThan(0);

  const detail = JSON.stringify(rows[0]);
  expect(detail, 'the audit row must record WHICH field changed, not just that something did')
    .toContain('uHeight');

  // And the operator must be able to SEE it without leaving the page. The panel
  // is a separate wiring path from the API — template, data-related-orb-ids and
  // the shared.js fetch — so a green API assertion says nothing about it.
  await page.reload();
  const audit = page.getByTestId('generic-audit');
  await expect(audit).toContainText('updateRack', { timeout: 10_000 });
  await expect(audit).toContainText('uHeight');
});
