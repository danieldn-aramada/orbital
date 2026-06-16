# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Orbital** is an API-first, graph-native configuration management system for modular data centers. Written in Go.

### Key Concepts

- **`orbital`** — Server running in cloud. Central configuration hub — holds design intent (configuration items) for all modular data centers, serves the Topology API for digital twin building, and exposes a config export API for orbs to consume.
- **`orb`** — Self-contained edge service running inside a modular data center. Stores and serves orbital's intended state offline. Exposes an API for other edge components (e.g., cb-agent) to submit divergence reports, which orb publishes to external storage (S3/OCI) for orbital to consume. Suitable for air-gapped deployments.

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

Orbital provides APIs — consumers wire the transport. Four APIs: Export (`POST /api/v1/export`), Publish (`POST /api/v1/export/jobs/:jobId/publish`), Report intake, Topology (DGraph GraphQL proxy). Orbital never initiates contact with the edge.

See `docs/reference/DGRAPH.md` for schema, Namespace/DataCenter conventions, and blue-green export topology.

### Other subsystems

Two runtime constraints worth knowing in every session:
- **Clients never query DGraph directly** — all GraphQL goes through the Go server (auth, rate limiting, caching).
- **Valkey is an optimization, not a dependency** — orbital must operate correctly without it.

Auth, audit, export/OCI/backup/restore, and orb details are in the Reference Index below.

### Orb

`orb` is a single binary (`cmd/orb/`). `orb start` is the long-running edge service — passive cache + local UI. Orb does not scan hardware, does not execute against K8s, is not a K8s controller. `orb scan` is post-MVP.

All architecture and settled decisions for orb (import pipeline, consumer dispatch, DGraphBackend, divergence intake, orb scan) are in `docs/reference/ORB.md`.

## Model & Workflow Guide

**Default model: Sonnet.** Use Opus only at specific decision points. Opus sessions should be short (15–30 min) and design-focused — then hand back to Sonnet to implement.

| Sonnet | Opus |
|---|---|
| Implementation, UI, bug fixes, seeding, scripts | Design decisions, security/authz, spike planning, cross-cutting review |
| Anything with a settled decision in CLAUDE.md | Tasks touching 3+ domains simultaneously |
| Known-spec features | New spikes being planned for the first time |

### When to suggest switching to Opus (`/effort max`)

Proactively suggest before proceeding if: (1) design work with no settled decision, (2) task touches 3+ domains, (3) security-sensitive design (authz, signing, JWT), (4) planning a new spike for the first time, (5) user says `discuss:` or `thoughts:` with significant design implications.

**Signal:** *"This is a design decision with long-term consequences — consider switching to Opus (`/effort max`) before I implement anything."*

### When Opus should signal Sonnet-ready (`/effort normal`)

When a clear plan exists and work is execution: UI changes, bug fixes, seeding, scripts, known-spec features.

**Signal:** *"Design is settled — switch to Sonnet (`/effort normal`) to implement."*

### Spike lifecycle checkpoints

1. **Before starting a new spike** → `/plan` or Opus design session; read ROADMAP.md spike definition
2. **After implementing a complex spike** → consider Opus review against deployment model invariants before marking done
3. **Before wrapping up** → check if any decisions belong in the relevant domain file (see Reference Index below)

### Session hygiene

Start a new session after each natural milestone (feature done, spike complete, bug fixed). Don't try to span a full spike in one session — compaction loses precision.

## Reference Index

### Domain files

Read the relevant file before starting work in that area. Each file contains settled decisions, patterns, and gotchas. **When a decision is made, document it in the domain file — not in CLAUDE.md.**

| Working on | File |
|---|---|
| DGraph schema, queries, mutations, export, seeding | `docs/reference/DGRAPH.md` |
| UI templates, HTMX, JavaScript, CSS | `docs/reference/UI.md` |
| Auth, sessions, OIDC, bearer tokens, keychain | `docs/reference/AUTH.md` |
| Audit events, mutation recording, `graphql.go` | `docs/reference/AUDIT.md` |
| OCI publish, export jobs, backup, restore | `docs/reference/OCI.md` |
| Orb import pipeline, consumer dispatch, DGraphBackend, orb UI | `docs/reference/ORB.md` |
| Divergence resolution semantics, accept/force/ignore actions, orbital→cb-bundler contract | `docs/reference/DIVERGENCE.md` |
| Producer-facing divergence intake API (orb-side) | `docs/reference/DIVERGENCE-INTAKE.md` |
| Planning or starting any spike | `ROADMAP.md` |

### Decision records

Architecture decisions with full rationale. Read when the context would otherwise be invisible from the code.

| When working on | Read |
|---|---|
| Any new REST endpoint | `docs/decisions/002-api-design-philosophy.md` |
| Audit mutation recording, orbId extraction | `docs/decisions/001-mutation-audit-recording.md` |
| Audit event_category, operation names | `docs/decisions/003-audit-event-categories.md` |
| DGraph schema migration (namespace edge) | `docs/decisions/004-namespace-id-scalar-migration.md` |
| Security/audit logging (OWASP/NIST alignment) | `docs/decisions/004-security-logging.md` |
| Backup scheduler design | `docs/decisions/005-backup-scheduler.md` |
| Restore mechanism (why subprocess, not exec) | `docs/decisions/006-dgraph-restore-backend.md` |
| DGraph schema migration (tooling landscape, sharp edges, production approach) | `docs/decisions/007-dgraph-schema-migration.md` |
| Bundler / internal-service auth to orbital (OAuth2 client credentials) | `docs/decisions/010-bundler-service-auth.md` |
| OCI bundler pipeline, ConfigBundle integration | `docs/configbundle-integration.md` |

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

Starts DGraph (blue `:8080`/`:9080` + scratch `:8081`/`:9081`), PostgreSQL (`:5432`), Valkey, MinIO, OCI registry, and orb's DGraph (`:8082`/`:9082`). No env sourcing required — all local dev defaults are in `config.go` / `orbconfig/config.go`. See `deploy/local/docker-compose.yml` for full port map.

### Seeding

```bash
make seed   # seed DGraph with example data (run after make run-orbital is up)
```

### Running tests

```bash
make test-unit         # no services required
make test-integration  # requires: make up
make test-e2e          # Playwright UI for orbital + orb (requires both running)
make release-check     # pre-release: build images, start containers, full publish→import→restore flow (~22min)
```

## Repository Structure

`cmd/` — entry points (`orbital/`, `orb/`). `internal/` — all application logic (`handler/`, `auth/`, `graph/`, `server/`, `config/`). `web/templates/` — Go templates split by app (`orbital/`, `orb/`, `shared/`). `web/shared/static/` — JS modules, CSS, vendor libs. `schema/` — DGraph GraphQL schema. `ent/` — PostgreSQL schema + generated client. `deploy/local/` — docker-compose dev stack. `docs/reference/` — domain reference files. `docs/decisions/` — ADRs. `e2e/` — Playwright tests.

## Working Style

- Don't add comments that just restate what the code does
- Don't refactor code that wasn't part of the request — ask first
- Don't add third-party packages without asking first
- Only touch files relevant to the task
- Don't clean up unrelated code while working on something else
- Don't add TODOs or placeholder comments
- All styles go in `web/sass/main.scss` — never edit `web/shared/static/css/main.css` directly. See `docs/reference/UI.md` for JS/HTMX/template patterns.
- **HTML fragment negotiation uses `HX-Request: true`, never a separate URL** — when a handler needs to return an HTML fragment for the UI and JSON for API callers, branch on `c.Request().Header.Get("HX-Request") == "true"` inside the **existing** handler. Do not create a sibling `/rows` or `/fragment` route. Violates REST content negotiation (RFC 7231) and breaks the `/api/v1/` contract.
- **Write tests only when they guard a specific regression class — not by default.** Before writing a test, name the regression in one sentence: "this catches X if someone changes Y." If the assertion is just "the function returns what I just made it return," it's tautological theater — skip it. Default to NO test unless ONE of these holds: (a) you can name a concrete future change that would silently break behavior and this test would catch it; (b) the failure mode isn't obvious from the visual check the user already performs (HTML escaping, error-path branching, edge cases); (c) the function is pure with edge cases (nil, empty, missing fields) where the edge cases ARE the regression class; (d) one of the specific rules below (persistence round-trip, etc.) mandates it. The cost of a test is paid forever — review time, maintenance churn when the code shifts, false confidence when a passing test asserts the wrong thing. The benefit is one specific regression avoided. Most behavioral changes don't earn a test under this calibration; that's the right answer.
- **Run tests after writing them** — always run the relevant test command after writing new tests. If tests fail, diagnose and fix before reporting done. Do not hand back failing tests.
- **Test at the lowest isolatable level** — don't write an e2e test when a unit test can cover the same behavior. Level order: unit (no services) → integration (real services) → e2e (browser). Choose the lowest level where the behavior is fully exercised.
- **Any persistence requires a round-trip test** — if data is written to disk, PostgreSQL, or any file: write a test that writes, reads back, and asserts. Persistence bugs are invisible without this. E.g., a new `bool` field on a struct written to JSON must be verified to survive encode+decode. This is a concrete regression class (silent serialization loss) — it earns the test by default.
- **Don't write integration tests you can't run locally.** When the surrounding package has an unrelated build break (e.g. a separate `_integration_test.go` references a stale signature), an integration test you can't execute is zero-signal noise. Skip it, note the gap in the PR description, and revisit when the build is restored. Do not write tests "for symmetry" with code you can't validate.
- **Don't duplicate visual validation.** If the user verifies the change by clicking once in the browser, a unit test asserting the same DOM shape is overhead. Reserve tests for what the user can't see by clicking: HTML escape, error branching, edge cases, contract shape, security-sensitive paths.
- Before marking a task as done: check whether any decisions made this session should be documented. Domain-specific decisions go in the relevant `docs/reference/` file (see Reference Index above). Only cross-cutting platform decisions go in CLAUDE.md's Settled Decisions.

### Conversation conventions

- Messages starting with **"thoughts:"** or **"discuss:"** — do not write any code or files, respond conversationally only.
- Messages starting with **"propose:"** — produce a written design proposal for review, do not write any code.
- Messages starting with **"challenge:"** — no code. User will lead with a thesis ("I believe X because Y"). Respond by: (1) verifying you understood the design correctly, (2) comparing it to standard/best practices from your knowledge base, (3) surfacing gaps or risks in the reasoning. Be adversarial — the goal is to stress-test the design, not validate it. Read relevant design docs before responding.
- Messages starting with **"validate:"** — no code. Check the user's reasoning against the design docs and your knowledge base. Confirm what holds, flag what doesn't. Less adversarial than `challenge:` — the user believes this is correct and wants exceptions surfaced.
- Use `/plan` mode for architecture and schema design discussions before any implementation begins.
- Run `/wrap-up` at the end of a session to update CLAUDE.md, save memories, and update Current State.

## Settled Decisions

These are cross-cutting platform decisions. Domain-specific decisions live in the domain files listed above — **that is where new decisions belong when you document them.**

- **Do not replace DGraph** — chosen deliberately; RDF model fits configuration items naturally
- **Do not switch to Redis** — Valkey chosen over Redis due to licensing
- **Do not use `schollz/progressbar` alone for spinners** — indeterminate mode causes terminal jitter; use `briandowns/spinner` for spinners and `schollz/progressbar` for determinate progress bars
- **Do not prescribe a data transport mechanism** — orbital's contract ends at the export API (`json.gz` + `schema.gz`). How that payload is transported, packaged, or stored is the consumer's concern.
- **Report intake API is transport-agnostic** — how reports travel from edge to orbital is the deployment layer's concern. Do not couple the intake API to any specific transport.
- **Schema migration automation is out of MVP scope** — a runbook is sufficient for MVP. Do not build a custom migration tool until explicitly scoped.
- **Do not proxy Ratel through orbital** — Ratel is a React SPA with `PUBLIC_URL=/`; webpack bakes absolute paths that bypass any sub-path reverse proxy. Correct solution: dedicated DNS hostname with its own Istio VirtualService. Until then, show a todo toast when the link is clicked.
- **PLM and ITSM integrations are out of v1 scope** — vendor selection in progress. Design behind Go interfaces when the time comes.
- **Network infrastructure config items are out of v1 scope** — VLANs and general network IPs are owned by an external system. Functional IPs tied to specific workloads (Tinkerbell, K8s control plane) are in scope as properties or dedicated nodes — discuss before adding.
- **ConfigBundle is a separate project, built after orbital** — orbital's APIs (export, divergence intake) are the contract. ConfigBundle is designed around orbital's APIs, not the inverse. Do not add ConfigBundle awareness to orbital.
- **Orbital is the sole OCI producer** — no downstream system needs registry write credentials. See `docs/reference/OCI.md` for bundler/signing details.
- **Product naming: "Orbital" (cloud) / "Orb" (edge) — this is the north star.** Do not use "Orbital Edge" or conflate the two. Orb is a purpose-built edge agent, not a deployment variant of Orbital. `AppName: "Orbital"` in orbital handlers; `AppName: "Orb"` in orb handlers.
- **`actorFromContext(c echo.Context) string` is the canonical identity helper** — in `internal/handler/actor.go`. Prefers email over display name. All handlers recording "created by" or "actor" must call this. Never inline `c.Get("user_name")` / `c.Get("user_email")` in new handlers. `ui.go` is the only legitimate exception.
- **REST API convention: operation-centric triggers, resource-centric jobs** — orbital is GraphQL-first for CRUD; REST endpoints exist only for async operational workflows. Trigger endpoints create a job and return a job ID. Do not create resource-centric paths for operations that have no corresponding GET/PUT/DELETE. Rationale: `docs/decisions/002-api-design-philosophy.md`.
- **Local dev defaults must point to local services** — `OCIRegistry`, `S3Endpoint`, `S3Bucket`, `S3AccessKey`, `S3SecretKey` all default to local Docker Compose services. Production credentials must never appear as code defaults.
- **All Docker Compose images must use pinned versions** — no `latest` tags. Look up the actual release tag from the project's GitHub releases.
- **`cosign.key` lives at `deploy/local/cosign.key`** — never at the project root. Local dev secrets live alongside local dev config. Gitignored via `deploy/local/*.key`. The compose file mounts it at `./cosign.key:/app/deploy/local/cosign.key:ro`.
- **`schema/VERSION` is the authoritative schema version label** — single line (e.g. `v1`). Bumped manually on DGraph-relevant schema changes only. Comments, whitespace, and formatting changes to `schema/schema.graphql` do NOT bump it. See `docs/decisions/007-dgraph-schema-migration.md`.
- **`schema/schema.graphql` is the production schema** — was `schema-demo.graphql`; renamed 2026-06-07. All references must use `cfg.SchemaPath` (env: `ORBITAL_SCHEMA_PATH`, default `schema/schema.graphql`). Never hardcode the path.
- **GraphQL proxy strips `orbId` from variables only when the query doesn't declare `$orbId`** — `orbIdIsQueryVar := strings.Contains(req.Query, "$orbId")` controls this. If the query declares `$orbId`, stripping it causes a DGraph "must be defined" error. Both inline-literal and declared-variable patterns must work.
- **Per-component versioning across binaries** — server tags as `v*` (existing lineage), orbctl as `cli/v*`, orb as `orb/v*`. Makefile derives each via `git describe --match`/`--exclude`. Each build target injects its own value into `internal/version.Version` at link time. Same package var, different injected values per binary. Rationale: `docs/decisions/009-per-component-versioning.md`. Do NOT re-unify under a single tag scheme — CLI version conflation with server version was the problem this solves.
- **CLI binary is `orbctl`, source lives in `cmd/orbctl/` and `internal/orbctl/`** — follows the `kubectl`/`istioctl`/`etcdctl` convention. Distinguishes the CLI from the `orbital` cloud daemon and the `orb` edge daemon. One CLI targets both — there is no per-app CLI. Future auth divergence (e.g. orb gaining its own IdP) is handled via context profiles in `orbctl`, not a forked binary. Tag scheme stays `cli/v*` (don't churn tag history).
- **K8s probes: readiness only, always-200 `/healthz`** — both orbital and orb expose `GET /healthz` that returns `{"status":"ok"}` 200, registered before any auth middleware. K8s manifests configure `readinessProbe` against it. Do NOT add `livenessProbe` and do NOT make `/healthz` depend on downstream services (DB, DGraph). Probes are footguns: a DB-checking liveness causes restart loops on transient backend failures, making debugging impossible. Readiness toggles traffic; liveness kills the process. The right separation is K8s observing reachability, while orbital handlers surface backend health to the caller.
- **GraphQL endpoint is `/graphql` in both apps** — orbital and orb both register the proxy at `/graphql`. This follows the GraphQL community convention (GitHub, GitLab, NetBox, Apollo): GraphQL is not versioned by URL because schemas evolve via field deprecation. `/api/v1/` is reserved for REST endpoints where version semantics matter. `GraphQLPath` in `layout.UIConfig` must match. Do NOT put GraphQL under `/api/v1/`.
- **Swagger docs are always regenerated via `swag`, never hand-edited** — `docs/swagger.yaml`, `docs/swagger.json`, `docs/docs.go` (and the `docs/orb/` equivalents) are generated artifacts. Run `make docs` after changing any `@Router`, `@Tags`, or `@Summary` annotations in either app — it regenerates both. Direct edits to the generated files will be overwritten.
- **Deployment manifests: kustomize overlays are canonical** — `deploy/base/` + `deploy/overlays/dev-netbox/` + `deploy/overlays/dev-orbital/` is the deploy path. Raw manifests in `deploy/legacy/` exist for historical reference only. Do not add new raw manifests to `deploy/legacy/`; new resources go in `deploy/base/` and are patched per-overlay if they vary. See `deploy/README.md`.
- **Tests validate the deployable artifact, not dev-only code paths** — when a test needs functionality that only works in the container (e.g. `dgraph live` requires the binary, which orbital's multi-stage Dockerfile bakes in), the answer is "run the container under test," not "add a host-mode backend so `go run` can simulate it." Dev-only backends (the deleted `DockerBackend`/`DockerRestoreBackend`) are tempting because they unblock fast iteration, but they create a parallel code path that smoke can validate while production never executes. `make release-check` builds the actual orbital + orb images and runs them as containers against the local stack — that's the only way to validate what ships. `make run-orbital` / `make run-orb` stay as `go run` for fast iteration; restore + import flows fail under `go run` because dgraph isn't on PATH, and that's the correct trade-off.
- **Single `Dockerfile`, two targets** — `docker build --target=orbital -t orbital:local .` and `docker build --target=orb -t orb:local .` produce the two deployable images from one Dockerfile. The shared `runtime-base` stage (alpine + tools + web/ + schema/ + dgraph binary) guarantees both images stay in lockstep. Default target is `orbital` (last `FROM`) so `docker build .` and `make push` behavior is unchanged. Do not fork into per-app Dockerfiles — divergence drift is the reason this was consolidated.
- **`make release-check` is the pre-release gate, not "smoke"** — industry convention: smoke = shallow sanity check (seconds). Our 22-min build-images + start-containers + seed + publish→import→restore flow is the opposite. It validates the deployable artifact end-to-end. Use `release-check` for pre-release validation; reserve `smoke-aks` for shallow post-deploy probes.
- **Access log captures `request.id` + GraphQL operation name; UA only at login events** — `internal/middleware/logger.go` writes `request.id` (from Echo `RequestID` middleware), `graphql.operation.name`/`graphql.operation.type` (set by `graphql.go` handler). `user_agent.original` is NOT in access logs — UA is captured in `loginSuccess`/`loginFailed`/`logout` audit events only (Stripe/Datadog convention: log volume + UA correlation belongs at session boundaries). Status escalates log level: 5xx → ERROR, 4xx → WARN, else INFO. Do not re-add UA to access logs.

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
