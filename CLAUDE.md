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
- **DGraph** (community edition) — Graph database with native GraphQL API on top of RDF-like storage; stores all configuration items. Chosen because the RDF model fits configuration items naturally, and the GraphQL API lets external teams (e.g. a digital twin UI) consume data without custom endpoints. Self-hosted in the same Kubernetes namespace as orbital. Some enterprise features (namespaces, backups) may be implemented in-house later.
- **PostgreSQL** — Stores all managed-service operational data for `orbital`: orb registry (`orbs` table — id, datacenter_id, Ed25519 public key, registered_at, registered_by, status), user accounts, audit logs, job/sync history, schema versions, DGraph backup metadata. Anything typical for running a managed service goes here, not in DGraph.
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

### Configuration items
Configuration items span the full spectrum from physical (racks, servers, switches, screws, door parts) to logical (VLANs, IPs, K8s clusters, app configs). The schema is intentionally broad and user-defined. DGraph's RDF model fits this naturally.

**v1 scope note:** Network infrastructure config items (VLANs, IPs, etc.) are currently owned by an external system and are out of scope for v1. Do not add network config types to the schema until this scope is explicitly revisited.

### Orb identity
Orbital is the system of record for orb identity. Each orb has a per-orb Ed25519 key pair generated at bootstrap by the deployment layer. The public key is registered with orbital by an admin at onboarding time and stored in the `orbs` PostgreSQL table. The private key never leaves the orb.

Orbital uses the registered public key to verify the Ed25519 signature on incoming drift/divergence reports via the report intake API. Any report that fails verification is rejected. Registration → `INSERT` into `orbs`. Revocation → `UPDATE status = 'revoked'`. Verification → `SELECT public_key WHERE id = ? AND status = 'active'`.

The transport between the edge and orbital's intake API is entirely the deployment layer's concern — orbital does not know or care how the report arrived.

### Orb import API
Orb does not connect to orbital directly for config — config arrives via the delivery layer (e.g. `configbundle`'s edge agent calling orb's local `/import` API). See Spike 7 in ROADMAP.md for API contract design.

### Orb CLI vs orb server
`orb` is a single binary with subcommands (`cmd/orb/`). `orb start` runs the long-running edge service. Other subcommands (`orb scan`, `orb export`, `orb import`) are admin operations. All share packages from `internal/`.

### Schema management
DGraph schema is defined in versioned GraphQL files under `schema/` (e.g. `schema/v1.graphql`) and applied to DGraph via its admin API (`POST /admin/schema`). Orbital owns the schema — orb never modifies it.

**Rules:**
- Schema changes must always be backwards compatible. Orbs may be running an older version while orbital is newer. Breaking changes (removing/renaming types or fields, adding non-null fields to existing types) are rejected.
- Safe changes: new types, new nullable fields on existing types.
- Orbital tracks the active schema version in PostgreSQL (`schema_versions` table: version, checksum, applied timestamp, applied by).
- On startup, orbital compares its bundled schema version against PostgreSQL and applies if behind, after validating backwards compatibility.
- Schema is never applied manually — always through orbital's startup or admin API.

A custom schema migration tool will be built into orbital (not a standalone binary) to handle version tracking, compatibility validation, and DGraph apply. This is deliberate — no existing tool handles DGraph GraphQL migrations with the version skew constraints this project requires.

### Export API
Orbital exposes a data center-scoped export endpoint (`POST /api/v1/datacenters/{id}/export`) that returns a `json.gz` + `schema.gz` pair representing the intended state for that data center's subgraph. This is what the deployment layer (e.g. `configbundle`'s Bundle Generator) calls to produce a ConfigBundle. It is not a raw pass-through of DGraph's export mutation — orbital must partition the graph by data center before exporting.

How the resulting payload is packaged, signed, and delivered is outside orbital's scope. Orbital's responsibility ends at producing a correct, complete, scoped export. In deployments using [`configbundle`](https://github.com/armada/configbundle), it wraps the export as a signed OCI artifact and handles delivery. A different deployment model can consume the raw export directly.

Orb always has a complete local copy of its data center's intended state, usable fully offline.

### External integrations (PLM, ITSM)
Orbital may integrate with external systems such as PLM (product lifecycle management) for bill of materials and ITSM (IT service management) for linking tickets to configuration changes. These integrations must be designed behind Go interfaces — orbital defines the interface, concrete implementations are swappable. Do not couple orbital directly to any specific vendor or product. The integration layer should be designed so that a different PLM or ITSM vendor can be adopted without changing orbital's core.

### Topology API
Orbital proxies DGraph's auto-generated GraphQL API as-is. No custom GraphQL layer for now. External consumers (digital twin UI) query orbital's GraphQL endpoint, which forwards to DGraph. Orbital adds auth, rate limiting, and caching in the middleware layer — but does not transform the GraphQL schema.

### DGraph topology
One shared DGraph instance for all modular data centers in orbital. `DataCenter` is the root partitioning node in the graph. A future evolution may run one DGraph instance per data center (e.g. using DGraph namespaces or separate clusters), but the current schema and graph package should not assume multi-instance.

### Caching (Valkey)
Orbital must operate correctly without Valkey — cache is an optimization, not a dependency. Use cache-aside: check Valkey first, fall back to DGraph on miss, populate cache on response. Heavy read load is expected from digital twin frontends rendering data center topology. Cache DGraph GraphQL query responses. Invalidate on config changes.

### Drift reporting
Orb observes actual state, compares it to intended state, and reports the gap. Orbital exposes a transport-agnostic report intake API to receive these reports — it does not act on them, does not trigger reconciliation, and is not in the reconciliation path. How reports travel from the edge to orbital's intake API is the deployment layer's concern.

### GraphQL middleware
Clients never query DGraph directly. All queries go through the Go server, which handles rate limiting, caching, auth, and other cross-cutting concerns. This applies to external consumers (e.g. digital twin UI teams) as well.

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
  config/             # Shared config (port, timeouts, DGraph URL)
  discovery/          # Discovery orchestration (used by orb — runs at the edge, not in orbital)
    bmc/              # BMC/bare metal discovery
  drift/              # Drift reporting (used by orb)
  graph/              # DGraph client, schema loading, topology operations; orbital's graph import logic lives here (not in discovery/)

  handler/            # HTTP handlers (GraphQL proxy, topology API)
  server/             # Shared Echo server setup and lifecycle
  static/             # Static files (GraphiQL UI)
schema/
  v1.graphql          # DGraph GraphQL schema (versioned, managed by orbital)
```

## Working Style

- Don't add comments that just restate what the code does
- Don't refactor code that wasn't part of the request — ask first
- Don't add third-party packages without asking first
- Only touch files relevant to the task
- Don't clean up unrelated code while working on something else
- Don't add TODOs or placeholder comments
- Before marking a task as done: check whether any architectural decisions, conventions, or settled rules from this session should be added to CLAUDE.md. If AI assistance was used, append a row to the AI.md audit log.

## Settled Decisions

These have been explicitly decided. Do not re-suggest them.

- **Do not replace DGraph** — chosen deliberately; RDF model fits configuration items naturally
- **Do not switch to Redis** — Valkey chosen over Redis due to licensing
- **Do not use `schollz/progressbar` alone for spinners** — indeterminate mode causes terminal jitter; use `briandowns/spinner` for spinners and `schollz/progressbar` for determinate progress bars
- **Do not prescribe a data transport mechanism** — orbital's contract ends at the export API (`json.gz` + `schema.gz`). How that payload is transported, packaged, or stored (e.g. OCI registry) is the consumer's concern. Suggesting a transport mechanism in orbital would break the project boundary and couple it to a specific deployment model.
- **Report intake API is transport-agnostic** — orbital exposes an intake API and receives structured reports. How reports travel from the edge to that API (shared external location, polling agent, direct call) is the deployment layer's concern. Do not couple the intake API to any specific transport or storage mechanism.

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

This is an early-stage project. The Go module is initialized at `github.com/armada/orbital`.
