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

import { BASE, safeDomId } from './shared.js'

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
function buildUpdateCall({ kind, orbId, set, payloadField }) {
  return {
    query: `mutation Update${kind}($orbId: String!, $set: ${kind}Patch!) {
      update${kind}(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { ${payloadField} { orbId } }
    }`,
    variables: { orbId, set },
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
}) {
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
      if (changed) changes.push({ target: t, currentSub, existed })
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
    const valueForMutation = (t, f, v) => {
      const jsonStrFields = new Set(t.jsonStringFields || [])
      if (!jsonStrFields.has(f)) return v
      return typeof v === 'string' ? v : JSON.stringify(v)
    }
    // Build the `set` map (for update) or `input` map (for add) of scalar field
    // values from a target's currentSub — the JSON editor's post-submit shape.
    const scalarPayload = (t, sub) => {
      const out = {}
      for (const f of t.fields) if (f in sub) out[f] = valueForMutation(t, f, sub[f])
      return out
    }
    // Standard ConfigItem interface fields orbital's ent schema requires on
    // every insert. `version: 1` is what a fresh @id gets; the GraphQL proxy
    // auto-bumps on subsequent updates.
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
      rootSet = { updatedBy: currentUser, updatedAt: now, ...scalarPayload(rootChange.target, rootChange.currentSub || {}) }
    }
    // Children folded into rootSet — tracked so the per-target loop skips
    // them (otherwise we'd double-emit them as separate add mutations).
    const foldedOrbIds = new Set()
    for (const w of wrappersNeeded.values()) {
      if (rootSet === null) {
        rootSet = { updatedBy: currentUser, updatedAt: now }
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
          set: { updatedBy: currentUser, updatedAt: now, ...scalarPayload(t, sub) },
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
