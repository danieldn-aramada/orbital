# 004 — Replace `namespace` Edge with `namespaceID` Scalar on ConfigItem

**Status:** Proposed (not yet executed)

**Date:** 2026-06-03

---

## Problem

`ConfigItem` has `namespace: Namespace!` — a DGraph edge to a `Namespace` node. This edge:

- **Cannot be filtered on** in DGraph's auto-generated GraphQL. `@search` only works on scalar fields, not relationship fields. There is no way to write `queryServer(filter: { namespace: { name: { eq: "..." } } })`.
- **Cannot be filtered on** in Topology API consumers like Atlas. Any consumer that needs namespace-scoped queries must either traverse the full graph from a known DataCenter, or fetch everything and filter client-side.

This blocks Atlas integration. Atlas builds a per-data-center digital twin and needs to scope any `query*` call to a namespace.

---

## What the `namespace` Edge Currently Does

### 1. Subgraph copy (export pipeline)

`fetchNamespaceSubgraph` in `internal/handler/export.go` uses this DQL pattern:

```dql
var(func: type(Namespace)) @filter(eq(Namespace.name, %q)) { NS as uid }
items(func: has(ConfigItem.namespace)) @filter(uid_in(ConfigItem.namespace, uid(NS))) {
    uid dgraph.type expand(_all_)
}
edges(func: has(ConfigItem.namespace)) @filter(uid_in(ConfigItem.namespace, uid(NS))) {
    uid <uid predicates...>
}
```

Two-step: find the Namespace node UID, then `uid_in` filter on the edge.

### 2. DC info lookup

`fetchDCInfo` traverses `getDataCenter(orbId: ...) { namespace { name } }` to get the namespace name for the export DQL query.

### 3. Mutations

Every `add*` mutation (seed files, UI-driven GraphQL) takes `namespace: { name: "..." }` as a linked object input. The Namespace node must be pre-created in DGraph before any config item can reference it.

### 4. UI display

`datacenter.go` renders `dc.Namespace.Name` in the DC detail view. This is a GraphQL traversal, not a filter.

---

## Options Considered

### Option A: Keep edge + add `namespaceID` scalar (redundant)

Two representations of the same fact on every node.

**Problems:**
- Every `add*` mutation must set both `namespace: { name: "..." }` and `namespaceID: "..."`. Miss one and the subgraph export (edge-based) and Atlas queries (scalar-based) return different result sets silently.
- `dgraph_id` field on ent `namespaces` schema signals the cross-store consistency burden — this option doubles down on it.

### Option B: Replace edge with scalar, keep `Namespace` node ✓ Recommended

Add `namespaceID: String! @search(by: [exact])` to `ConfigItem`. Remove `namespace: Namespace!` edge from `ConfigItem`. **Keep the `Namespace` type** — DGraph remains authoritative for namespace identity. Drop the PostgreSQL `namespaces` table, which is confirmed unused (ent `Edges()` returns nil, `db.Namespace` is never called in any handler).

### Option C: Status quo

Atlas integration stays blocked. Not viable.

---

## Why Option B

### The edge is the problem, not the node

The `namespace: Namespace!` edge has one functional job — powering the `uid_in` DQL export pattern. That job is done more cleanly by a scalar. The `Namespace` node itself is valuable: it is DGraph's authoritative record of namespace existence, it travels with the exported subgraph so orb has the full picture, and it carries `createdBy`/`createdAt` for the namespace audit trail.

```dql
# Before (two steps, edge-based)
var(func: type(Namespace)) @filter(eq(Namespace.name, %q)) { NS as uid }
items(func: has(ConfigItem.namespace)) @filter(uid_in(ConfigItem.namespace, uid(NS))) { ... }

# After (one step, scalar-based; Namespace node fetched separately by name)
ns(func: type(Namespace)) @filter(eq(Namespace.name, %q)) { uid dgraph.type expand(_all_) }
items(func: eq(ConfigItem.namespaceID, %q)) { ... }
```

### The PostgreSQL `namespaces` table is confirmed unused

Code audit findings:
- `ent/schema/namespace.go` has `Edges() []ent.Edge { return nil }` — nothing in PostgreSQL FKs to it.
- `db.Namespace` (the ent client) is never called in any handler — no query, create, or delete.
- All Go references to `Namespace.Name` are DGraph GraphQL response fields, not PostgreSQL reads.
- The only Go reference to the table is `internal/testutil/db.go:64` — test cleanup truncation.

DGraph is already the de facto authority. The PostgreSQL table is a vestigial mirror that was never read.

### Architectural principle

DGraph owns config data (config items, their relationships, namespace identity).
PostgreSQL owns service data (users, sessions, jobs, audit log, export jobs).
The namespace name string is the shared identifier — no cross-store pointer needed.

---

## namespaceID Value

**The namespace name string.** Same value as `Namespace.name` in DGraph (which has `@id` — the unique identifier). Examples: `"colo"`, `"alaska-dot-galleon"`, `"seattle"`.

**Source at creation time:** orbital's app layer. The namespace is always known from HTTP request context when any config item is created.

**Already embedded in orbId:** every config item's orbId is `namespace:resource-slug` (e.g., `colo:colo-galleon`). The namespace name is `strings.Split(orbId, ":")[0]`. The backfill can derive it purely from existing data without any cross-store lookup.

---

## Migration Steps

### Step 1 — Add scalar (additive, safe, deploy independently)

Add to `ConfigItem` interface in `schema/schema-demo.graphql`:

```graphql
interface ConfigItem {
    id: ID!
    namespaceID: String! @search(by: [exact])   # new
    namespace: Namespace!                        # keep until step 5
    orbId: String! @id @search(by: [hash])
    ...
}
```

Apply schema to DGraph. No existing data breaks — new field is absent on existing nodes until backfill.

### Step 2 — Backfill existing nodes

For every config item node in DGraph, set `namespaceID` = namespace name, derivable from orbId:

```
namespaceID = strings.Split(orbId, ":")[0]
```

One-off DQL mutation or script. No cross-store join required.

### Step 3 — Update export DQL

In `internal/handler/export.go`, `fetchNamespaceSubgraph`:

```go
// Before
dql := fmt.Sprintf(`{
    var(func: type(Namespace)) @filter(eq(Namespace.name, %q)) { NS as uid }
    ns(func: uid(NS)) { uid dgraph.type expand(_all_) }
    items(func: has(ConfigItem.namespace)) @filter(uid_in(ConfigItem.namespace, uid(NS))) { ... }
    edges(func: has(ConfigItem.namespace)) @filter(uid_in(ConfigItem.namespace, uid(NS))) { ... }
}`, namespaceName, ...)

// After — Namespace node fetched directly by name; config items filtered by scalar
dql := fmt.Sprintf(`{
    ns(func: type(Namespace)) @filter(eq(Namespace.name, %q)) { uid dgraph.type expand(_all_) }
    items(func: eq(ConfigItem.namespaceID, %q)) { uid dgraph.type expand(_all_) }
    edges(func: eq(ConfigItem.namespaceID, %q)) { uid %s }
}`, namespaceName, namespaceName, namespaceName, edgeLines.String())
```

No more `var` block or `uid_in`. The `NS` variable is gone.

### Step 4 — Update `fetchDCInfo`

```go
// Before: traverse namespace edge
query := fmt.Sprintf(`{ getDataCenter(orbId: %q) { name orbId namespace { name } } }`, dcOrbID)
// ... decode dc.Namespace.Name

// After: read namespaceID directly
query := fmt.Sprintf(`{ getDataCenter(orbId: %q) { name orbId namespaceID } }`, dcOrbID)
// ... decode dc.NamespaceID
```

Function signature `(name, orbID, namespaceName string, err error)` stays the same; third return value is now `dc.NamespaceID`.

### Step 5 — Remove `namespace` edge from `ConfigItem` (breaking, schema version bump)

Remove from `schema/schema-demo.graphql`:
- `namespace: Namespace!` from the `ConfigItem` interface only
- **Keep the `Namespace` type** — it is still a standalone DGraph entity

This is a breaking schema change. Coordinate with a schema version bump. Since orb always does `drop_all` + live load on import, any orb that imports a new snapshot cleanly replaces the old schema.

### Step 6 — Update all `add*` mutations and seed files

Every mutation input that currently sets `namespace: { name: "..." }` changes to `namespaceID: "..."`.

Affected:
- All 22 files under `examples/seed/*.graphql` — mechanical find/replace
- Any handler that constructs GraphQL mutation strings (grep `internal/handler/` for `namespace:`)
- The `addNamespace` call at the top of each seed file stays — the Namespace node is still created

### Step 7 — Update DC detail view

`datacenter.go` and `orbserver/dc_handlers.go` render `dc.Namespace.Name` / `raw.Namespace.Name`.
Replace with `dc.NamespaceID` / `raw.NamespaceID` (same string value, direct field instead of traversal).
Same fix needed in `internal/orbital-cli/get_dc.go` and `internal/orbserver/server_handlers.go`.

### Step 8 — Drop PostgreSQL `namespaces` table

1. Remove `ent/schema/namespace.go`
2. Run `go generate ./ent/...` to regenerate ent client
3. Remove `"namespaces"` from `internal/testutil/db.go` truncation list
4. Write and run a PostgreSQL migration to `DROP TABLE namespaces`

No cascade effects — confirmed no FK relationships in ent.

---

## What Atlas Gets

After migration, Atlas (or any Topology API consumer) can write:

```graphql
queryServer(filter: { namespaceID: { eq: "colo" } }) {
    orbId hostname model rack { orbId }
}

queryIPAddress(filter: { namespaceID: { eq: "colo" } }) {
    address role
}
```

Indexed, DB-level, works on every config item type through the interface, no knowledge of DGraph graph topology required.

---

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Steps 1–4 deployed without step 5 — edge and scalar coexist | Acceptable transient state: export uses scalar (step 3), UI uses edge for display (step 7 pending). Both are set on creation. |
| Backfill misses a node | orbId is `@id` — every config item has one. Backfill query: `has(orbId)` covers all config items regardless of type |
| Seed files updated but handlers miss a mutation string | Grep for `namespace:` in `internal/handler/` and `examples/seed/` after step 6 |
| Schema version bump coordination | Bump `schema_versions` table entry; orbital applies on startup |
