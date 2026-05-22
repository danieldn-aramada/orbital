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
- Topology API (digital twin) — build and query a live, traversable graph of infrastructure design intent; consumers define their own query shape
- Intent-only CMDB — mutations update authoritative design intent only; orbital is never in the reconciliation path

### Non-Goals

- Full DCIM system with dashboards, alerting, and observability
- End-to-end infrastructure control plane or management suite
- Reconciling configuration drift — orbital surfaces divergence to administrators but never auto-resolves it and is never in the reconciliation path
- Packaging, signing, or transporting config payloads — orbital's contract ends at the export API (`json.gz` + `schema.gz`); how that is packaged into a ConfigBundle, signed, and delivered to the edge is the deployment layer's concern (implemented in a separate repository)

## Stack

- **Go** — both `orbital` and `orb`
- **DGraph** (community edition) — graph DB; stores all configuration items. Self-hosted in the same K8s namespace as orbital. Do not replace — see Settled Decisions.
- **PostgreSQL** — all operational data for `orbital` (orb registry, users, audit logs, jobs, schema versions, backup records). PostgreSQL backup handled by Azure managed PostgreSQL.
- **Valkey** — in-memory cache for `orbital`. Do not switch to Redis — see Settled Decisions.

## Architecture Notes

### Project boundary

Orbital's contract ends at the export API and report intake API. How payloads are transported, packaged, or applied at the edge is the deployment layer's concern — not orbital's.

### Deployment model invariants

The following invariants apply to Kubernetes-based deployments of orbital. Orbital's design must not violate them:

1. **Nothing in the cloud executes directly against a modular data center.** Orbital publishes intent. Edge components pull and apply configuration locally.
2. **Desired state and observed state are represented explicitly and may diverge.** Divergence during disconnection windows is data, not an error condition.
3. **Authoritative reconcilers run locally within the modular data center.** The cloud is never part of the reconciliation path. The CMDB is not part of the reconciliation path.
4. **The CMDB (DGraph) is a graph index and relationship store.** Configuration actuation flows through the deployment layer — not through the CMDB.
5. **GraphQL mutations on orbital update authoritative intent only.** They do not execute actions remotely or trigger actuation.

### Data flow

Orbital provides the APIs — consumers wire the transport. Orbital does not prescribe how its APIs are called or how payloads move between systems.

- **Export API** (`POST /api/v1/datacenters/{id}/export`) — produces a scoped `json.gz` + `schema.gz` for a data center's subgraph.
- **Publish API** (`POST /api/v1/export/jobs/:jobId/publish`) — pushes a completed export as a signed OCI artifact to the configured registry. Requires `ORBITAL_OCI_REGISTRY` and `ORBITAL_OCI_SIGNING_KEY_PATH`.
- **Report intake API** — receives drift and divergence reports. Transport is the deployment layer's concern. Orbital never initiates contact with the edge.
- **Topology API** — proxies DGraph's GraphQL API for digital twin consumers.

### Namespace and DataCenter

`Namespace` is a pure tenancy boundary — it is not a config item. Exists solely as an isolation scope. No config fields.

`DataCenter implements ConfigItem` — root node for a data center's subgraph.

**Convention: 1:1 between Namespace and DataCenter.** Enforced by orbital's application layer. Never add config fields to `Namespace` or allow multiple data centers per namespace.

### Schema management

DGraph schema is defined in versioned GraphQL files under `schema/` and applied to DGraph via its admin API. Orbital owns the schema — orb never modifies it. Changes must always be backwards compatible. Orbital tracks the active schema version in PostgreSQL (`schema_versions` table) and applies on startup if behind.

See `docs/claude/DGRAPH.md` for schema gotchas, DQL patterns, and blue-green export topology.

### Other subsystems

- **Authentication:** local email/password + OIDC/SSO via Azure AD. See `docs/claude/AUTH.md`.
- **Backup/Restore:** DGraph backup to S3-compatible storage; restore via idle `dgraph-live` pod. See `docs/claude/OCI.md`.
- **Export + OCI publish:** blue-green DGraph export flow, signed OCI artifacts via oras-go v2 + cosign. See `docs/claude/OCI.md`.
- **Audit log:** one event per HTTP request, three-source orbId extraction. See `docs/claude/AUDIT.md`.
- **Caching:** Valkey cache-aside. Orbital must operate correctly without Valkey — cache is an optimization, not a dependency.
- **Divergence reporting:** orb records local overrides, orbital surfaces them for human decision (accept/reject/ignore). Orbital is never in the reconciliation path.
- **GraphQL middleware:** all queries go through the Go server (rate limiting, caching, auth). Clients never query DGraph directly.
- **Topology API:** proxies DGraph's auto-generated GraphQL as-is. Orbital adds auth, rate limiting, caching — does not transform the schema.

### Orb

- **CLI vs server:** `orb` is a single binary (`cmd/orb/`). `orb start` is the long-running edge service. `orb scan`, `orb export`, `orb import` are admin operations.
- **Identity:** per-orb Ed25519 key pair generated at bootstrap. Public key registered with orbital by admin, stored in `orbs` table. Private key never leaves the orb. Orbital verifies signatures on incoming reports when key is registered.
- **Onboarding:** orb scans locally → exports graph to file → admin carries to orbital (USB/upload) → orbital becomes source of truth. Discovered reality flows from orb into orbital, not the other way.

## Current State

**Phase:** Prototyping → MVP (target July 2026; GA August 2026)

**Active spikes:**
- **Spike 11 (Authorization)** ← blocks MVP — bearer validation done; remaining: Azure AD App Roles, DGraph `@auth` directives, Echo middleware role enforcement, offline JWT integration tests ⚠️ Opus design session first

**Recently completed:**
- **Spike 13 (Orb import API)** — done: OCI puller, cosign verify, dgraph live import, polling loop
- **Spike 17 (Orb UI)** — done: shared template infrastructure, `UIConfig` + `PageActions` (read-only mode), orb Echo server, status page (pre/post import states), import subgraph, inventory (Config Items), schema version, DC + servers (read-only DataTables + HTMX tabs), import history, divergence report; `DCSlug` removed — orb is stateless re: DC identity, DC name derived from imported DGraph data
- **Orbital inventory namespace filter** — page-level namespace selector above the table; derived from orbId prefix; regex column search; persisted to localStorage

**MVP gaps remaining:**
- Authorization (Spike 11) ← next priority
- Valkey cache-aside (Spike 9b) — not yet implemented
- Schema management — versioned apply with backwards compat check on startup
- Orb registry — register, authenticate, and revoke orbs
- Orb: deployment model (Spike 15), API surface & authN/Z (Spike 16), divergence reporting (Spike 14)
- Orb local overrides / config actuation abstraction — belongs to ConfigBundle domain + Spike 14; orb needs abstractions for how users handle config actuation and local overrides, not a hard-coded override system
- Testing foundations — unit, integration, code coverage, CI pipeline, AKS smoke suite
- Security hardening — critical/high findings before any prod exposure
- Production deployment — AKS prod, ingress, TLS, CI/CD

**Next priority:** Start Spike 11 App Roles → DGraph `@auth` directives.

*Update this section at each session wrap-up.*

## Model & Workflow Guide

**Default model: Sonnet.** Use Opus only at specific decision points. Opus sessions should be short (15–30 min) and design-focused — then hand back to Sonnet to implement.

| Sonnet | Opus |
|---|---|
| Implementation, UI, bug fixes, seeding, scripts | Design decisions, security/authz, spike planning, cross-cutting review |
| Anything with a settled decision in CLAUDE.md | Tasks touching 3+ domains simultaneously |
| Known-spec features | New spikes being planned for the first time |

### When to suggest switching to Opus (`/effort max`)

If on Sonnet and the user asks any of the following, **proactively suggest switching to Opus before proceeding**:

- "How should we design / approach / implement [spike or new feature]" — design work where the answer is not already a settled decision
- Any task touching 3+ domains simultaneously (e.g. schema + auth + middleware + CLI)
- Security-sensitive design: authz model, signing, JWT validation, key management
- Planning a spike that is "Not started" in ROADMAP.md for the first time
- Reviewing a completed spike for correctness against architectural invariants
- User says "discuss:" or "thoughts:" but the topic has significant design implications

**Signal to user:** *"This is a design decision with long-term consequences — consider switching to Opus (`/effort max`) before I implement anything."*

### When Opus should signal Sonnet-ready (`/effort normal`)

If on Opus and the task is now implementation of a settled plan:

- A clear implementation plan exists from this session or from settled decisions in CLAUDE.md
- Work is execution: UI changes, bug fixes, seeding, scripts, known-spec features

**Signal:** *"Design is settled — switch to Sonnet (`/effort normal`) to implement."*

### Spike lifecycle checkpoints

1. **Before starting a new spike** → `/plan` or Opus design session; read ROADMAP.md spike definition
2. **After implementing a complex spike** → consider Opus review against deployment model invariants before marking done
3. **Before wrapping up** → check if any decisions from this session belong in CLAUDE.md settled decisions or domain files

### Session hygiene

- Start a new session after each natural milestone (feature done, spike complete, bug fixed). Don't try to span a full spike in one session — compaction loses precision.
- Domain files: before working in a specific area, read the relevant file (see below).

## Domain Reference Files

Before starting work in a specific area, read the relevant file. These contain all settled decisions, patterns, and gotchas for that domain.

| Working on | Read |
|---|---|
| DGraph schema, queries, mutations, export, seeding | `docs/claude/DGRAPH.md` |
| UI templates, HTMX, JavaScript, CSS | `docs/claude/UI.md` |
| Auth, sessions, OIDC, bearer tokens, keychain | `docs/claude/AUTH.md` |
| Audit events, mutation recording, `graphql.go` | `docs/claude/AUDIT.md` |
| OCI publish, export jobs, backup, restore | `docs/claude/OCI.md` |
| Planning or starting any spike | `ROADMAP.md` |

## Local Development

### Dev invariant

Every developer on this project must be able to run the following without any extra setup:

```bash
make up           # terminal 1 — start all dependencies
make run-orbital  # terminal 2 — orbital on :8001
make run-orb      # terminal 3 — orb on :8010
```

Then open both UIs side by side:
- Orbital: http://localhost:8001
- Orb: http://localhost:8010

**Nothing we commit should break this flow.** Before merging any change that touches templates, handlers, routes, or the template loader, verify both UIs load without 500 errors.

### Services started by `make up`

| Service | Port(s) | Notes |
|---|---|---|
| DGraph Zero | 5080, 6080 | Orbital cluster coordinator |
| DGraph Alpha | 8080 (HTTP/GraphQL), 9080 (gRPC) | GraphQL playground at http://localhost:8080 |
| DGraph Ratel | 8000 | DGraph UI |
| PostgreSQL | 5432 | user/password/db: `orbital` |
| Orb DGraph Zero | 5082, 6082 | Orb cluster coordinator |
| Orb DGraph Alpha | 8082 (HTTP/GraphQL), 9082 (gRPC) | Orb local graph |
| MinIO / OCI registry | various | S3-compatible storage + artifact registry |

No env sourcing required — all local dev defaults are in `config.go` / `orbconfig/config.go`.

### Seeding

```bash
make seed   # seed DGraph with example data (run after make run-orbital is up)
```

### Running tests

```bash
make test-unit         # no services required
make test-integration  # requires: make up
make test-e2e          # requires: make run-orbital
make test-e2e-orb      # requires: make run-orb
```

## Repository Structure

```
cmd/
  orb/                # orb binary — subcommand-driven (orb start, orb scan, orb export, orb import)
  orbital/            # orbital server entry point
deploy/
  local/              # Local development stack (docker-compose)
  orb/                # Deployment files for orb
  orbital/            # Deployment files for orbital
docs/
  claude/             # Domain reference files for AI sessions (DGRAPH, UI, AUTH, AUDIT, OCI)
  decisions/          # Architecture decision records (ADRs)
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
- All styles go in `web/sass/main.scss` — never edit `web/static/css/main.css` directly
- Before marking a task as done: check whether any architectural decisions, conventions, or settled rules from this session should be added to CLAUDE.md or the relevant domain file

### Conversation conventions

- Messages starting with **"thoughts:"** or **"discuss:"** — do not write any code or files, respond conversationally only.
- Messages starting with **"propose:"** — produce a written design proposal for review, do not write any code.
- Messages starting with **"challenge:"** — no code. User will lead with a thesis ("I believe X because Y"). Respond by: (1) verifying you understood the design correctly, (2) comparing it to standard/best practices from your knowledge base, (3) surfacing gaps or risks in the reasoning. Be adversarial — the goal is to stress-test the design, not validate it. Read relevant design docs before responding.
- Messages starting with **"validate:"** — no code. Check the user's reasoning against the design docs and your knowledge base. Confirm what holds, flag what doesn't. Less adversarial than `challenge:` — the user believes this is correct and wants exceptions surfaced.
- Use `/plan` mode for architecture and schema design discussions before any implementation begins.
- Run `/wrap-up` at the end of a session to update CLAUDE.md, save memories, and update Current State.

## Settled Decisions

These have been explicitly decided. Do not re-suggest them.

- **Do not replace DGraph** — chosen deliberately; RDF model fits configuration items naturally
- **Do not switch to Redis** — Valkey chosen over Redis due to licensing
- **Do not use `schollz/progressbar` alone for spinners** — indeterminate mode causes terminal jitter; use `briandowns/spinner` for spinners and `schollz/progressbar` for determinate progress bars
- **Do not prescribe a data transport mechanism** — orbital's contract ends at the export API (`json.gz` + `schema.gz`). How that payload is transported, packaged, or stored is the consumer's concern.
- **Report intake API is transport-agnostic** — how reports travel from edge to orbital is the deployment layer's concern. Do not couple the intake API to any specific transport.
- **Schema migration automation is out of MVP scope** — a runbook is sufficient for MVP. Do not build a custom migration tool until explicitly scoped.
- **Do not proxy Ratel through orbital** — Ratel is a React SPA with `PUBLIC_URL=/`; webpack bakes absolute paths (`/3rdpartystatic/`, `/static/js/`) that bypass any sub-path reverse proxy. Correct solution: dedicated DNS hostname (`ratel.devnew.armada.internal`) with its own Istio VirtualService. Until infra provisioning, show a todo toast when the link is clicked.
- **PLM and ITSM integrations are out of v1 scope** — vendor selection in progress. Design behind Go interfaces when the time comes; do not couple to any specific vendor now.
- **Network infrastructure config items are out of v1 scope** — VLANs and general network IPs are owned by an external system. Functional IPs tied to specific workloads (Tinkerbell, K8s control plane) are in scope as properties or dedicated nodes — discuss before adding.
- **Orb DGraph is a read-only intent mirror** — `override_handlers.go` must never mutate DGraph. Local overrides write only to `overrides.json`. DGraph retains orbital's authoritative intent verbatim.
- **"Import is sudo"** — `orb import` always runs `drop_all` + live load, overwriting all local DGraph state. `overrides.json` is cleared on successful import. Local overrides do not survive an import.
- **Orb divergence transport is not direct HTTP** — orb never sends divergence reports directly to orbital over HTTP. Transport is S3/OCI (deployment layer concern). Direct HTTP between orb and orbital violates the air-gap invariant.
- **Orb UI pages mirror orbital client-side patterns** — orb pages use the same interaction model as orbital: GraphQL proxy fetch, DataTables, HTMX tab swap. Not simplified server-rendered alternatives.
- **Orb is stateless re: DC identity** — `DCSlug` was removed from `orbconfig.Config`. Orb derives which data center it serves from the imported DGraph data (one `DataCenter` node after `drop_all` + live load). `ORB_OCI_REPO` carries the full DC-specific path (e.g. `orbital/colo-galleon`). Do not re-add a `DCSlug` field.
- **Inventory namespace filter is page-level, not a DataTable column filter** — the namespace selector lives in the page header (above the table) and uses regex search on the orbId column (`^namespace:`). Do not move it into the DataTable toolbar — namespace is a scope, type is a column filter; they are different cognitive categories.
- **Product naming: "Orbital" (cloud) / "Orb" (edge) — this is the north star.** The project is called Orbital. The cloud component UI shows "Orbital." The edge component UI shows "Orb." Do not use "Orbital Edge" or conflate the two. Orb is a purpose-built edge agent — not a deployment variant of Orbital. `AppName: "Orbital"` in orbital handlers; `AppName: "Orb"` in orb handlers.

*Domain-specific settled decisions live in `docs/claude/DGRAPH.md`, `docs/claude/UI.md`, `docs/claude/AUTH.md`, `docs/claude/AUDIT.md`, `docs/claude/OCI.md`.*

## E2E Tests (Playwright)

Tests live in `e2e/`. Run with `make test-e2e` (requires orbital running on `:8001`).

**Auth setup:** `e2e/global-setup.ts` logs in as `admin@armada.ai` / `admin` once, saves session cookie to `e2e/.auth.json`. All tests reuse this state. The `.auth.json` file is gitignored and regenerated automatically.

**Test conventions:**
- Use `data-testid` attributes on elements that need stable selectors
- Assert against values read from the page rather than hardcoded seed data — hardcoded counts break when seed data changes

## Go Conventions

- **Error wrapping** — use `fmt.Errorf("...: %w", err)`; never discard or log-and-return
- **Context** — always the first argument: `func Foo(ctx context.Context, ...)`
- **Constructors** — named `New[Type]`, e.g. `NewServer`, `NewClient`
- **`cmd/` is thin** — entry points only; all logic lives in separate packages
- **Tests** — table-driven with `t.Run`; avoid test helpers that obscure failure sites
- No `init()` functions — exception: Cobra command files in `internal/cli/` may use `init()` to register subcommands and flags
- No global variables
- No `panic()` outside of `main()`

## Development Status

Early-stage project. The Go module is initialized at `github.com/armada/orbital`.
