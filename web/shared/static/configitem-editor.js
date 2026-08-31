// configitem-editor.js — generic edit-modal submit handler for any parent
// ConfigItem page (Server / DataCenter / KubernetesCluster). Mirrors the
// Go-side `internal/configitems` registry: the page handler hands this module
// a `targets` array describing every editable entity in the JSON editor tree
// (the parent itself + each owned child), and this module:
//
//   1. Snapshots each target's subtree at modal open.
//   2. On submit, diffs each target against its snapshot.
//   3. Dispatches one canonical update{Kind} mutation per changed target
//      (or add{Kind} for first-time creates), in PARALLEL.
//   4. Reloads the parent fragment via the shared helper.
//
// Why generic
//
// Every silent bug from the cluster-backup feature (this session) had a
// per-page reimplementation as its root cause: wrong reload-target id,
// missing snapshot diff, composite-mutation bypassing the audit diff
// renderer, `addX.upsert` for edits instead of `update{Kind}`, custom
// variable names breaking the audit resource extractor. Centralizing the
// logic kills the entire bug class for any page that adopts this module.
//
// What the target shape looks like
//
//   {
//     // JSON path into the editor's tree. [] = the root entity. ["backup","etcd"] = nested.
//     path: string[],
//     // The ConfigItem type name (matches internal/configitems registry).
//     kind: string,
//     // This entity's orbId. Used as $orbId in update mutations.
//     orbId: string,
//     // Scalar fields the editor exposes. Used to build the `set` map.
//     fields: string[],
//     // GraphQL response selection field name on Add{Kind}Payload — used so
//     // the audit extractor finds the orbId in the response body.
//     payloadField: string,
//     // Optional: only present for owned children. The @hasInverse field on
//     // THIS type that points back to the parent (e.g. EtcdBackup.clusterBackupEtcd).
//     parentInverseField?: string,
//     // Optional: parent entity's orbId — used when linking a new child to
//     // its parent on first-time create.
//     parentOrbId?: string,
//     // Optional: when adding a new child whose parent ConfigItem might not
//     // exist yet, this declares the parent's metadata so we can create it.
//     parentWrapper?: { kind: string, orbId: string, name: string },
//   }
//
// The page handler builds this list from the Go-side registry — see
// internal/handler/cluster.go for the reference implementation.

import { BASE, safeDomId, subtreeOrbIds } from './shared.js'

// getByPath walks `obj` following `path` (an array of keys) and returns the
// value at that location, or undefined if any intermediate key is missing.
function getByPath(obj, path) {
  let cur = obj
  for (const key of path) {
    if (cur == null || typeof cur !== 'object') return undefined
    cur = cur[key]
  }
  return cur
}

// rootWithoutSubtrees returns a shallow copy of `state` with the top-level
// keys consumed by any target path removed. Used to compute the root
// entity's diff (everything that isn't a nested editable child).
function rootWithoutSubtrees(state, targets) {
  if (state == null || typeof state !== 'object') return {}
  const out = { ...state }
  for (const t of targets) {
    if (t.path.length > 0) {
      delete out[t.path[0]]
    }
  }
  return out
}

// buildUpdateCall constructs the canonical
// `update{Kind}(input: { filter: { orbId: { eq: $orbId } }, set: $set })`
// mutation. This shape triggers orbital's generic before-fetch + diff renderer.
// Do NOT change to add{Kind}.upsert — that path skips the before-fetch and
// shows raw variables instead of a colored diff. See docs/reference/AUDIT.md.
function buildUpdateCall({ kind, orbId, set, remove, payloadField }) {
  // `remove` clears fields the user emptied. DGraph ignores null in `set` and
  // rejects "" on typed scalars (DateTime), so clearing requires `remove` with
  // the field's prior value. Only declare/send it when non-empty so existing
  // set-only edits are byte-for-byte unchanged.
  const hasRemove = remove && Object.keys(remove).length > 0
  return {
    query: `mutation Update${kind}($orbId: String!, $set: ${kind}Patch!${hasRemove ? `, $remove: ${kind}Patch` : ''}) {
      update${kind}(input: { filter: { orbId: { eq: $orbId } }, set: $set${hasRemove ? ', remove: $remove' : ''} }) { ${payloadField} { orbId } }
    }`,
    variables: hasRemove ? { orbId, set, remove } : { orbId, set },
  }
}

// buildAddCall constructs a first-time-create mutation. Response selection
// includes the payload field with orbId so the audit extractor links the new
// resource. No diff is rendered (no before-state); audit shows raw variables.
function buildAddCall({ kind, input, payloadField }) {
  return {
    query: `mutation Add${kind}($input: [Add${kind}Input!]!) {
      add${kind}(input: $input, upsert: true) { numUids ${payloadField} { orbId } }
    }`,
    variables: { input: [input] },
  }
}

// valueForMutation stringifies fields declared String in the schema whose VALUE
// is JSON (DataCenter.assetDataV2). Sending them as structure is rejected —
// "cannot use as String" from DGraph on the mutation path, and by the changeset
// validator on the proposal path, which checks it before a reviewer ever sees it.
function valueForMutation(t, f, v) {
  const jsonStrFields = new Set(t.jsonStringFields || [])
  if (!jsonStrFields.has(f)) return v
  return typeof v === 'string' ? v : JSON.stringify(v)
}

// An "empty" value the user cleared: null, "", or a deleted key. These must
// never go into `set` (DGraph ignores null and rejects "" on DateTime) —
// clearing is expressed via `remove`/`clear` instead.
function isEmpty(v) { return v === undefined || v === null || v === '' }

// scalarPayload and removePayload were closures inside onSubmit until the
// change-request path needed the same two computations. Both are pure — they
// capture nothing — so they are hoisted rather than copied: a second copy would
// drift the first time someone changes what counts as empty, and the two paths
// would then propose something different from what they would have written.

function scalarPayload(t, sub) {
  const out = {}
  for (const f of t.fields) if (f in sub && !isEmpty(sub[f])) out[f] = valueForMutation(t, f, sub[f])
  return out
}

// removePayload builds the `remove` map: fields that HAD a value in the snapshot
// and are now empty in the edited state. DGraph clears a scalar only via
// `remove` (with its prior value), never via set:null/set:"". Type-agnostic —
// DateTime, String, etc. all clear the same way.
function removePayload(t, before, sub) {
  const out = {}
  if (before == null) return out
  for (const f of t.fields) {
    if (!isEmpty(before[f]) && isEmpty(sub == null ? undefined : sub[f])) {
      out[f] = valueForMutation(t, f, before[f])
    }
  }
  return out
}

// STAMPED_FIELDS are written by orbital, never proposed by a client. The
// changeset validator rejects them outright — `version` is the MVCC counter and
// created*/updated* are provenance, so accepting them from a proposal would let
// a change request rewrite the very trail the approval depends on.
const STAMPED_FIELDS = new Set([
  'id', 'orbId', 'namespace', 'version', 'createdAt', 'createdBy', 'updatedAt', 'updatedBy',
])

function withoutStamped(obj) {
  const out = {}
  for (const [k, v] of Object.entries(obj || {})) {
    if (!STAMPED_FIELDS.has(k)) out[k] = v
  }
  return out
}

// buildChangeset renders the same edit the mutation path renders, in the
// store-neutral form the change-request API takes.
//
// It is NOT a translation of the `calls` array, and cannot be: the mutation
// path FOLDS a brand-new child subtree into its parent's `set` as a nested
// object, and DGraph link semantics make that legal there. A changeset forbids
// it — an edge value may carry only a reference, because DGraph links on an
// edge rather than writing through it, so a nested payload would be accepted
// and silently discarded. This flattens the fold instead: one item per entity,
// wrapper before the children that reference it, which the API permits because
// merge applies items in order.
//
// Exported for test: the fold-flattening is the part with no visual tell, so it
// is asserted directly rather than inferred from a screenshot.
export function buildChangeset({
  namespace, rootTarget, rootOrbId, rootScalars, rootRemove,
  changes, wrappersNeeded, foldedOrbIds,
}) {
  const items = []

  // 1. Wrappers first — nothing may reference an entity that does not exist yet.
  for (const w of wrappersNeeded.values()) {
    items.push({
      orbId: w.orbId,
      type: w.kind,
      op: 'upsert',
      set: withoutStamped({ name: w.name }),
    })
  }

  // 2. Children that the mutation path would have folded into the wrapper.
  //    Each becomes its own item, pointing back at its wrapper by reference.
  for (const ch of changes) {
    const t = ch.target
    if (!foldedOrbIds.has(t.orbId)) continue
    const set = withoutStamped({ name: deriveName(t), ...scalarPayload(t, ch.currentSub || {}) })
    if (t.parentInverseField && t.parentOrbId) {
      set[t.parentInverseField] = { orbId: t.parentOrbId }
    }
    items.push({ orbId: t.orbId, type: t.kind, op: 'upsert', set })
  }

  // 3. The root's own scalar edits, plus a REFERENCE to any wrapper just
  //    created — a reference, never the nested object the mutation path uses.
  const rootSet = withoutStamped(rootScalars || {})
  for (const w of wrappersNeeded.values()) {
    rootSet[w.parentField] = { orbId: w.orbId }
  }
  const rootClear = Object.keys(rootRemove || {}).filter(f => !STAMPED_FIELDS.has(f))
  if (Object.keys(rootSet).length > 0 || rootClear.length > 0) {
    items.push({
      orbId: rootOrbId,
      type: rootTarget ? rootTarget.kind : undefined,
      op: 'update',
      set: rootSet,
      clear: rootClear,
    })
  }

  // 4. Everything else — edits to existing children, and creates under a
  //    wrapper that already exists.
  for (const ch of changes) {
    const t = ch.target
    if (t.path.length === 0) continue        // root handled above
    if (foldedOrbIds.has(t.orbId)) continue  // folded child handled above
    const sub = ch.currentSub || {}
    if (ch.existed) {
      const set = withoutStamped(scalarPayload(t, sub))
      // `remove` carries the field's PRIOR VALUE because DGraph needs it to
      // clear; the store-neutral form needs only the name. Lossless: merge
      // re-reads the current value when it builds the mutation, and if that
      // value moved, the request is stale and will not merge — so the prior
      // value can never be needed and wrong at the same time.
      const clear = Object.keys(removePayload(t, ch.before, sub)).filter(f => !STAMPED_FIELDS.has(f))
      if (Object.keys(set).length === 0 && clear.length === 0) continue
      items.push({ orbId: t.orbId, type: t.kind, op: 'update', set, clear })
    } else {
      const set = withoutStamped({ name: deriveName(t), ...scalarPayload(t, sub) })
      if (t.parentInverseField && t.parentOrbId) {
        set[t.parentInverseField] = { orbId: t.parentOrbId }
      }
      items.push({ orbId: t.orbId, type: t.kind, op: 'upsert', set })
    }
  }

  return { namespace, changes: items }
}

// activeChangeRequestsFor answers "what is in flight for this item?" for a whole
// ConfigItem subtree — the entity plus everything it owns.
//
// `active` is open PLUS approved-not-yet-merged: `status=open` would miss the
// approved ones, because approved is derived rather than stored.
//
// ONE request, not a chunked set. The API's orbId cap sits above the largest
// real subtree, so the list always fits; if it ever stops fitting the API
// refuses outright rather than truncating, which is a visible failure instead
// of a quietly narrowed answer. This deliberately does NOT split-and-merge:
// chunks overlap in their results — a request naming both a server and its
// iDRAC comes back in two of them — so every consumer would need an
// overlap-aware union by id, and that is the API's job to make unnecessary.
//
// Best-effort by design: an unreachable endpoint leaves the caller with an
// empty answer rather than an error, because no notice is better than blocking
// an edit on a badge.
export function activeChangeRequestsFor(orbIds) {
  const ids = [...new Set((orbIds || []).filter(Boolean))]
  if (ids.length === 0) return Promise.resolve({ total: 0, items: [] })

  const qs = ids.map(id => 'orbId=' + encodeURIComponent(id)).join('&')
  return fetch(BASE + '/api/v1/change-requests?status=active&' + qs, { headers: { Accept: 'application/json' } })
    .then(r => (r.ok ? r.json() : null))
    .then(j => ({ total: (j && j.total) || 0, items: (j && j.items) || [] }))
    .catch(() => ({ total: 0, items: [] }))
}

// applyGateState resolves how this edit will be written and tells the user
// BEFORE they spend effort on it.
//
// Two things are surfaced, and both are about not wasting someone's work:
//   - the Save button says what it will actually do, so a gated user does not
//     type an edit expecting it to land and then meet a refusal;
//   - a change already in flight on this entity is flagged, so two people do not
//     unknowingly propose conflicting edits to the same thing.
//
// Both reads are best-effort. If either call fails the editor stays exactly as
// it was — an unreachable policy endpoint must not stop someone editing.
function applyGateState({ modal, submitBtnId, reloadOrbId, rootKind, targets, namespaceOf, setMode }) {
  const btn = submitBtnId ? document.getElementById(submitBtnId) : null
  const namespace = namespaceOf(reloadOrbId)
  if (!namespace) return

  // Notices go INSIDE the footer, above the buttons — not between the body and
  // the footer, and not next to the buttons.
  //
  // Three earlier placements each misaligned it. Inside `.buttons` it became a
  // flex sibling of Save and Cancel and shoved them sideways. In the body it
  // scrolled away with the JSON editor. Between body and footer it was flush to
  // the card while the editor sits inside the body's padding and the buttons sit
  // inside the footer's — three different left edges in one dialog.
  //
  // Inside the footer it inherits the footer's padding, so its left edge is the
  // Save button's left edge exactly. `.has-gate-notice` makes the footer wrap
  // (see main.scss) so the notice takes a full row of its own and the buttons
  // keep theirs.
  function addNotice(el) {
    const foot = btn && btn.closest('.modal-card')?.querySelector('.modal-card-foot')
    if (foot) {
      el.classList.add('js-gate-notice')
      foot.classList.add('has-gate-notice')
      foot.insertBefore(el, foot.firstChild)
    } else if (btn && btn.parentNode) {
      btn.parentNode.insertBefore(el, btn)
    }
  }

  const notice = document.createElement('div')
  notice.className = 'is-size-7'
  notice.style.display = 'none'
  addNotice(notice)

  const say = (cls, html) => {
    notice.className = 'notification is-light py-2 is-size-7 js-gate-notice ' + cls
    notice.innerHTML = html
    notice.style.display = ''
  }

  const q = new URLSearchParams({ namespace })
  if (rootKind) q.set('type', rootKind)
  fetch(BASE + '/api/v1/approval-policies/resolve?' + q.toString(), { headers: { Accept: 'application/json' } })
    .then(r => r.ok ? r.json() : null)
    .then(p => {
      if (!p || !p.required) return
      if (p.callerMayBypass) {
        setMode('privileged')
        // Stated, never silent. The frictionless path is the audited one — an
        // admin who bypasses review should know they did, at the moment they do
        // it, not discover it later in an audit row.
        // "Bypasses review" rather than "privileged write": it says what
        // happens instead of naming an internal concept, and it is the same
        // wording the audit log uses for the row this produces.
        say('is-warning', '<strong>Bypasses review.</strong> Edits here need approval, '
          + 'but your role writes directly. The audit log records it.')
      } else {
        setMode('propose')
        if (btn) btn.textContent = 'Propose change'
        // Says what happens and what does NOT. "changing intent" was orbital's
        // own vocabulary leaking into a sentence aimed at whoever is editing.
        say('is-info', '<strong>Needs approval.</strong> Your edit goes for review — '
          + 'nothing changes until someone approves it.')
      }
    })
    .catch(() => {})

  // Ask about the whole item, not just its root node. An edit to an owned child
  // is recorded against the CHILD's orbId — a maintenance edit lands as
  // `<ns>:server-maintenance-<serial>` — so the root orbId alone reports
  // "nothing in flight" while a proposal for this very modal sits open.
  //
  // Two sources, unioned: what the page rendered (data-related-orb-ids, the
  // children that EXIST) and what this modal can edit (targets, which include a
  // child that does not exist yet and whose orbId a proposal would create).
  const scope = new Set(subtreeOrbIds(reloadOrbId))
  for (const t of targets || []) if (t.orbId) scope.add(t.orbId)

  activeChangeRequestsFor([...scope])
    .then(j => {
      if (!j || !j.total) return
      const cr = (j.items || [])[0]
      const link = cr ? ' <a href="' + BASE + '/change-requests/' + encodeURIComponent(cr.id) + '">Review it</a>.' : ''
      const pending = document.createElement('div')
      pending.className = 'notification is-warning is-light py-2 is-size-7'
      pending.setAttribute('data-testid', 'pending-change-notice')
      pending.innerHTML = '<strong>' + j.total + ' change' + (j.total === 1 ? '' : 's')
        + ' already proposed</strong> for this item.' + link
      addNotice(pending)
    })
}

// changesetTitle names a proposal by WHAT IT CHANGES, not just what it touches.
//
// It used to be `'Update ' + orbId`, which named the entity. Every edit to the
// same server produced the same title, so the queue showed rows that were
// indistinguishable in every visible column — two different changes and two
// identical proposals looked exactly alike, and only the second is a mistake
// worth noticing.
//
// Deriving from the changeset means two rows read the same only when the
// changes really are the same. Owned children are qualified by their type,
// because `enabled` alone does not say enabled on what.
function changesetTitle(rootOrbId, changeset) {
  const entity = String(rootOrbId).replace(/^[^:]+:/, '')
  const items = (changeset && changeset.changes) || []

  const fields = []
  for (const item of items) {
    // The root's own fields need no qualifier; a child's do.
    const prefix = item.orbId === rootOrbId ? '' : (item.type ? item.type + '.' : '')
    for (const name of Object.keys(item.set || {})) {
      fields.push({ label: prefix + name, value: item.set[name], cleared: false })
    }
    for (const name of (item.clear || [])) {
      fields.push({ label: prefix + name, value: null, cleared: true })
    }
  }

  if (fields.length === 0) return 'Update ' + entity      // create-only, or nothing resolvable
  if (fields.length > 1) return entity + ' · ' + fields.length + ' fields'

  const f = fields[0]
  if (f.cleared) return entity + ' · clear ' + f.label
  // A value is shown only when it stays readable — same rule as the field
  // marks. A title is a label, not a payload dump.
  if (f.value !== null && typeof f.value !== 'object') {
    const v = String(f.value)
    if (v.length <= 40) return entity + ' · ' + f.label + ' → ' + v
  }
  return entity + ' · ' + f.label
}

// proposeChange opens a change request from the edit the user just made and
// leaves the user where they were.
//
// It used to navigate to the new request's review page, on the reasoning that a
// proposal is the start of a review rather than an end state. That was wrong in
// practice: proposing is a save, and a save that teleports you out of the thing
// you were editing breaks the flow — especially when the next edit is on the
// same server. It also made the gated path behave differently from the ordinary
// one for no reason the user asked for.
//
// Reloading the fragment instead is what surfaces the answer: the entity's
// pending-change banner appears ("1 change in review for this item or something
// it owns — …"), naming the request and linking to it. The confirmation and the
// way through are the same element, and the user chooses when to leave.
async function proposeChange({
  namespace, rootTarget, rootOrbId, rootScalars, rootRemove,
  changes, wrappersNeeded, foldedOrbIds, showError, reloadFn,
}) {
  const changeset = buildChangeset({
    namespace, rootTarget, rootOrbId, rootScalars, rootRemove,
    changes, wrappersNeeded, foldedOrbIds,
  })
  if (!changeset.changes.length) {
    showError('Nothing to propose — no fields changed.')
    return false
  }

  const title = changesetTitle(rootOrbId, changeset)
  let resp
  try {
    resp = await fetch(BASE + '/api/v1/change-requests', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title, namespace, changes: changeset.changes }),
    })
  } catch (_) {
    showError('Request failed — check your connection and try again.')
    return false
  }

  const body = await resp.json().catch(() => ({}))
  if (!resp.ok) {
    // The validator reports EVERY problem at once, so show them all rather than
    // the first — the whole point is one round-trip's worth of fixes.
    if (body.problems && body.problems.length) {
      showError(body.problems.map(p => (p.field ? p.field + ': ' : '') + p.message).join(' · '))
    } else {
      showError(body.error || `Could not open a change request (${resp.status}).`)
    }
    return false
  }

  // Same as an ordinary save: reload the fragment so the page reflects reality.
  // Here that reality is "a change is in review", which the banner states.
  if (reloadFn) await reloadFn(rootOrbId)
  return true
}

// initConfigItemEditor wires the JSON editor + submit button + targets into a
// working edit modal. Call this once per modal open (the cluster/server/dc
// page-specific code is responsible for opening the modal and wiring this in).
//
// Returns the submit handler so the page can attach it to the Save button.
// Captures snapshots at the moment of call, so the page should call this
// AFTER editor.set(...) has been called with the initial state.
export function initConfigItemEditor({
  modal,            // the modal DOM element (used for currentUser, reload target)
  editor,           // JSONEditor instance
  initialState,     // parsed object — what the editor was populated with
  targets,          // [{ path, kind, orbId, fields, payloadField, parentInverseField?, parentOrbId?, parentWrapper? }]
  reloadOrbId,      // orbId to reload the fragment for (the root parent)
  reloadFn,         // function(orbId) → Promise — page-specific fragment reload
  showError,        // function(msg) — render error to the user
  clearError,       // function() — clear previous error
  submitBtnId,      // optional: id of the Save button, so this module can relabel it
}) {
  // How this edit will be written, resolved ONCE here.
  //
  //   save        — ordinary mutation, exactly as before this feature existed
  //   propose     — the class needs review; Save opens a change request instead
  //   privileged  — the class needs review, but this caller may bypass it
  //
  // Resolved in the module, not per page, because four copies of "am I gated?"
  // is the drift pattern this codebase has been bitten by before. Pages say
  // which button to label; they never ask the policy themselves.
  let mode = 'save'
  const namespaceOf = (orbId) => String(orbId || '').split(':')[0]
  const rootKind = (targets.find(t => t.path.length === 0) || {}).kind

  applyGateState({ modal, submitBtnId, reloadOrbId, rootKind, targets, namespaceOf, setMode: (m) => { mode = m } })

  // Snapshot every target's subtree at this moment. Each snapshot is a JSON
  // string of the field map (or null if the subtree didn't exist).
  const snapshots = new Map()
  for (const t of targets) {
    const sub = t.path.length === 0
      ? rootWithoutSubtrees(initialState, targets)
      : getByPath(initialState, t.path)
    snapshots.set(snapshotKey(t), JSON.stringify(sub ?? null))
  }

  // Returns the click handler the page attaches to its Save button.
  return async function onSubmit() {
    clearError()

    let current
    try { current = JSON.parse(editor.get().text) } catch (_) {
      showError('Invalid JSON — fix the syntax and try again.')
      return false
    }

    const currentUser = modal.dataset.currentUser || ''
    const now = new Date().toISOString()

    // For each target, determine: did this entity change? did it previously exist?
    const changes = []
    for (const t of targets) {
      const currentSub = t.path.length === 0
        ? rootWithoutSubtrees(current, targets)
        : getByPath(current, t.path)
      const before = JSON.parse(snapshots.get(snapshotKey(t)))
      const changed = JSON.stringify(currentSub ?? null) !== JSON.stringify(before)
      const existed = before != null
      if (changed) changes.push({ target: t, currentSub, existed, before })
    }

    if (changes.length === 0) {
      // Nothing changed — close the modal silently.
      return true
    }

    // Fields declared in the schema as `String # json` round-trip through
    // the editor as nested objects (the page handler parses them for nice
    // display); on submit they must be re-stringified or DGraph rejects with
    // "cannot use as String". The set of such fields per target type is
    // emitted by configitems.BuildEditTargets as JSONStringFields.
    // Build the `set` map (for update) or `input` map (for add) of scalar field
    // values from a target's currentSub — the JSON editor's post-submit shape.
    // Empty values are skipped; see removePayload for how they're cleared.
    // Metadata for entities created as a NESTED subtree inside an update{Root}
    // `set` (first-time wrapper + children). The GraphQL proxy stamps
    // createdBy/At + updatedBy/At + version on top-level update `set` maps and
    // top-level add inputs, but does NOT recurse into nested objects within a
    // set — so these nested nodes must carry their own. Do NOT remove: top-level
    // edits/adds are server-stamped (see graphql.go), nested creates are not.
    const configItemDefaults = () => ({
      version: 1,
      createdBy: currentUser, createdAt: now,
      updatedBy: currentUser, updatedAt: now,
    })

    // First-time-create of a child entity requires its wrapper (e.g.
    // ClusterBackup) to exist before the child can link to it. Prior design
    // dispatched wrapper-link + child-add in parallel, which raced DGraph
    // into auto-creating a partial wrapper node (fails NotEmpty on
    // namespace). Fix: fold the wrapper + all its new children into ONE
    // nested update{Root} mutation. DGraph's nested-write semantics create
    // the whole subtree atomically. Editing existing children is unaffected
    // — they still get targeted update{Kind} mutations.
    //
    // A wrapper "needs creation" if NO sibling under the same parentOrbId
    // was present in the pre-submit snapshot.
    const wrappersNeeded = new Map() // wrapper.orbId → wrapper
    for (const ch of changes) {
      if (ch.existed) continue
      const w = ch.target.parentWrapper
      if (!w) continue
      const siblingExisted = targets.some(other =>
        other !== ch.target &&
        other.parentOrbId === ch.target.parentOrbId &&
        JSON.parse(snapshots.get(snapshotKey(other))) != null
      )
      if (!siblingExisted && !wrappersNeeded.has(w.orbId)) {
        wrappersNeeded.set(w.orbId, w)
      }
    }

    // Compose the root's update set: root scalar edits (if the root itself
    // changed) plus one nested subtree per wrapper that needs creation.
    // Whether or not this rootSet ends up non-empty determines if we emit
    // an update{Root} mutation at all.
    const rootTarget = targets.find(t => t.path.length === 0)
    let rootSet = null
    const rootChange = changes.find(ch => ch.target.path.length === 0)
    if (rootChange) {
      rootSet = { ...scalarPayload(rootChange.target, rootChange.currentSub || {}) }
    }
    // Children folded into rootSet — tracked so the per-target loop skips
    // them (otherwise we'd double-emit them as separate add mutations).
    const foldedOrbIds = new Set()
    for (const w of wrappersNeeded.values()) {
      if (rootSet === null) {
        rootSet = {}
      }
      // Wrapper metadata. `@hasInverse` on wrapper.cluster fills back to root
      // automatically when root sets `[w.parentField] = wrapper`, so we don't
      // need to set the wrapper's back-reference here.
      const wrapperNode = {
        orbId: w.orbId, name: w.name, namespace: w.namespace,
        ...configItemDefaults(),
      }
      // Fold every new child of this wrapper as a nested object. Field name
      // on the wrapper for each child = last segment of the child's path
      // (e.g. path=["backup","velero"] → "velero"). DGraph @hasInverse fills
      // the child's back-reference automatically.
      for (const ch of changes) {
        if (ch.existed) continue
        if (ch.target.parentOrbId !== w.orbId) continue
        const t = ch.target
        const childField = t.path[t.path.length - 1]
        wrapperNode[childField] = {
          orbId: t.orbId, name: deriveName(t),
          namespace: t.namespace || '',
          ...configItemDefaults(),
          ...scalarPayload(t, ch.currentSub || {}),
        }
        foldedOrbIds.add(t.orbId)
      }
      rootSet[w.parentField] = wrapperNode
    }

    // Build the call list. Root subtree (if any) plus per-target mutations
    // for everything not folded above. Everything is independent at this
    // point — no cross-call ordering dependencies — so parallel is safe.
    const calls = []
    if (rootSet !== null) {
      calls.push(buildUpdateCall({
        kind: rootTarget.kind, orbId: reloadOrbId, set: rootSet, payloadField: rootTarget.payloadField,
        remove: rootChange ? removePayload(rootTarget, rootChange.before, rootChange.currentSub) : {},
      }))
    }
    for (const ch of changes) {
      const t = ch.target
      if (t.path.length === 0) continue     // root already handled
      if (foldedOrbIds.has(t.orbId)) continue // subtree-folded into root update
      const sub = ch.currentSub || {}
      if (ch.existed) {
        // EDIT — canonical update{Kind} triggers the diff renderer.
        calls.push(buildUpdateCall({
          kind: t.kind, orbId: t.orbId, payloadField: t.payloadField,
          set: { ...scalarPayload(t, sub) },
          remove: removePayload(t, ch.before, sub),
        }))
      } else {
        // CREATE under an already-existing wrapper (sibling exists). Safe
        // to parallelize — the wrapper is in DGraph, this is just a link.
        const input = {
          orbId: t.orbId, name: deriveName(t),
          namespace: t.namespace || '',
          ...configItemDefaults(),
          ...scalarPayload(t, sub),
        }
        if (t.parentInverseField && t.parentOrbId) {
          input[t.parentInverseField] = { orbId: t.parentOrbId }
        }
        calls.push(buildAddCall({ kind: t.kind, input, payloadField: t.payloadField }))
      }
    }

    // The proposal path. Same computed edit, different destination: a change
    // request rather than the graph. Nothing is written here — that is the
    // point of the gate.
    if (mode === 'propose') {
      return await proposeChange({
        namespace: namespaceOf(reloadOrbId),
        rootTarget, rootOrbId: reloadOrbId,
        rootScalars: rootSet, rootRemove: rootChange ? removePayload(rootTarget, rootChange.before, rootChange.currentSub) : {},
        changes, wrappersNeeded, foldedOrbIds,
        showError, reloadFn,
      })
    }

    // Dispatch in parallel. Each becomes its own audit row.
    let responses
    try {
      responses = await Promise.all(calls.map(c =>
        fetch(BASE + '/graphql', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(c),
        })
      ))
    } catch (_) {
      showError('Request failed — check your connection and try again.')
      return false
    }

    // Fail-fast on the first error.
    for (const r of responses) {
      if (!r.ok) {
        if (r.status === 409) {
          const body = await r.json().catch(() => ({}))
          showError(body.error || 'Conflict — please reload and try again.')
        } else if (r.status === 403) {
          const body = await r.json().catch(() => ({}))
          if (body.code === 'APPROVAL_REQUIRED') {
            // The policy changed between opening this modal and saving, so the
            // resolved mode was stale. A refusal must never dead-end when the
            // remedy is one click away and we are holding the exact edit it
            // needs — so offer it rather than reporting a 403.
            if (confirm((body.error || 'This change needs approval.') + '\n\nOpen a change request with this edit?')) {
              return await proposeChange({
                namespace: namespaceOf(reloadOrbId),
                rootTarget, rootOrbId: reloadOrbId,
                rootScalars: rootSet, rootRemove: rootChange ? removePayload(rootTarget, rootChange.before, rootChange.currentSub) : {},
                changes, wrappersNeeded, foldedOrbIds,
                showError, reloadFn,
              })
            }
            showError(body.error || 'This change needs approval.')
          } else {
            showError(body.error || 'You do not have permission to make this change.')
          }
        } else {
          showError(`Server error (${r.status}) — try again.`)
        }
        return false
      }
      const body = await r.json()
      const errMsg = window.gqlErrorMessage ? window.gqlErrorMessage(body) : null
      if (errMsg) { showError(errMsg); return false }
    }

    // All good. Trigger the fragment reload via the page-provided helper.
    if (reloadFn) await reloadFn(reloadOrbId)
    return true
  }
}

function snapshotKey(target) {
  return target.kind + '|' + target.path.join('.')
}

// deriveName returns a default `name` for a new entity. The orbId convention
// is `<namespace>:<id>` so we derive name from the trailing segment.
function deriveName(target) {
  const orbId = target.orbId || ''
  const idx = orbId.indexOf(':')
  return idx >= 0 ? orbId.slice(idx + 1) : orbId
}

// Expose for non-ES-module callers (orbital.js, orb.js) that use the window bridge.
window.initConfigItemEditor = initConfigItemEditor

// safeDomId re-export for any consumer that needs to look up DOM ids.
export { safeDomId }
