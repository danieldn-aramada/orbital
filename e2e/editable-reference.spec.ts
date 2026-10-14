import { test, expect } from '@playwright/test';
import { openEditor, saveEditor } from './helpers/generic';

// Re-pointing a reference from the editor — `Server.rack`.
//
// Not an owned child: the editor offers which rack the server points AT, not
// the rack's own fields. Before this, `rack` appeared in no form at all —
// create skips it (nullable) and the JSON tree holds scalars only.

const SERVER = 'colo:server-6CVD664';

async function rackOf(request: any): Promise<string | null> {
  const r = await request.post('/graphql', {
    data: { query: `{ getServer(orbId: "${SERVER}") { rack { orbId } } }` },
  });
  const j = await r.json();
  return j.data?.getServer?.rack?.orbId ?? null;
}

// Variables, not inline literals — the proxy refuses an inline single-entity
// update (it resolves the row via `$orbId` to bump the version).
async function setRack(request: any, orbId: string | null, current: string | null) {
  if (orbId) {
    await request.post('/graphql', {
      data: {
        query: `mutation U($orbId: String!, $set: ServerPatch!) {
          updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`,
        variables: { orbId: SERVER, set: { rack: { orbId } } },
      },
    });
  } else if (current) {
    await request.post('/graphql', {
      data: {
        query: `mutation U($orbId: String!, $remove: ServerPatch!) {
          updateServer(input: { filter: { orbId: { eq: $orbId } }, remove: $remove }) { numUids } }`,
        variables: { orbId: SERVER, remove: { rack: { orbId: current } } },
      },
    });
  }
}

test.describe('editable reference', () => {
  let original: string | null = null;

  test.beforeAll(async ({ request }) => { original = await rackOf(request); });

  // Seeded data shared with whoever is developing locally — put it back.
  test.afterAll(async ({ request }) => {
    const now = await rackOf(request);
    if (now !== original) await setRack(request, original, now);
  });

  test('the editor offers a rack select, scoped to the server namespace', async ({ page }) => {
    await openEditor(page, 'servers', SERVER);
    const sel = page.getByTestId('edit-rel-rack');
    await expect(sel).toBeVisible();

    // Scoped: a cross-namespace link would disagree with the orbId prefix.
    const values = await sel.locator('option').evaluateAll(
      os => os.map(o => (o as HTMLOptionElement).value).filter(Boolean));
    expect(values.length).toBeGreaterThan(0);
    for (const v of values) expect(v).toMatch(/^colo:rack-/);
  });

  test('the select opens on the rack the server currently points at', async ({ page, request }) => {
    const current = await rackOf(request);
    await openEditor(page, 'servers', SERVER);
    await expect(page.getByTestId('edit-rel-rack')).toHaveValue(current ?? '');
  });

  test('choosing a different rack re-points the server', async ({ page, request }) => {
    const before = await rackOf(request);
    const domId = await openEditor(page, 'servers', SERVER);

    const sel = page.getByTestId('edit-rel-rack');
    const other = await sel.locator('option').evaluateAll(
      (os, cur) => os.map(o => (o as HTMLOptionElement).value)
        .filter(v => v && v !== cur)[0],
      before);
    expect(other).toBeTruthy();

    await sel.selectOption(other!);
    await saveEditor(page, domId);
    await expect(page.locator('.modal.is-active')).toHaveCount(0);

    // Read back through the graph — the rendered row could show cache.
    await expect.poll(() => rackOf(request)).toBe(other);
  });

  test('clearing the select unlinks the rack', async ({ page, request }) => {
    // `set: null` is a no-op in DGraph, so clearing goes through `remove` —
    // the half most likely to be dropped, leaving the old link in place.
    const before = await rackOf(request);
    expect(before).toBeTruthy();

    const domId = await openEditor(page, 'servers', SERVER);
    await page.getByTestId('edit-rel-rack').selectOption('');
    await saveEditor(page, domId);
    await expect(page.locator('.modal.is-active')).toHaveCount(0);

    await expect.poll(() => rackOf(request)).toBeNull();
  });

  test('saving without touching the select leaves the edge alone', async ({ page, request }) => {
    // Rewriting an untouched edge would bump the far end and add a phantom
    // audit row.
    const before = await rackOf(request);
    const domId = await openEditor(page, 'servers', SERVER);
    await saveEditor(page, domId);
    await expect(page.locator('.modal.is-active')).toHaveCount(0);
    expect(await rackOf(request)).toBe(before);
  });
});
