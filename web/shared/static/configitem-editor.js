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
// changedOnly drops fields whose value is what it already was.
//
// The mutation path sends an entity's WHOLE scalar payload on every update —
// harmless for DGraph, since writing a field its current value is a no-op. A
// changeset is not a mutation: it is a record of what someone proposed, it is
// read by a reviewer, and merging it WRITES every field it names. Carrying six
// fields for a one-field edit made the request claim authority over five the
// author never touched, and made every count derived from it read six.
//
// Compared against the SNAPSHOT taken when the modal opened — not against
// current intent. Only the editor knows which fields this person actually
// touched; the server can only ask "does this differ from intent now?", which
// is a different question with a worse answer: if a colleague edited a field
// while this modal was open, comparing against current intent would KEEP the
// stale value here and silently revert their write on merge.
function changedOnly(next, prev) {
  const out = {}
  for (const k of Object.keys(next || {})) {
    if (JSON.stringify(next[k]) !== JSON.stringify((prev || {})[k])) out[k] = next[k]
  }
  return out
}

// assertBefore picks the prior values for the fields an item actually writes,
// so the request can be CONDITIONAL: orbital refuses it at creation if one of
// them has already moved, and refuses the merge if one moves during review.
//
// The editor is the one place that knows what the author was looking at. The
// server cannot infer it — Create reads state when it is called, which may be
// minutes after the modal was opened, so an edit landing meanwhile would
// silently become the recorded ancestor and the reviewer would diff against a
// state the author never saw.
//
// PRIMITIVES ONLY, deliberately. Edge references are objects ({orbId: …}) and
// live in a different part of the snapshot, so asserting them would compare two
// shapes rather than two values and could refuse a legitimate proposal. Scalars
// are also where a silent overwrite actually costs something.
function assertBefore(set, clear, before) {
  const out = {}
  const take = (k) => {
    if (!(k in before)) return
    const v = before[k]
    if (v !== null && typeof v === 'object') return
    out[k] = v
  }
  for (const k of Object.keys(set || {})) take(k)
  for (const k of (clear || [])) take(k)
  return out
}

export function buildChangeset({
  namespace, rootTarget, rootOrbId, rootScalars, rootBefore, rootRemove,
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
  // Narrowed before the wrapper references go in: a new wrapper is a real
  // change and has no prior value to compare against.
  const rootSet = changedOnly(
    withoutStamped(rootScalars || {}),
    rootTarget && rootBefore ? withoutStamped(scalarPayload(rootTarget, rootBefore)) : {},
  )
  for (const w of wrappersNeeded.values()) {
    rootSet[w.parentField] = { orbId: w.orbId }
  }
  const rootClear = Object.keys(rootRemove || {}).filter(f => !STAMPED_FIELDS.has(f))
  if (Object.keys(rootSet).length > 0 || rootClear.length > 0) {
    const rootPrior = rootTarget && rootBefore
      ? withoutStamped(scalarPayload(rootTarget, rootBefore))
      : {}
    items.push({
      orbId: rootOrbId,
      type: rootTarget ? rootTarget.kind : undefined,
      op: 'update',
      set: rootSet,
      clear: rootClear,
      before: assertBefore(rootSet, rootClear, rootPrior),
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
      // `existed` is the branch that matters: a create legitimately needs every
      // field, so only updates are narrowed.
      const set = changedOnly(
        withoutStamped(scalarPayload(t, sub)),
        withoutStamped(scalarPayload(t, ch.before || {})),
      )
      // `remove` carries the field's PRIOR VALUE because DGraph needs it to
      // clear; the store-neutral form needs only the name. Lossless: merge
      // re-reads the current value when it builds the mutation, and if that
      // value moved, the request is stale and will not merge — so the prior
      // value can never be needed and wrong at the same time.
      const clear = Object.keys(removePayload(t, ch.before, sub)).filter(f => !STAMPED_FIELDS.has(f))
      if (Object.keys(set).length === 0 && clear.length === 0) continue
      const prior = withoutStamped(scalarPayload(t, ch.before || {}))
      items.push({
        orbId: t.orbId, type: t.kind, op: 'update', set, clear,
        before: assertBefore(set, clear, prior),
      })
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
function applyGateState({ modal, submitBtnId, reloadOrbId, rootKind, targets, namespaceOf, setMode, proposeNow }) {
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
  // `atEnd` orders the two notices deterministically. They are inserted at
  // different moments — the gate notice synchronously at init, the pending line
  // when its fetch resolves — so a plain prepend would order them by network
  // timing. The gate notice explains what the button does and comes first;
  // "already proposed" follows it, and both stay above the action row.
  function addNotice(el, { atEnd = false } = {}) {
    const foot = btn && btn.closest('.modal-card')?.querySelector('.modal-card-foot')
    if (foot) {
      el.classList.add('js-gate-notice')
      foot.classList.add('has-gate-notice')
      foot.insertBefore(el, atEnd ? btn.closest('.buttons') : foot.firstChild)
    } else if (btn && btn.parentNode) {
      btn.parentNode.insertBefore(el, btn)
    }
  }

  // addTitleField gives the proposer the one thing a diff cannot supply.
  //
  // Prefilled with the entity name and selected on first focus, so replacing it
  // is one gesture and accepting it is none. Left untouched (or cleared), the
  // submit path falls back to the fully derived label — entity plus field count
  // — rather than storing the bare entity name, which is what made the review
  // page look like it had a title when it did not.
  //
  // Built with DOM APIs, not an HTML string: the value is user text on the way
  // back in, and there is no esc() in this module to get it wrong with.
  function addTitleField(mode) {
    if (!btn) return null
    const card = btn.closest('.modal-card')
    if (card && card.querySelector('#cr-propose-title')) return null

    // Literally the same function the submit path falls back to, so the
    // prefilled value and the stored-when-blank value cannot drift apart.
    const entity = fallbackTitle(reloadOrbId)

    const wrap = document.createElement('div')
    wrap.className = 'field mb-2'

    const label = document.createElement('label')
    label.className = 'label is-size-7 mb-1'
    label.setAttribute('for', 'cr-propose-title')
    label.textContent = 'Change request title'
    wrap.appendChild(label)

    const control = document.createElement('div')
    control.className = 'control'
    const input = document.createElement('input')
    input.className = 'input is-small'
    input.id = 'cr-propose-title'
    input.type = 'text'
    // Matches the API bound, which matches the varchar(255) column.
    input.maxLength = 255
    input.value = entity
    control.appendChild(input)
    wrap.appendChild(control)

    // Only the privileged footer needs this: there, one of the two buttons
    // ignores the field, and nothing else on screen says so. In the gated
    // footer Propose is the only way out, so a help line would be noise.
    if (mode === 'privileged') {
      const help = document.createElement('p')
      help.className = 'help'
      help.textContent = 'Used when proposing. Ignored by Save directly.'
      wrap.appendChild(help)
    }

    addNotice(wrap, { atEnd: true })
    input.addEventListener('focus', () => input.select(), { once: true })
    return input
  }

  // addProposeButton puts a second action next to Save for a caller who may
  // bypass, so both destinations are one click away.
  //
  // Owned here rather than in each page's modal wiring: four copies of "does
  // this footer need a propose button?" is exactly the drift this module exists
  // to prevent, and the answer depends on a policy lookup only this function
  // makes. Pages keep wiring their own Save button and know nothing about it.
  //
  // The button is created only in the privileged branch, so a gated caller
  // never gets a control that would 403, and an ungated one never sees a footer
  // that differs from the pre-approval-engine UI by so much as a pixel.
  function addProposeButton() {
    if (!btn || !btn.parentNode || !proposeNow) return null
    if (btn.parentNode.querySelector('[data-testid="propose-change"]')) return null

    const propose = document.createElement('button')
    propose.className = 'button is-success'
    propose.type = 'button'
    propose.setAttribute('data-testid', 'propose-change')
    propose.textContent = 'Propose change'
    btn.parentNode.insertBefore(propose, btn)

    propose.addEventListener('click', async () => {
      propose.classList.add('is-loading')
      propose.disabled = true
      // Disable the sibling too: the two buttons submit the same edit to
      // different destinations, and a second click mid-flight would either
      // write the change it is currently proposing or propose it twice.
      btn.disabled = true
      try {
        if (await proposeNow()) {
          modal.classList.remove('is-active')
          document.documentElement.style.overflow = ''
        }
      } finally {
        propose.classList.remove('is-loading')
        propose.disabled = false
        btn.disabled = false
      }
    })
    return propose
  }

  const notice = document.createElement('div')
  notice.className = 'is-size-7'
  notice.style.display = 'none'
  addNotice(notice)

  // Notices are TEXT, not boxes. Colour in this footer belongs to the actions:
  // once Propose, Save directly and Cancel each carry a hue, a tinted panel
  // above them is a fourth coloured rectangle competing with three buttons, and
  // the eye has nowhere to land. An icon carries the same urgency a filled
  // panel did, in one glyph.
  const say = (icon, html) => {
    notice.className = 'is-size-7 js-gate-notice'
    notice.innerHTML = '<span class="icon-text"><span class="icon ' + icon.cls
      + '"><i class="' + icon.i + '"></i></span><span>' + html + '</span></span>'
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
        // A bypass-capable caller gets BOTH paths, and review is the primary
        // one. Being allowed to skip review is not the same as wanting to: the
        // people holding the bypass role are also the ones most likely to want
        // a second pair of eyes on a production edit, and until this button
        // existed their only route to a change request was to lose the role.
        //
        // Propose keeps the green primary treatment every other Save in the app
        // has, so the habitual click is the reviewed one. Direct write is solid
        // amber: caution, distinct from both the green primary and the plain
        // Cancel beside it.
        //
        // NO notice here, deliberately. A paragraph explaining that this role
        // may write directly said what the two buttons already say — the word
        // "directly", set against "Propose change", IS the statement that this
        // one skips review. The prose was a third thing to read before a choice
        // the labels had already made obvious. The one fact the labels cannot
        // carry — that the bypass is recorded — rides on the button's tooltip.
        addProposeButton()
        addTitleField('privileged')
        if (btn) {
          btn.textContent = 'Save directly'
          btn.classList.remove('is-success')
          btn.classList.add('is-warning')
          btn.title = 'Writes straight to intent, skipping review. Recorded in the audit log as a privileged write.'
        }
      } else {
        setMode('propose')
        addTitleField('propose')
        if (btn) btn.textContent = 'Propose change'
        // Says what happens and what does NOT. "changing intent" was orbital's
        // own vocabulary leaking into a sentence aimed at whoever is editing.
        say({ cls: 'has-text-info', i: 'fa-solid fa-circle-info' },
          '<strong>Needs approval.</strong> Your edit goes for review — '
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
      pending.className = 'is-size-7'
      pending.setAttribute('data-testid', 'pending-change-notice')
      // Amber survives as the ICON, not as a panel. This line has to be noticed
      // — it is what stops someone retyping an edit a colleague already
      // proposed — but a filled amber box next to three coloured buttons made
      // the footer read as four competing alerts.
      pending.innerHTML = '<span class="icon-text"><span class="icon has-text-warning">'
        + '<i class="fa-solid fa-triangle-exclamation"></i></span><span>'
        + '<strong>' + j.total + ' change' + (j.total === 1 ? '' : 's')
        + ' already proposed</strong> for this item.' + link + '</span></span>'
      addNotice(pending, { atEnd: true })
    })
}

// fallbackTitle names the thing being changed and nothing else — it is what a
// proposal is called when nobody named it.
//
// It used to append "· N fields", on the reasoning that a count makes a
// generated value read as a label rather than as a human title. The queue then
// gained a dedicated Change column carrying exactly that count, so the tail
// became the same fact twice on one row. It also made accepting the prefill
// differ from leaving the box empty, which nobody would predict.
//
// It also used to say "Update <entity>" when it could not count any fields —
// i.e. for a create-only changeset, the least update-like case there is. One
// form for every branch instead.
//
// This is a FALLBACK, not a design: the title is the proposer's to write, and
// addTitleField prefills this same string so the two can never disagree.
function fallbackTitle(rootOrbId) {
  return String(rootOrbId).replace(/^[^:]+:/, '')
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
  namespace, rootTarget, rootOrbId, rootScalars, rootBefore, rootRemove,
  changes, wrappersNeeded, foldedOrbIds, showError, reloadFn,
}) {
  const changeset = buildChangeset({
    namespace, rootTarget, rootOrbId, rootScalars, rootBefore, rootRemove,
    changes, wrappersNeeded, foldedOrbIds,
  })
  if (!changeset.changes.length) {
    showError('Nothing to propose — no fields changed.')
    return false
  }

  // The proposer's title wins; the entity name is the fallback. Accepting the
  // prefill and clearing the box produce the SAME stored title, because the
  // prefill IS the fallback — there is no third string that appears only when
  // you leave the field alone.
  const titleInput = document.getElementById('cr-propose-title')
  const typed = titleInput ? titleInput.value.trim() : ''
  const title = typed || fallbackTitle(rootOrbId)
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

  // Forward reference. applyGateState resolves the policy asynchronously and may
  // add a Propose button that submits the very handler defined below it. The
  // indirection is read at click time, never at wiring time, so the order is
  // safe — but it must be a closure, not a direct reference to `submit`.
  let submit = null
  applyGateState({
    modal, submitBtnId, reloadOrbId, rootKind, targets, namespaceOf,
    setMode: (m) => { mode = m },
    proposeNow: () => submit ? submit({ forcePropose: true }) : false,
  })

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
  //
  // `forcePropose` is how the Propose button reuses it: identical diff, identical
  // validation, different destination. Recomputing the changeset in a second
  // handler would be the one place the two paths could disagree about what the
  // user actually edited.
  submit = async function onSubmit(opts) {
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
      // Save closes silently: there is nothing to persist, and reporting that
      // as a problem would be noise.
      //
      // Propose must NOT. A modal that closes on a button labelled "Propose
      // change" reads as "a request was created" — and the reader would then go
      // looking in the queue for one that does not exist. The two buttons owe
      // different answers to the same no-op.
      if (mode === 'propose' || (opts && opts.forcePropose)) {
        showError('Nothing to propose — no fields changed.')
        return false
      }
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
    if (mode === 'propose' || (opts && opts.forcePropose)) {
      return await proposeChange({
        namespace: namespaceOf(reloadOrbId),
        rootTarget, rootOrbId: reloadOrbId,
        rootScalars: rootSet, rootBefore: rootChange ? rootChange.before : null,
        rootRemove: rootChange ? removePayload(rootTarget, rootChange.before, rootChange.currentSub) : {},
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
                rootScalars: rootSet, rootBefore: rootChange ? rootChange.before : null,
        rootRemove: rootChange ? removePayload(rootTarget, rootChange.before, rootChange.currentSub) : {},
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

  return submit
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
