import { openAllDetailPanels, listRows } from './helpers/generic';
import { test, expect } from '@playwright/test';

// The Clusters page after the bespoke handler was deleted.
//
// /clusters is backed by the KubernetesCluster INTERFACE, which is the only
// thing on this page that is structurally new: DGraph generates
// queryKubernetesCluster but no getKubernetesCluster, so the list is
// interface-backed while every detail page is a concrete type resolved from the
// stored data.

// e2e/cluster-detail.spec.ts was DELETED 2026-09-25 with the bespoke cluster
// page. It asserted the per-page RELOAD button and the inner switchable
// sub-tabs (Nodes / Backups / Workload Clusters), both of which the generic
// renderer does not have: it stacks the same content as boxes and has no
// per-page reload. Named losses, accepted deliberately — the content those
// sub-tabs held is asserted below, box by box.

test('the clusters list is interface-backed and carries implementation columns', async ({ page }) => {
  await page.goto('/clusters');
  await expect(page.locator('#generic-table')).toBeVisible();

  const headers = await page.locator('#generic-table thead th').allInnerTexts();
  // `clusterType` is declared by EksaKubernetesCluster, NOT by the interface.
  // Its presence is what proves the columns are a union rather than the
  // interface's own fields.
  expect(headers, `headers were ${JSON.stringify(headers)}`).toContain('Cluster Type');
  expect(headers).toContain('Provider');

  const rows = page.locator('#generic-table tbody tr');
  expect(await rows.count()).toBeGreaterThan(0);
});

test('a cluster detail page resolves its concrete type from the data', async ({ page }) => {
  await page.goto('/clusters');
  const orbId = (await listRows(page).first().getAttribute('data-orb-id'))!;
  // The row is listed under the INTERFACE's slug, so this is the url that opens
  // it — /eksa-kubernetes-clusters does not exist and must not.
  await page.goto('/clusters?open=' + encodeURIComponent(orbId));
  await openAllDetailPanels(page);

  // clusterType exists only on EksaKubernetesCluster. Rendering it proves the
  // page resolved the concrete type — the interface has no such field and no
  // get query at all.
  await expect(page.getByTestId('generic-fields')).toContainText('Cluster Type');
  // ...and it opened on the INTERFACE's list. An EksaKubernetesCluster has no
  // page of its own, so there is one entry in the menu rather than one per
  // provider, and the way back is the Summary tab — the breadcrumb that used to
  // carry this claim belonged to the full page, which no longer exists.
  await expect(page).toHaveURL(/\/clusters(\?|$)/);
  await expect(page.locator('#tab-summary')).toBeVisible();
});

test('cluster fields show relationships as links', async ({ page }) => {
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
  await openAllDetailPanels(page);
  const fields = page.getByTestId('generic-fields');

  // Each of these is a single relationship, and the bespoke page showed every
  // one as a summary row. A box per relationship would bury the fields.
  await expect(fields).toContainText('Data Center');
  await expect(fields).toContainText('Control Plane Endpoint');

  // IPAddress has NO page, so the row names the address and does not link —
  // the dead-row rule. The value must still read as an address rather than a
  // raw orbId with its namespace prefix.
  const cpeRow = fields.locator('tr', { hasText: 'Control Plane Endpoint' });
  await expect(cpeRow.locator('a')).toHaveCount(0);
  // An IPAddress has no `name`, so the value must still read as an address —
  // not as a raw orbId with its namespace prefix.
  await expect(cpeRow.locator('td').nth(1)).toHaveText(/^\d+\.\d+\.\d+\.\d+$/);
});

test('the backup tree renders inline, and says so when absent', async ({ page }) => {
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
  await openAllDetailPanels(page);
  const backup = page.getByTestId('generic-owned').filter({ hasText: 'Backup' });
  await expect(backup).toContainText('Etcd');
  await expect(backup).toContainText('Velero');
  await expect(backup).toContainText('S3 Sync');
  await expect(backup).toContainText('Schedule');

  // "No backup configured" and "no information about backups" are different
  // answers. An omitted box gives neither.
  await page.goto('/clusters?open=' + encodeURIComponent('alaska-dot-cruiser:adot-m'));
  await openAllDetailPanels(page);
  const absent = page.getByTestId('generic-owned').filter({ hasText: 'Backup' });
  await expect(absent).toContainText('Etcd');
  await expect(absent).toContainText('Not configured');
});

test('nodes and workload clusters render as tables with populated columns', async ({ page }) => {
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
  await openAllDetailPanels(page);
  const nodes = page.getByTestId('generic-tab').filter({ hasText: 'Nodes' }).first();
  await expect(nodes).toBeVisible();

  // The Server column is a reference on an OWNED child. Its selection comes
  // from a different code path than an unowned relationship's, and the two
  // disagreeing rendered a header row above a column of dashes.
  const headers = await nodes.locator('thead th').allInnerTexts();
  expect(headers).toContain('Server');
  const serverCell = nodes.locator('tbody tr').first().locator('td').nth(headers.indexOf('Server'));
  await expect(serverCell).not.toHaveText('—');
});

test('a cluster detail page shows metadata and its audit log', async ({ page }) => {
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
  await openAllDetailPanels(page);
  const meta = page.getByTestId('generic-meta');
  await expect(meta).toContainText('Namespace');
  await expect(meta).toContainText('Version');
  await expect(page.getByTestId('generic-audit')).toBeVisible();
});

test('a cluster can be edited through the generic editor, backup subtree included', async ({ page }) => {
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
  await openAllDetailPanels(page);
  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await page.locator('[data-generic-edit-id]').click();

  const initial = await page.evaluate((id) => {
    const el = document.getElementById('generic-edit-data-' + id);
    return JSON.parse(el!.textContent!.trim());
  }, domId);

  // The backup subtree must be IN the editor's tree — it is the only way to
  // change it, the page itself being read-only.
  expect(Object.keys(initial), `editor tree was ${JSON.stringify(Object.keys(initial))}`).toContain('backup');
  expect(initial.backup).toHaveProperty('etcd');

  const marker = 'v1.99.' + (500 + (Math.floor(Date.now() / 1000) % 99));
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, kubernetesVersion: marker } });

  await page.locator('[id^="generic-edit-submit-"]').click();
  await expect(page.getByTestId('generic-fields')).toContainText(marker, { timeout: 15_000 });
});

test('a stale version is refused rather than overwriting', async ({ page }) => {
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
  await openAllDetailPanels(page);
  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');

  // Staled BEFORE the first open: configitem-editor.js parses the targets
  // element once, when the modal is first opened, so mutating it afterwards
  // changes nothing and the real version is sent. An earlier version of this
  // test did exactly that, "passed", and proved nothing.
  const staled = await page.evaluate((id) => {
    const el = document.getElementById('generic-edit-targets-' + id)!;
    const targets = JSON.parse(el.textContent!.trim());
    const before = targets[0].version;
    targets[0].version = 1;
    el.textContent = JSON.stringify(targets);
    return { before, after: targets[0].version };
  }, domId);
  expect(staled.before, 'the root target must carry a real version to stale').toBeGreaterThan(1);

  await page.locator('[data-generic-edit-id]').click();
  const initial = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);
  // A value unique to this run. Reusing a fixed marker made the second run a
  // NO-OP — the field already held it, so there was no change to guard and the
  // test passed while proving nothing.
  const probe = 'stale-probe-' + Date.now();
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, cni: probe } });

  await page.locator('[id^="generic-edit-submit-"]').click();

  // The save must fail loudly, in the modal, naming the conflict.
  const err = page.locator('#generic-edit-error-' + domId);
  await expect(err).toBeVisible({ timeout: 15_000 });
  await expect(err).toContainText('changed by someone else');
  await expect(err).toContainText('Nothing was saved');

  // And nothing may have been written. Asserted from the SERVER, not from the
  // page, because the page may simply not have reloaded yet.
  const after = await page.evaluate(async () => {
    const r = await fetch('/graphql', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query: '{ getEksaKubernetesCluster(orbId: "colo:dev-main") { cni } }' }),
    });
    return (await r.json()).data.getEksaKubernetesCluster.cni;
  });
  expect(after, 'a stale write must not land').not.toBe(probe);
});

// The two cases moved here from e2e/configitem-editor.spec.ts when /clusters
// became the generic page. Same guarantees, new renderer.

test('a cluster edit produces an audit row for the cluster, with a rendered diff', async ({ page }) => {
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
  await openAllDetailPanels(page);
  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await page.locator('[data-generic-edit-id]').click();

  const initial = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);
  expect(initial.kubernetesVersion).toBeTruthy();
  const newVersion = `v1.99.${Date.now() % 1000}`;
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, kubernetesVersion: newVersion } });

  await page.locator('[id^="generic-edit-submit-"]').click();
  await expect(page.getByTestId('generic-fields')).toContainText(newVersion, { timeout: 15_000 });

  await page.reload();
  const audit = page.getByTestId('generic-audit');
  await expect(audit).toContainText('updateEksaKubernetesCluster', { timeout: 10_000 });
  // The diff renderer ran — the row names the FIELD, not just the operation.
  await expect(audit.locator('strong:has-text("kubernetesVersion")').first()).toBeVisible();
});

test('an owned-child edit is attributed to the child, not blobbed into the parent', async ({ page }) => {
  // The critical one. A backup schedule lives on EtcdBackup, and editing it
  // through the parent's JSON tree must still record updateEtcdBackup —
  // otherwise the audit log says "the cluster changed" and loses which of its
  // owned records actually did.
  await page.goto('/clusters?open=' + encodeURIComponent('colo:dev-main'));
  await openAllDetailPanels(page);
  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  await page.locator('[data-generic-edit-id]').click();

  const initial = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-data-' + id)!.textContent!.trim()), domId);
  test.skip(!initial.backup?.etcd, 'dev-main has no etcd backup configured; this covers the edit path, not create');

  const newSchedule = `0 ${(Date.now() % 12) + 1} * * *`;
  await page.evaluate(({ id, next }) => {
    (window as any).genericEditors.get(id).set({ text: JSON.stringify(next, null, 2) });
  }, { id: domId, next: { ...initial, backup: { ...initial.backup, etcd: { ...initial.backup.etcd, schedule: newSchedule } } } });

  await page.locator('[id^="generic-edit-submit-"]').click();

  // The read-only Backup box reflects it.
  await expect(page.getByTestId('generic-owned').filter({ hasText: 'Backup' }))
    .toContainText(newSchedule, { timeout: 15_000 });

  await page.reload();
  const audit = page.getByTestId('generic-audit');
  await expect(audit).toContainText('updateEtcdBackup', { timeout: 10_000 });
  await expect(audit.locator('strong:has-text("schedule")').first()).toBeVisible();
});

test('the schema pins column order, and back-references are dropped', async ({ page }) => {
  await page.goto('/clusters');
  const headers = await page.locator('#generic-table thead th').allInnerTexts();

  // `order:` on the KubernetesCluster interface pins the leading fields. The
  // list is the INTERFACE view, so `clusterType` — which only
  // EksaKubernetesCluster declares — is proof the pin is applied to the union
  // of implementations and not just to the interface's own fields.
  const pinned = ['Provider', 'Cluster Type', 'Environment', 'Kubernetes Version'];
  const positions = pinned.map((f) => headers.indexOf(f));
  expect(positions.every((p) => p > 0), `pinned fields missing from ${JSON.stringify(headers)}`).toBeTruthy();
  for (let i = 1; i < positions.length; i++) {
    expect(positions[i], `${pinned[i]} must follow ${pinned[i - 1]}`).toBeGreaterThan(positions[i - 1]);
  }
  // Everything unpinned falls BEHIND. cni sorts first alphabetically, so it
  // would lead without the pin — that is the case that prompted this.
  expect(headers.indexOf('CNI')).toBeGreaterThan(positions[positions.length - 1]);

  // A relationship table must not carry a column naming the entity you are on.
  await page.goto('/clusters?open=' + encodeURIComponent('houston:g2-m'));
  await openAllDetailPanels(page);
  const nodes = page.getByTestId('generic-tab').filter({ hasText: 'Nodes' }).first();
  await expect(nodes).toBeVisible();
  expect(await nodes.locator('thead th').allInnerTexts(),
    'the Nodes table must not repeat the cluster on every row').not.toContain('Cluster');

  const workloads = page.getByTestId('generic-tab').filter({ hasText: 'Workload Clusters' }).first();
  if (await workloads.count()) {
    expect(await workloads.locator('thead th').allInnerTexts()).not.toContain('Management Cluster');
  }
});

test('relationship tables fit their container', async ({ page }) => {
  // The symptom the two changes above were for: 13 columns overflowing the box.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/clusters?open=' + encodeURIComponent('houston:g2-m'));
  await openAllDetailPanels(page);
  const tabs = page.getByTestId('generic-tab');
  for (let i = 0; i < await tabs.count(); i++) {
    const table = await tabs.nth(i).locator('table').boundingBox();
    const container = await tabs.nth(i).locator('.table-container').boundingBox();
    expect(table!.width, `table ${i} overflows its container`).toBeLessThanOrEqual(container!.width + 1);
  }
  // And the page itself must never scroll sideways.
  const [scrollW, clientW] = await page.evaluate(() =>
    [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
  expect(scrollW).toBeLessThanOrEqual(clientW);
});
