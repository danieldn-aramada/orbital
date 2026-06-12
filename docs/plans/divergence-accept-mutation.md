# Plan — Accept on Divergence Should Mutate Intent

**Status:** Proposed (2026-06-11). Closes Phase A.1 of Spike 22 (Divergence Reporting).

## Problem

The current Accept handler (`internal/handler/divergence.go:106-108`) records the admin's decision but does **not** update orbital's intent. This produces an infinite divergence loop:

1. Edge applies a local override (e.g. `sshEnabled = true`); intended value in orbital is `false`.
2. Orb publishes a snapshot; orbital ingests; UI shows the divergence.
3. Admin clicks **Accept** → orbital writes a `DivergenceResolution` row but DGraph is unchanged.
4. Next time cb-bundler builds a bundle, it reads orbital's intent (`false`) and the bundle tells edge `false`.
5. Edge's local override re-applies → divergence re-appears → admin sees the same entry again.

The only stable end-state when an admin accepts an override is for orbital's intent to actually change. Without that, "accepted" entries are noise that never resolves.

This was the original design intent (Q4 in the 2026-06-11 design session: *"accept overrides means a graphql mutation to change node/field in orbital — yes"*). The current Phase A implementation deferred it; this plan closes the gap.

## Conceptual symmetry once Accept mutates

| Action | Effect on orbital's intent | Effect on edge (next bundle) |
|---|---|---|
| Accept | mutates to match edge | bundle reflects new intent; edge stays as-is |
| Force | unchanged | cb-bundler emits `spec.takeover[]`; edge reverts |
| Ignore | unchanged | cb-bundler omits the field entirely; edge keeps its local value |

Each resolution produces a stable state. Today, only Force does.

## Design

When the admin clicks **Accept**:

1. Orbital loads the `DivergenceEntry`.
2. Orbital dispatches a GraphQL mutation of the form `update{Type}(filter: {orbId: {eq: "X"}}, set: {<field>: <value>}) { numUids }` through the existing `internal/handler/graphql.go` proxy (so authz, audit, and rate limiting all apply unchanged).
3. **Only if the mutation succeeds**, orbital writes the `DivergenceResolution` row.
4. If the mutation fails, no resolution is recorded; the UI surfaces the error and the admin can retry.

The mutation is auditable through the GraphQL handler's existing audit path (`writeAuditEvent` in `graphql.go`). The resolution record adds a second audit event tagged `resolveDivergence/accept`. Two events, one per side of the action — full traceability.

### Type discovery

To build `update{Type}`, orbital needs the type name of the diverged node. Decided approach: **carry it from edge through the divergence pipeline**.

- cb-controller already knows the type when it maps K8s paths → orbital orbIds (the mapping is built per-Kind).
- The orb mapping file (`mapping.json`) gains a `type` field per item.
- Orb's translation step (`internal/divergence/mapping.go: Resolve`) returns `(orbId, field, type)`.
- The `OverrideEntry` struct in `internal/divergence/divergence.go` gains a `Type string` field.
- Snapshots carry it; orbital's ingester persists it on `DivergenceEntry`.

Alternatives considered:
- *Query DGraph at Accept-time for `__typename`* — requires the request to specify a type already, so this is circular without a fallback. Brittle.
- *Naming convention in orbId* — fails the moment one type's orbId collides with another's.
- *Server-side registry* — adds an opaque indirection and another place to keep in sync.

Carrying it through the pipeline is the only approach that has a single source of truth (cb-controller's existing kind→orbId mapping).

## Implementation steps

### Orbital (this repo)

1. **ent schema** — add `type_name string` field to `DivergenceEntry` (`ent/schema/divergence_entry.go`). Run `go generate ./ent/...`. Update `internal/testutil/db.go` if needed (no — UPSERT on the existing unique key still works).
2. **`internal/divergence/divergence.go`** — add `Type string` to `OverrideEntry` struct (JSON key `type`).
3. **`internal/divergenceingest/store.go`** — `applySnapshot` reads `ov.Type` and calls `.SetTypeName(...)` on the new + update branches.
4. **`internal/handler/divergence.go`** — refactor `recordResolution`:
   - For `ActionAccept`: call a new private method `dispatchAcceptMutation(c, entry)` *before* the resolution write. On failure, return the error (status 400/500 depending on cause); do not record the resolution.
   - For `ActionForce` and `ActionIgnore`: behavior unchanged.
5. **`dispatchAcceptMutation`**:
   - If `entry.TypeName == ""`, return `echo.NewHTTPError(http.StatusUnprocessableEntity, "entry missing type info; update intent manually")`. (Backward-compat path for entries ingested before this change.)
   - Build mutation string: `mutation { update{Type}(input: {filter: {orbId: {eq: "{orbId}"}}, set: {{field}: {value}}}) { numUids } }`. JSON value is inserted as-is from `entry.OverrideValue` (already JSON-encoded), so booleans/numbers/strings round-trip natively. Strings need to be quoted if they aren't already — but they are, since the storage is `json.RawMessage`.
   - Dispatch via a new exported method `(h *GraphQL) DispatchMutation(c echo.Context, query string) error` on `internal/handler/graphql.go`. This is the existing handler entry point factored out so internal callers don't have to re-implement audit/authz.
   - On 200 with no GraphQL errors: success. On non-200 or GraphQL `errors[]` non-empty: return error with the GraphQL message.
6. **Tests**:
   - Unit: `divergence_test.go` — Accept with empty `TypeName` returns 422; Accept with valid type calls the dispatched mutation; mutation failure path skips resolution write.
   - Integration: new test in `internal/handler/divergence_integration_test.go` — seed a real `Server` node in DGraph with `sshEnabled: false`, insert a `DivergenceEntry` with `type_name: "Server"`, override_value `true`. Call Accept. Assert DGraph node's `sshEnabled` is now `true` AND resolution row exists.

### Orb (this repo)

1. **`internal/divergence/mapping.go`** — `MappingItem` gains `Type string` field. `Resolve` returns `(orbId, field, type, ok)`. Update tests (`mapping_test.go`).
2. **`internal/orbserver/divergence_handlers.go`** — `receiveDivergence` calls `Resolve` and includes `type` in the saved canonical `OverrideEntry`.
3. **Tests**: extend `divergence_handlers_test.go` — mapping translation populates type; snapshot serialization includes type.

### Cross-repo (configbundle, separate spike)

Out of scope for this plan; updates the contract:

- **`docs/plans/divergence-cb-controller-contract.md`** — bump mapping.json schema: each item now `{path, orbId, field, type}`. Document that orbital's Accept requires `type`; absence triggers manual-fallback.
- cb-bundler emits `type` from its existing Kind→orbId mapping logic.

### Backward compatibility

- Entries already in postgres before this change have empty `type_name`. Accept on those returns 422 with a friendly message; the admin uses the manual flow (edit the server inventory directly). No DB migration backfill needed — the entries are short-lived (next snapshot overwrites them once cb-controller is updated to emit `type`).
- Force and Ignore are unaffected — they don't need `type_name`.

## What is NOT in this plan

- Auto-deleting an entry from `DivergenceEntry` after Accept succeeds. The entry will disappear naturally on the next snapshot once cb-controller no longer detects the divergence (orbital's intent now matches edge). Until then, the entry stays visible with the green "accepted" tag.
- Multi-field accept (one button per entry → one mutation per entry). Future optimization: a "batch accept" for an entire group on the UI. Not needed for MVP.
- Concurrency control beyond DGraph's. If two admins Accept the same field at the same time, last-write-wins. That's consistent with the rest of the GraphQL surface.

## Acceptance criteria

- Clicking Accept on a divergence entry with a known `type_name` updates the corresponding DGraph node's field with the override value.
- The mutation passes through the normal `/graphql` audit path; an `events` row is written for the mutation in addition to the `resolveDivergence` event.
- If the mutation fails (network, GraphQL error, schema mismatch), no resolution is recorded and the UI shows the error.
- Existing pre-this-change divergence entries (empty `type_name`) return 422 on Accept with a clear message; Force and Ignore still work on them.
- ROADMAP Spike 22 row gains a **Phase A.1 ✅** marker once this lands.

## Plan dependencies

- This plan is independent of the configbundle Spike 7 work (Phase B). cb-controller doesn't need to be ready — once orbital + orb support `type`, manually-curl-ed test snapshots can carry it.
- The shape change to `mapping.json` and `Snapshot.overrides[]` is additive; old snapshots without `type` still ingest (the field is empty, Accept falls back to 422).
