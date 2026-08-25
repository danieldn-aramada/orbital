# Playbook: Add a new ConfigItem to orbital

Use this when you're adding a new GraphQL type to `schema/schema.graphql` that
implements the `ConfigItem` interface — i.e., something with an orbId that
lives in the parent/child relationship graph (e.g. `EtcdBackup`, `IdracSettings`,
a future `PvBackup`).

**This used to be a 13-place touch-list with silent failure modes.** Today it's
three steps because everything else is registry-driven.

---

## Step 1 — Declare in the schema

Add the type to `schema/schema.graphql`:

```graphql
type MyNewKind implements ConfigItem {
    enabled: Boolean
    schedule: String

    # Parent back-ref. Match @hasInverse cardinality to operational reality
    # (singular T if 1:1, list [T] if 1:N — see docs/reference/DGRAPH.md).
    clusterBackupMyNewKind: ClusterBackup @hasInverse(field: myNewKind)
}
```

**Cardinality gotcha:** wrong `@hasInverse` cardinality silently corrupts data
on the inverse side. If multiple parents can share this child, use `[T]`. See
`docs/reference/DGRAPH.md` "reverse-pointer pattern".

Bump `schema/VERSION` if this is a v→v+1 deployment-time schema change.

**Define this type's `orbId` convention** — `<namespace>:<kind>-<natural-key>` (kebab-case type name + a stable natural key; never random/UUID). Add a row to the per-type table in `docs/reference/DGRAPH.md`. See CLAUDE.md Settled Decisions for the rule.

---

## Step 2 — Register in the registry

Add one entry to `internal/configitems/registry.go::Types`:

```go
{
    Name:         "MyNewKind",
    OwnerType:    "ClusterBackup",          // parent type
    OwnerField:   "clusterBackupMyNewKind", // @hasInverse field on this type
    ChildField:   "myNewKind",              // field on the parent that points here
    BeforeFields: "id orbId name version enabled schedule",
    FormFields:   []string{"enabled", "schedule"},  // editor-exposed scalars
    PayloadField: "myNewKind",              // response selection for add{Kind}
},
```

**What this auto-wires:**

- `knownMutationRe` (audit allowlist) — `add/update/delete MyNewKind` now records audit events
- `typeBeforeFields` / `BeforeFields("MyNewKind")` — audit before-fetcher knows what to select
- `BuildEditTargets(...)` — page handlers' edit modal includes this kind in its JSON editor + dispatches `update{Kind}` on edit / `add{Kind}` on first-time create
- `configitem-editor.js` (the JS module) — consumes the registry-derived targets blob from any page that exports it; no JS edits needed
- **ownership (Spike 33)** — the export **diff-preview rollup** (`graphdiff.ownerEdges`) AND the **audit related-orbId collector** BOTH derive from this entry's `OwnerType`/`OwnerField`/`ChildField`, so they can never drift from each other again. For a **multi-parent** type — owned by more than one type, or where the rollup parent differs from the down-path (e.g. `NetworkInterface`, `IPAddress`, `StorageVolume`) — declare an ordered `OwnerEdges []OwnerEdge` (most-specific first; the first present edge on a node is its canonical parent, à la Kubernetes `controller:true`)

Pin behavior with `internal/configitems/registry_test.go` (parity) and
`schema_consistency_test.go` (the R3 guard — fails the build if the registry's
ownership fields drift from `schema.graphql`, or a new ConfigItem type is
unregistered).

---

## Step 3 — Extend the parent handler's GraphQL query

The page handler that renders the parent (e.g. `internal/handler/cluster.go`)
needs to fetch the new child's fields so the JSON editor displays them. Add
the new fields to the cluster's GraphQL `getCluster` query and to the
response struct.

This is the **only handler-side change required**. Everything downstream —
audit pipeline, edit-target JSON, JS submit handler, diff rendering — picks
it up automatically from the registry.

If the new type is **a new owned child of an existing parent**, audit
aggregation surfaces its events on the parent's Audit Log tab **automatically** —
the single generic `collectRelatedOrbIDs` (`internal/handler/related_orbids.go`)
derives owned children from the registry (`configitems.OwnedChildren`), so no
hand-written walker to update (Spike 33 removed the per-type ones). Just make
sure your `OwnerType`/`OwnerField`/`ChildField` (and `OwnerEdges` if multi-parent)
are correct — the R3 test enforces they match the schema.

---

## What you do NOT need to touch (and shouldn't)

These all derive from the registry — modifying them by hand will drift from
the registry and produce silent bugs:

- `knownMutationRe` — derived from `Types[].Name`
- `typeBeforeFields` — derived from `Types[].BeforeFields`
- `configitem-editor.js` mutation shapes — driven by the targets blob
- Audit diff rendering — generic, no per-type code
- **The export / subgraph selection** — orbital's export is schema-driven, not
  hand-enumerated. `fetchUIDPredicates` derives the edge list from the live
  DGraph schema (`schema {}`) and scalars come via `expand(_all_)`
  (`internal/handler/export.go`). A new type, its edges, and its scalar fields
  flow into the export automatically once the schema is applied — **no
  export-code change, ever.** ⚠ This is only *orbital's* export. A **downstream**
  consumer with a hand-enumerated GraphQL query — notably the configbundle
  bundler's `ConfigBundleByOrbID` query — must add the new field on *its* side;
  that's the consumer's change, not orbital's. Don't conflate the two.

If your new type needs behavior the registry doesn't model today (e.g. a new
relationship cardinality, a wrapper type pattern, computed fields), extend
the registry struct + add the derivation logic in `registry.go`. **Don't
fork off a parallel hand-maintained map** — that's the bug class this whole
refactor exists to prevent.

---

## Validating end-to-end

After the three steps:

```bash
go test ./internal/configitems/   # registry parity test catches the most common misses
go build ./...
make run-orbital                  # browser-test the edit modal
```

In the browser:

1. Open the parent page, click Edit
2. The new kind appears as a key in the JSON editor (under its `ChildField` path)
3. Edit a field, save
4. Audit Log tab shows `update<MyNewKind>` row with green/red field diff
5. Delete the JSON key, save → `add<MyNewKind>` row on next configure (first-time create flow)

If the audit row is **missing** → check `knownMutationRe` is picking it up
(usually a registry typo).
If the audit row appears but **shows no diff** → check `BeforeFields` includes
the editable fields (the diff renderer needs both sides).
If the new kind **doesn't appear in the editor** → check the parent handler's
GraphQL query fetches the new fields, and that `BuildEditTargets` sees the
new type as a child of the parent.

---

## Reference reading

- `docs/reference/UI.md` — Edit pattern, JSON editor convention
- `docs/reference/AUDIT.md` — audit pipeline architecture
- `docs/reference/DGRAPH.md` — schema conventions, @hasInverse cardinality
- `internal/configitems/registry.go` — the registry itself, with comments on each field
- `web/shared/static/configitem-editor.js` — generic JS submit handler
