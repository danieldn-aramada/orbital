import { test, expect } from '@playwright/test';

// Creating a node from the UI.
//
// Orbital had no create path at all until now — everything in the graph arrived
// through `make seed` or an external GraphQL writer. The form is GENERATED from
// the view, so these tests assert the derivation, not markup: a type that earns
// a page earns a create form with no per-type code.

const TAG = 'E2ECREATE1';
const ORB_ID = `colo:server-${TAG}`;
const IDRAC_ID = `colo:${TAG}-idrac`;

// KubernetesCluster's orbIdPattern is `{name}`, so the id is the namespace and
// the typed name — nothing else to derive.
const CLUSTER_NAME = 'e2e-create-cluster';
const CLUSTER_ID = `colo:${CLUSTER_NAME}`;

// A data centre is a ROOT: nothing above it supplies a namespace, so the form
// asks for one and the create brings the Namespace node into existence too.
const DC_NS = 'e2ecreatens';
const DC_NAME = 'e2e-create-dc';
const DC_ID = `${DC_NS}:${DC_NAME}`;

// Remove anything a run left behind. Deletes BY THE IDS THIS SPEC CREATES, never
// a broad filter — the local graph is shared with whoever is developing.
//
// Alias `g` is the id the REFUSAL test tries to create. Nothing should ever
// write it, but if a regression lets one through, leaving it behind would make
// that test pass for the wrong reason on every later run.
//
// ⚠️ Comments do NOT belong inside the query string below. GraphQL comments are
// `#`, so a `//` there makes the whole mutation a syntax error — and because
// the response is not checked, cleanup then silently stops deleting anything
// and later tests fail on stale nodes instead.
async function cleanup(request: any) {
  await request.post('/graphql', {
    data: {
      query: `mutation {
        a: deleteIdracSettings(filter: { orbId: { eq: "${IDRAC_ID}" } }) { numUids }
        b: deleteServerMaintenance(filter: { orbId: { eq: "colo:server-maintenance-${TAG}" } }) { numUids }
        c: deleteServer(filter: { orbId: { eq: "${ORB_ID}" } }) { numUids }
        d: deleteEksaKubernetesCluster(filter: { orbId: { eq: "${CLUSTER_ID}" } }) { numUids }
        e: deleteDataCenter(filter: { orbId: { eq: "${DC_ID}" } }) { numUids }
        f: deleteNamespace(filter: { name: { eq: "${DC_NS}" } }) { numUids }
        g: deleteDataCenter(filter: { orbId: { eq: "colo:${DC_NAME}" } }) { numUids }
      }`,
    },
  });
}

test.beforeEach(async ({ request }) => cleanup(request));
test.afterEach(async ({ request }) => cleanup(request));

test('a New button appears only where a node can actually be created', async ({ page }) => {
  // Acceptance 1. Every page that models a creatable type now offers it, each
  // for a different reason, which is the point — none of them is a special case:
  //
  //   servers, network-devices  a plain type with a required parent
  //   clusters                  an INTERFACE, resolved to its one implementation
  //   data-centers              a ROOT, so the namespace is typed rather than
  //                             inherited
  //
  // A page would lose the button only by losing one of those properties: no
  // orbIdPattern, an interface with none or several implementations, or an id
  // orbital does not mint.
  for (const slug of ['servers', 'network-devices', 'clusters', 'data-centers']) {
    await page.goto('/' + slug);
    await expect(page.getByTestId('create-open'), `/${slug} should offer create`).toBeVisible();
  }
});

test('an interface page creates a CONCRETE implementation, never the interface', async ({ page, request }) => {
  // DGraph generates addEksaKubernetesCluster and no addKubernetesCluster, so an
  // interface-backed page must resolve to an implementation before it can create.
  //
  // Worth a test because NOTHING ON SCREEN shows which type was chosen. The page
  // heading, the button and every field look identical whether the form resolved
  // correctly or is about to send a mutation DGraph has no resolver for — the
  // difference only appears as a failed submit.
  await page.goto('/clusters');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await expect(modal, 'the modal must name the IMPLEMENTATION; the interface has no add mutation')
    .toHaveAttribute('data-kind', 'EksaKubernetesCluster');

  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: 'colo:colo-galleon' });
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill(CLUSTER_NAME);
  // clusterType exists ONLY on EksaKubernetesCluster. Filling it proves the form
  // was generated from the implementation's field set rather than the interface's.
  await modal.locator('[data-create-field="clusterType"]:not([data-create-child])').fill('workload');

  await modal.getByTestId('create-submit').click();
  await expect(page.locator(`#generic-table tr[data-orb-id="${CLUSTER_ID}"]`),
    'the new row must appear once the list reloads').toHaveCount(1, { timeout: 15_000 });

  // Read back through ConfigItem and ASK WHAT TYPE IT IS. Querying
  // getEksaKubernetesCluster would assume the answer; this way a form that
  // resolved to the wrong implementation shows up as a different __typename.
  //
  // Not queryKubernetesCluster: that interface does NOT extend ConfigItem
  // (EksaKubernetesCluster implements both separately), so it carries no orbId
  // and KubernetesClusterFilter has no orbId to filter on. The implementation's
  // own fields need an inline fragment either way — a bare `clusterType` on an
  // interface selection is refused at validation.
  const res = await request.post('/graphql', {
    data: {
      query: `{ queryConfigItem(filter: { orbId: { eq: "${CLUSTER_ID}" } }) {
        __typename orbId namespace name
        ... on EksaKubernetesCluster { clusterType dataCenter { orbId } }
      } }`,
    },
  });
  const [c] = (await res.json()).data.queryConfigItem;
  expect(c, 'the cluster must exist').toBeTruthy();
  expect(c.__typename, 'a concrete implementation, not the interface').toBe('EksaKubernetesCluster');
  expect(c.namespace, 'namespace comes from the data centre, never typed').toBe('colo');
  expect(c.clusterType, 'an implementation-only field must survive the create').toBe('workload');
  expect(c.dataCenter.orbId).toBe('colo:colo-galleon');
});

test('the form is generated from the view, including the unit', async ({ page }) => {
  // Acceptance 2 and 6. The fields are the type's editable scalars, the
  // relationship is the SCHEMA's non-null edge, and the unit is the same set the
  // editor writes — no per-type markup anywhere.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');
  await expect(modal).toBeVisible();

  // Acceptance 3: a required relationship is a PICKER, not free text.
  const dc = modal.locator('[data-create-rel="dataCenter"]');
  await expect(dc, 'dataCenter is non-null, so it must be collected').toBeVisible();
  expect(await dc.evaluate((el) => el.tagName)).toBe('SELECT');
  expect(await dc.locator('option').count(), 'the picker must offer real rows').toBeGreaterThan(1);

  for (const f of ['name', 'serviceTag', 'hostname', 'model']) {
    await expect(modal.locator(`[data-create-field="${f}"]:not([data-create-child])`)).toBeVisible();
  }
  // The unit: created in the same mutation, so it is on the same form.
  await expect(modal.locator('[data-create-child="idracSettings"]').first()).toBeVisible();
  await expect(modal.locator('[data-create-child="serverMaintenance"]').first()).toBeVisible();
});

test('the orbId is constructed from the schema pattern and shown before submit', async ({ page }) => {
  // Acceptance 4 and 5. Built from `orbIdPattern` with the namespace taken from
  // the chosen data centre — never typed, so the two cannot disagree. Shown
  // BEFORE committing, because a wrong orbId is permanent.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');
  const preview = modal.getByTestId('create-orbid');

  await expect(preview, 'nothing chosen yet — a PARTIAL id would collide').toHaveText('—');

  await modal.locator('[data-create-field="serviceTag"]:not([data-create-child])').fill(TAG);
  await expect(preview, 'still no namespace until a data centre is chosen').toHaveText('—');

  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: 'colo:colo-galleon' });
  await expect(preview).toHaveText(ORB_ID);
});

test('creating a server with its iDRAC settings writes both, linked', async ({ page, request }) => {
  // Acceptance 6, 7 and 9 — the headline. ONE nested mutation creates the server
  // AND its subgraph member, and the data centre is LINKED rather than copied.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: 'colo:colo-galleon' });
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-create-1');
  await modal.locator('[data-create-field="serviceTag"]:not([data-create-child])').fill(TAG);
  await modal.locator('[data-create-field="model"]:not([data-create-child])').fill('PowerEdge R650');
  await modal.locator('[data-create-child="idracSettings"][data-create-field="firmwareVersion"]').fill('7.10.00.00');

  await modal.getByTestId('create-submit').click();

  // Success reloads the list, so the new ROW is the signal — waited for by its
  // orbId rather than by text, which also races the reload. The row carries
  // data-orb-id because the name cell is plain text (single click is DataTables
  // row selection), so this is the stable handle.
  await expect(page.locator(`#generic-table tr[data-orb-id="${ORB_ID}"]`),
    'the new row must appear once the list reloads').toHaveCount(1, { timeout: 15_000 });

  // Read it back through the graph, not off the page: the claim is that nodes
  // and relationships were written, which the rendered list cannot prove.
  const res = await request.post('/graphql', {
    data: {
      query: `{ getServer(orbId: "${ORB_ID}") {
        orbId namespace name model
        dataCenter { orbId name servers { orbId } }
        idracSettings { orbId firmwareVersion server { orbId } }
      } }`,
    },
  });
  const srv = (await res.json()).data.getServer;
  expect(srv, 'the server must exist').toBeTruthy();
  expect(srv.namespace, 'namespace comes from the data centre').toBe('colo');
  expect(srv.model).toBe('PowerEdge R650');

  // Acceptance 7: LINKED, not duplicated.
  expect(srv.dataCenter.orbId).toBe('colo:colo-galleon');
  expect(srv.dataCenter.name, 'the existing data centre must be untouched').toBe('colo-galleon');

  // Acceptance 6: the subgraph member exists, carries ITS OWN values, and points back.
  expect(srv.idracSettings, 'the unit child must be created too').toBeTruthy();
  expect(srv.idracSettings.firmwareVersion,
    'a nested child that LINKED instead of being created would silently drop this').toBe('7.10.00.00');
  expect(srv.idracSettings.server.orbId).toBe(ORB_ID);
});

test('a duplicate orbId is refused, and nothing is written', async ({ page, request }) => {
  // Acceptance 11. DGraph errors on a duplicate root, but the raw message is
  // "couldn't rewrite mutation ... already exists for field orbId inside type
  // Server" — so the form pre-checks and says something a person can act on.
  await request.post('/graphql', {
    data: {
      query: `mutation { addServer(input: [{ orbId: "${ORB_ID}", namespace: "colo", version: 1,
        name: "already-here", serviceTag: "${TAG}", dataCenter: { orbId: "colo:colo-galleon" } }]) { numUids } }`,
    },
  });

  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');
  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: 'colo:colo-galleon' });
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('second-attempt');
  await modal.locator('[data-create-field="serviceTag"]:not([data-create-child])').fill(TAG);
  await modal.getByTestId('create-submit').click();

  await expect(modal.getByTestId('create-error')).toContainText(ORB_ID, { timeout: 10_000 });

  // And the existing node is untouched — a refused create must not half-apply.
  const res = await request.post('/graphql', {
    data: { query: `{ getServer(orbId: "${ORB_ID}") { name } }` },
  });
  expect((await res.json()).data.getServer.name).toBe('already-here');
});

test('a missing required relationship is refused before any mutation is sent', async ({ page }) => {
  // Acceptance 10. Refused in the form, with a reason — not sent and bounced,
  // which would surface as a schema error nobody can act on.
  let mutations = 0;
  page.on('request', (r) => {
    if (r.url().includes('/graphql') && r.method() === 'POST'
        && (r.postData() || '').includes('addServer')) mutations++;
  });

  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('no-datacentre');
  await modal.locator('[data-create-field="serviceTag"]:not([data-create-child])').fill(TAG);
  await modal.getByTestId('create-submit').click();

  await expect(modal.getByTestId('create-error')).toBeVisible();
  expect(mutations, 'nothing may be sent when the form is incomplete').toBe(0);
});

test('a page that offers create carries exactly one resolved pattern', async ({ page }) => {
  // One type, one orbId shape. A type declaring TWO (NetworkInterface: a NIC
  // from the server's serviceTag, a port from the device's serial) has an
  // unresolved modelling question and gets no create form at all — the RULE is
  // unit-tested in TestBuildCreateForm_AmbiguousIdentityOffersNoForm; this
  // checks the consequence on the pages that do offer one.
  for (const slug of ['servers', 'network-devices']) {
    await page.goto('/' + slug);
    const tmpl = await page.locator('[id^="create-modal-"]').getAttribute('data-orbid-template');
    expect(tmpl, `/${slug} must carry a resolved pattern`).toBeTruthy();
    expect(tmpl, 'no edge-crossing placeholder may survive into the form').not.toContain('.');
    expect(tmpl, '{kind} is resolved server-side, not in JS').not.toContain('{kind}');
  }
});

test('defaults pre-fill the form, and identity fields never carry one', async ({ page }) => {
  // A default is a suggestion a human SEES and may change — not a hidden write.
  // Never on a field the orbId is built from: a pre-filled identity creates a
  // node under a key nobody chose, and unlike a wrong manufacturer that cannot
  // be corrected.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await expect(modal.locator('[data-create-field="manufacturer"]:not([data-create-child])')).toHaveValue('Dell');
  await expect(modal.locator('[data-create-field="model"]:not([data-create-child])')).toHaveValue('PowerEdge R450');

  // serviceTag feeds `{kind}-{serviceTag}`, so it must arrive EMPTY.
  await expect(modal.locator('[data-create-field="serviceTag"]:not([data-create-child])'),
    'an identity field must never be pre-filled').toHaveValue('');
  await expect(modal.locator('[data-create-field="name"]:not([data-create-child])')).toHaveValue('');

  // Booleans are selects, not text boxes: a text box yields the STRING "false",
  // which DGraph refuses, and lets someone type "flase".
  const ssh = modal.locator('[data-create-child="idracSettings"][data-create-field="sshEnabled"]');
  expect(await ssh.evaluate((el) => el.tagName)).toBe('SELECT');
  await expect(ssh).toHaveValue('false');
  await expect(modal.locator('[data-create-child="idracSettings"][data-create-field="racadmEnabled"]')).toHaveValue('true');
  // ...and a String child field is still a text input.
  const fw = modal.locator('[data-create-child="idracSettings"][data-create-field="firmwareVersion"]');
  expect(await fw.evaluate((el) => el.tagName)).toBe('INPUT');
});

test('a defaulted boolean is written as a boolean, not the string "false"', async ({ page, request }) => {
  // The regression: every input was a text box, so a Boolean field received
  // "false" — a string — and DGraph refuses it, failing the whole create. Typing
  // the control is what makes defaults usable on iDRAC settings at all.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: 'colo:colo-galleon' });
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-create-1');
  await modal.locator('[data-create-field="serviceTag"]:not([data-create-child])').fill(TAG);
  // Touch ONE child field so the unit is created; the rest ride on their defaults.
  await modal.locator('[data-create-child="idracSettings"][data-create-field="firmwareVersion"]').fill('7.20.10.05');
  await modal.getByTestId('create-submit').click();

  await expect(page.locator(`#generic-table tr[data-orb-id="${ORB_ID}"]`)).toHaveCount(1, { timeout: 15_000 });

  const res = await request.post('/graphql', {
    data: {
      query: `{ getServer(orbId: "${ORB_ID}") {
        manufacturer model uHeight
        idracSettings { racadmEnabled sshEnabled usbManagementPortEnabled firmwareVersion }
      } }`,
    },
  });
  const srv = (await res.json()).data.getServer;

  // Defaults the user never touched are stored, as their real types.
  expect(srv.manufacturer).toBe('Dell');
  expect(srv.model).toBe('PowerEdge R450');
  expect(srv.uHeight).toBe(1);
  expect(typeof srv.idracSettings.racadmEnabled, 'a Boolean must arrive as a boolean').toBe('boolean');
  expect(srv.idracSettings.racadmEnabled).toBe(true);
  expect(srv.idracSettings.sshEnabled).toBe(false);
  expect(srv.idracSettings.usbManagementPortEnabled).toBe(true);
});

test('`create: false` hides a field from the form but leaves it editable', async ({ page }) => {
  // A third axis, distinct from the two that already exist: `editable: false`
  // would remove the field from the EDITOR too (so nobody could ever set a
  // maintenance window), and `tableHidden` is about table columns and does not
  // touch the form at all. The case it serves is a field whose expected format
  // the form cannot yet convey.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await expect(modal.locator('[data-create-child="serverMaintenance"][data-create-field="enabled"]'),
    'the switch is still asked for').toBeVisible();
  for (const f of ['reason', 'windowStart', 'windowEnd']) {
    await expect(modal.locator(`[data-create-child="serverMaintenance"][data-create-field="${f}"]`),
      `${f} is declared create:false`).toHaveCount(0);
  }

  // ...and the SAME fields are still writable through the editor. Hiding one
  // from create must not quietly make it read-only forever.
  await page.goto('/servers?open=' + encodeURIComponent('colo:server-CFRHDX3'));
  await expect(page.getByTestId('generic-fields')).toBeVisible();
  const domId = await page.locator('[data-generic-edit-id]').getAttribute('data-generic-edit-id');
  const targets = await page.evaluate((id) => JSON.parse(
    document.getElementById('generic-edit-targets-' + id)!.textContent!.trim()), domId);
  const sm = targets.find((t: any) => t.kind === 'ServerMaintenance');
  expect(sm, 'ServerMaintenance must still be an edit target').toBeTruthy();
  for (const f of ['reason', 'windowStart', 'windowEnd']) {
    expect(sm.fields, `${f} must stay editable`).toContain(f);
  }
});

test('a field says what it expects, so nothing has to be guessed', async ({ page }) => {
  // The reason `create: false` exists at all is that an empty DateTime box
  // invites "today", "14:30" and "06/10/2026" — two of which fail, one of which
  // fails differently per locale. A placeholder is the cheaper fix, and when
  // every type carries one the hiding can go.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await expect(modal.locator('[data-create-field="rackPosition"]:not([data-create-child])'))
    .toHaveAttribute('placeholder', 'whole number');
  await expect(modal.locator('[data-create-field="rackPosition"]:not([data-create-child])'))
    .toHaveAttribute('type', 'number');
  // A String needs no hint — an empty text box already says "text".
  await expect(modal.locator('[data-create-field="hostname"]:not([data-create-child])'))
    .toHaveAttribute('placeholder', '');
});


// ── A root type: the namespace has nowhere to come from ───────────────────────

test('only a ROOT asks for a namespace; anything with a parent inherits one', async ({ page }) => {
  // Acceptance 1, 2 and 7. The input is driven by "this type has no required
  // parent", not by the type's name — so the assertion that matters most is the
  // NEGATIVE one: adding it to a type that inherits a namespace would give two
  // sources for one value, and two chances to disagree.
  await page.goto('/data-centers');
  await page.getByTestId('create-open').click();
  await expect(page.locator('.modal.is-active').getByTestId('create-namespace'),
    'a root has nothing to inherit from, so it must ask').toBeVisible();

  for (const slug of ['servers', 'clusters']) {
    await page.goto('/' + slug);
    await page.getByTestId('create-open').click();
    await expect(page.locator('.modal.is-active').getByTestId('create-namespace'),
      `/${slug} inherits its namespace from a required parent and must NOT ask`).toHaveCount(0);
  }
});

test('creating a data centre creates its namespace too', async ({ page, request }) => {
  // Acceptance 3 and 4. Nothing else creates a Namespace node, so a data centre
  // whose namespace does not exist would leave every orbId beneath it prefixed
  // with a namespace the graph has never heard of.
  await page.goto('/data-centers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.getByTestId('create-namespace').fill(DC_NS);
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill(DC_NAME);

  // Acceptance 4: the id is built from the TYPED namespace, shown before commit.
  await expect(modal.getByTestId('create-orbid'),
    'the identity must be visible before it is committed to').toHaveText(DC_ID);

  await modal.getByTestId('create-submit').click();
  await expect(page.locator(`#generic-table tr[data-orb-id="${DC_ID}"]`)).toHaveCount(1, { timeout: 15_000 });

  const res = await request.post('/graphql', {
    data: {
      query: `{
        getDataCenter(orbId: "${DC_ID}") { orbId namespace name }
        queryNamespace(filter: { name: { eq: "${DC_NS}" } }) { name }
      }`,
    },
  });
  const d = (await res.json()).data;
  expect(d.getDataCenter, 'the data centre must exist').toBeTruthy();
  expect(d.getDataCenter.namespace).toBe(DC_NS);
  expect(d.queryNamespace,
    'the Namespace node must exist too — nothing else would ever create it').toHaveLength(1);
});

test('a namespace that already holds a data centre is refused', async ({ page, request }) => {
  // One namespace holds one data centre. Enforced against the DATA CENTRES, not
  // against the Namespace node: an empty namespace is what a create that failed
  // halfway leaves behind, and refusing that would make the retry impossible.
  await page.goto('/data-centers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.getByTestId('create-namespace').fill('colo'); // already holds colo-galleon
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill(DC_NAME);
  await modal.getByTestId('create-submit').click();

  await expect(modal.getByTestId('create-error'),
    'the refusal must name the namespace and what already holds it').toContainText('colo');

  // And nothing was written — a refusal that half-applies is worse than one that
  // explains itself.
  const res = await request.post('/graphql', {
    data: { query: `{ getDataCenter(orbId: "colo:${DC_NAME}") { orbId } }` },
  });
  expect((await res.json()).data.getDataCenter,
    'a refused create must leave no node behind').toBeNull();
});


test('an identity field left blank is refused, even when the pattern has a prefix', async ({ page, request }) => {
  // `{kind}-{serviceTag}` with an empty service tag substitutes to `server-`,
  // which leaves NO unresolved brace — so a check for "are all placeholders
  // filled?" passes and the form would mint `colo:server-` for every nameless
  // server, colliding on the second one.
  //
  // Found 2026-10-13 when Rack's pattern gained a `{kind}-` prefix and the
  // blank-row case stopped being refused. The root form had the same hole all
  // along; only patterns with no prefix (`{name}`) were accidentally safe.
  await page.goto('/servers');
  await page.getByTestId('create-open').click();
  const modal = page.locator('.modal.is-active');

  await modal.locator('[data-create-rel="dataCenter"]').selectOption({ value: 'colo:colo-galleon' });
  await modal.locator('[data-create-field="name"]:not([data-create-child])').fill('e2e-blank-identity');
  // serviceTag deliberately left empty — it is what the orbId is built from.

  await expect(modal.getByTestId('create-orbid'),
    'a half-built id must not be shown as if it were real').toHaveText('—');

  await modal.getByTestId('create-submit').click();
  await expect(modal.getByTestId('create-error')).toBeVisible();

  const res = await request.post('/graphql', {
    data: { query: `{ getServer(orbId: "colo:server-") { orbId } }` },
  });
  expect((await res.json()).data.getServer, 'nothing may be written under a stub id').toBeNull();
});
