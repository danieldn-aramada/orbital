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
| **Accept** | intent ← override value | unchanged (was already at override) | enforce intent: take ownership of the field with the new intent value | cb-controller sole owner; `local:admin` evicted | resolution active until orb stops reporting the entry; entry+resolution deleted together on loop closure |
| **Reject** | unchanged | reverts to original intent | enforce intent: take ownership of the field, reset edge to intent value | cb-controller sole owner; `local:admin` evicted | resolution active until orb stops reporting the entry; entry+resolution deleted together on loop closure |
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
3. `divergence_resolutions` row written with `action`.
4. Operator triggers an export + publish. Orbital calls `POST /bundle` on the deployment layer (cb-bundler today).
5. The deployment layer queries orbital for active accept/reject resolutions (`GET /api/v1/divergences?action=accept&action=reject`) and enforces intent for each: in K8s terms, emit a `spec.takeover[]` entry so cb-controller force-applies the current intent value, evicting `local:*` from `managedFields`.
6. Orbital pushes the OCI artifact.
7. Orb imports the bundle, dispatches cb-manifest to cb-controller. cb-controller reconciles; `local:admin` is evicted; cb-controller becomes sole manager.
8. cb-controller's divergence reporter on the next tick sees no `local:*` on this field → no divergence emitted.
9. Next orb snapshot omits the entry. Orbital ingester's stale sweep deletes the `divergence_entries` row AND the matching `divergence_resolutions` row in the same pass. See `internal/divergenceingest/store.go applyReport`.
10. Loop closed. Audit history of the decision lives in the `events` table (the `acceptDivergence`/`rejectDivergence` event recorded at step 3) — the resolution row itself is gone.

**Orbital does NOT track edge propagation.** Its contract ends at "intent is captured and exposed via the export API." Whether the deployment layer actually applied a force is the edge's concern. If local admin re-overrides the same field after a Reject, orb reports a fresh divergence on the next tick and the operator re-decides from a clean slate — no inherited "pending propagation" state.

**Ignore:**

1. Admin clicks Ignore and submits. No DGraph change.
2. `divergence_resolutions` row written with `action=ignore`. The row itself IS the standing instruction.
3. On every future bundle build, the deployment layer queries orbital for ignored divergences (`GET /api/v1/divergences?action=ignore`) and surfaces those `(orbId, field)` pairs as `spec.ignored[]` in the cb-manifest.
4. cb-controller's apply omits the field's value from its own claim while still seeing it in `spec.ignored[]`. `local:admin` retains sole ownership. Edge value unchanged.
5. Reporter keeps emitting divergence for the field every tick — it's still locally-owned at the edge, just with an attached `action=ignore` resolution that suppresses any further admin prompt in the UI.
6. The standing instruction persists as long as the edge admin holds the field.

**Ignore loop closure via edge handback (configbundle ADR-009):**

The Ignore row is not strictly permanent. If the edge admin releases their SSA claim on the field, configbundle's ReclaimController fires, replays the last-imported manifest, and cb-controller becomes sole owner with the intent value. From orbital's perspective the loop then closes through the standard path:

1. cb-controller is now sole manager of the field; value = intent.
2. cb-controller's divergence reporter on the next tick sees no `local:*` on this field → no divergence emitted.
3. Next orb snapshot omits the entry.
4. Orbital ingester's stale sweep deletes the `divergence_entries` row AND the `divergence_resolutions` row (action=ignore) in the same pass.
5. An audit `Event` is written with operation `closeIgnoreOnHandback` so the cloud admin sees the closure as an explicit edge-driven action, not a silent disappearance.

This is identical to Accept/Reject loop closure (step 9 above) except for the trigger: Accept/Reject is cloud-initiated via `spec.takeover[]`; Ignore closure is edge-initiated via SSA release. Same ingester code path (`internal/divergenceingest/store.go applyReport`).

## orbital → deployment-layer contract

The deployment layer (today: cb-bundler) calls **one endpoint** with **two filter shapes** per bundle build:

| Query | Returns | Lifecycle |
|---|---|---|
| `GET /api/v1/divergences?action=accept&action=reject` | divergences with an active accept or reject resolution | active until orb stops reporting the underlying divergence entry — at which point the ingester deletes entry + resolution together; the query no longer returns them. |
| `GET /api/v1/divergences?action=ignore` | divergences whose resolution is ignore | persistent while the field remains locally claimed at the edge; the disengagement re-applies on every bundle build. Closes via the ingester when cb-controller reclaims after an SSA release from `local:*` (configbundle ADR-009), at which point entry + resolution are deleted in the same pass — same code path as Accept/Reject. |

The shape is filterable REST: one resource (`/divergences`), state-as-query-string. Filters are deployment-layer-neutral domain terms (`action`, `dc`). How the deployment layer USES these — `spec.takeover[]`, omission from cb-manifest, force-apply SSA, etc. — is the deployment layer's concern. See [`configbundle-integration.md`](../configbundle-integration.md) for ConfigBundle's specific interpretation.

The full REST surface for divergence is four endpoints, all under `/api/v1/divergences`:

```
GET    /api/v1/divergences                       list, with ?action=&dc= filters
GET    /api/v1/divergences/:id                   one (resolution embedded)
PUT    /api/v1/divergences/:id/resolution        upsert resolution      body: {"action":"accept|reject|ignore"}
DELETE /api/v1/divergences/:id/resolution        clear resolution
```

**Resolution lifecycle is bound 1:1 with the active divergence entry.** The ingester deletes entry + resolution together on loop closure — both are wrapped in a single transaction so partial-failure can't leave an orphan resolution that would silently re-attach to a future re-divergence with the same `(entry_orb_id, field)` key. Orbital's source of truth for "is this field still diverging?" is what orb's next snapshot shows — not bundler assertion. See `internal/divergenceingest/store.go applyReport`.

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

Three branches at the ingester (`internal/divergenceingest/store.go applyReport`, ADR 012):

- **Content matches AND no resolutions for this DC's entries** → no-op. Orb is still echoing the same divergence because the operator hasn't decided yet (staged decisions live client-side until Submit). Touch `last_seen_at` and `last_report_published_at` so the UI shows freshness; leave entries otherwise intact.
- **Content matches BUT one or more resolutions exist for this DC's entries** → **atomic supersede**. Resolutions are bound to the report that triggered them; once Submit has dispatched a decision, the next identical-content report is treated as a *new* divergence occurrence (cloud-admin and local-admin reproduced the same drift). Drop entries + resolutions in one transaction and reinsert the incoming set as fresh pending. Ingest-time supersede assumes the identical-content report arrived AFTER propagation of the prior decision — respecting that invariant is the scheduler-cadence constraint (see Settled Decisions).
- **Content differs (any added, removed, or value-changed tuple)** → **atomic supersede**. Same drop-and-replace as above.

All supersede paths emit one `supersedeDivergenceReport` management audit event with `{dcOrbId, dropped, added}` counts. Forensic history of individual dropped resolutions lives in the prior `resolveDivergence` / `acceptDivergence` / etc. events — the resolution row itself is gone from `divergence_resolutions`.

Content equality is checked on the tuple `(entry_orb_id, field, override_value)`, order-independent (`contentEqual` in the same file). `intended_value`, `who`, and `first_seen_at` do not participate in the equality test — the admin's decision was against `override_value`, and that's the field that determines whether the operator is looking at the same problem or a new one.

See also: [AUDIT.md "Resolved divergence freeze-vs-supersede behavior on re-ingest"](./AUDIT.md).

## Open questions / deferred

- **Multi-manager (>2) scenarios.** This doc assumes two owners: `cb-controller` (cloud / upstream) and `local:admin`. If a third manager appears on a field (another reconciler, another tool), the eviction semantics still hold per SSA spec, but the policy questions get more complex. Out of MVP scope.
- **"Un-ignoring."** Admin removes an ignore resolution (or replaces it with accept/force). Next bundle re-includes the field. cb-controller applies, co-ownership forms, reporter surfaces it again on the next divergence tick. Admin re-decides. This works without code changes today; UI affordance for "remove resolution" may be worth adding.
- **Audit of persistent ignore effect.** A single `ignoreDivergence` event fires at decision time. Every future bundle then quietly omits the field. There's no per-bundle audit event recording "this bundle omitted field F because of resolution R." Likely fine — the resolution row + the single event are the trail. Add per-bundle logging if it becomes investigation-worthy.
- **Bulk operations.** Accept-all-pending, ignore-by-pattern, etc. Useful when one local admin makes 50 overrides in an incident. Out of MVP scope; current UI is per-row + batch-submit.

## Version handling — Accept, Dismiss, staleness

Orbital's general MVCC (auto-increment `version: Int!` on every ConfigItem, opt-in `ifVersion` for user-driven UI edits) applies to the DGraph mutation orbital dispatches inside Accept. But **the divergence path itself carries no version anchor** — no `intended_at_version` column on `divergence_entries` or `divergence_resolutions`, no version-based staleness gate on Accept or Dismiss. Per ADR 012, staleness on the ingest side is handled by supersede, and staleness on the bundler-filter side is handled by per-field value comparison. Both are described below.

### Auto-increment (general MVCC, still true)

Orbital's GraphQL proxy (`internal/handler/graphql.go Handle`) injects `version: before.version + 1` on every UPDATE through the canonical `set: $set` pattern, and `version: 1` on every ADD through the `input: [$input]` pattern. Applies to every mutation orbital dispatches — including the one Accept fires — so `updateServer`, `updateIdracSettings`, etc., all bump the target ConfigItem's version. Clients don't need to track or send version.

### `ifVersion` (general MVCC, opt-in, still true)

When a UI Edit modal wants strict optimistic-concurrency semantics, it includes `ifVersion: <currentVersion>` in the mutation variables. The proxy compares to the actual current version and returns 409 on mismatch. Raw GraphQL / Ratel users may omit and get last-writer-wins. This is deliberate: `ifVersion`-required-everywhere is K8s-strict (fine for K8s, friction-heavy for our usage pattern); opt-in matches HTTP ETags / DynamoDB conditional-update conventions and is enough for the actual race classes orbital faces.

### Accept is last-writer-wins (per ADR 012)

`dispatchAcceptMutation` (`internal/handler/divergence.go`) does NOT pre-check that intent has moved since the divergence was reported. It dispatches the `update{Type}` mutation unconditionally. If another cloud admin edited the same field between report and Accept, the Accept's mutation still fires and overwrites. Rationale: whatever the admin just wrote is authoritative, and the ingester's supersede path catches any content-diverging state on the next report.

The Accept mutation, like any other, bumps the target ConfigItem's DGraph version by one. That version bump lands in the audit log alongside the resolution decision — anyone auditing "what changed on this ConfigItem" can see the Accept-dispatched update and any concurrent admin edit ordered by version.

### Dismiss is a straight delete (per ADR 012)

`Dismiss` (`internal/handler/divergence.go:440`) is `DELETE /api/v1/divergences/:id` — hard-deletes the entry and its resolution row. No staleness gate, no "stale badge" precondition, no re-validation query. Operator owns the call. If orb keeps reporting the same divergence, the next ingest cycle re-creates the entry (fresh UUID) via supersede. Dismiss is "I want this gone now," not "purge permanently." Audit-logged as `dismissDivergence`.

### Bundler-filter staleness is per-FIELD value comparison

The List handler at `/api/v1/divergences?action=accept&action=reject` refuses to surface stale resolutions to the deployment layer. For each candidate resolution the handler queries DGraph for that field's current value and compares against the post-decision expectation: Reject expects `current == entry.intended_value` (admin chose to keep that intent); Accept expects `current == entry.override_value` (admin's mutation adopted the override). Mismatch ⇒ another cloud admin's edit has moved this field since the decision ⇒ exclude from the bundler list. Ignore is exempt (standing instruction, not one-shot). UI calls without `?action=` see all resolutions.

Per-field value comparison replaces an earlier version-based staleness anchor. See Settled Decisions below for why version-based failed.

### Implementation pins

- `internal/handler/graphql.go Handle` — auto-increment + opt-in `ifVersion` (still active).
- `internal/handler/divergence.go dispatchAcceptMutation` — last-writer-wins Accept dispatch.
- `internal/handler/divergence.go Dismiss` — no-gate delete.
- `internal/handler/divergence.go List` — per-field value staleness check for `?action=accept&action=reject`.
- `internal/divergenceingest/store.go applyReport` — three-branch supersede on ingest (see "Re-ingesting an already-resolved entry" above).

## Settled Decisions

- **Accept and Reject both result in the deployment layer taking ownership of the field.** The two actions differ only in which value ends up at the edge, not in who ends up owning the field.
- **Ignore is implemented as omission from the apply config, not as a no-op.** Without active omission, cb-controller's apply would co-own the field and the divergence reporter would never stop reporting it.
- **`divergence_resolutions.action` stays as the bare verb (`accept`/`reject`/`ignore`).** Audit operation names use the verbNoun form (`acceptDivergence`, etc.). The action column is internal state.
- **The action verb is `reject`, not `force`.** "Force" leaks the K8s SSA verb into orbital's domain vocabulary. "Reject" is the policy decision; "force-apply" is one possible mechanism a deployment layer uses to honor it.
- **Conventional REST API surface.** One resource (`/divergences`), standard verbs (GET, PUT, DELETE), query strings for filters. No coined nouns in URLs, no state-as-path-segment, no batch endpoint (client fires N parallel PUTs). ConfigBundle-specific terms ("takeover," "cb-manifest," "omission") live in the configbundle integration docs, not the divergence API contract.
- **The List handler refuses to surface stale accept/reject resolutions to bundlers, checked per-FIELD by value comparison.** When `GET /api/v1/divergences?action=accept&action=reject` runs, for each resolution the handler queries DGraph for that field's current value and compares against the post-decision expectation: Reject expects `current == entry.intended_value` (admin chose to keep that intent); Accept expects `current == entry.override_value` (admin's mutation adopted the override). Mismatch ⇒ another cloud admin's edit has moved this field since the decision ⇒ exclude from the bundler list. Ignore is exempt (standing instruction, not one-shot). UI calls without `?action=` see all resolutions. Pinned by `TestList_ActionFilter_ExcludesStaleResolution` + `TestList_ActionFilter_BatchAcceptAndRejectOnSameConfigItem`.
- **No co-ownership of VALUE fields.** cb-controller force-claims when values match local:*'s; bows out when values differ (override case → reported); always claims for `spec.takeover[]` (Accept/Reject) and never claims for `spec.ignored[]` (Ignore). Co-ownership of SSA structural fields (`listMapKey`, entry-presence, struct wrappers) is K8s-native and inert — both managers writing the same `orbId` is normal, doesn't trigger divergence reports, doesn't block force-apply. See configbundle ADR-008.
- **Ignore is a standing instruction, not a suppression.** While the field remains locally claimed at the edge, it stays surfaced as divergence in every report. The resolution row in orbital records the deliberate "leave to edge" decision; the bundler emits a parallel `spec.ignored[]` entry on every build. The attached `action=ignore` resolution suppresses any further admin prompt in the UI but doesn't close the loop. Loop closure happens only when the edge admin releases their SSA claim — per configbundle ADR-009, cb-controller's ReclaimController then reclaims the field with intent value, the divergence reporter no longer sees `local:*`, and the next orb snapshot omits the entry, triggering the standard ingester cleanup (entry + resolution deleted together). The cleanup writes a `closeIgnoreOnHandback` audit event so the cloud admin sees the closure as an explicit edge-driven action, not silent disappearance.
- **Bundler-filter staleness is per-FIELD by value, NOT per-ConfigItem by version.** Earlier drafts used ConfigItem `version: Int!` as the staleness anchor. It false-positives every batch resolution that touches multiple fields of the same ConfigItem (e.g., Accept `ipmiEnabled` + Reject `sshEnabled` on the same `IdracSettings`: the Accept's mutation increments the shared version, which silently invalidated the sibling Reject). The current per-field VALUE check catches genuine cross-admin contradictions while leaving batched decisions on sibling fields intact. No `intended_at_version` column exists on `divergence_entries` or `divergence_resolutions` — the version anchor was removed entirely, not demoted to audit.
- **Orbital does NOT track edge propagation.** Its contract is "intent is captured and exposed via the export API." Whether the deployment layer applied a force is the edge's concern. There is no `propagated_at` column, no `?propagated` filter, no PATCH-for-recovery endpoint — those existed in an earlier draft and were removed (2026-06-15) because they violated the air-gap separation. If admin re-overrides after Reject, orb reports a fresh divergence on the next tick and the operator re-decides from a clean slate. Audit of every decision lives in the `events` table; the resolution row itself is bound 1:1 with the active entry and disappears with it on loop closure.
- **Topology API does not annotate ignored fields.** Cloud intent is the API's contract; consumers needing ground truth join with `/api/v1/divergence`. Annotation is deferred until a real consumer needs it.
- **Accept/reject resolutions and Ignore have similar lifecycle but different triggers.** All three actions' resolution rows are bound 1:1 to their entry and delete together on loop closure. The trigger differs: Accept/Reject closes when cb-controller successfully takes ownership via `spec.takeover[]` (cloud-initiated). Ignore closes when the edge admin releases their `local:*` claim and cb-controller's ReclaimController restores intent (edge-initiated, configbundle ADR-009). Until either trigger fires, the row is the standing instruction and re-applies on every bundle build. A third path — `DELETE /api/v1/divergences/:id/resolution` — also removes the row manually but doesn't close the entry (the entry stays pending until the next snapshot includes it again or the field is no longer reported).
- **Scheduler cadence must exceed worst-case propagation SLA.** The `applyReport` supersede branch treats "identical content + resolution exists" as a re-emergence event — dropping the resolution and requiring re-decision. This is correct when the identical-content report arrives AFTER the prior decision has propagated to the edge (loop should have closed but didn't → local admin reproduced the drift → fresh decision). It is INCORRECT when the identical-content report arrives BEFORE propagation lands (cb-controller still sees `local:*`, keeps reporting → orb re-publishes → supersede wipes an in-flight decision). Manual publish stays the primary path; scheduler (opt-in via `ORB_DIVERGENCE_PUBLISH_SCHEDULE`) is a safety net for "operator forgot," default off, minimum interval should be much larger than bundle-build + push + import + reconcile time (24h is safe for the colo-galleon deployment; faster cadences require explicit propagation-SLA analysis).
