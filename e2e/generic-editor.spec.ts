import { openAllDetailPanels, openEditor, editorState, setEditorState, saveEditor } from './helpers/generic';
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
  await openAllDetailPanels(page);

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
  await openAllDetailPanels(page);

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
  await openAllDetailPanels(page);

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

// A `jsonString` field survives the editor round-trip as a STRING.
//
// Replaces four `test.skip`s deleted 2026-10-02 that claimed Rack, IPAddress,
// KubernetesNode and ServerConfigurationProfile "have no edit modal in the
// current UI". Every ConfigItem type has one since the generic renderer
// landed, so three of those skips duplicated the tests above through the same
// code path, and the fourth named a field (`ServerConfigurationProfile.json`)
// that is `editorIgnored` and deliberately not editable.
//
// What they gestured at and nothing covered is this: `assetDataV2` is declared
// `String` but holds JSON, so the editor PARSES it into a tree on open and MUST
// re-stringify on submit. Forget that and DGraph rejects the write with
// "cannot use as String" — a whole class of silent-looking failure that no
// other field in the schema can expose, because it is the only `jsonString`.
test('a jsonString field is parsed for editing and stored back as a string', async ({ page }) => {
  const ORB_ID = '2f-uae:2f-uae';
  await page.goto(`/data-centers/${encodeURIComponent(ORB_ID)}`);

  const btn = page.locator('[data-generic-edit-id]');
  await expect(btn).toBeVisible();
  const domId = await btn.getAttribute('data-generic-edit-id');
  await btn.click();

  const initial = await page.evaluate(
    (id) => JSON.parse(document.getElementById('generic-edit-data-' + id)!.textContent!.trim()),
    domId);

  // Parsed into a TREE, not handed to the editor as an opaque string. If this
  // regresses, the field becomes an uneditable wall of escaped quotes.
  expect(typeof initial.assetDataV2, 'assetDataV2 must reach the editor parsed').toBe('object');

  const key = Object.keys(initial.assetDataV2)[0];
  const marker = `e2e-${Date.now()}`;
  const next = {
    ...initial,
    assetDataV2: { ...initial.assetDataV2, [key]: { ...initial.assetDataV2[key], kvmURL: marker } },
  };
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next });

  await Promise.all([
    page.waitForResponse(r => r.url().includes('/graphql') && r.status() === 200),
    page.locator(`#generic-edit-submit-${domId}`).click(),
  ]);
  await expect(page.locator(`#edit-modal-generic-${domId}`)).not.toHaveClass(/is-active/, { timeout: 10_000 });

  // Read back through a fresh page load. Getting the value back at all proves
  // it was stored as a String — DGraph refuses an object for a String field,
  // so a missed stringify fails the write rather than storing something odd.
  await page.goto(`/data-centers/${encodeURIComponent(ORB_ID)}`);
  await page.locator('[data-generic-edit-id]').click();
  const after = await page.evaluate(
    (id) => JSON.parse(document.getElementById('generic-edit-data-' + id)!.textContent!.trim()),
    domId);
  expect(after.assetDataV2[key].kvmURL, 'the edited value did not survive the round trip').toBe(marker);
});

// A nullable field appears in the editor as an explicit null, and leaving it
// alone writes nothing.
//
// The second half is the one that matters. configitem-editor.js decides a field
// was CLEARED by diffing the open-time snapshot against the edited tree, so
// adding keys to that snapshot is exactly the move that could emit a spurious
// `remove` — the documented data-loss path in CHANGE-CONTROL.md. Asserted
// against the GraphQL the editor actually sends, not against the resulting
// state, because a no-op write and no write at all look identical afterwards.
test('an unset field is editable, and leaving it null writes nothing', async ({ page }) => {
  const SERVER = 'colo:server-7MP6K74';
  const domId = await openEditor(page, 'servers', SERVER);
  const state = await editorState(page, domId);

  // Present as a key, so you can type a value without guessing the name.
  expect(state.serverMaintenance, 'maintenance must be in the tree').toBeTruthy();
  for (const f of ['windowStart', 'windowEnd', 'reason']) {
    expect(f in state.serverMaintenance, `${f} must be offered even when unset`).toBeTruthy();
    expect(state.serverMaintenance[f], `${f} should be null, not invented`).toBeNull();
  }

  // Change ONE unrelated field and save, capturing what goes over the wire.
  const sent: string[] = [];
  page.on('request', r => {
    if (r.url().includes('/graphql') && r.method() === 'POST') sent.push(r.postData() || '');
  });
  await setEditorState(page, domId, { ...state, hostname: state.hostname });
  await setEditorState(page, domId, {
    ...state,
    serverMaintenance: { ...state.serverMaintenance, enabled: !state.serverMaintenance.enabled },
  });
  await Promise.all([
    page.waitForResponse(r => r.url().includes('/graphql') && r.status() === 200),
    saveEditor(page, domId),
  ]);
  await expect(page.locator(`#edit-modal-generic-${domId}`)).not.toHaveClass(/is-active/, { timeout: 10_000 });

  const body = sent.join('\n');
  // The null fields must appear in NEITHER half of the mutation.
  for (const f of ['windowStart', 'windowEnd', 'reason']) {
    expect(body, `${f} must not be set when it was left null`).not.toContain(`"${f}"`);
  }
  expect(body, 'nothing should be cleared').not.toContain('remove');

  // Put it back.
  const after = await openEditor(page, 'servers', SERVER);
  const s2 = await editorState(page, after);
  await setEditorState(page, after, {
    ...s2,
    serverMaintenance: { ...s2.serverMaintenance, enabled: !s2.serverMaintenance.enabled },
  });
  await Promise.all([
    page.waitForResponse(r => r.url().includes('/graphql') && r.status() === 200),
    saveEditor(page, after),
  ]);
});
