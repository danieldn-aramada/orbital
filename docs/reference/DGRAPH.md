# DGraph Reference

Read this before: DGraph schema changes, query/mutation work, export/import, seeding, blue-green operations.

## Settled Decisions

- **A DQL delete does NOT maintain `@hasInverse` — clear the surviving parent's edge yourself.** *(Added 2026-09-23.)* `@hasInverse` is a GraphQL-layer construct: DGraph keeps the two forward predicates in step only for mutations through its **GraphQL** endpoint. Orbital's cascade delete (`bulkDeleteGuarded`) is a **DQL** upsert — deliberately, because that is the only way to get a version-guarded CAS — so an `S * *` delete clears the child and leaves the parent's list edge pointing at an empty uid.
  ⚠️ **The consequence is not cosmetic.** Any later GraphQL query that walks that edge and selects a non-nullable field fails **entirely**, because DGraph propagates the error to the root: *"Non-nullable field 'orbId' (type String!) was not present in result from Dgraph."* The export subgraph query is exactly that shape, so **one cluster delete permanently broke export for its whole data centre** — while the delete returned `200` with a correct audit event, and the damage surfaced later, in a different subsystem, in an error naming neither the delete nor the node. Eight such corpses accumulated on `colo-galleon`, one per e2e run, unnoticed until someone tried to export.
  **Rule:** any DQL write that removes a node must also remove the edges held by nodes that **SURVIVE** it, in the same transaction. Only survivors matter — when both ends are deleted the stale edge sits on a tombstone and is unreachable, which is what keeps this to a handful of cases rather than all 28 `@hasInverse` pairs. Today: `DataCenter.kubernetesClusters` (cluster delete); `DataCenter.servers`, `Rack.servers`, `KubernetesNode.server` (server delete); none for a data centre, which is the top of its own subtree. `TestDelete_LeavesNoDanglingParentEdge` reproduces the failure and is verified to fail without the fix. The same obligation applies to **any** new DQL write path, not just deletes.

## Schema rules

- Schema changes must be **backwards compatible** — orbs may lag orbital by versions. Safe: new types, new nullable fields. Breaking: removing/renaming types or fields, adding non-null fields to existing types.
- `id: ID` must be declared on the `ConfigItem` interface — DGraph does not auto-expose internal UIDs via GraphQL without it. Without it, `getDataCenter(id: $id)` queries fail. Always keep it.
- **`@id` on `orbId` is the API-immutability mechanism — load-bearing for external consumers.** DGraph's schema generator excludes `@id` fields from the auto-generated `XPatch` input type, so `updateServer(filter:{...}, set:{orbId:"..."})` is rejected at schema-validation time. ConfigBundle (cb-controller) uses this property as the basis for SSA list-map identity across the cloud → edge boundary — see `~/armada/configbundle/docs/plans/server-identity-orbid.md`. Do NOT remove `@id` from `orbId` and do NOT add custom mutations that bypass DGraph's auto-generated Patch by allowing `orbId` to be set on existing nodes. orbId format (`<namespace>:<entity>`) is also part of this contract — changing the separator or format forces a coordinated migration in every downstream CR.
- Applying a GraphQL schema to DGraph is **additive at the RDF predicate layer**. Removing a field from GraphQL does NOT delete underlying RDF triples — data persists but is no longer queryable. To permanently remove a field and its data: `POST /alter {"drop_attr": "<predicate_name>"}`. This is irreversible.
- `cfg.SchemaPath` is the authoritative schema file path — default `schema/schema.graphql`. All handlers (export, backup, schema UI) read from this env-configurable path. Never hardcode the path directly.
- `make seed` applies schema to both DGraph instances — blue (`:8080`) and scratch (`:8081`) via `apply_schema` in `scripts/seed-dgraph.sh`. The integration suite's own cluster (`:8083`) is deliberately NOT seeded here: `TestMain` applies the schema and its own fixtures, and `make seed` must not touch it — nor it, blue.
- **Orbital does NOT re-apply `schema.graphql` to DGraph on startup — a schema-bumping image deploy does NOT reach the running DGraph by itself.** Schema is applied only by `make seed`, restore, export-to-scratch, and orb import. Deploying an image whose query requests a new field against a DGraph still on the old schema makes the query error → zero rows → 404 / silently-truncated render (real burn 2026-07-27: v0.0.25's cluster query added `retentionDays`, AKS DGraph was still v3 → every cluster 404'd, then the edit modal vanished). **Deploy step for any `schema/VERSION` bump:** after the image rollout, apply the schema to the active (blue) DGraph — `kubectl port-forward svc/<blue>-alpha 8080:8080` then `curl -X POST localhost:8080/admin/schema -H 'Content-Type: application/graphql' --data-binary @schema/schema.graphql`. Additive changes (new nullable fields) are non-destructive; do this before/with the rollout, not after users hit 404s.
- **The two existing apply paths read DIFFERENT files, and nothing reconciles them.** A **restore** applies the schema baked into the *running image* (`restore.go:502` → `applyBlueSchema`, reading `cfg.SchemaPath` inside the container); **`make seed-aks-dgraph`** applies the one in your *local working tree* (`seed-aks.sh` → `seed-dgraph.sh:40`). Seeding a cluster from a checkout that is ahead of the deployed image silently puts DGraph on a schema the running code does not expect — and the reverse leaves a newer image querying fields DGraph lacks. **Before seeding a cluster you did not just deploy to, check which image is running.** Not hypothetical: the 2026-07-27 outage above is the same mismatch, arrived at by skipping the apply rather than by applying the wrong file.
- **Enums are a GraphQL-layer constraint only — they are NOT enforced on data at rest.** DGraph serializes enum values as strings (`DataCenter.model` is `type: string, tokenizer: [hash]` in the DQL schema); `dgraph live` writes DQL predicates directly and never sees the GraphQL schema. So restore, orb import, and any live-loader path can land a value outside the enum. **Verified 2026-09-17 on local blue, dgraph v25.3.1:** a DQL `set` of `"Galleon"` onto `DataCenter.model` returned `code: Success` with no error, and DQL read it back verbatim. Reading that node through GraphQL then **degrades, it does not fail** — `data` is still returned with `model: null`, and an entry appears in `errors`: `Error coercing value '"Galleon"' for field 'model' to type DataCenterModel`. A `queryDataCenter` list returned all 10 rows with the error scoped to `path: [queryDataCenter, 9, model]`; the other rows were unaffected. ⚠️ **The trap is a client that reads `data` and ignores `errors` — it sees `model: null` and cannot distinguish "unset" from "corrupt".** Treat an enum as a contract for API callers, not a guarantee about what is in the graph. Valid `@search` indexes for enums are `hash`, `exact`, `regexp` — **`term` and `trigram` are string-only and will be rejected.** Enum values must also be valid GraphQL names (`[_A-Za-z][_0-9A-Za-z]*`): no spaces, hyphens, or leading digits, so a product name like `Cruiser-2` cannot be an enum member without a display-name mapping.
  - **⚠️ `v7` adds `@search` to `ConfigItem.version` — an index apply BLOCKS.** DGraph reindexes the predicate across every ConfigItem before `/admin/schema` returns, and mutations wait behind it. Additive and non-destructive, but schedule it like a migration. **Schema before code**; the wrong order fails visibly and harmlessly — a DGraph on `v6` answers `Field "version" is not defined by type ServerFilter` and the mutation is refused unwritten.
  - **⚠️ `v9` adds `DataCenter.model` (`enum DataCenterModel`).** Additive and non-blocking. Chosen over `String` so consumers (AEP) read the valid set by introspection instead of hardcoding it — the first enum in this schema; `NetworkDevice.role` remains a String with a comment. **Adding a model is a schema change + `VERSION` bump + an apply to every DGraph**, unlike a String where a new value is just data.

**`Server.uHeight` is how many units a server OCCUPIES; `Rack.uHeight` is how
many the rack HAS.** Both nullable — `0` means a rack-mounted device consuming no
unit, which is not "unset". Server height is a MODEL-level fact stored per
instance; promote it to a model entity when a SECOND such fact appears (depth,
power draw, weight), not before.

Values were harvested from NetBox before its decommission; two of thirteen
models resolved only by joining on serviceTag or interface MAC, not by model
string, so the mapping is not reconstructible from orbital alone.

## ConfigItem interface

- `Namespace` is a pure tenancy boundary — no config fields, never implements `ConfigItem`. Exists solely as an isolation scope for graph partitioning and orphan detection.
- `DataCenter implements ConfigItem` — root node for a data center's subgraph.
- **1:1 between Namespace and DataCenter** — enforced by orbital's application layer, not DGraph. Never allow multiple data centers per namespace or add config fields to `Namespace`.
- The `namespace: Namespace!` field on every `ConfigItem` is a direct reference kept for query performance — always set to the same namespace as the data center. Avoids traversing up through `DataCenter` to reach the namespace boundary.

## orbId convention

`orbId` is `@id(interface: true)` on the `ConfigItem` interface — unique **across every implementing type**, enforced by DGraph as of **schema v8 (2026-09-17)**. A second type reusing an existing orbId is refused: *"already exists for field orbId in some other implementing type of interface ConfigItem"*. Before v8 the directive was a bare `@id`, which DGraph scopes **per implementing type** — a `Rack` and a `Server` could hold the same orbId, and only cross-type *convention* kept them apart. **The directive is NOT retroactive**: altering the schema succeeds even with duplicates already stored, without scanning or rejecting them, so applying it does not prove a graph is clean (verified on v25.3.1). The audit that does is at the end of this section; it passed 2064/2064 with 0 collisions before v8 was applied. **Keep following the prefix convention anyway** — it is what makes an orbId readable and derivable, and the constraint is a backstop, not a substitute. It is **always derivable, never random**: **`<namespace>:<kind>-<natural-key>`**. This makes upserts idempotent (same input → same id) and lets clients construct ids without a lookup. The rule lives in CLAUDE.md Settled Decisions; **adding a new type means adding a row here.**

| Type | `orbId` | Natural key |
|---|---|---|
| `Server` | `<ns>:server-<serviceTag>` | **The vendor's stable chassis identifier**, stored on `Server.serviceTag`. **Dell:** Redfish `ComputerSystem.SKU` — the Service Tag (`CFRHDX3`). **Supermicro:** Redfish `ComputerSystem.SerialNumber` (`S447008X3823034`), because Supermicro leaves SKU unset *(vendor behaviour — not verified against a live BMC; the Dell half was)*. **NOT Dell's `SerialNumber`** — on 15G that is a different value (R650: SKU `CFRHDX3` vs SerialNumber `MXFC400359006Z`, verified on iDRAC 7.20.10.05); on earlier generations the two coincide (R450: `DLP6K74` for both), which is what made the old "SerialNumber" wording look correct. `Server.serialNumber` holds the raw SerialNumber and is **never** an identity key. This same value is the `<serviceTag>` in the network-* ids below. **Never Redfish `AssetTag`** — org-assigned and often empty (`""` on the R650, null for the A100), which breaks scan-idempotency. |
| `ServerMaintenance` | `<ns>:server-maintenance-<serviceTag>` | owner server `serviceTag` — 1:1 with `Server`, so the natural key is just the owner's serviceTag (same value as `server-<serviceTag>`). No discriminator: one maintenance node per server (intent, not history — history lives in the audit log + edge Events). |
| `NetworkDevice` | `<ns>:network-device-<serial>` | switch/firewall serial |
| `NetworkAdapter` | `<ns>:network-adapter-<serviceTag>-<FQDD>` | owner serviceTag + Redfish adapter FQDD |
| `NetworkInterface` (server NIC) | `<ns>:network-interface-<serviceTag>-<FQDD>` | owner serviceTag + Redfish interface FQDD |
| `NetworkInterface` (BMC) | `<ns>:network-interface-<serviceTag>-<mgmt>` | owner serviceTag + Redfish Manager name: `iDRAC` (Dell) / `IPMI` (Supermicro) |
| `NetworkInterface` (device port) | `<ns>:network-interface-<deviceSerial>-<port>` | device serial + port (`ge-0/0/0`) |

**Legacy (pre-convention — migrate when next touched, don't treat network types as the special case):** `IPAddress` = `<ns>:<address>`, `Rack` = `<ns>:<rackName>`, `IdracSettings` = `<ns>:<serviceTag>-idrac`, cluster children = `<ns>:<clusterName>-<kind>`. (`Server` migrated to `server-<serviceTag>` 2026-08-12.)

**The audit — run it before applying the constraint to any graph, and after a bulk import:**

```bash
curl -s localhost:8080/query -H 'Content-Type: application/json' \
  -d '{"query":"{ q(func: has(ConfigItem.orbId)) { ConfigItem.orbId dgraph.type } }"}' \
| python3 -c "
import json,sys,collections
n=json.load(sys.stdin)['data']['q']
by=collections.defaultdict(set)
for x in n:
    by[x['ConfigItem.orbId']].add(tuple(sorted(t for t in x.get('dgraph.type',[]) if t!='ConfigItem')))
dups={k:v for k,v in by.items() if len(v)>1}
print(f'nodes {len(n)}  distinct orbIds {len(by)}  cross-type collisions {len(dups)}')
for k,v in list(dups.items())[:10]: print('  COLLISION', k, v)
"
```

A clean graph reports equal node and orbId counts and zero collisions. **A collision found here cannot be fixed by the constraint** — it is already stored, and it breaks reads today: `getConfigItem` on a duplicated orbId returns *"A list was returned, but GraphQL was expecting just one item"*, and `internal/graphdiff` keys its `Snapshot` by orbId (`graphdiff.go:203`), so one of the two nodes silently disappears from every diff, export preview and change-request base capture.

## ConfigItem ownership (owned-child model)

> **Ownership is declared in code, never as runtime data — and this is a settled non-goal, with evidence.** The comparative case against operator-tunable ownership is **ServiceNow CMDB**: there, which types may contain which lives in `cmdb_rel_type_suggest`, which is **advisory only** — it populates editor suggestions but does not block an off-model edge. Because that type-policy is admin-edited data with no build step, a new CI class introduces drift caught only by CMDB governance, never by code review. Every other surveyed system does the opposite (NetBox `parent_object`, Kubernetes `controller:true`, Backstage's single declaration) and makes ownership a compile-time property. Orbital follows them: `internal/configitems/registry.go` is the single declaration, and `schema_consistency_test.go` fails the build on drift. Do **not** reintroduce runtime-configurable ownership.
>
> **Precedence must stay an ordered slice, never a map.** `Type.OwnerEdges` is order-sensitive (most-specific-first: `NetworkInterface` nests under adapter before server before device). Go map iteration is randomized, so a map here yields a non-deterministic presentation parent.
>
> **Cross-namespace ownership is out of scope — and deliberately not enforced in code.** Ownership models physical/logical containment, so a `colo` server cannot contain an `alaska` disk; Kubernetes forbids the equivalent outright. Nothing in orbital can produce such an edge: `orbId` is `<namespace>:<kind>-<natural-key>`, and the editor derives a child's namespace from its parent on create. It would take a hand-written mutation deliberately pairing orbIds across two namespaces.
>
> **Do not "add a check to the schema-consistency test" for this** — that test is type-level (it parses `schema.graphql` and validates registry declarations), while namespace is instance data. The check is not expressible there; enforcement would have to be a mutation-time guard or a data-integrity query, which is not worth building for a case nothing produces.
>
> Worth knowing if it ever *did* occur: the failure is silent, not loud. Export scopes a data center by namespace filter (`eq(ConfigItem.namespace, …)`), not by graph traversal, so a cross-namespace child would be **excluded from its owner's artifact**, leaving a dangling edge with no error.


Some ConfigItems are **owned children** of another: they model physical/logical containment (`StorageDevice` ∈ `StorageController` ∈ `Server`; `NetworkInterface` on a `NetworkAdapter` ∈ `Server`; `ServerMaintenance` for a `Server`). Ownership is a **presentation / aggregation** concept, **never actuation** — edge controllers never consume it. It drives: nested JSON editing (children edited through the owner's tree, see UI.md), audit rollup (an owner's audit tab aggregates its children's events — `collectRelatedOrbIDs`), and delete-cascade. It does **not** drive the export diff preview — see the note below.

**Two layers, two homes:**
- **Ownership *instances*** ("this maintenance belongs to *that* server") already live in the CMDB — they ARE the child→owner **edge** in DGraph (`ServerMaintenance.server`), read live wherever needed.
- **Ownership *type-policy*** ("which edge *types* are containment, and their precedence") is **schema metadata**: it changes only when the schema changes, is not operator-tunable, and must be reviewed / versioned / deployed with the schema. It therefore belongs **co-located with the schema, in version control — NOT in Postgres** (the operational/runtime DB; model-definition there invites drift from the schema it describes and bypasses review).

**Single source (Spike 33, done 2026-08-24):** the type-policy lives once, in `internal/configitems/registry.go` — each `Type`'s `OwnerType`/`OwnerField`/`ChildField` (single-owner) plus an ordered, most-specific-first `OwnerEdges []OwnerEdge` for multi-parent types (`NetworkInterface`, `IPAddress`, `StorageVolume`). The **audit collector** (`internal/handler/related_orbids.go`) derives from it via `OwnedChildren()` / `OwnedOrbIDSelection()`; the per-type `collectRelatedOrbIDs` / `collectClusterRelatedOrbIDs` walkers are gone, which fixed real drift (storage devices/volumes and switch-side interfaces never rolled up; NetworkDevice and DataCenter aggregated nothing). A schema-consistency test (`configitems/schema_consistency_test.go`, R3) fails the build if the registry drifts from `schema.graphql` or a ConfigItem type goes unregistered. Adding an owned-child type = one registry entry (see `docs/playbooks/add-configitem.md`).

The export **diff preview** was briefly a second consumer (it rolled changes up under an owner). That was removed 2026-08-24 — owner was never a decided requirement for the preview, and the orbId convention already identifies the owning entity. Ownership today serves the audit tab, the JSON editor's subtree paths, and delete-cascade. Do not re-add it to the diff without an explicit decision.

> **Why not infer ownership from DGraph automatically?** DGraph encodes *relationships*, not *ownership* — `@hasInverse` is bidirectional, there is no `@owns`. Projecting the graph to a tree must pick one canonical parent per node (a NIC nests under its adapter, not its server *and* device), which is a domain policy, not a derivable fact. Ownership must be **declared**; the goal is to declare it once, explicitly, next to the schema.

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

### A nested child update in a `set` is SILENTLY DISCARDED

**Do NOT try to update an owned child by nesting it inside the parent's `set`.** It looks like it works and it does not. *(Measured 2026-09-16 against DGraph v25.3.1.)*

```graphql
updateServer(input: { filter: { orbId: { eq: "ns:server" } },
                      set: { hostname: "h-NEW",
                             idracSettings: { orbId: "ns:idrac", firmwareVersion: "9.9.9" } } })
```
```
→ numUids: 1, no errors array, HTTP 200
→ hostname                       = "h-NEW"   ✅ parent updated
→ idracSettings.firmwareVersion  = "1.0.0"   ❌ 9.9.9 discarded
```

A nested object in `set` **links by `@id`**; the field values it carries are dropped. There is no error, no warning, and `numUids` counts the parent — so every signal says success. `add{Type}(input: […], upsert: true)` behaves the same way: the parent upserts, the child is untouched.

**Update an existing child with its own `update{Kind}(orbId, set)` mutation.** This is why `configitem-editor.js` dispatches one mutation per affected concrete type rather than one nested mutation — it is a constraint, not a style choice.

**The one nesting that DOES work is a CREATE.** `addServer(input: [{…, idracSettings: {…}}])` creates parent and child together, in one mutation, atomically. So nesting is correct for a new subtree and wrong for editing an existing one.

### One mutation is one transaction — so N mutations are N transactions, never one

**A single GraphQL mutation IS atomic.** That is not the problem, and reading it as reassurance is the trap. The problem is that *one user action is often several mutations*, and nothing binds them together:

| Scope | One transaction? |
|---|---|
| One mutation field — `updateServer(…)` | **Yes**, atomic |
| N fields in one document — `{ updateServer(…) updateIdracSettings(…) }` | **No** — N transactions |
| N separate HTTP requests | **No** — N transactions |
| N mutation blocks in one **DQL upsert** | **Yes** — one transaction |

So editing a Server and its iDRAC is **two** atomic writes with no relationship: the first can commit while the second fails. Two atomic operations, one half-finished result. Say "a save is several transactions", not "each mutation is a transaction" — the load-bearing word is *several*.

**Do NOT batch mutations into a single GraphQL document expecting all-or-nothing** — batching changes nothing, because the document is not the unit. This is vendor-confirmed, not merely observed — Dgraph maintainer Arijit Das, [Transactions in GraphQL](https://discuss.dgraph.io/t/transactions-in-graphql/6861):

> "Every GraphQL mutation starts a new transaction. A mutation is done via an upsert query, and then a request is sent to Dgraph to commit the transaction." … "if `txn 1` passes but `txn 2` errors out, **Only `txn 2` is rolled back, not `txn 1`.**"

An RFC to add native GraphQL transactions (Jira `GRAPHQL-429`, May 2020) never shipped; a [2021 thread](https://discuss.dgraph.io/t/graphql-transaction-for-entire-mutation/6998) is still asking for it.

```graphql
mutation { addDataCenter(input:$dc){ numUids }   addRack(input:$r){ numUids } }
→ addDataCenter: numUids 1   (PERSISTED)
→ addRack: null + error
```

**Order matters, and it is not "all fields run and some fail".** Root mutation fields execute **serially**. A genuine error aborts the fields AFTER it — Dgraph says so explicitly: *"Mutation second was not executed because of a previous error."* — while any field that already committed **stays committed**. So the damage is every field before the failure, never after it.

**But a compare-and-swap miss is not an error**, so it does not stop anything. That is the case that actually bites, because the response looks clean:

```
updateServer        → numUids: 1   ← committed
updateIdracSettings → numUids: 0   ← compare-and-swap missed
HTTP 200, no errors array
```

Half the write lands, nothing reports a problem, and the later fields ran anyway.

**For a genuinely atomic multi-entity write, use a DQL upsert block.** The whole upsert — the query plus every mutation block in it — commits or rolls back as one transaction. `bulkDeleteGuarded` in `delete.go` is the reference implementation.

⚠️ **Atomicity and conditionality are separate things here, and conflating them is the trap.**

- **Failure** rolls back the entire upsert, across every mutation block.
- **`@if` is evaluated per block.** A false condition skips *that block only* — silently, with `code: Success` and no `errors` array — and the remaining blocks still commit.

So several blocks each carrying their own `@if` ARE still one transaction; what they are not is one *guard*. If the intent is "all these entities or none of them", put them under a **single** condition — otherwise a condition that goes false on one block is a silent partial write, which is the failure mode that actually bites.

**Verification (re-run independently 2026-09-16, dgraph v25.3.1).**

*Structural, and the most durable evidence:* a successful two-block upsert returns **one `start_ts` and one `commit_ts`**, with `preds` spanning predicates from every block — `start_ts: 20435, commit_ts: 20436, preds: [atomtest_name, atomtest_num]`. Both blocks are in one transaction, so Dgraph's snapshot-isolation guarantee gives all-or-nothing.

*Commit-time rollback,* shown with a transaction-conflict abort — the only failure mode that unambiguously happens **after** a write is staged:

```
txn A (2 blocks, no commitNow), block 1 modifies node X  → start_ts 20444, 3 keys, NO commit_ts   (staged)
txn B (commitNow) writes X                               → X = 999, commit_ts 20450
commit txn A                                             → "Transaction has been aborted"
FINAL: X = 999, and block 2's node absent
```

Block 1's write was staged and then discarded at commit. Rollback crosses the block boundary.

⚠️ **Do NOT use a `@unique` violation to test transactional rollback — it cannot.** Verified: a block that DELETES the conflicting value does not clear the way for a later block to insert it; the check reads committed state plus the request's own values and **ignores deletes in the same request**. Its error code is `ErrorInvalidRequest`, the same class as an RDF parse error. So a `@unique` failure is decided **before execution**, and every such test is equally consistent with "rolled back" and "never executed". An earlier version of this section rested on exactly that test and claimed more than it showed.

⚠️ **Dgraph's own documentation does not state multi-block atomicity.** [Upsert](https://docs.dgraph.io/dql/upserts/) says *"The upsert block contains one query block and mutation blocks"* — plural, so the structure is documented — but its only atomicity statement is *"the upsert has to be an atomic operation such that either a new node is created, or an existing node is modified"*, which is the create-or-modify race, not N mutation blocks. `@if` semantics across blocks are undocumented too. Treat all of the above as local measurement and re-verify on a Dgraph upgrade.

### GraphQL get vs query
`get{Type}(id: ID!)` — reliable for most types. For acronym-named types (e.g. `IPAddress`), prefer `query{Type}(filter: { orbId: { eq: $orbId } })` which is more reliable than `getIPAddress`.

## Comparing two graph snapshots (normalization)

Anything that diffs or fingerprints the graph — the export preview, its `contentHash`, a future published-vs-published diff — must normalize first, because **the same graph reaches you in two different wire shapes**. `internal/graphdiff` owns this; read it before writing any new comparison.

| | Live DQL (`fetchNamespaceSubgraph`) | DGraph native export (`data.json.gz`) |
|---|---|---|
| Shape | one **merged map per node** | a **flat array of per-predicate fragments** |
| `uid` | appears once per node | **recurs** — one fragment per predicate, and one per edge target |
| `dgraph.type` | an **array** | a **string**, one fragment per type |
| Booleans | real JSON `false` | **quoted strings `"false"`** |

Five rules fall out, each of which caused (or would have caused) a real bug:

- **Reassemble the export by `uid` before comparing.** A 663-node export arrives as ~8,000 fragments; fold each fragment's single predicate into the accumulating node (scalars overwrite, edges/types union).
- **Booleans must be coerced.** `"false"` (export) vs `false` (DQL) compares unequal — this produced **174 false-positive "modified" nodes** against real colo data and was invisible to synthetic tests. `canonScalar` normalizes both sides.
- **Compare edges by target `orbId`, never by `uid`.** `dgraph live` reassigns UIDs on restore, so any UID-based comparison reports every edge as changed after a restore.
- **Exclude the churn predicates:** `ConfigItem.version`, `ConfigItem.updatedAt`, `ConfigItem.updatedBy` (rewritten on every write, so a diff would mark every touched node modified) plus DGraph internals `uid` and the tenant `namespace` (`"0x0"` — **not** `ConfigItem.namespace`). `createdAt`/`createdBy` are deliberately KEPT: they only change on delete+recreate, which is real signal.
- **Key everything by `orbId`.** It's the stable identity across instances and restores (see § orbId convention).

**Do NOT fingerprint the exported artifact instead of the normalized graph** — DGraph's native export is **not byte-deterministic** (the same finding that killed checksum-based backup dedup). Hash the canonical model; it's stable by construction.

The one guard that proves normalization is correct is a **round-trip**: export a DC, then diff the current graph against that same artifact — the result must be empty. Any normalizer disagreement shows up immediately.

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
