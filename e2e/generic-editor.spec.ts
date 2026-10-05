import { openAllDetailPanels, openEditor, editorState, setEditorState, saveEditor, listRows } from './helpers/generic';
import { test, expect } from '@playwright/test';

// Editing through the GENERIC detail page.
//
// Uses network-devices — every page is the generic renderer now, so an edit here goes through
// the derived field list, derived edit targets and the shared editor, with no
// per-type code anywhere in the path.

test('a type with no bespoke page can be edited, and the change persists', async ({ page }) => {
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  // There is one view model: the detail view is a tab. Following the row's href
  // is what a person does and lands in exactly the same place a click does.
  const orbId = (await listRows(page).first().getAttribute('data-orb-id'))!;
  await page.goto('/network-devices?open=' + encodeURIComponent(orbId));

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
  expect(Object.keys(initial)).toContain('rackPosition');

  const marker = 30 + (Math.floor(Date.now() / 1000) % 9);
  await page.evaluate(({ id, next }) => {
    const editor = (window as any).genericEditors.get(id);
    editor.set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, rackPosition: marker } });

  await modal.locator('[id^="generic-edit-submit-"]').click();

  // The save re-renders THIS tab's panel in place — not the whole window, which
  // would throw the reader back to the list and (lazily) unload the tab.
  await expect(page.getByTestId('generic-fields')).toContainText(String(marker), { timeout: 15_000 });
  await expect(page.locator('#tablist li.tab')).toHaveCount(2);
});

test('a pageless type still DISPLAYS its scanned fields, and is not an edit target', async ({ page }) => {
  // StorageDevice's fields are all editor-ignored, and it has no page of its
  // own — it renders as rows on the Server page's Storage Devices tab. Both
  // halves matter: `editable: false` is editor-scoped, not display-scoped (a
  // scanned hardware fact is exactly what someone opens this page to read), and
  // a type the page does not declare editable must not reach the editor.
  await page.goto('/servers?open=' + encodeURIComponent('colo:server-CFRHDX3'));
  await openAllDetailPanels(page);

  const devices = page.getByTestId('generic-tab').filter({ hasText: 'Storage Devices' });
  await expect(devices).toContainText('Capacity Bytes');
  await expect(devices).toContainText('Serial Number');

  // A pageless type's rows are DEAD — text, not links to a route that 404s.
  await expect(devices.locator('tbody tr td:first-child a')).toHaveCount(0);

  // And nothing in the editor addresses one.
  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  const kinds = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-targets-' + id)!.textContent!.trim(),
  ).map((t: any) => t.kind), domId);
  expect(kinds).not.toContain('StorageDevice');
  expect(kinds).not.toContain('StorageController');
});

test('every reachable edit target on a generic page carries an OCC version', async ({ page }) => {
  // The same invariant edit_targets_invariant_test pins for the bespoke pages.
  // "Reachable" means the edit-data tree actually contains the target's path,
  // so the editor renders it and a user can change it. A reachable target with
  // no version is written UNGUARDED: a concurrent edit is overwritten silently
  // instead of refused. MVCC was off for every UI edit for two and a half
  // months once, and nothing failed.
  //
  // The cluster is the demanding case: ClusterBackup is a wrapper with no
  // scalars and no page of its own, and its GRANDchildren are the edit targets.
  // The page declares `backup` editable; everything below that is containment.
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
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
  await page.goto('/network-devices');
  await expect(page.locator('#generic-table')).toBeVisible();
  const orbId = (await listRows(page).first().getAttribute('data-orb-id'))!;
  await page.goto('/network-devices?open=' + encodeURIComponent(orbId));
  await openAllDetailPanels(page);

  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await page.locator('[data-generic-edit-id]').click();

  const initial = await page.evaluate((id) => {
    const el = document.getElementById('generic-edit-data-' + id);
    return JSON.parse(el!.textContent!.trim());
  }, domId);

  const marker = 20 + (Math.floor(Date.now() / 1000) % 9);
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, rackPosition: marker } });

  await page.locator('[id^="generic-edit-submit-"]').click();
  await expect(page.getByTestId('generic-fields')).toContainText(String(marker), { timeout: 15_000 });

  // The audit row must name the concrete type AND carry the field that changed.
  // "Something was updated" is a different and much weaker claim.
  const events = await page.evaluate(async (orbId) => {
    const r = await fetch(`/api/v1/audit-log?orbId=${encodeURIComponent(orbId)}&limit=20`);
    return await r.json();
  }, orbId);

  const rows = (events.events ?? []).filter((e: any) =>
    (e.operations ?? []).some((op: string) => op === 'updateNetworkDevice'));
  expect(rows.length, `no updateNetworkDevice audit row for ${orbId}: ${JSON.stringify(events).slice(0, 400)}`).toBeGreaterThan(0);

  const detail = JSON.stringify(rows[0]);
  expect(detail, 'the audit row must record WHICH field changed, not just that something did')
    .toContain('rackPosition');

  // And the operator must be able to SEE it without leaving the page. The panel
  // is a separate wiring path from the API — template, data-related-orb-ids and
  // the shared.js fetch — so a green API assertion says nothing about it.
  await page.reload();
  const audit = page.getByTestId('generic-audit');
  await expect(audit).toContainText('updateNetworkDevice', { timeout: 10_000 });
  await expect(audit).toContainText('rackPosition');
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
  await page.goto(`/data-centers?open=${encodeURIComponent(ORB_ID)}`);

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
  await page.goto(`/data-centers?open=${encodeURIComponent(ORB_ID)}`);
  await expect(page.getByTestId('generic-fields')).toBeVisible();
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
