import { test, expect } from '@playwright/test';

// Creating a parent AND its list children in ONE submit.
//
// A data centre with its racks. One NESTED mutation, never addParent then
// addChild: a single GraphQL mutation is one transaction and two are two, with
// the first staying committed when the second fails. Verified against DGraph
// 2026-10-13 — addNamespace + addDataCenter in one request left the namespace
// behind when the data centre failed.

const NS = 'e2enested';
const DC = `${NS}:e2e-nested-dc`;
// `rack-` prefixed: Rack's pattern became `{kind}-{name}` on 2026-10-13, so a
// rack id now says what it is — and cannot collide with the data centre, whose
// pattern is a bare `{name}` and whose ids share the same interface-level @id.
const R1 = `${NS}:rack-e2e-rack-a`;
const R2 = `${NS}:rack-e2e-rack-b`;

async function clean(request: any) {
  await request.post('/graphql', {
    data: {
      query: `mutation {
        a: deleteRack(filter: { orbId: { in: ["${R1}", "${R2}"] } }) { numUids }
        b: deleteDataCenter(filter: { orbId: { eq: "${DC}" } }) { numUids }
        c: deleteNamespace(filter: { name: { eq: "${NS}" } }) { numUids }
      }`,
    },
  });
}

test.beforeEach(async ({ request }) => clean(request));
test.afterEach(async ({ request }) => clean(request));

test('a data centre and its racks are created in one submit', async ({ page, request }) => {
  await page.goto('/data-centers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.getByTestId('create-namespace').fill(NS);
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-nested-dc');

  // Two rack rows, cloned from the server-rendered <template>.
  await modal.getByTestId('create-add-row').click();
  await modal.getByTestId('create-add-row').click();
  const rows = modal.locator('[data-create-row="racks"]');
  await expect(rows).toHaveCount(2);

  await rows.nth(0).locator('[data-create-row-field="name"]').fill('e2e-rack-a');
  await rows.nth(0).locator('[data-create-row-field="uHeight"]').fill('42');
  await rows.nth(1).locator('[data-create-row-field="name"]').fill('e2e-rack-b');
  await rows.nth(1).locator('[data-create-row-field="uHeight"]').fill('48');

  // Each row previews its OWN id. Rack's pattern is `{name}` — filling that
  // from the parent would show the data centre's id on every row.
  await expect(rows.nth(0).locator('[data-create-row-orbid]')).toHaveText(R1);
  await expect(rows.nth(1).locator('[data-create-row-orbid]')).toHaveText(R2);

  await modal.getByTestId('create-submit').click();
  await expect(page.locator(`#generic-table tr[data-orb-id="${DC}"]`)).toHaveCount(1, { timeout: 15_000 });

  const res = await request.post('/graphql', {
    data: {
      query: `{ getDataCenter(orbId: "${DC}") {
        orbId namespace
        racks { orbId name uHeight namespace dataCenter { orbId } }
      } }`,
    },
  });
  const dc = (await res.json()).data.getDataCenter;
  expect(dc, 'the data centre must exist').toBeTruthy();

  const racks = (dc.racks as any[]).sort((a, b) => a.orbId.localeCompare(b.orbId));
  expect(racks.map(r => r.orbId), 'both racks created, with their OWN ids').toEqual([R1, R2]);
  expect(racks[0].uHeight, 'each row keeps its own values').toBe(42);
  expect(racks[1].uHeight).toBe(48);
  for (const r of racks) {
    expect(r.namespace, 'children inherit the typed namespace').toBe(NS);
    expect(r.dataCenter.orbId, 'and are LINKED to the parent created alongside them').toBe(DC);
  }
});

test('two rows that would mint the same Orb ID are refused before anything is sent', async ({ page, request }) => {
  // The failure a shared placeholder scope produces.
  //
  // DGraph would refuse this too — duplicate XIDs inside ONE nested input are a
  // hard error, verified 2026-10-13: "couldn't rewrite mutation addDataCenter
  // ... duplicate XID found". So this is not about correctness; it is about
  // WHICH refusal the reader meets. Asserting the form's own wording, not the
  // orbId, is what makes this a test of the guard rather than of DGraph —
  // both messages name the id.
  //
  // (The silent-LINK hazard is a DIFFERENT case: an id that already exists in
  // the graph. That one DGraph performs without error, and orbIdsInUse is what
  // catches it.)
  await page.goto('/data-centers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.getByTestId('create-namespace').fill(NS);
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-nested-dc');
  await modal.getByTestId('create-add-row').click();
  await modal.getByTestId('create-add-row').click();
  const rows = modal.locator('[data-create-row="racks"]');
  await rows.nth(0).locator('[data-create-row-field="name"]').fill('e2e-rack-a');
  await rows.nth(1).locator('[data-create-row-field="name"]').fill('e2e-rack-a');

  await modal.getByTestId('create-submit').click();
  await expect(modal.getByTestId('create-error'),
    'the FORM must refuse it, in words a person can act on').toContainText('same Orb ID');
  await expect(modal.getByTestId('create-error')).toContainText(R1);

  const res = await request.post('/graphql', {
    data: { query: `{ getDataCenter(orbId: "${DC}") { orbId } }` },
  });
  expect((await res.json()).data.getDataCenter, 'nothing may be written when the form refuses').toBeNull();
});


test('a row field that builds the Orb ID is required; one that does not is optional', async ({ page }) => {
  // The root form stars `name` because the orbId is built from it. A repeating
  // row owes the same treatment for the same reason — without its identity
  // field the row has no orbId, and a nested create with a missing id fails the
  // WHOLE mutation, taking the parent with it.
  //
  // Which fields those are is read off the row's own template: a bare `{x}` is
  // the row's field, `{parent.x}` is the parent's and is already required up
  // top. Rack's pattern is `{name}`, so `name` is required and `uHeight` is not.
  await page.goto('/data-centers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');
  await modal.getByTestId('create-add-row').click();
  const row = modal.locator('[data-create-row="racks"]').first();

  await expect(row.locator('[data-create-row-field="name"]'),
    'the identity field must be required').toHaveAttribute('data-create-identity', '1');
  await expect(row.locator('[data-create-row-field="uHeight"]'),
    'a field the orbId is NOT built from must stay optional').not.toHaveAttribute('data-create-identity', '1');

  // Pre-filled, as a suggestion a human sees and can change — not a value
  // written behind their back.
  await expect(row.locator('[data-create-row-field="uHeight"]')).toHaveValue('42');

  // And a row left without its identity is refused BEFORE anything is sent,
  // rather than failing the whole nested mutation at DGraph.
  await modal.getByTestId('create-namespace').fill('e2enested');
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-nested-dc');
  await modal.getByTestId('create-submit').click();
  await expect(modal.getByTestId('create-error')).toBeVisible();
});
