# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Orbital** is an API-first, graph-native configuration management system for modular data centers. Written in Go.

### Key Concepts

- **`orbital`** — Server running in cloud. Central configuration hub — holds design intent (configuration items) for all modular data centers, serves the Topology API for digital twin building, and exposes a config export API for orbs to consume.
- **`orb`** — Self-contained edge service running inside a modular data center. Serves configuration, reports drift, suitable for air-gapped deployments.

### Goals

- Air-gap ready design — operates in disconnected and edge environments without external dependencies
- Graph-first infrastructure model — represent data centers as relationships between physical and logical resources
- Multi-source infrastructure discovery — ingest from bare metal systems (BMC) and external inventory systems via API integrations
- Topology API (digital twin) — build and query a live, traversable graph of infrastructure design intent

### Non-Goals

- Full DCIM system with dashboards, alerting, and observability
- End-to-end infrastructure control plane or management suite

## Stack

- **Go** — Implementation language for both `orbital` and `orb`
- **DGraph** (community edition) — Graph database with native GraphQL API on top of RDF-like storage; stores all configuration items. Chosen because the RDF model fits configuration items naturally, and the GraphQL API lets external teams (e.g. a digital twin UI) consume data without custom endpoints. Self-hosted in the same Kubernetes namespace as orbital.
- **PostgreSQL** — Stores all managed-service operational data for `orbital`: orb registry, user accounts, audit logs, job/sync history, schema versions, DGraph backup records. Anything typical for running a managed service goes here, not in DGraph. PostgreSQL backup is handled by Azure managed PostgreSQL — not orbital's concern.
- **Valkey** — In-memory cache for `orbital`; chosen over Redis due to licensing.

## Architecture Notes

### Project boundary

Orbital is responsible for: the configuration graph, the Topology API, drift reporting, and producing an exportable config payload for edge consumption.

Orbital is **not** responsible for: how that payload is packaged or signed, how it is delivered to the edge (registries, OCI, USB), or how configuration is reconciled against real infrastructure. Those concerns belong to the deployment layer above orbital.

This boundary keeps orbital adoptable outside any specific deployment context. A consumer that doesn't use Kubernetes controllers or OCI registries should still be able to use orbital as a CMDB and Topology API.

### Deployment model invariants

The following invariants apply to Kubernetes-based deployments of orbital. Orbital's design must not violate them, but orbital does not enforce them — they are maintained by the deployment layer (K8s controllers, bundle infrastructure, etc.):

1. **Nothing in the cloud executes directly against a modular data center.** Orbital publishes intent. Edge components pull and apply configuration locally.
2. **Desired state and observed state are represented explicitly and may diverge.** Divergence during disconnection windows is data, not an error condition.
3. **Authoritative reconcilers run locally within the modular data center.** The cloud is never part of the reconciliation path. The CMDB is not part of the reconciliation path.
4. **The CMDB (DGraph) is a graph index and relationship store.** Configuration actuation flows through the deployment layer — not through the CMDB.

### Data flow
Configuration intent flows **orbital → orb**. In deployments using [`configbundle`](https://github.com/armada/configbundle), it sits between them — its Bundle Generator calls orbital's export API and its edge agent delivers the payload to orb. In other deployments, consumers may call the export API directly.

For reporting, the edge agent writes drift and divergence reports to a shared external location when connected. A cloud-side polling agent (deployment layer concern) reads from that location and calls orbital's report intake API. Orbital never communicates directly with the edge for reporting — the transport is entirely the caller's concern.

The exception to the one-way config flow is onboarding: orb discovers existing infrastructure, exports a graph, and an admin manually carries it to orbital (USB/file upload) to seed the cloud control plane. After import, orbital becomes the source of truth going forward.

### Air-gap sync
Two mechanisms for getting config into an air-gapped orb:
1. **Scheduled sync** — when connectivity is available, the delivery layer syncs updated configuration from the source and loads it into orb via the local import API
2. **Manual file import** — admin physically carries a config payload (e.g. via USB) and imports it into orb when the modular data center is fully disconnected

### Discovery and onboarding
For customers with existing infrastructure, discovery runs at the edge:
1. Orb scans the local modular data center (BMC, inventory APIs)
2. Orb exports the discovered graph to a portable file
3. Admin copies the file out (e.g. USB)
4. Admin uploads to orbital (`orbital import`) to seed the cloud control plane
5. Admin registers the orb's Ed25519 public key with orbital (generated by the deployment layer at bootstrap)

This is the primary onboarding workflow — discovered reality flows from orb into orbital, not the other way. Public key registration is a one-time manual step at provisioning time; no automated edge→cloud call is needed.

### Namespace and DataCenter

`Namespace` is a pure tenancy boundary — it is not a config item and does not implement `ConfigItem`. It exists solely as an isolation scope for graph partitioning and orphan detection. It has no config fields.

`DataCenter implements ConfigItem` — it holds all data center configuration fields (location, region, size, etc.) and is the root node for a data center's subgraph.

**Convention: 1:1 between Namespace and DataCenter.** Each data center has exactly one namespace; each namespace contains exactly one data center. DGraph does not enforce this — orbital's application layer does. This convention must not be violated.

The `namespace: Namespace!` field on every `ConfigItem` (inherited from the interface) is a direct reference kept for query performance — it avoids traversing up through `DataCenter` to reach the namespace boundary when scoping queries. It is always set to the same namespace as the data center the item belongs to.

### Configuration items
Configuration items span the full spectrum from physical (racks, servers, switches) to logical (clusters, app configs). The schema is intentionally broad. DGraph's RDF model fits this naturally.

**v1 scope note:** VLANs and general network infrastructure IPs are owned by an external system and remain out of scope. Functional IPs tied to specific workloads (e.g. Tinkerbell provisioning IP, K8s control plane IP) are in scope as properties or dedicated nodes — discuss before adding.

### Authentication and sessions

Orbital supports two login flows:

- **Local login** — email/password verified against PostgreSQL (`users` table, bcrypt cost 12). Always available for development.
- **OIDC/SSO** — Azure AD via OpenID Connect. Enabled when `ORBITAL_OIDC_ISSUER_URL` and `ORBITAL_OIDC_CLIENT_SECRET` are both set. Disabled with a startup warning if the secret is missing.

Sessions use gorilla/sessions cookie store with HMAC-SHA256 signing (`ORBITAL_SESSION_HMAC_KEY`) and AES-256 encryption (`ORBITAL_SESSION_ENCRYPTION_KEY`, must be exactly 32 bytes). Both keys have local dev defaults in `config.go`; production values must be injected via environment variables.

### Backup

Orbital backs up the DGraph graph to any S3-compatible storage (including Azure Blob Storage). Backups are triggered manually via the UI or `POST /api/v1/backups`. Each backup:
1. Triggers DGraph's native export mutation on the blue instance → `json.gz` written to a host-side volume mount (`DGRAPH_EXPORT_DIR`, default `./dgraph-exports`)
2. SHA-256 checksums the export; skips upload if graph is unchanged since last backup (dedup)
3. Packages `data.json.gz` + `schema.gz` into a zip and uploads to S3
4. Cleans the export directory after upload
5. Enforces a configurable retention count (`ORBITAL_S3_RETENTION_COUNT`), pruning oldest completed backups

Azure Blob Storage is auto-detected by `.blob.core.windows.net` in the endpoint and uses Shared Key auth (not AWS Signature V4). All other endpoints use the AWS SDK with path-style addressing.

Backup records (status, checksum, size, S3 key, initiated by) are stored in PostgreSQL via the `backups` ent table.

### Orb identity
Orbital is the system of record for orb identity. Each orb has a per-orb Ed25519 key pair generated at bootstrap by the deployment layer. The public key is registered with orbital by an admin at onboarding time and stored in the `orbs` PostgreSQL table. The private key never leaves the orb.

The public key is optional — signing of divergence reports is not guaranteed in all deployments. When a public key is registered, orbital verifies the Ed25519 signature on incoming reports via the report intake API and rejects reports that fail verification. When no key is registered, reports are accepted without signature verification.

The transport between the edge and orbital's intake API is entirely the deployment layer's concern — orbital does not know or care how the report arrived.

### Orb import API
Orb does not connect to orbital directly for config — config arrives via the delivery layer (e.g. `configbundle`'s edge agent calling orb's local `/import` API). See Spike 7 in ROADMAP.md for API contract design.

### Orb CLI vs orb server
`orb` is a single binary with subcommands (`cmd/orb/`). `orb start` runs the long-running edge service. Other subcommands (`orb scan`, `orb export`, `orb import`) are admin operations. All share packages from `internal/`.

### Schema management
DGraph schema is defined in versioned GraphQL files under `schema/` and applied to DGraph via its admin API (`POST /admin/schema`). Orbital owns the schema — orb never modifies it.

**Rules:**
- Schema changes must always be backwards compatible. Orbs may be running an older version while orbital is newer. Breaking changes (removing/renaming types or fields, adding non-null fields to existing types) are rejected.
- Safe changes: new types, new nullable fields on existing types.
- Orbital tracks the active schema version in PostgreSQL (`schema_versions` table: version, checksum, applied timestamp, applied by).
- On startup, orbital compares its bundled schema version against PostgreSQL and applies if behind, after validating backwards compatibility.
- Schema is never applied manually — always through orbital's startup or admin API.

**DGraph schema update behavior — critical:** Applying a new GraphQL schema to DGraph is additive at the RDF predicate layer. Removing a field from the GraphQL schema does NOT delete the underlying RDF triples — the data persists in DGraph, it is just no longer queryable via GraphQL. To permanently remove a field and its data, you must explicitly drop the predicate via the alter API:
```
POST /alter  {"drop_attr": "<predicate_name>"}
```
This is irreversible. The migration tool must treat field removals as explicit, versioned, destructive steps — never silent side effects of applying a new schema file.

### Export API
Orbital exposes a data center-scoped export endpoint (`POST /api/v1/datacenters/{id}/export`) that returns a `json.gz` + `schema.gz` pair representing the intended state for that data center's subgraph.

**Export mechanism — blue-green DGraph:** Orbital runs two DGraph instances (blue and green). Blue is live and serves the Topology API. Green is idle-warm and used exclusively for export and validation. The export workflow:
1. Query blue for the target data center's subgraph (GraphQL, scoped to DC)
2. Load subgraph into green via `addDataCenter(upsert: true)` mutation
3. Run native DGraph export mutation on green → `json.gz`
4. Validate green is queryable (sanity check)
5. Ship `json.gz` + `schema.gz`
6. Wipe green for next use (or preserve on failure for debugging)

Only one export may run at a time per data center.

DGraph's export mutation does not support subgraph filtering — it is a full-graph dump. The blue-green approach scopes the export by loading only the target DC's subgraph into green before exporting.

### External integrations (PLM, ITSM)
These integrations must be designed behind Go interfaces — orbital defines the interface, concrete implementations are swappable. Do not couple orbital directly to any specific vendor or product.

### Topology API
Orbital proxies DGraph's auto-generated GraphQL API as-is. No custom GraphQL layer for now. External consumers (digital twin UI) query orbital's GraphQL endpoint, which forwards to DGraph. Orbital adds auth, rate limiting, and caching in the middleware layer — but does not transform the GraphQL schema.

### DGraph topology
Orbital runs two DGraph instances: **blue** (live, serves the Topology API and all client queries) and **green** (idle-warm, used exclusively for export validation). The Topology API always queries blue. Green is never exposed to external clients.

One shared blue DGraph instance serves all modular data centers. `DataCenter` is the root partitioning node. Do not assume or design for multi-instance blue topology.

### Caching (Valkey)
Orbital must operate correctly without Valkey — cache is an optimization, not a dependency. Use cache-aside: check Valkey first, fall back to DGraph on miss, populate cache on response. Cache DGraph GraphQL query responses. Invalidate on config changes.

### Drift reporting
Orb observes actual state, compares it to intended state, and reports the gap. Orbital exposes a transport-agnostic report intake API to receive these reports — it does not act on them, does not trigger reconciliation, and is not in the reconciliation path.

### GraphQL middleware
Clients never query DGraph directly. All queries go through the Go server, which handles rate limiting, caching, auth, and other cross-cutting concerns.

## Local Development

Start the local stack (DGraph + PostgreSQL) with:

```bash
docker compose -f deploy/local/docker-compose.yml up -d
```

| Service | Port(s) | Notes |
|---|---|---|
| DGraph Zero | 5080, 6080 | Cluster coordinator |
| DGraph Alpha | 8080 (HTTP/GraphQL), 9080 (gRPC) | GraphQL playground at http://localhost:8080 |
| DGraph Ratel | 8000 | DGraph UI |
| PostgreSQL | 5432 | user/password/db: `orbital` |

Run orbital:
```bash
make run-orbital
```
No env sourcing required — all local dev defaults are in `config.go`.

## Repository Structure

```
cmd/
  orb/                # orb binary — subcommand-driven (orb start, orb scan, orb export, orb import)
  orbital/            # orbital server entry point
deploy/
  local/              # Local development stack (docker-compose)
  orb/                # Deployment files for orb
  orbital/            # Deployment files for orbital
internal/
  auth/               # Session management, CSRF, OIDC state, bearer validation
  config/             # Config struct with envconfig defaults
  discovery/          # Discovery orchestration (used by orb — runs at the edge, not in orbital)
    bmc/              # BMC/bare metal discovery
  drift/              # Drift reporting (used by orb)
  graph/              # DGraph client, schema loading, topology operations
  handler/            # HTTP handlers (GraphQL proxy, backup, export, UI, login, OIDC)
  server/             # Echo server setup and lifecycle
schema/
  schema-demo.graphql # DGraph GraphQL schema (demo/dev version)
web/
  static/             # Static assets — all page JS lives in app.js here
  templates/          # Go HTML templates (layouts, pages, partials)
```

## Working Style

- Don't add comments that just restate what the code does
- Don't refactor code that wasn't part of the request — ask first
- Don't add third-party packages without asking first
- Only touch files relevant to the task
- Don't clean up unrelated code while working on something else
- Don't add TODOs or placeholder comments
- All page JavaScript goes in `web/static/app.js` — never inline `<script>` blocks in templates
- Before marking a task as done: check whether any architectural decisions, conventions, or settled rules from this session should be added to CLAUDE.md

### Conversation conventions

- Messages starting with **"thoughts:"** or **"discuss:"** mean the user is thinking out loud or wants dialogue — do not write any code or files, just respond conversationally.
- Use `/plan` mode for architecture and schema design discussions before any implementation begins.
- Run `/wrap-up` at the end of a session to update CLAUDE.md, save memories, and commit.

## Settled Decisions

These have been explicitly decided. Do not re-suggest them.

- **Do not replace DGraph** — chosen deliberately; RDF model fits configuration items naturally
- **Do not switch to Redis** — Valkey chosen over Redis due to licensing
- **Do not use `schollz/progressbar` alone for spinners** — indeterminate mode causes terminal jitter; use `briandowns/spinner` for spinners and `schollz/progressbar` for determinate progress bars
- **Do not prescribe a data transport mechanism** — orbital's contract ends at the export API (`json.gz` + `schema.gz`). How that payload is transported, packaged, or stored is the consumer's concern.
- **Report intake API is transport-agnostic** — how reports travel from edge to orbital is the deployment layer's concern. Do not couple the intake API to any specific transport.
- **Namespace and DataCenter are 1:1** — one namespace per data center, enforced by orbital's application layer. `Namespace` is a pure boundary node (no config fields). Do not add config fields to `Namespace` or allow multiple data centers per namespace.
- **DGraph export mutation has no subgraph filtering** — scoped exports use the blue-green mechanism. Do not attempt to filter DGraph's export output directly.
- **Authorization uses Azure AD App Roles + DGraph `@auth`** — roles (`orbital-admin`, `orbital-viewer`) are defined in the Azure app manifest as App Roles. App Roles appear in the JWT `roles` claim as strings. Do not use Azure AD group GUIDs as the authz primitive.
- **DGraph `@auth` for mutation protection** — `@auth(add/update/delete)` directives on each type restrict mutations to authorized roles. `ClosedByDefault: true` requires a valid JWT for all operations. Field-level authz is not supported by DGraph and will not be attempted.
- **Go middleware for REST authz** — Echo route-group middleware enforces role checks on REST mutation endpoints. DGraph `@auth` is defense-in-depth, not the primary enforcement layer for REST.
- **Offline JWT testing for authz** — integration tests generate and sign JWTs locally with a test RSA key pair. No network call to Azure AD required in tests or CI.
- **Session encryption key must be exactly 32 bytes** — gorilla/sessions silently fails to decode sessions if the AES key is the wrong length. Orbital validates this at startup and refuses to start if misconfigured.
- **Azure Blob Storage uses Shared Key auth, not AWS Signature V4** — Azure's standard endpoint rejects AWS signatures. Auto-detected by `.blob.core.windows.net` in the endpoint; uses the Azure SDK. All other S3-compatible endpoints use the AWS SDK with path-style addressing.

## Example Data / Seeding

Example GraphQL mutation files live in `examples/`. Each file seeds one data center (namespace + DC + racks + servers) into DGraph via the GraphQL playground at `http://localhost:8080`.

**Seeding rules — learned from practice:**
- `addNamespace` takes a single object (not array): `addNamespace(input: { name: "..." }, upsert: true)`
- Cross-type references must use `orbId`, not `name`, since `orbId` is the `@id` field. Example: `dataCenter: { orbId: "ns:dc-name" }`, `rack: { orbId: "ns:rack-name" }`. Using `{ name: "..." }` fails with "field orbId cannot be empty" because DGraph treats it as a new object with no orbId.
- `orbId` format convention: `"<namespace>:<entity-name>"` — e.g. `"alaska-dot:alaska-dot-galleon"`, `"alaska-dot:Rack-5"`, `"alaska-dot:GRTLY24"`
- All ConfigItem nodes require `orbId`, `name`, `namespace`, and `createdBy`/`createdAt`
- Run `addNamespace` → `addDataCenter` → `addRack` → `addServer` in that order within a single mutation batch

## Go Conventions

- **Error wrapping** — use `fmt.Errorf("...: %w", err)`; never discard or log-and-return
- **Context** — always the first argument: `func Foo(ctx context.Context, ...)`
- **Constructors** — named `New[Type]`, e.g. `NewServer`, `NewClient`
- **`cmd/` is thin** — entry points only; all logic lives in separate packages
- **Tests** — table-driven with `t.Run`; avoid test helpers that obscure failure sites
- No `init()` functions — exception: Cobra command files in `internal/cli/` may use `init()` to register subcommands and flags, which is the standard Cobra pattern
- No global variables
- No `panic()` outside of `main()`

## Development Status

Early-stage project. The Go module is initialized at `github.com/armada/orbital`.
