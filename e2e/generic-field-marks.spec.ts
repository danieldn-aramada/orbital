import { test, expect } from '@playwright/test';
import { listRows } from './helpers/generic';

// Proposed-change marks on generic pages.
//
// The mark machinery was built for the Server page but was never
// Server-specific: it is driven entirely by DOM attributes — `data-field-orbid`
// and `data-field-values` on a table, `data-field` plus a `.js-field-mark` slot
// per row — which `loadFieldMarks` in orbital.js reads. A generic page joins it
// by emitting the same attributes.
//
// Worth a browser test rather than a unit one: the value comparison that
// suppresses a no-op proposal happens in JS against the RAW values the server
// embedded, so nothing below the page asserts that the two halves agree.

const CLUSTER = 'colo:dev-main';

async function proposeCni(page: any, value: string) {
  return await page.evaluate(async ({ orbId, value }) => {
    const cur = await (await fetch('/graphql', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query: `{ getEksaKubernetesCluster(orbId:${JSON.stringify(orbId)}){ version } }` }),
    })).json();
    const r = await fetch('/api/v1/change-requests', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: 'field-mark e2e', namespace: 'colo',
        changes: [{ op: 'update', orbId, set: { cni: value }, version: cur.data.getEksaKubernetesCluster.version }],
      }),
    });
    return await r.json();
  }, { orbId: CLUSTER, value });
}

// CLOSE, not DELETE. There is no delete endpoint — a change request is part of
// the audit record — and leaving one open makes the NEXT run see two competing
// proposals on the same field, at which point the mark shows a count instead of
// the value and this test fails on its own debris.
async function discard(page: any, id: string) {
  if (!id) return;
  await page.evaluate(async (id) => {
    await fetch(`/api/v1/change-requests/${encodeURIComponent(id)}/close`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ reason: 'e2e cleanup' }),
    });
  }, id);
}

test('a pending proposal marks the field it targets, and only that field', async ({ page }) => {
  await page.goto('/');
  const cr = await proposeCni(page, 'calico-field-mark-e2e');
  expect(cr.id, `change request not created: ${JSON.stringify(cr).slice(0, 200)}`).toBeTruthy();

  try {
    await page.goto('/clusters?open=' + encodeURIComponent(CLUSTER));

    // The marked row names the proposed value, so you can see what is pending
    // without opening the change request.
    const marked = page.locator('[data-field="cni"]').first();
    await expect(marked).toContainText('calico-field-mark-e2e', { timeout: 15_000 });
    await expect(marked).toContainText('proposed');

    // Only that field. A mark on a field nobody proposed would make the whole
    // signal untrustworthy — this is the negative half.
    const untouched = page.locator('[data-field="environment"]').first();
    await expect(untouched.locator('.js-field-mark')).toBeEmpty();
  } finally {
    await discard(page, cr.id);
  }
});

test('subgraph members carry their own marks, keyed to the child', async ({ page }) => {
  // An edit to a subgraph member records the CHILD's orbId and never the
  // parent's, so a child's rows can only be marked from the child's own id.
  // Getting this wrong shows a backup-schedule proposal on the cluster's own
  // fields, or nowhere at all.
  await page.goto('/clusters?open=' + encodeURIComponent(CLUSTER));
  // The detail body arrives by HTMX — wait for it, or this reads an empty DOM
  // and reports "no mark table" for a view that renders one perfectly well.
  await expect(page.getByTestId('generic-fields')).toBeVisible();

  const tables = await page.locator('[data-field-orbid]').evaluateAll((els) =>
    els.map((e) => ({
      orbId: e.getAttribute('data-field-orbid'),
      rows: e.querySelectorAll('[data-field]').length,
      slots: e.querySelectorAll('.js-field-mark').length,
    })));

  const root = tables.find((t) => t.orbId === CLUSTER);
  expect(root, `no mark table for the root: ${JSON.stringify(tables)}`).toBeTruthy();
  expect(root!.rows).toBeGreaterThan(0);

  // Every subgraph member gets its own table under its own orbId.
  const children = tables.filter((t) => t.orbId !== CLUSTER);
  expect(children.length, 'subgraph members must carry their own mark tables').toBeGreaterThan(0);
  for (const c of children) {
    expect(c.orbId, 'a child mark table must name the CHILD').not.toBe(CLUSTER);
    // A slot per markable row, never more: a slot with no data-field can never
    // be filled and is a column of nothing.
    expect(c.slots).toBe(c.rows);
  }
});

// e2e/change-request-tab-dots.spec.ts was DELETED 2026-09-26 with the bespoke
// Server page.
//
// It pinned two properties of the tab DOT — a marker on a panel tab saying
// "something inside here is proposed". That existed because a field mark was
// invisible until you opened the tab holding it, so a proposal on a server's
// maintenance window was reachable only by clicking through every panel.
//
// The generic renderer stacks panels as boxes instead of tabbing them, so there
// is no closed tab for a mark to hide behind: every mark on the page is visible
// on load. The problem the dot solved is structurally gone rather than
// unimplemented, and the "is anything proposed here at all" question the dot
// also answered is now the pending-change banner at the top of the page.
//
// `renderTabDots` stays in orbital.js while NetworkDevice is still a tabbed
// page; it goes with that migration.

test('nothing proposed leaves every mark slot empty', async ({ page }) => {
  // The negative the dot spec also protected: a marker that is always on is
  // indistinguishable from a working one until someone notices it never turns
  // off. Asserted on a page with no proposals against it.
  await page.goto('/network-devices');
  const orbId = (await listRows(page).first().getAttribute('data-orb-id'))!;
  await page.goto('/network-devices?open=' + encodeURIComponent(orbId));
  await expect(page.getByTestId('generic-fields')).toBeVisible();
  // Let the marks fetch resolve, so this cannot pass by asserting too early.
  await page.waitForTimeout(1_500);
  for (const slot of await page.locator('.js-field-mark').all()) {
    await expect(slot).toBeEmpty();
  }
  await expect(page.locator('[data-pending-changes-for]')).toBeEmpty();
});

// e2e/server-edit-tab-restore.spec.ts was DELETED 2026-09-26 with the bespoke
// Server page. It asserted that the Audit Log SUBTAB stayed selected after an
// edit-modal submit reloaded the fragment beneath it — a property of inner
// tabs, which the generic renderer does not have. Every panel is a stacked box
// and visible at once, so there is no selection to lose. Like the tab dots, the
// problem is structurally gone rather than unimplemented.
