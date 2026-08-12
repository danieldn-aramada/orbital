# DGraph Reference

Read this before: DGraph schema changes, query/mutation work, export/import, seeding, blue-green operations.

## Schema rules

- Schema changes must be **backwards compatible** — orbs may lag orbital by versions. Safe: new types, new nullable fields. Breaking: removing/renaming types or fields, adding non-null fields to existing types.
- `id: ID` must be declared on the `ConfigItem` interface — DGraph does not auto-expose internal UIDs via GraphQL without it. Without it, `getDataCenter(id: $id)` queries fail. Always keep it.
- **`@id` on `orbId` is the API-immutability mechanism — load-bearing for external consumers.** DGraph's schema generator excludes `@id` fields from the auto-generated `XPatch` input type, so `updateServer(filter:{...}, set:{orbId:"..."})` is rejected at schema-validation time. ConfigBundle (cb-controller) uses this property as the basis for SSA list-map identity across the cloud → edge boundary — see `~/armada/configbundle/docs/plans/server-identity-orbid.md`. Do NOT remove `@id` from `orbId` and do NOT add custom mutations that bypass DGraph's auto-generated Patch by allowing `orbId` to be set on existing nodes. orbId format (`<namespace>:<entity>`) is also part of this contract — changing the separator or format forces a coordinated migration in every downstream CR.
- Applying a GraphQL schema to DGraph is **additive at the RDF predicate layer**. Removing a field from GraphQL does NOT delete underlying RDF triples — data persists but is no longer queryable. To permanently remove a field and its data: `POST /alter {"drop_attr": "<predicate_name>"}`. This is irreversible.
- `cfg.SchemaPath` is the authoritative schema file path — default `schema/schema.graphql`. All handlers (export, backup, schema UI) read from this env-configurable path. Never hardcode the path directly.
- `make seed` applies schema to both DGraph instances — blue (`:8080`) and scratch (`:8081`) via `apply_schema` in `scripts/seed-dgraph.sh`.
- **Orbital does NOT re-apply `schema.graphql` to DGraph on startup — a schema-bumping image deploy does NOT reach the running DGraph by itself.** Schema is applied only by `make seed`, restore, export-to-scratch, and orb import. Deploying an image whose query requests a new field against a DGraph still on the old schema makes the query error → zero rows → 404 / silently-truncated render (real burn 2026-07-27: v0.0.25's cluster query added `retentionDays`, AKS DGraph was still v3 → every cluster 404'd, then the edit modal vanished). **Deploy step for any `schema/VERSION` bump:** after the image rollout, apply the schema to the active (blue) DGraph — `kubectl port-forward svc/<blue>-alpha 8080:8080` then `curl -X POST localhost:8080/admin/schema -H 'Content-Type: application/graphql' --data-binary @schema/schema.graphql`. Additive changes (new nullable fields) are non-destructive; do this before/with the rollout, not after users hit 404s.

## ConfigItem interface

- `Namespace` is a pure tenancy boundary — no config fields, never implements `ConfigItem`. Exists solely as an isolation scope for graph partitioning and orphan detection.
- `DataCenter implements ConfigItem` — root node for a data center's subgraph.
- **1:1 between Namespace and DataCenter** — enforced by orbital's application layer, not DGraph. Never allow multiple data centers per namespace or add config fields to `Namespace`.
- The `namespace: Namespace!` field on every `ConfigItem` is a direct reference kept for query performance — always set to the same namespace as the data center. Avoids traversing up through `DataCenter` to reach the namespace boundary.

## orbId convention

`orbId` is `@id` on the `ConfigItem` interface → **globally unique across every implementing type**. It is **always derivable, never random**: **`<namespace>:<kind>-<natural-key>`**. This makes upserts idempotent (same input → same id) and lets clients construct ids without a lookup. The rule lives in CLAUDE.md Settled Decisions; **adding a new type means adding a row here.**

| Type | `orbId` | Natural key |
|---|---|---|
| `Server` | `<ns>:server-<serial>` | **Redfish System SerialNumber** — Dell Service Tag (`BFRHDX3`) and Supermicro (`S447008X3823034`) are both just this; it's also the `<serverTag>` in the network-* ids below. **Never `asset_tag`** — that's org-assigned and can be null (was null for the A100), which breaks scan-idempotency. |
| `NetworkDevice` | `<ns>:network-device-<serial>` | switch/firewall serial |
| `NetworkAdapter` | `<ns>:network-adapter-<serverTag>-<FQDD>` | owner serial + Redfish adapter FQDD |
| `NetworkInterface` (server NIC) | `<ns>:network-interface-<serverTag>-<FQDD>` | owner serial + Redfish interface FQDD |
| `NetworkInterface` (BMC) | `<ns>:network-interface-<serverTag>-<mgmt>` | owner serial + Redfish Manager name: `iDRAC` (Dell) / `IPMI` (Supermicro) |
| `NetworkInterface` (device port) | `<ns>:network-interface-<deviceSerial>-<port>` | device serial + port (`ge-0/0/0`) |

**Legacy (pre-convention — migrate when next touched, don't treat network types as the special case):** `IPAddress` = `<ns>:<address>`, `Rack` = `<ns>:<rackName>`, `IdracSettings` = `<ns>:<serviceTag>-idrac`, cluster children = `<ns>:<clusterName>-<kind>`. (`Server` migrated to `server-<serial>` 2026-08-12.)

## Query patterns

### DQL tilde traversal (reverse edges)
DQL can follow any predicate in reverse using `~`. Used for: finding all nodes in a namespace, finding all items connected to an IP.
```
{ ip(func: eq(IPAddress.address, "10.0.1.15")) {
    uid IPAddress.address
    ~Server.oobIP { uid Server.hostname }
    ~EksaKubernetesCluster.tinkerbellIP { uid EksaKubernetesCluster.clusterType }
    ~KubernetesCluster.controlPlaneEndpoint { uid }
} }
```
Same pattern used for `~ConfigItem.namespace` to find all nodes in a namespace. DQL can traverse any predicate by UID regardless of GraphQL type boundaries. **Use the current concrete-type names from `schema/schema.graphql`** — `EksaConfig` was renamed to `EksaKubernetesCluster` and `controlPlaneIP` to `controlPlaneEndpoint` during the cluster polymorphism refactor; DQL takes the raw predicate string, so a stale name silently returns zero results.

### Reverse-pointer pattern (typed back-refs)
`@hasInverse` requires the back-ref target to **match the level where the forward edge is declared** — interface-level forward edge → interface back-ref; concrete-only forward edge → concrete back-ref. **Exception:** the top-level `ConfigItem` interface itself cannot be a `@hasInverse` target (too generic — DGraph can't reify a single back-edge predicate across every concrete type). For types like `IPAddress` referenced from many directions, the same rule applies per forward edge.

- **Back-ref naming**: `<typeName-camelCase><FieldName-PascalCase>` — e.g. `Server.oobIP` → `IPAddress.serverOobIP`; `EksaKubernetesCluster.tinkerbellIP` → `IPAddress.eksaKubernetesClusterTinkerbellIP`; `KubernetesCluster.backup` → `ClusterBackup.kubernetesClusterBackup` (or `cluster` as a friendly alias when there's no ambiguity, as we did on `ClusterBackup.cluster`). Long but mechanically derived — never invent shorter aliases for the IPAddress hub.
- **Cardinality must match reality**: the back-ref field is `T` (singular) when at most one cluster/server/node can legitimately claim a given target, and `[T]` (list) when multiple can. EKS-A workload clusters reuse their management's tinkerbell stack — so `eksaKubernetesClusterTinkerbellIP` is `[EksaKubernetesCluster]`, not singular. Server OOB IPs are 1:1 per server — so `serverOobIP` stays singular. Wrong cardinality silently corrupts data (last-write-wins on the inverse); choose based on the operational relationship, not the schema's syntactic cleanliness.
- **Interface back-refs work when forward edge is interface-level**: `IPAddress.kubernetesClusterControlPlaneEndpoint: KubernetesCluster @hasInverse(field: controlPlaneEndpoint)` works because `controlPlaneEndpoint` is on the `KubernetesCluster` interface. Likewise `ClusterBackup.cluster: KubernetesCluster @hasInverse(field: backup)` works because `backup` is on the interface. Don't fork per concrete type unless the forward edge is concrete-only (like `tinkerbellIP`, which is `EksaKubernetesCluster`-specific).

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
- **`ORBITAL_EXPORT_DIR` must be PVC-backed in AKS** — default `./subgraph-exports` is ephemeral, lost on pod restart. Set to `/scratch-exports/zips` in `deploy/base/deploy.yaml`.
- **Helm chart `backups.full.enabled` gates PVC mount on scratch DGraph** — set to `true` with never-firing cron (`"0 0 31 2 *"`) to keep PVC mounted without running backup jobs. Setting to `false` silently removes the PVC and export fails.

## Seeding

- `orbId` format convention: `"<namespace>:<entity-name>"` — e.g. `"alaska-dot:alaska-dot-galleon"`, `"alaska-dot:Rack-1"`
- Cross-type references must use `orbId`, not `name` — `orbId` is the `@id` field. Using `{ name: "..." }` fails with "field orbId cannot be empty".
- `addNamespace` takes a single object (not array): `addNamespace(input: { name: "..." }, upsert: true)`
- All ConfigItem nodes require `orbId`, `name`, `namespace`, `createdBy`, `createdAt`
- Order: `addNamespace` → `addDataCenter` → `addRack` → `addServer` in a single mutation batch
- DGraph upsert never deletes stale nodes — add explicit `deleteX` mutations in `seed-dgraph.sh` for removed nodes
- `hostname` and `rackPosition` on `Server` are **design intent** fields (admin-set, not orb scan). Convention: `r{rack:02d}-u{position:02d}.{datacenter}` — e.g. `r01-u17.alaska-dot-cruiser`
- `make seed-aks` is additive; `make seed-aks CLEAN=1` drops all data first (clean slate). Both also seed the Postgres admin user.
- **Full seed produces 1,351 config items** — 9 DC + 24 Rack + 188 Server + 155 IdracSettings + 106 StorageController + 313 StorageVolume + 368 StorageDevice + 188 IPAddress

## Redfish / hardware naming

- Redfish model convention: `PowerEdge R650`, `PowerEdge XE9680` — no "Dell" prefix in the model field. Manufacturer (`Dell`) stored as a separate field.
- Display-side field ordering rule (Server, IdracSettings, and any new detail view) is in `docs/reference/UI.md` under "Field-value row ordering": hierarchy → identity → alphabetical. Schema declaration order is NOT a display contract.
