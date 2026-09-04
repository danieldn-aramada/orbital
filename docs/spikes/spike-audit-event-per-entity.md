# Spike: one audit event per entity, correlated by request id

**Status:** Partially shipped. **§4 (write-path pre-flight) is DONE — implemented 2026-09-03**, and its decisions now live in `docs/reference/AUDIT.md`; the section is kept below only for the deliberation, and can go when the rest of this spike closes. §§1–3 (one audit event per entity) are still design-only: target shape decided (option B below), phasing and two open questions remain.
**Date:** 2026-09-01.
**Folds into:** `docs/reference/AUDIT.md` on close; this doc is then deleted.

## 1. The problem

`GET /api/v1/audit-log?orbId=<node>` is orbital's per-node changelog — the API behind every ConfigItem's audit tab, and the first thing an adopter reaches for after "does it have an audit log?". It has holes:

- **`changes` is absent for creates.** `computeChanges(before, variables)` iterates `before`'s keys and intersects with `after`. A create has no `before`, so the intersection is empty and the field diff is omitted. Adding a server — and every seeded entity — leaves a changelog row with no field detail.
- **`changes` is absent for multi-entity events**, and could not be attributed if computed: entries carry `{field, before, after}` with no `orbId`, while one event may reference N resources (`audit_event_resource` is 1:N).
- **Field-level attribution is best-effort** for the same reason — the event records mutation *input*, so "who set this field" is inferred from the most recent event whose variables include it.

The data is not lost — `details.variables` holds it — but clients must parse a raw payload for exactly the cases the normalised `changes` array exists to spare them. That is the API-first failure: every integrator re-implements the same walk.

## 2. Evidence — NetBox solves this with granularity, not shape

NetBox writes **one `ObjectChange` per object** and correlates everything from one request with a **`request_id` UUID**. Consequences: a change record always describes exactly one object, so its diff is always single-entity and always renderable; the per-object changelog is complete by construction; and "what else happened in that request" is a query on `request_id`.

Orbital's own ownership research already noted this pattern when it looked at how components roll up to their device.

## 3. Decision — option B: one event per entity

Two designs were considered:

**(a) Keep one event per request; add `orbId` to each change entry.** `changes: [{orbId, field, before, after}]`. Additive, no migration.

**(b) One event per entity, correlated by `request_id`.** ← **chosen**

Why (b):
- `changes` becomes single-entity **by construction** — no response-shape change, and no way to reintroduce the ambiguity later.
- `?orbId=` becomes exact rather than "this event touched, among others, your node".
- Per-node changelog is complete by definition — the thing this spike exists to fix.
- It is what the closest comparable does, and orbital already emits `request.id` on access logs, so the correlation key exists in the request path.

(a) was rejected as a waypoint: it fixes the symptom while leaving one event describing many nodes, which is the root of both the attribution and the filtering weakness.

### The fact that makes this cheap: the new shape is a special case of the old

`audit_event` has `edge.To("resources", AuditEventResource.Type)` — a 1:N edge. **An event with exactly one resource is already legal.** So:

- **No data migration is required.** Historical multi-resource events stay exactly as they are and keep rendering; new events simply always have one resource.
- The read path is unchanged — it already joins through `audit_event_resource`.
- `request_id` is a new nullable column; old rows have none, which is honest (they predate the concept).

This matters because orbital has no versioned migration tooling yet (Spike 27, Atlas, is designed but not started), and ent auto-migration can add a column but cannot split rows. A design needing backfill would be blocked on that spike. This one is not.

### What changes on the write path

One mutation request producing N entities writes N events sharing a `request_id`, each with its own `operation`, its own resource, and its own `details.before`. `computeChanges` then always receives one before-map and one after-map — the shape it was written for.

**The split is unconditional — it does not depend on whether a `before` was captured.** A multi-op *update* still writes N events even while the N-entity before-fetch is scoped out (open question 1); those events simply carry no `before`, so `changes` is absent for them exactly as it is today. One write-path rule, not two: granularity is always per entity, and `before` availability is a separate question layered on top. Splitting only when a diff is computable would make `?orbId=` exact for some shapes and approximate for others — the ambiguity this spike exists to remove.

### Creates get a diff; the expensive case is scoped out

- **Creates (single or bulk) are cheap** — there is no before-state to fetch, by definition. Emit `{field, before: null, after: <value>}` per field, mirroring NetBox's `prechange_data: null`. This closes the most common hole (adding an entity, seeding) at near-zero cost.
- **Multi-op *updates* are the expensive case** — they need an N-entity before-fetch on the write path. Orbital's own paths largely avoid producing them: the config editor dispatches one request per entity, and change-request merge applies items one at a time, so both already yield single-entity events. This shape comes mainly from external API clients. **Decide it on evidence, not in this spike** (open question 1).

### Constraint carried from `AUDIT.md`

**Extend `computeChanges`; do not add a second diff implementation.** `buildDiffHTML` is a pure renderer over it, which is why the API's `changes` and the HTML panel cannot disagree. The create path is a new branch *inside* it, not beside it.

## 4. Companion work — the write-path pre-flight lives in the wrong function ✅ SHIPPED 2026-09-03

**Added 2026-09-01**, after two bugs in one day traced to the same asymmetry. Not a hard blocker for §3's create branch, but it touches the same two functions this spike touches, and §3 assumes something only `Handle` can currently deliver.

### The asymmetry

`Handle` (`graphql.go:89`) does three things before a mutation reaches DGraph:

1. the **before-fetch** (`fetchBeforeByID` / `fetchBeforeByOrbID`) — the input to `computeChanges`,
2. **stamping** — `version` auto-increment on update, `version: 1` on add, plus `createdBy`/`createdAt`/`updatedBy`/`updatedAt` from the authenticated identity and the server clock,
3. the opt-in **`ifVersion` MVCC check**.

All three live *inside* `Handle`. `writeToDGraph` — the function every write already funnels through, and where the approval gate was deliberately placed (Spike 36 D14: *a chokepoint for WRITES, not just for clients*) — does only the gate check and the POST. So `DispatchMutation` gets none of the three and says so in its own doc comment, leaving each to the caller with nothing enforcing that a caller remembers.

Both existing callers forgot a different one:

| Caller | before-fetch | stamping |
|---|---|---|
| Change-request merge | ❌ read only `version` + the `clear` fields, so the audit row dumped raw variables instead of a diff — **fixed 2026-09-01** | ✅ sets `version` explicitly |
| Divergence-Accept (`divergence.go:453`) | ✅ hand-built from the entry's intended value | ❌ **no `version`, no `updatedAt`, no `updatedBy`** |

The Accept gap is measured, not inferred: with an open change request against `colo:server-maintenance-8CVD664`, writing a field on that node without bumping `version` left the request reporting `stale: false` and fully mergeable — `base_hash` is a hash of the scope's `orbId@version` vector, so a write that does not move `version` does not move the hash. An approval cast before the Accept keeps counting. Filed as a bug in `docs/planning/debt.md` Track B; the design below is the fix.

### Why it belongs in this spike

§3 states that one request producing N entities writes N events *"each with its own `details.before`"*. Producing a per-entity before-state is the before-fetch, and today it exists in one entry point only — so that sentence is true for `/graphql` callers and false for merge and Accept. Open question 1 (pay for the N-entity before-fetch, or not) is a question about **where that fetch lives** as much as whether it runs, and the answer is the same function either way.

**The create branch itself does NOT depend on this.** `computeChanges` runs at read time over stored `details.variables`, so `{field, before: null, after}` needs no before-state and works for a merge-created entity today. Either piece can go first; doing them separately means editing `Handle` and `writeToDGraph` twice.

### Design — move all three into `writeToDGraph`

They move together because they are coupled: stamping's UPDATE branch is gated on `before != nil` (it reads `before["version"]`), and `ifVersion` compares against `before["version"]`. Leaving `ifVersion` behind means `Handle` still needs the fetch, which defeats the move.

- An `ifVersion` mismatch returns a **typed error** that `Handle` maps to `409 MVCC_CONFLICT`, mirroring how `gatedError` already surfaces the approval refusal. `DispatchMutation` callers never set `ifVersion`, so it is inert for them.
- `DispatchMutation`'s `before` parameter becomes a **fallback** — the fetched value wins when it resolves. This costs divergence-Accept its deliberate optimisation (its comment notes it avoids a round trip because the read was already paid at ingest) in exchange for a complete, current before-state: one extra read per Accept.
  - **Refined while building, 2026-09-03.** "Fetched wins" is per FIELD, not wholesale. `BeforeFields` is a curated per-type selection — `NetworkInterface`'s is `id orbId name version` — and `dispatchAcceptMutation` names its field at runtime, so discarding the caller's map would have emptied the audit diff for exactly the fields outside the list. The fetch is overlaid ON the fallback.
  - **Also found while building:** the move alone fixes nothing for Accept. Its mutation declared `$filter: {Type}Filter!`, and the row to stamp is resolved from the `orbId` VARIABLE — so the pre-flight resolved no row and the defect would have survived the refactor with the suite green. Accept now dispatches the canonical `update{Kind}($orbId, $set)` shape, and `TestAccept_DispatchesMutationAndRecordsResolution` asserts that shape rather than the old one.
- **Merge keeps stamping `version` itself**; the stamping guard preserves a caller-set version, so there is no double-increment. Accepted cost: merge performs a second read it does not strictly need, since `fetchMergeTargets` already ran. Optimise later with a skip hint, not now.
- The body is **parsed once** in `writeToDGraph` and shared with `checkApprovalPolicy`, which currently unmarshals it separately.
- **Stays in `Handle`:** auth, the inline-selector rejection (it needs the echo context and returns a client-facing rewrite hint), operation-name context, and error mapping.
- **Delete or correct the false comment at `divergence.go:444`**, which claims version-bumping is handled by the general mutation pipeline. It is not, on the path that handler takes.

### Acceptance for this section

1. A divergence Accept bumps `version` and stamps `updatedAt`/`updatedBy` on the node.
2. An open change request covering an accepted entity reads `stale: true` afterwards, and approvals cast before it stop counting.
3. The audit event for an Accept carries a field diff computed from the fetched before-state.
4. A `/graphql` update increments `version` by exactly one — no double-stamp introduced by the move.
5. A `/graphql` add still gets `version: 1` plus `createdBy`/`createdAt`/`updatedBy`/`updatedAt`.
6. `ifVersion` mismatch is still `409 MVCC_CONFLICT`; a match still proceeds.
7. `ifVersion` and `orbId` are still stripped before the body reaches DGraph, on the same conditions.
8. An inline-selector update is still refused `400 VARIABLE_FORM_REQUIRED` with the rewrite hint.
9. A change-request merge still applies, writes `version = target+1` once, and still records its field diff.
10. **The negative:** a direct DQL write still bypasses everything. `TestCR_OutOfBandWriteThatSkipsTheVersionCounterIsNotSeen` stays unchanged and passing — out-of-band writes remain invisible to staleness by design; what changes is that Accept stops being one.

Items 4–8 and 10 are already pinned by the twelve tests in `graphql_handler_test.go` and the change-request integration suite — name the covering test rather than write a duplicate. Items 1–3 have no coverage today and are the actual guarantee, so they need one integration test that performs an Accept and reads the result back through `/graphql` and `GET /api/v1/change-requests/:id`, per the standing rule that audit and persistence assertions read through the consumer API.

**Unblocks:** *Make a change-request merge a true compare-and-swap* (`backlog.md`) — guarding `version` protects a row only if every writer bumps it.

## 5. Phasing

0. ✅ **Done 2026-09-03** *(companion, either order — see §4)* Move the before-fetch, stamping and `ifVersion` check from `Handle` into `writeToDGraph`.
1. Add `request_id` (nullable) to `audit_event`; stamp it from the existing request id.
2. Write path emits one event per entity. `operations` becomes a single-element array for new rows (field kept, not narrowed, so old rows stay valid).
3. Extend `computeChanges` with the create branch (`before: null`).
4. UI groups by `request_id` where "these happened together" matters.
5. *Later, optional:* denormalise `orb_id` onto `audit_event` and drop the join. Pure performance; not required for correctness.

## 6. Acceptance

1. Creating an entity produces an audit event whose `changes` lists every set field with `before: null`.
2. A bulk create of N entities produces **N events**, sharing one `request_id`, each with `changes` for its own entity.
3. `GET /api/v1/audit-log?orbId=X` returns only events whose single resource is X — no event that merely mentions X alongside others.
4. Historical multi-resource events still render in the API and the audit tab, unchanged.
5. A parent's audit tab still aggregates owned-child events (the `RelatedOrbIDsCSV` → `data-related-orb-ids` path is unaffected).
6. `changes` and the rendered HTML diff continue to agree — one `computeChanges`, one renderer.
7. Read back through `GET /api/v1/audit-log`, not the table (the standing rule for audit assertions).
8. **The negative:** an update that changes nothing still produces no phantom field entries.

## 7. Open questions

1. **Multi-op updates — pay for the N-entity before-fetch, or leave `changes` absent there?** Leaving it absent keeps today's behaviour for a shape orbital's own paths do not generate. Paying for it costs N reads on the write path. Lean: leave it, revisit with evidence that a real client sends multi-op updates.
2. **Does the write path currently know a mutation was a create vs an update** at the point `computeChanges` is called? If not, that detection is the first piece of work.
3. **Is `operations` worth narrowing to a single `operation` column** for new rows, or is a one-element array acceptable indefinitely? Narrowing is cleaner but touches every reader.

## 8. Non-goals

- **A per-field history table.** `AUDIT.md` names this the rejected antipattern — field-level attribution stays best-effort. This spike improves it as a side effect of finer event granularity; it does not chase exactness.
- **Backfilling historical events.** Old rows keep their shape. Splitting them would need migration tooling orbital does not have (Spike 27) and would rewrite history to look like something it was not.
- **Changing the `changes` entry shape.** Option (b) is chosen precisely so `{field, before, after}` stays as it is.
