# Divergence Resolution Reference

> **Audience:** anyone touching orbital's side of divergence — ingestion, resolution actions, the orbital→cb-bundler contract, the audit log for resolution events, or the cb-manifest projection.
>
> **Distinct from** [`DIVERGENCE-INTAKE.md`](./DIVERGENCE-INTAKE.md), which documents the orb-side API that producers (cb-controller, etc.) call to **submit** divergence reports. This doc starts where that one ends: orb has published a snapshot, orbital has ingested it, now what?

Read this before: `internal/handler/divergence.go`, `internal/divergenceingest/`, the `divergence_resolutions` ent table, the `/api/v1/divergence/resolutions/*` endpoints, or the cb-bundler integration around takeover/omission lists.

## Actor model

Two human roles. Ownership in K8s `managedFields` should reflect **who has authority over the field**, not just who touched it last.

| Role | Where they act | Why they act |
|---|---|---|
| **Local admin** | Directly at the edge (iDRAC, BMC, K8s, etc.) | Incident responder. Makes a temporary override to unblock something *now*. Their contract: also notify the cloud admin so the long-term decision gets made. |
| **Cloud admin** | Orbital UI (`/divergence-reports`) | Policy maker. Reviews local-admin overrides and decides each field's long-term disposition: accept the override into intent, force a rollback, or knowingly ignore. |

The local admin is not adversarial — they're the team running the edge, and they have legitimate reasons to make temporary changes. The divergence loop exists so those changes get reviewed and resolved instead of silently drifting.

## K8s SSA primitives this doc relies on

The mechanics in 5 bullets. Skim if you already know SSA.

- An `apply` operation with `manager: M` adds `M` to the set of managers in `managedFields` for every field in the apply config. Existing managers are kept (co-ownership). One field can have many managers.
- If two managers claim a field with **different values**, that's a conflict. K8s surfaces it; the caller resolves via `force: true` (which evicts other managers from that field).
- If two managers claim a field with the **same value**, no conflict — both are recorded as co-owners.
- `force: true` on an apply makes the applying manager the **sole** manager of every field in the config. Other managers are evicted.
- Omitting a field from a subsequent apply **releases** that manager's claim on it. If `M` previously included field F and the next apply doesn't, F is no longer in M's manager entry. If only one other manager remained (e.g., `local:admin`), it becomes the sole owner.

The last bullet is the trick that makes "ignore" work cleanly.

## The three actions

Each row of `divergence_entries` ends in one of three states. The action determines what changes at orbital, what changes at the edge, and what the audit log records.

| Action | Intent change in orbital DGraph | Edge value after propagation | Deployment-layer responsibility | Final manager state | Lifecycle |
|---|---|---|---|---|---|
| **Accept** | intent ← override value | unchanged (was already at override) | enforce intent: take ownership of the field with the new intent value | cb-controller sole owner; `local:admin` evicted | one-shot: orbital observes propagation via snapshot delta → marks `propagated_at` |
| **Reject** | unchanged | reverts to original intent | enforce intent: take ownership of the field, reset edge to intent value | cb-controller sole owner; `local:admin` evicted | one-shot: orbital observes propagation via snapshot delta → marks `propagated_at` |
| **Ignore** | unchanged (stays stale vs. edge — intentionally) | unchanged | DISengage: exclude the field from any apply config | `local:admin` sole owner; cb-controller never co-owns | persistent: re-applied on every bundle build until the resolution row is deleted |

Audit event operation names: `acceptDivergence`, `rejectDivergence`, `ignoreDivergence`. Always `verbNoun` per [AUDIT.md](./AUDIT.md). The `divergence_resolutions.action` column stores the bare verb (`accept`/`reject`/`ignore`) — that's internal state, not an audit operation name.

## The 2×2 framing

The three actions sit on a 2×2 over two independent questions:

|  | **Cloud takes ownership** | **Cloud releases / declines** |
|---|---|---|
| **Cloud updates intent** | `accept` | (unsupported — see note) |
| **Cloud keeps intent** | `reject` | `ignore` |

The empty cell — "update intent but don't take ownership" — has no use case today. It would mean "I agree this is the right value, but the local admin can keep changing it." If a real case emerges, a fourth action could be added without disturbing the existing three.

## Ignore is the escape hatch

Most overrides should be **accepted** (cloud absorbs the value, takes over the field) or **rejected** (cloud reverts the value, takes over the field). Both produce a stable, single-owner end state with no further reporting.

**`ignore` exists for cases where neither resolution is appropriate:**

- Cloud doesn't have the context to decide *right now* and wants to defer indefinitely without leaving a pending entry around.
- The override is intentionally permanent and per-server, not something to roll into intent.
- The local team is the source of truth for this field on this server, and the cloud admin formally cedes control.

Treat `ignore` as a deliberate operational choice, not a "skip this one" shortcut. The UI may want to require justification or a separate confirmation to discourage casual use.

## What "loop closure" means per action

**Accept / Reject:**

1. Admin clicks Accept (or Reject) and submits.
2. For Accept: `dispatchAcceptMutation` updates DGraph intent. For Reject: no DGraph change.
3. `divergence_resolutions` row written with `action` and `propagated_at=NULL`.
4. Operator triggers an export + publish. Orbital calls `POST /bundle` on the deployment layer (cb-bundler today).
5. The deployment layer queries orbital for divergences whose resolution is unpropagated (`GET /api/v1/divergences?action=accept&action=reject&propagated=false`) and enforces intent for each: in K8s terms, emit a `spec.takeover[]` entry so cb-controller force-applies the current intent value, evicting `local:*` from `managedFields`.
6. Orbital pushes the OCI artifact. (No callback from the deployment layer — orbital does NOT learn "propagated" from a bundler response. See next step.)
7. Orb imports the bundle, dispatches cb-manifest to cb-controller. cb-controller reconciles; `local:admin` is evicted; cb-controller becomes sole manager.
8. cb-controller's divergence reporter on the next tick sees no `local:*` on this field → no divergence emitted.
9. Next orb snapshot omits the entry. Orbital ingester's stale sweep deletes the `divergence_entries` row, AND sets `propagated_at=NOW()` on any resolution that pointed at it. See `internal/divergenceingest/store.go applySnapshot`.
10. Loop closed. Resolution row retained for audit (with `propagated_at` set).

**Lifecycle is derived from observation, not asserted by the deployment layer.** Orbital's source of truth for "did the loop close?" is what orb's next snapshot shows. The deployment layer doesn't tell orbital "I propagated these IDs" — orbital observes propagation by snapshot delta. This is intentional: bundler-asserted state would be a lie if the bundle silently dropped a field or cb-controller had a bug. Observation tells the truth.

**Ignore:**

1. Admin clicks Ignore and submits. No DGraph change.
2. `divergence_resolutions` row written with `action=ignore`. `propagated_at` is not meaningful for ignore — the row itself IS the persistent instruction.
3. On every future bundle build, the deployment layer queries orbital for ignored divergences (`GET /api/v1/divergences?action=ignore`) and **filters those (orbId, field) pairs out** of its apply config projection. The field never appears in the apply config cb-controller receives.
4. cb-controller releases the field (or never claimed it). `local:admin` retains sole ownership. Edge value unchanged.
5. Reporter doesn't surface the field (not in cb's current `managedFields` for the object).
6. Loop closed silently — no value change, no ownership change at the edge, no further audit.

## orbital → deployment-layer contract

The deployment layer (today: cb-bundler) calls **one endpoint** with **two filter shapes** per bundle build:

| Query | Returns | Lifecycle |
|---|---|---|
| `GET /api/v1/divergences?action=accept&action=reject&propagated=false` | divergences whose resolution is accept or reject AND has not yet been observed-as-propagated | one-shot per resolution: orbital marks `propagated_at` automatically when orb stops reporting the field as diverged. No callback required. |
| `GET /api/v1/divergences?action=ignore` | divergences whose resolution is ignore (regardless of `propagated_at`) | **persistent** — every bundle build re-queries; the disengagement re-applies forever until the resolution is deleted |

The shape is filterable REST: one resource (`/divergences`), state-as-query-string. Filters are deployment-layer-neutral domain terms (`action`, `propagated`, `dc`). How the deployment layer USES these — `spec.takeover[]`, omission from cb-manifest, force-apply SSA, etc. — is the deployment layer's concern. See [`configbundle-integration.md`](../configbundle-integration.md) for ConfigBundle's specific interpretation.

The full REST surface for divergence is five endpoints, all under `/api/v1/divergences`:

```
GET    /api/v1/divergences                       list, with ?action=&propagated=&dc= filters
GET    /api/v1/divergences/:id                   one (resolution embedded)
PUT    /api/v1/divergences/:id/resolution        upsert resolution      body: {"action":"accept|reject|ignore"}
DELETE /api/v1/divergences/:id/resolution        clear resolution
PATCH  /api/v1/divergences/:id/resolution        partial update         body: {"propagatedAt":"now"|RFC3339}  — operator recovery only
```

**Lifecycle observation:** orbital does NOT receive a callback from the deployment layer to mark resolutions propagated. Propagation is observed by the divergence ingester when orb's snapshot stops reporting a previously diverged field (loop closure). The ingester sets `propagated_at=NOW()` on the resolution and deletes the divergence entry in the same sweep. This is the design's most important architectural choice — observation is the source of truth, not bundler assertion. See `internal/divergenceingest/store.go applySnapshot`.

## What the divergence reporter sees

cb-controller's divergence reporter derives "fields cb-controller wants to manage" from the **currently applied cb-manifest** for each object, not from a schema-level allowlist. This is load-bearing — it's what makes `ignore` naturally silence the reporter:

- For each managed object, the reporter walks `managedFields` of the live object.
- For each field cb-controller has in its apply config (i.e., wants to manage), check for any non-cb manager (any `local:*` prefix).
- Found → emit divergence entry for that (orbId, field).
- Not found → silent.

Because ignored fields are filtered out of cb-manifest before SSA, cb-controller doesn't "want" them → reporter doesn't check them → no divergence emitted. The loop closes by absence.

For the orb-side API the reporter calls, see [`DIVERGENCE-INTAKE.md`](./DIVERGENCE-INTAKE.md).

## Topology API caveat

Orbital's GraphQL topology API (`getServer`, `getIdracSettings`, etc.) returns **cloud intent only** — what's in DGraph. For fields with an active `ignore` resolution, that intent is intentionally stale and may not match the edge.

- Consumers building a digital twin that needs ground-truth values should join against `/api/v1/divergence` for active resolutions and treat ignored fields' intent as advisory.
- Consumers reading intent to drive policy decisions (export, bundle build, configuration validation) should likely subtract ignored fields from their working set, same way cb-bundler does for the cb-manifest projection.
- Future enhancement: a `divergent: { resolution, edgeValue }` annotation on topology query results would let consumers see the state without a second round-trip. Not in MVP.

## Re-ingesting an already-resolved entry

Two cases at the ingester (`internal/divergenceingest/store.go applySnapshot`):

- **Same override as stored** → orb is still echoing the same divergence because the loop hasn't closed yet (bundle hasn't shipped, or just shipped and orb hasn't republished a clean snapshot). Refresh `last_seen_at`, `last_snapshot_published_at`, `type_name`. **Freeze** `intended_value`, `override_value`, `who` — the admin decided based on those exact values.
- **Different override than stored** → edge state drifted to a new value since the admin's decision. The prior decision was for a different override and is now stale. **Delete the `divergence_resolutions` row** and update the entry's values normally. The entry reappears as pending; admin re-decides on the new state.

Audit history of the prior resolution stays in the `events` table (`acceptDivergence` / `forceDivergence` / `ignoreDivergence` + any dispatched `update{Type}`). The `divergence_resolutions` table holds the **current** decision only.

See also: [AUDIT.md "Resolved divergence entries freeze on re-ingest WHEN the override matches; supersede when it changes"](./AUDIT.md).

## Open questions / deferred

- **Multi-manager (>2) scenarios.** This doc assumes two owners: `cb-controller` (cloud / upstream) and `local:admin`. If a third manager appears on a field (another reconciler, another tool), the eviction semantics still hold per SSA spec, but the policy questions get more complex. Out of MVP scope.
- **"Un-ignoring."** Admin removes an ignore resolution (or replaces it with accept/force). Next bundle re-includes the field. cb-controller applies, co-ownership forms, reporter surfaces it again on the next divergence tick. Admin re-decides. This works without code changes today; UI affordance for "remove resolution" may be worth adding.
- **Audit of persistent ignore effect.** A single `ignoreDivergence` event fires at decision time. Every future bundle then quietly omits the field. There's no per-bundle audit event recording "this bundle omitted field F because of resolution R." Likely fine — the resolution row + the single event are the trail. Add per-bundle logging if it becomes investigation-worthy.
- **Bulk operations.** Accept-all-pending, ignore-by-pattern, etc. Useful when one local admin makes 50 overrides in an incident. Out of MVP scope; current UI is per-row + batch-submit.

## MVCC and version handling

### The version field

Every ConfigItem in DGraph has `version: Int!` — a monotonically increasing counter. Two independent concerns:

- **Auto-increment** (mandatory, server-managed). Orbital's GraphQL proxy (`internal/handler/graphql.go Handle`) injects `version: before.version + 1` on every UPDATE through the canonical `set: $set` pattern, and `version: 1` on every ADD through the `input: [$input]` pattern. Clients don't need to track or send version. The proxy fetches the current entity before any single-entity update anyway (for MVCC + audit before-snapshot); the inject is one extra map write.
- **MVCC race detection** (opt-in via `ifVersion`). When a client wants strict optimistic-concurrency semantics, they include `ifVersion: <currentVersion>` in the mutation variables. The proxy compares to the actual current version and returns 409 on mismatch. UI Edit modals send it. Raw GraphQL / Ratel users may omit and get last-writer-wins. **This is deliberate**: ifVersion-required-everywhere is K8s-strict (fine for K8s, friction-heavy for our usage pattern); opt-in matches HTTP ETags / DynamoDB conditional-update conventions and is enough for the actual race classes orbital faces.

### Divergence-specific MVCC: `intended_at_version`

Each `divergence_entries` row carries `intended_at_version` (nullable int) — a snapshot of the target ConfigItem's DGraph `version` field as orb's local DGraph saw it at the moment the divergence was first reported. Anchors the observation in time so the resolution path can detect intervening intent edits.

**Where the value comes from:** orb's intake handler (`internal/orbserver/divergence_handlers.go receiveDivergence`). When a divergence report arrives, orb looks up each target ConfigItem's `version` in its local DGraph and stamps it on each override before persisting. Orb's DGraph is read-only outside of `orb import`, so the value captured at intake is what was actually current when the producer observed the drift. Producers (cb-controller) do NOT send version themselves.

**The invariant on the orbital side: capture at INSERT only. Never touch it on UPDATE.**

```
T0:  DGraph intent.F = false, version = 5.
T1:  Local admin sets edge.F = true.
T2:  Orb reports divergence — looks up local version (5), POSTs to orbital
     with intendedAtVersion=5 per override. Orbital INSERTs divergence row.
T3:  Another cloud admin edits intent.F to true via the regular Edit UI.
     DGraph version bumps to 6.
T4:  Orb's next snapshot still reports the same divergence (orb hasn't
     imported a new bundle, still sees its local version as 5). Orbital
     UPDATEs last_seen_at. MUST NOT touch intended_at_version (stays 5).
T5:  Admin clicks Accept. MVCC check: captured=5, current=6 → 409 "intent
     has changed since this divergence was reported — please re-review."
     Admin reloads → page shows row as STALE → dismisses it.
```

If T4 had refreshed `intended_at_version` to 6, T5 would have been 6==6 → no conflict → silent overwrite. The whole point of the column is to catch the T3 edit; refreshing it on UPDATE blinds the check.

### Why orb does the lookup (and not cb-controller)

Producer contract stays minimal — cb-controller (or any future producer) sends only `{orbId, field, intendedValue, overrideValue, who, when, type}`. Orb owns the lookup because:
- Orb's DGraph is the source of truth for "what was orbital's published intent at the moment the producer observed" — it's read-only outside of `orb import`, so the lookup is race-free relative to the observation.
- Future non-cb-controller producers don't need to learn about the orbital-domain `version` field.
- Single point of failure handling for "version unavailable" cases.

### Two-layer stale-detection at Accept time

1. **Version-based (primary).** If `intended_at_version` is non-nil, compare to current DGraph version. Precise — catches any intervening write.
2. **Value-based (fallback).** If `intended_at_version` is nil (legacy entry, orb couldn't look up version, ConfigItem absent from orb's local DGraph), query the current value of the field directly and compare to the report's stored `intended_value`. Less precise — misses edit-then-revert cycles where value matches but version moved — but catches the common case.

Both surface as 409 with the same message, so the UI behavior is uniform.

### Dismissal

When a row's `intended_at_version` differs from current (or the value-based check disagrees), the UI renders a "stale" badge. Stale rows are eligible for `DELETE /api/v1/divergences/:id` (`Dismiss`), which hard-deletes the entry + its resolution. Audit-logged as `dismissDivergence`. The dismiss handler re-validates staleness at request time (one extra DGraph query) to close any race between page render and click.

Non-stale rows cannot be dismissed — admin must accept, reject, or ignore them.

### Implementation pins

- `internal/divergence/version.go FetchCurrentVersion` — single-entity version lookup, used by orb intake + orbital UI/handler.
- `internal/divergenceingest/store.go applySnapshot` — INSERT-only capture; UPDATE never touches intended_at_version.
- `internal/handler/divergence.go dispatchAcceptMutation` — version-based primary + value-based fallback.
- `internal/handler/divergence.go isStale + currentValueMatches` — shared stale check for Dismiss.
- `internal/handler/graphql.go Handle` — auto-increment logic for both UPDATE (`set` map) and ADD (`input` array) patterns.

### Supersede edge case (not a bug, worth knowing)

When the override changes after a resolution exists, the ingester deletes the resolution but **preserves** `intended_at_version`. A subsequent Accept will then 409 because the prior Accept bumped DGraph version. Admin re-reviews and re-clicks. Strictly correct ("intent HAS changed since this divergence was reported — by your own prior accept"), slightly chatty UX. The right fix if it ever bothers people is on the resolve path (retry transparently after MVCC failure when the row was superseded), not on the ingest path.

## Settled Decisions

- **Accept and Reject both result in the deployment layer taking ownership of the field.** The two actions differ only in which value ends up at the edge, not in who ends up owning the field.
- **Ignore is implemented as omission from the apply config, not as a no-op.** Without active omission, cb-controller's apply would co-own the field and the divergence reporter would never stop reporting it.
- **`divergence_resolutions.action` stays as the bare verb (`accept`/`reject`/`ignore`).** Audit operation names use the verbNoun form (`acceptDivergence`, etc.). The action column is internal state.
- **The action verb is `reject`, not `force`.** "Force" leaks the K8s SSA verb into orbital's domain vocabulary. "Reject" is the policy decision; "force-apply" is one possible mechanism a deployment layer uses to honor it.
- **Conventional REST API surface.** One resource (`/divergences`), standard verbs (GET, PUT, DELETE, PATCH), query strings for filters. No coined nouns in URLs, no state-as-path-segment, no batch endpoint (client fires N parallel PUTs). ConfigBundle-specific terms ("takeover," "cb-manifest," "omission") live in the configbundle integration docs, not the divergence API contract.
- **Lifecycle is observed, not asserted.** `propagated_at` is set by the divergence ingester when orb stops reporting a field as diverged — NOT by a callback from the deployment layer. The deployment layer doesn't tell orbital "I propagated these IDs" because that would be an assertion orbital can't verify. Observation tells the truth even when bundlers or reconcilers have bugs.
- **`PATCH /divergences/:id/resolution` is operator-recovery only.** Not part of normal bundle flow. Manual unblock for the rare case where the ingester can't observe propagation (e.g., orb stuck, snapshot publishing broken). Standard PATCH semantics — partial update of the resolution resource.
- **`propagated_at` (nullable timestamp) replaces the legacy `cb_consumed`+`cb_consumed_at` pair.** A single nullable timestamp captures both "is it propagated?" (NULL = no) and "when?" (the timestamp). The `cb_` prefix leaked deployment-layer naming into orbital's schema.
- **Topology API does not annotate ignored fields.** Cloud intent is the API's contract; consumers needing ground truth join with `/api/v1/divergence`. Annotation is deferred until a real consumer needs it.
- **One-shot vs. persistent resolutions have different lifecycles.** Accept/reject transition `NULL → timestamp` exactly once via observed propagation. Ignore has no transition — the resolution row IS the standing instruction and re-applies on every bundle build until manually removed.
- **`intended_at_version` is captured at INSERT, never on UPDATE.** Refreshing it on re-ingest would silently disable the MVCC check that catches "admin edited intent between report and resolution." See the dedicated section above for the failure-mode walkthrough. This rule is load-bearing for race-condition correctness.
