# DGraph Reference

Read this before: DGraph schema changes, query/mutation work, export/import, seeding, blue-green operations.

## Schema rules

- Schema changes must be **backwards compatible** — orbs may lag orbital by versions. Safe: new types, new nullable fields. Breaking: removing/renaming types or fields, adding non-null fields to existing types.
- `id: ID` must be declared on the `ConfigItem` interface — DGraph does not auto-expose internal UIDs via GraphQL without it. Without it, `getDataCenter(id: $id)` queries fail. Always keep it.
- Applying a GraphQL schema to DGraph is **additive at the RDF predicate layer**. Removing a field from GraphQL does NOT delete underlying RDF triples — data persists but is no longer queryable. To permanently remove a field and its data: `POST /alter {"drop_attr": "<predicate_name>"}`. This is irreversible.
- `cfg.SchemaPath` is the authoritative schema file path — default `schema/schema-demo.graphql`. All handlers (export, backup, schema UI) read from this env-configurable path. Never hardcode `schema/schema-v1.graphql`.
- `make seed` applies schema to both DGraph instances — blue (`:8080`) and scratch (`:8081`) via `apply_schema` in `scripts/seed-dgraph.sh`.

## ConfigItem interface

- `Namespace` is a pure tenancy boundary — no config fields, never implements `ConfigItem`. Exists solely as an isolation scope for graph partitioning and orphan detection.
- `DataCenter implements ConfigItem` — root node for a data center's subgraph.
- **1:1 between Namespace and DataCenter** — enforced by orbital's application layer, not DGraph. Never allow multiple data centers per namespace or add config fields to `Namespace`.
- The `namespace: Namespace!` field on every `ConfigItem` is a direct reference kept for query performance — always set to the same namespace as the data center. Avoids traversing up through `DataCenter` to reach the namespace boundary.

## Query patterns

### DQL tilde traversal (reverse edges)
DQL can follow any predicate in reverse using `~`. Used for: finding all nodes in a namespace, finding all items connected to an IP.
```
{ ip(func: eq(IPAddress.address, "10.0.1.15")) {
    uid IPAddress.address
    ~Server.oobIP { uid Server.hostname }
    ~EksaConfig.tinkerbellIP { uid EksaConfig.clusterType }
    ~EksaConfig.controlPlaneIP { uid EksaConfig.clusterType }
} }
```
Same pattern used for `~ConfigItem.namespace` to find all nodes in a namespace. DQL can traverse any predicate by UID regardless of GraphQL type boundaries.

### IPAddress hub pattern (typed back-refs)
`@hasInverse` in DGraph requires both sides to be the same **concrete type** — cannot use the `ConfigItem` interface as a back-ref target. Solution: explicit named back-ref fields on `IPAddress` for each concrete type that references it (`serverOobIP: Server`, `eksaConfigTinkerbellIP: EksaConfig`, `eksaControlPlaneIP: EksaConfig`). Adding a new type connected to an `IPAddress` requires a new back-ref field — this is a deliberate, versioned schema change.

### Cross-type IP queries
GraphQL cannot traverse typed back-refs polymorphically. For queries like "is this IP already assigned anywhere?" use DQL via `/query` with tilde predicates (see above).

### DGraph update mutation syntax
`update{Type}(input: { filter: ..., set: ... })` — filter and set are wrapped inside `input`, not top-level args.

### GraphQL get vs query
`get{Type}(id: ID!)` — reliable for most types. For acronym-named types (e.g. `IPAddress`), prefer `query{Type}(filter: { orbId: { eq: $orbId } })` which is more reliable than `getIPAddress`.

## Blue-green DGraph topology

- **Blue:** live, serves Topology API and all client queries. Never expose to external clients directly.
- **Scratch (green):** idle-warm, used exclusively for export and validation. Never exposed to external clients.
- One shared blue instance serves all data centers. `DataCenter` is the root partitioning node. Do not design for multi-instance blue topology.
- **Scratch wiped at START of each export** (`drop_all`) — prevents stale data from prior exports bleeding in. A wipe-at-end-only approach caused stale data in subsequent exports.
- **Export jobs globally serialized** — scratch is shared state; only one may be pending/running at a time across all data centers. Returns 409 if another is in progress.
- **Per-job scratch export directories** — each job writes to `/dgraph/export/<jobID>/` inside scratch container (host-side: `DGRAPH_SCRATCH_EXPORT_DIR/<jobID>/`). Container-side base path `/dgraph/export` is hardcoded; only host-side mount path is configurable. Directory persists until user deletes the job — never auto-cleaned.
- **DGraph export `destination` parameter** — routes output to a specific path. DGraph writes a timestamped subdirectory (`dgraph.r<raft>.u<date>.<time>/`) inside the destination.
- **`ORBITAL_EXPORT_DIR` must be PVC-backed in AKS** — default `./subgraph-exports` is ephemeral, lost on pod restart. Set to `/scratch-exports/zips` in `deploy/dev/deploy.yaml`.
- **Helm chart `backups.full.enabled` gates PVC mount on scratch DGraph** — set to `true` with never-firing cron (`"0 0 31 2 *"`) to keep PVC mounted without running backup jobs. Setting to `false` silently removes the PVC and export fails.

## Seeding

- `orbId` format convention: `"<namespace>:<entity-name>"` — e.g. `"alaska-dot:alaska-dot-galleon"`, `"alaska-dot:Rack-1"`
- Cross-type references must use `orbId`, not `name` — `orbId` is the `@id` field. Using `{ name: "..." }` fails with "field orbId cannot be empty".
- `addNamespace` takes a single object (not array): `addNamespace(input: { name: "..." }, upsert: true)`
- All ConfigItem nodes require `orbId`, `name`, `namespace`, `createdBy`, `createdAt`
- Order: `addNamespace` → `addDataCenter` → `addRack` → `addServer` in a single mutation batch
- DGraph upsert never deletes stale nodes — add explicit `deleteX` mutations in `seed-dgraph.sh` for removed nodes
- `hostname` and `rackPosition` on `Server` are **design intent** fields (admin-set, not orb scan). Convention: `r{rack:02d}-u{position:02d}.{datacenter}` — e.g. `r01-u17.alaska-dot-cruiser`
- `make seed-aks-clean` drops all data before seeding (clean slate). `make seed-aks` does NOT drop first.
- **Full seed produces 1,351 config items** — 9 DC + 24 Rack + 188 Server + 155 IdracSettings + 106 StorageController + 313 StorageVolume + 368 StorageDevice + 188 IPAddress

## Redfish / hardware naming

- Redfish model convention: `PowerEdge R650`, `PowerEdge XE9680` — no "Dell" prefix in the model field. Manufacturer (`Dell`) stored as a separate field.
- Server summary field ordering: Data Center first (reflects DC→server hierarchy), then all remaining fields alphabetically.
