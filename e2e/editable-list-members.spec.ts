import { test, expect } from '@playwright/test';

// Editing a LIST member from its parent's editor.
//
// `racks` is a list in DataCenter's subgraph. Each stored rack becomes its own
// edit target, addressed by the row's own orbId and carrying the row's own
// version — never derived from the parent, and never addressed by position.
//
// Membership is not editable here: adding goes through create, removing through
// delete, where the preview names what a removal would orphan.

const DC = 'colo:colo-galleon';

// Restore every rack to the height it had before the test touched it.
//
// These are SEEDED racks, not fixtures this spec created — the local graph is
// shared with whoever is developing, and a test that leaves `uHeight: 47`
// behind has edited their data. Caught 2026-10-13 when 3 of 24 local racks had
// a height and the other 21 had none: the three were this spec's leftovers.
async function restore(request: any, before: any[]) {
  const now = await racks(request);
  for (const r of before) {
    const cur = now.find(c => c.orbId === r.orbId);
    if (!cur || cur.uHeight === r.uHeight) continue;
    // ⚠️ DGraph clears a scalar only via `remove` carrying its CURRENT value —
    // `remove: { uHeight: null }` and `set: { uHeight: null }` are both no-ops,
    // so a field that was unset before the test stays set without this.
    // ⚠️ VARIABLES, not inline literals. Orbital's proxy refuses a
    // single-entity update written inline — it resolves the row via `$orbId`
    // to bump the version and stamps provenance into `$set`, and can do
    // neither against literals. An inline restore 400s and leaves the data
    // edited, which is how this was silently not restoring.
    await request.post('/graphql', {
      data: r.uHeight == null
        ? {
            query: `mutation U($orbId: String!, $remove: RackPatch!) {
              updateRack(input: { filter: { orbId: { eq: $orbId } }, remove: $remove }) { numUids } }`,
            variables: { orbId: r.orbId, remove: { uHeight: cur.uHeight } },
          }
        : {
            query: `mutation U($orbId: String!, $set: RackPatch!) {
              updateRack(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`,
            variables: { orbId: r.orbId, set: { uHeight: r.uHeight } },
          },
    });
  }
}

async function racks(request: any) {
  const res = await request.post('/graphql', {
    data: { query: `{ getDataCenter(orbId: "${DC}") { racks { orbId name uHeight version } } }` },
  });
  const rs = (await res.json()).data.getDataCenter.racks as any[];
  return rs.sort((a, b) => a.orbId.localeCompare(b.orbId));
}

test('editing one list member writes that member and leaves its neighbours alone', async ({ page, request }) => {
  // THE assertion that matters. A list is addressed by orbId precisely because
  // addressing by position would send a value to whichever row happened to sort
  // there — a silent cross-write, which no error and no toast would reveal.
  const before = await racks(request);
  expect(before.length, 'precondition: the data centre has racks to edit').toBeGreaterThan(1);

  await page.goto('/data-centers');
  await page.locator(`#generic-table tbody tr[data-orb-id="${DC}"]`).dblclick();
  const panel = page.locator('[id^="tab-content-generic-data-centers-"]').locator('visible=true');
  await expect(panel.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });

  const domId = await panel.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await panel.locator('[data-generic-edit-id]').click();
  await expect(page.locator('.modal.is-active')).toBeVisible();

  const tree = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);

  // The tree carries the list, each entry naming which member it is.
  expect(Array.isArray(tree.racks), '`racks` must be an array in the editor tree').toBe(true);
  expect(tree.racks[0].orbId, 'each entry carries its own orbId, which is how the save addresses it').toBeTruthy();

  // Change exactly ONE member, and not the first — picking index 1 means a
  // position-based implementation writes the wrong row rather than the right
  // one by luck.
  const targetOrbID = before[1].orbId;
  const marker = 10 + (Math.floor(Date.now() / 1000) % 80);
  const next = {
    ...tree,
    racks: tree.racks.map((r: any) => (r.orbId === targetOrbID ? { ...r, uHeight: marker } : r)),
  };
  await page.evaluate(({ id, n }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(n, null, 2) });
  }, { id: domId, n: next });
  await page.locator('[id^="generic-edit-submit-"]').click();
  await expect(page.locator('.modal.is-active')).toHaveCount(0, { timeout: 15_000 });

  const after = await racks(request);
  await restore(request, before);
  const edited = after.find(r => r.orbId === targetOrbID)!;
  expect(edited.uHeight, 'the edited member must carry the new value').toBe(marker);

  for (const b of before) {
    if (b.orbId === targetOrbID) continue;
    const a = after.find(r => r.orbId === b.orbId)!;
    expect(a.uHeight, `${b.orbId} must be untouched — a cross-write is silent`).toBe(b.uHeight);
    expect(a.name, `${b.orbId}'s name must be untouched`).toBe(b.name);
    expect(a.version, `${b.orbId} must not have been written at all`).toBe(b.version);
  }
});

test('membership is not editable from the parent: removing an entry writes nothing', async ({ page, request }) => {
  // Dropping an array entry reads as "no change", NOT as a delete. Deleting a
  // config item from a form row would route around the delete preview, which
  // exists to name what a removal would orphan.
  const before = await racks(request);

  await page.goto('/data-centers');
  await page.locator(`#generic-table tbody tr[data-orb-id="${DC}"]`).dblclick();
  const panel = page.locator('[id^="tab-content-generic-data-centers-"]').locator('visible=true');
  await expect(panel.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });

  const domId = await panel.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await panel.locator('[data-generic-edit-id]').click();
  const tree = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);

  await page.evaluate(({ id, n }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(n, null, 2) });
  }, { id: domId, n: { ...tree, racks: tree.racks.slice(1) } });
  await page.locator('[id^="generic-edit-submit-"]').click();
  await expect(page.locator('.modal.is-active')).toHaveCount(0, { timeout: 15_000 });

  const after = await racks(request);
  expect(after.map(r => r.orbId), 'every rack must survive — membership is not editable here')
    .toEqual(before.map(r => r.orbId));
});


test('each member edit is audited against THAT member, and an untouched one is not', async ({ page, request }) => {
  // Acceptance 14, plus its negative. An audit row is written for someone to
  // read later, so it is asserted through the consumer API — not off a log
  // line, which would prove the log and nothing else.
  //
  // The negative matters as much: an event attributed to the parent, or to a
  // member nobody edited, trains whoever reads the trail to distrust it.
  const before = await racks(request);
  const target = before[1];
  const untouched = before[0];

  await page.goto('/data-centers');
  await page.locator(`#generic-table tbody tr[data-orb-id="${DC}"]`).dblclick();
  const panel = page.locator('[id^="tab-content-generic-data-centers-"]').locator('visible=true');
  await expect(panel.getByTestId('generic-fields')).toBeVisible({ timeout: 10_000 });

  const domId = await panel.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await panel.locator('[data-generic-edit-id]').click();
  const tree = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);

  const marker = 10 + (Math.floor(Date.now() / 1000) % 80);
  await page.evaluate(({ id, n }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(n, null, 2) });
  }, {
    id: domId,
    n: { ...tree, racks: tree.racks.map((r: any) => (r.orbId === target.orbId ? { ...r, uHeight: marker } : r)) },
  });
  await page.locator('[id^="generic-edit-submit-"]').click();
  await expect(page.locator('.modal.is-active')).toHaveCount(0, { timeout: 15_000 });

  await restore(request, before);

  const res = await request.get('/api/v1/audit-log?limit=25');
  const events = (await res.json()).events as any[];

  const mine = events.filter(e => (e.resourceIds || []).includes(target.orbId));
  expect(mine.length, `an edit to ${target.orbId} must be auditable against that member`).toBeGreaterThan(0);
  expect(mine[0].resourceTypes, 'attributed to the MEMBER type, not the parent').toContain('Rack');
  expect(mine[0].operations.join(','), 'and written through the member\'s own mutation').toContain('Rack');

  // The negative, scoped to this run: no event names the member nobody edited.
  const strays = events
    .filter(e => new Date(e.createdAt) >= new Date(mine[0].createdAt))
    .filter(e => (e.resourceIds || []).includes(untouched.orbId));
  expect(strays, `${untouched.orbId} was not edited and must have no event`).toHaveLength(0);
});
