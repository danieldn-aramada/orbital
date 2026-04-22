# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Orbital** is an API-first, graph-native configuration management system for modular data centers. Written in Go.

### Key Concepts

- **`orbital`** — Server running in cloud. Central configuration hub — holds design intent (configuration items) for all modular data centers, serves the Topology API for digital twin building, and pushes configuration down to orbs.
- **`orb`** — Self-contained edge service running inside a modular data center. Serves configuration, detects drift, suitable for air-gapped deployments.

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
- **DGraph** (community edition) — Graph database with native GraphQL API on top of RDF-like storage; stores all configuration items. Chosen because the RDF model fits configuration items naturally, and the GraphQL API lets external teams (e.g. a digital twin UI) consume data without custom endpoints. Do not suggest replacing DGraph. Self-hosted in the same Kubernetes namespace as orbital. Some enterprise features (namespaces, backups) may be implemented in-house later.
- **PostgreSQL** — Stores all managed-service operational data for `orbital`: orb registry, user accounts, audit logs, job/sync history, DGraph backup metadata (e.g. S3 locations). Anything typical for running a managed service goes here, not in DGraph.
- **Valkey** — In-memory cache for `orbital`; chosen over Redis due to licensing. Do not suggest switching to Redis.

## Architecture Notes

### Data flow
For ongoing config management, flow is **orbital → orb**. Orb does not write back to orbital directly over the network. The exception is onboarding: orb discovers existing infrastructure, exports a graph, and an admin manually carries it to orbital (USB/file upload) to seed the cloud control plane. After import, orbital becomes the source of truth going forward.

### Air-gap sync
Two mechanisms for getting config into an air-gapped orb:
1. **Scheduled polling** — orb polls orbital on an admin-controlled schedule when connectivity is available
2. **Manual file import** — admin physically imports a config file (e.g. via USB) into orb when the modular data center is fully disconnected

### Discovery and onboarding
For customers with existing infrastructure, discovery runs at the edge:
1. Orb scans the local modular data center (BMC, inventory APIs)
2. Orb exports the discovered graph to a portable file
3. Admin copies the file out (e.g. USB)
4. Admin uploads to orbital (`orbital import`) to seed the cloud control plane

This is the primary onboarding workflow — discovered reality flows from orb into orbital, not the other way.

### Configuration items
Configuration items span the full spectrum from physical (racks, servers, switches, screws, door parts) to logical (VLANs, IPs, K8s clusters, app configs). The schema is intentionally broad and user-defined. DGraph's RDF model fits this naturally.

### Orb registration and auth
**Not yet designed — see Spike 7 in ROADMAP.md.** The current hypothesis is a GitHub Actions runner pattern (one-time token → long-lived API key), but this has not been validated. Do not implement until the spike is complete.

Do not use expiring JWTs for orb auth. Orbs may be disconnected for months — a JWT that expires while air-gapped bricks the orb until someone rotates it. A long-lived opaque API key, revocable from orbital, is more resilient.

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

### Orb sync payload
When orb polls orbital, it receives a DGraph export — the same output as DGraph's export mutation:

```graphql
mutation { export(input: { format: "json" }) { response { code message } } }
```

This produces `json.gz` (data) and `schema.gz` (schema). Orb receives these files and loads them into its local DGraph instance. This means orb always has a complete local copy of its data center's graph, usable fully offline.

### External integrations (PLM, ITSM)
Orbital may integrate with external systems such as PLM (product lifecycle management) for bill of materials and ITSM (IT service management) for linking tickets to configuration changes. These integrations must be designed behind Go interfaces — orbital defines the interface, concrete implementations are swappable. Do not couple orbital directly to any specific vendor or product. The integration layer should be designed so that a different PLM or ITSM vendor can be adopted without changing orbital's core.

### Topology API
Orbital proxies DGraph's auto-generated GraphQL API as-is. No custom GraphQL layer for now. External consumers (digital twin UI) query orbital's GraphQL endpoint, which forwards to DGraph. Orbital adds auth, rate limiting, and caching in the middleware layer — but does not transform the GraphQL schema.

### DGraph topology
One shared DGraph instance for all modular data centers in orbital. `DataCenter` is the root partitioning node in the graph. A future evolution may run one DGraph instance per data center (e.g. using DGraph namespaces or separate clusters), but the current schema and graph package should not assume multi-instance.

### Caching (Valkey)
Orbital must operate correctly without Valkey — cache is an optimization, not a dependency. Use cache-aside: check Valkey first, fall back to DGraph on miss, populate cache on response. Heavy read load is expected from digital twin frontends rendering data center topology. Cache DGraph GraphQL query responses. Invalidate on config changes.

### Drift detection
When orb detects that actual state diverges from intended state, it alerts. No auto-reconciliation for now — reconciliation services are a future concern.

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
  drift/              # Drift detection (used by orb)
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

## Go Conventions

- **Error wrapping** — use `fmt.Errorf("...: %w", err)`; never discard or log-and-return
- **Context** — always the first argument: `func Foo(ctx context.Context, ...)`
- **Constructors** — named `New[Type]`, e.g. `NewServer`, `NewClient`
- **`cmd/` is thin** — entry points only; all logic lives in separate packages
- **Tests** — table-driven with `t.Run`; avoid test helpers that obscure failure sites
- No `init()` functions
- No global variables
- No `panic()` outside of `main()`

## Development Status

This is an early-stage project. The Go module is initialized at `github.com/armada/orbital`.
