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

- **Go** — Implementation language for both `orbital` and `orb`
- **DGraph** (community edition) — Graph database with native GraphQL API on top of RDF-like storage; stores all configuration items. Chosen because the RDF model fits configuration items naturally, and the GraphQL API lets external teams (e.g. a digital twin UI) consume data without custom endpoints. Self-hosted in the same Kubernetes namespace as orbital.
- **PostgreSQL** — Stores all managed-service operational data for `orbital`: orb registry, user accounts, audit logs, job/sync history, schema versions, DGraph backup records. Anything typical for running a managed service goes here, not in DGraph. PostgreSQL backup is handled by Azure managed PostgreSQL — not orbital's concern.
- **Valkey** — In-memory cache for `orbital`; chosen over Redis due to licensing.

## Architecture Notes

### Project boundary

Orbital is responsible for: the configuration graph, the Topology API, drift reporting, producing an exportable config payload for edge consumption, and optionally publishing those exports as signed OCI artifacts to an operator-configured registry.

Orbital is **not** responsible for: how edge consumers pull and apply OCI artifacts, how configuration is reconciled against real infrastructure, or how orbs receive and apply configuration. Those concerns belong to the deployment layer above orbital.

**OCI publishing is a standalone delivery capability.** When `ORBITAL_OCI_REGISTRY` and `ORBITAL_OCI_SIGNING_KEY_PATH` are configured, orbital can push subgraph exports as signed OCI artifacts directly — no external tooling required. This does not conflict with the `configbundle` project: `configbundle` remains a valid consumer that calls orbital's export API and handles its own packaging. An operator can use orbital's built-in publish feature, use `configbundle`, or implement a completely different transport. These are not mutually exclusive.

This boundary keeps orbital adoptable outside any specific deployment context. A consumer that doesn't use OCI registries should still be able to use orbital as a CMDB and Topology API.

### Deployment model invariants

The following invariants apply to Kubernetes-based deployments of orbital. Orbital's design must not violate them, but orbital does not enforce them — they are maintained by the deployment layer (K8s controllers, bundle infrastructure, etc.):

1. **Nothing in the cloud executes directly against a modular data center.** Orbital publishes intent. Edge components pull and apply configuration locally.
2. **Desired state and observed state are represented explicitly and may diverge.** Divergence during disconnection windows is data, not an error condition.
3. **Authoritative reconcilers run locally within the modular data center.** The cloud is never part of the reconciliation path. The CMDB is not part of the reconciliation path.
4. **The CMDB (DGraph) is a graph index and relationship store.** Configuration actuation flows through the deployment layer — not through the CMDB.
5. **GraphQL mutations on orbital update authoritative intent only.** They do not execute actions remotely or trigger actuation. Actuation is deferred to the deployment layer — it occurs when config is delivered to and reconciled at the edge.

### Data flow
Orbital provides the APIs — consumers wire the transport. Orbital does not prescribe how its APIs are called or how payloads move between systems.

- **Export API** (`POST /api/v1/datacenters/{id}/export`) — produces a scoped `json.gz` + `schema.gz` for a data center's subgraph. How that payload is packaged and delivered to the edge is the caller's concern.
- **Publish API** (`POST /api/v1/export/jobs/:jobId/publish`) — pushes a completed export as a signed OCI artifact to the configured registry. Optional — requires `ORBITAL_OCI_REGISTRY` and `ORBITAL_OCI_SIGNING_KEY_PATH`. Signing is mandatory when publishing; keys are configured via env vars, never via UI forms.
- **Report intake API** — receives drift and divergence reports. How those reports travel from the edge to orbital is the caller's concern. Orbital never initiates contact with the edge.
- **Topology API** — proxies DGraph's GraphQL API for digital twin consumers. No transport concern.

The configbundle project (separate repository) is one example of a deployment layer built on top of orbital: its Bundle Generator calls the export API, packages the result as a signed OCI artifact, and its edge agent delivers it to orb. This is a reference implementation, not a requirement.

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
1. Query blue for the target namespace subgraph via DQL (`has(ConfigItem.namespace)` + `uid_in`) — fetches all scalar and edge predicates separately, merged by UID in Go
2. Wipe scratch (`drop_all`) at the START of each export — prevents stale data from a previous export bleeding in
3. Apply schema to scratch
4. Bump scratch Zero's UID lease to cover the highest UID in the subgraph
5. Load subgraph into scratch via DQL mutate (preserves original UIDs from blue)
6. Trigger native DGraph export mutation on scratch → `json.gz`
7. Package `json.gz` + `schema.gz` into a zip artifact

Only one export may run at a time across all data centers (scratch is shared state).

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

### Divergence reporting
Divergence reports are generated when a local edge admin overrides the intended configuration on-site. Orb records the override and sends a report to a shared location (e.g. S3) that orbital polls. Orbital surfaces these reports to cloud admins, who can accept, reject, or ignore each change. Accepting updates the intent in orbital; rejecting or ignoring leaves intent unchanged. Orbital is never in the reconciliation path — it only surfaces the divergence for human decision.

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
- All styles go in `web/sass/main.scss` — never edit `web/static/css/main.css` directly, it is generated. Rebuild with `make build-css` (one-time) or `make watch-css` (watch mode)
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
- **OCI publishing uses oras-go v2 + cosign Go SDK** — `oras.land/oras-go/v2` for pushing, `github.com/sigstore/cosign/v2` for signing. Do not use the cosign binary — the SDK is used directly in-process. Cosign keys are configured via `ORBITAL_OCI_SIGNING_KEY_PATH` (private key file, unencrypted); signing is mandatory and publish fails if key is not configured.
- **OCI credentials stay in env vars** — `ORBITAL_OCI_USERNAME`/`ORBITAL_OCI_PASSWORD` are env-only. No credential storage in PostgreSQL. The signing private key is also env/file-only, never a form field.
- **OCI artifact format** — `artifactType: application/vnd.orbital.subgraph.v1`, two layers: `data.json.gz` (`application/vnd.orbital.subgraph.data.v1+gzip`) and `schema.gz` (`application/vnd.orbital.subgraph.schema.v1+gzip`). Manifest annotations use `com.armada.orbital.*` prefix.
- **OCI tag strategy** — monotonic `v{n}` tags per data center repo, derived from count of existing `registry_artifacts` rows. `:latest` updated on every successful publish. Re-publishing creates a new `registry_artifacts` row — full audit trail retained.
- **OCI signing is air-gap safe** — `TlogUpload: false` is set; no Sigstore network calls. Signature stored as OCI referrer. Public key distributed via orb onboarding response (primary, air-gap) and `GET /api/v1/oci/public-key` (secondary).
- **Export job lifecycle** — `pending → running → completed → stale`. Stale detection: on export job list page load, orbital checks scratch file existence for each completed job and marks stale if missing. Delete removes the PostgreSQL record, the export zip, and the job's scratch directory.
- **Export and publish are separate actions** — publish never happens automatically on export. Publish button appears on completed jobs. Re-publishing is allowed (creates new `registry_artifacts` row).
- **Export jobs are globally serialized** — scratch DGraph is shared state; only one export job may be pending or running at a time across all data centers. Trigger returns 409 if any export is already in progress.
- **Scratch DGraph is wiped at the START of each export, not the end** — `wipeScratch` (`drop_all`) runs before loading new data. If a previous job failed and left data, the next export starts clean. A wipe-at-end-only approach caused stale data from prior exports to bleed into subsequent ones.
- **Per-job scratch export directories** — each export job writes DGraph output to `/dgraph/export/<jobID>/` inside the scratch container (host-side: `DGRAPH_SCRATCH_EXPORT_DIR/<jobID>/`). The container-side base path `/dgraph/export` is hardcoded; only the host-side mount path is configurable. The directory persists until the user deletes the job — never auto-cleaned.
- **DGraph export `destination` parameter** — DGraph's export mutation accepts `destination` to route output to a specific path. Used to isolate per-job output. DGraph writes a timestamped subdirectory (`dgraph.r<raft>.u<date>.<time>/`) inside the destination.
- **Backup zip naming** — `orbital-<version>-<timestamp>.zip` (e.g. `orbital-v0.1.0-20260509T135041Z.zip`). Version comes from `internal/version.Version` injected at build time via ldflags.
- **Swagger docs regenerated via `make docs`** — runs `swag init -g cmd/orbital/main.go -o docs`. Both `make build-orbital` and `make run-orbital` depend on this target, so docs are always up to date. Swagger tag names: `backup graph`, `export subgraph`, `oci`.
- **`registry_artifact.datacenter_name` stores DC name at publish time** — denormalized for display; avoids a DGraph lookup on every artifact list. Default `""` allows migration on existing rows.
- **`IPAddress` uses typed back-refs (hub pattern), not a generic interface field** — `@hasInverse` in DGraph requires both sides to be the same **concrete type**. A generic back-ref like `assignedTo: [ConfigItem]` cannot be wired with `@hasInverse` because `ConfigItem` is an interface, not a concrete type. The solution: explicit named back-ref fields on `IPAddress` for each concrete type that references it (`serverOobIP: Server`, `eksaConfigTinkerbellIP: EksaConfig`, `eksaControlPlaneIP: EksaConfig`). Adding a new type connected to an `IPAddress` requires adding a new back-ref field to `IPAddress` — this is a deliberate, versioned schema change.
- **Use DQL to query "all items connected to an IP"** — GraphQL cannot traverse typed back-refs polymorphically. For queries like "is 10.0.1.15 already assigned anywhere?" use DQL via the `/query` endpoint with tilde (`~`) predicates to follow edges in reverse:
  ```
  { ip(func: eq(IPAddress.address, "10.0.1.15")) {
      uid IPAddress.address
      ~Server.oobIP { uid Server.hostname }
      ~EksaConfig.tinkerbellIP { uid EksaConfig.clusterType }
      ~EksaConfig.controlPlaneIP { uid EksaConfig.clusterType }
  } }
  ```
  This is the same pattern used for `~ConfigItem.namespace` to find all nodes in a namespace. DQL can traverse any predicate by UID regardless of GraphQL type boundaries.
- **`id: ID` must be declared on `ConfigItem` interface** — DGraph does not auto-expose the internal UID via GraphQL unless `id: ID` is explicitly present on the type or interface. Without it, `getDataCenter(id: $id)` queries fail. Always keep `id: ID` on the `ConfigItem` interface.
- **DC detail tab state uses localStorage, cleared on tab close** — the active panel (Servers/Racks/Divergence) is persisted per datacenter ID under key `dc-detail-tab-{id}`. It is cleared when the tab is closed so reopening always defaults to Servers. Do not persist tab state across tab close/reopen.
- **Go embedded struct field shadowing** — if a `page.*` struct embeds `layout.Base` and also declares the same field name (e.g. `AppVersion`), the outer field shadows the embedded one and template `{{.AppVersion}}` resolves to the outer (zero) value. Never redeclare fields that already exist on embedded types.
- **orbital-cli uses Authorization Code + PKCE for OIDC login** — Device Code flow was rejected because Conditional Access policies can block it. Auth Code + PKCE with a local redirect server (random port, `http://localhost:{port}`) is more resilient and opens the browser automatically.
- **Credential storage split: keychain + file** — `orbital login` is the only command that touches the OS keychain (via `github.com/zalando/go-keyring`). It stores `{refresh_token, name, email}` as a JSON blob in the keychain (service: `"orbital"`, account: `"credentials"`). The access token is NOT stored in the keychain — Azure AD JWTs are ~6KB which exceeds go-keyring's 4096-byte command limit on macOS (`security -i`). The access token + expiry is written to `~/.orbital/credentials.json` (mode 0600) after each login.
- **Subcommands never touch the keychain** — all subcommands read from `~/.orbital/credentials.json` only. If the access token is expired or missing, they exit with "run `orbital login`" — they do NOT silently refresh. Only `orbital login` does the keychain read + refresh token exchange.
- **orbital-cli vs orb login storage** — `orb login` uses a plain `FileStore` at `~/.orb/credentials.json` (stores full credentials including access token). `orbital-cli login` uses `KeychainStore` (refresh token) + `FileStore` at `~/.orbital/credentials.json` (access token only).
- **macOS keychain uses CGo + Security framework directly** — `go-keyring` was replaced with a self-contained CGo bridge calling `SecItemAdd`/`SecItemCopyMatching`/`SecItemDelete` directly. Uses `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` — credentials are locked when device is locked, not synced to iCloud, not migratable. Touch ID ACLs (`kSecAttrAccessControl` with biometry) require the binary to be code-signed with Apple entitlements (`errSecMissingEntitlement = -34018`) — not practical for an unsigned developer CLI. No biometric prompt; credentials are stored silently on login.
- **JSON blob in keychain, not separate entries** — storing all keychain fields as one JSON blob is conventional (GitHub CLI, Azure CLI pattern). It's atomic, avoids multiple keychain prompts, and is easy to version.
- **`orbauth` is the shared auth package** — `internal/orbauth/` contains PKCE flow, token exchange, refresh, Store interface, FileStore, and KeychainStore. Both `orb` and `orbital-cli` import it. Neither CLI contains auth logic directly.
- **Azure AD app registration must set `requestedAccessTokenVersion: 2`** — default is `null` (v1 tokens). v1 tokens have `iss: "https://sts.windows.net/..."` which does not match go-oidc's v2 discovery issuer. Set `api.requestedAccessTokenVersion: 2` in the app manifest (Azure portal → App Registrations → Manifest). v2 tokens have `iss: "https://login.microsoftonline.com/{tenant}/v2.0"`.
- **Bearer token audience is the bare client GUID, not `api://` prefixed** — Azure AD v2 access tokens set `aud` to the bare GUID (e.g. `5fc832f6-...`), not `api://5fc832f6-...`. Configure `go-oidc` with `cfg.OIDCClientID` directly, not `"api://"+cfg.OIDCClientID`.
- **`orbital get datacenter <name|orbId|id>` uses bearer token** — the CLI subcommand resolves identifiers in order: `0x`-prefix → DGraph UID, contains `:` → orbId, otherwise tries orbId then name. POSTs to `/api/v1/graphql` with `Authorization: Bearer` header. The GraphQL endpoint is registered on both `e.Any("/graphql")` (session auth, for browser) and `api.Any("/graphql")` (bearer auth, for CLI/API clients).
- **HTMX does not re-execute `<script type="module">` in swapped content** — use the window bridge pattern: load the library once in `head.gohtml` as a module and assign to `window.MyLib`. Components then access `window.MyLib` from plain scripts. Applied to JSONEditor: `head.gohtml` sets `window.JSONEditor = JSONEditor`; edit modals use `window.JSONEditor` directly.
- **JSONEditor must be initialized in a visible (non-zero-size) container** — initializing while the modal is hidden produces a blank editor. Always initialize lazily on the first Edit button click (after `modal.classList.add('is-active')`), not on HTMX swap.
- **GraphQL always returns HTTP 200, even for errors** — check `resp.ok` for transport-level failure, then separately check `result.errors` in the response body for GraphQL-layer errors. Both checks are required. Dgraph returns errors in `{ "errors": [...] }` with HTTP 200.
- **DataTables Bulma select wrapper — `dtWrapLengthSelect()`** — DataTables renders a bare `<select>` for page length. Bulma needs a `<div class="select is-small">` wrapper for the custom arrow. Wrap it after init via `initComplete: function() { dtWrapLengthSelect(this.api()) }`. The CSS override in `styles.css` sets font-size on the inner `<select>` to match Bulma's is-small sizing.
- **Server summary field ordering convention** — Data Center first (reflects DC→server hierarchy), then all remaining fields in alphabetical order.
- **Redfish model naming convention** — `PowerEdge R650`, `PowerEdge XE9680` — no "Dell" prefix in the model field. Manufacturer (`Dell`) is stored as a separate field. Seed data follows this convention.
- **Config domain is an organizational/navigation concept, not a schema concept** — "Config domain" means a menu section grouping related config types (Data Centers, Servers, Clusters, etc.). There is one DGraph instance, one schema, no domain partitioning at the data layer.
- **`ShowDCBack` / `dcCtx=1` pattern for context-aware server reload** — when a server tab is opened by drilling from a DC tab, the URL includes `?dcCtx=1`. The handler sets `ShowDCBack: true`, which renders a back button and sets `data-reload-url`/`data-reload-target` on the edit modal so post-save reload targets the DC tab content instead of a standalone server tab.
- **`localStorage.serverTabs` is separate from `localStorage.tabs`** — DC tabs persist under `localStorage.tabs`; Servers page tabs persist under `localStorage.serverTabs`. Both follow the same `TabItem` class pattern.
- **Logout clears all localStorage and sessionStorage** — the logout form submit handler calls `localStorage.clear()` and `sessionStorage.clear()` before POSTing. Next login starts with no tab state.
- **`writeAuditEvent` is a package-level helper in `internal/handler/event.go`** — shared by `GraphQL.writeEvent`, `Export.Trigger`, and `BackupHandler.Trigger`. Accepts `*ent.Client`, `*slog.Logger`, actor, opName, operations, resourceTypes, resourceIDs, and a details map. Failures are logged and swallowed — audit writes must never block or fail a request.
- **REST-triggered audit events have no child row in the audit log UI** — `renderPayload` in `app.js` returns `null` when `details.query` is absent. The expand arrow is also hidden via `createdRow`. Export and backup events carry `{jobId, datacenterId, datacenterName}` or `{jobId}` in details — no GraphQL query/variables.
- **Single-tab pages use title+subtitle heading, not tab nav** — pages with only one tab (audit log, schema, divergence reports, signed artifacts) use `<p class="is-size-4">` + `<p class="has-text-grey">` instead of `<nav class="tabs is-boxed">`. Keep the `<div class="tab-content">` wrapper if the page contains `.box` elements — `.app-main .tab-content .box` applies a custom shadow in `main.scss`.
- **`make run-orbital` uses version `dev`** — avoids noisy git-describe strings (`v0.0.3-1-gcd4b553-dirty`) in local dev. `make build-orbital` and `make push` still use full git-describe version via `$(VERSION)`.
- **`cfg.SchemaPath` is the authoritative schema file path** — default `schema/schema-demo.graphql`. All handlers (export, backup, schema UI) read from this env-configurable path. Never hardcode `schema/schema-v1.graphql`.
- **Edge delivery page is now Signed Artifacts** — route `/signed-artifacts`, template `signed-artifacts.gohtml`, template key `"signed-artifacts"`. No auto-poll on that page — manual reload button only.
- **Audit events are one-per-HTTP-request, not one-per-entity** — a compound GraphQL mutation touching multiple entities produces a single event row. `operations` (JSON array) lists all DGraph operation names found in the query body (e.g. `["updateDataCenter","updateServer"]`). `resource_types` and `resource_ids` are JSON arrays of all types and orbIds touched. The full raw payload (`{operationName, query, variables}`) is stored in `details`.
- **Audit `resource_ids` extraction uses three sources** — `extractResourceIDs` merges: (1) single `variables["orbId"]`; (2) bulk `input` array walk; (3) recursive walk of the DGraph response JSON for every `"orbId"` value (covers nested creates, any entity the client selected orbId for). Only remaining gap: mutations filtered by a non-orbId field where the client also selects no orbId in the response (e.g. `filter: { hostname: {...} }` + `{ numUids }`) — these record an empty resource_ids list.
- **Audit event detection uses regex on query body** — `knownMutationRe` matches `(add|update|delete)(DataCenter|Server|...)` in the query string. Adding a new mutable type requires updating the regex. This is acknowledged tech debt — `vektah/gqlparser` AST parsing is the right long-term fix but not worth adding yet.
- **`ifVersion` MVCC is decoupled from event recording** — MVCC check is opt-in (presence of `ifVersion` variable). Events are always recorded for mutations touching known types regardless of whether `ifVersion` is present.
- **ent schema fields should not use `Immutable()`** — immutability enforced at the application layer (never call `.Update()` on event records). `Immutable()` on ent fields causes migration pain: changing a field requires drop/recreate rather than ALTER. Use plain fields; immutability is a code convention, not a schema constraint.
- **`make seed` applies schema to both DGraph instances** — blue (`:8080`) and scratch (`:8081`) both receive the schema on every `make seed` run via the `apply_schema` function in `scripts/seed.sh`.
- **`updatedBy` and `updatedAt` are filtered from audit event variable display** — these are metadata fields set by the system, not user-supplied input. They are excluded from the Variables section in the audit log child row (`skipVars` set in `app.js`). They remain in `details.variables` in the database.
- **DC-to-server back button uses `is-warning` not `is-link`** — matches the Grafana button style. Do not change it back to `is-link`.
- **Use plain `fetch()` for programmatic tab reloads, never `htmx.ajax()`** — `htmx.ajax()` carries hidden request context (triggering element, OOB swap hints, lifecycle state) that was designed for declarative attribute-driven flows. When called imperatively from an async JS handler, it can route responses to the wrong target. Pattern: `fetch(url, { headers: { 'HX-Request': 'true' } }).then(r => r.text()).then(html => { el.innerHTML = html; htmx.process(el); initXxx(...) })`. Always send `HX-Request: true` so Go handlers return fragments, not full pages.
- **Startup log must use slog, not `log.Printf`** — `cmd/orbital/main.go` calls `slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))` before anything else so the startup line emits JSON consistent with all other log output. Never use `log.Printf` / `log.Fatalf` for the startup message.
- **Restore uses a dedicated `dgraph-live` idle pod, not exec into DGraph Alpha** — a permanent pod (`deploy/dev/dgraph-live.yaml`) runs `sleep infinity` with the `dgraph/dgraph` image and mounts `orbital-restore-pvc`. Orbital execs into it (by label selector `app.kubernetes.io/name=dgraph-live`) to run `dgraph live`. The Alpha pod is not exec'd into — it is only hit via HTTP (`/alter` for `drop_all`, `/admin/schema` to re-apply schema). The idle pod stays resident so exec is instant; no Kubernetes Job startup delay.
- **Restore job lifecycle** — `pending → running → completed → failed`. Jobs are permanent (never deleted). Restore is blocked if any backup or export job is pending/running; backup and export are blocked if any restore job is pending/running — all three job types check each other before starting (409 on conflict).
- **Restore scope is DGraph only (MVP)** — Graph restore restores the DGraph configuration graph only. PostgreSQL operational data (audit logs, events, users, job history) is not restored to the same point in time. Restore is scoped to disaster recovery of the config graph; full point-in-time recovery of PostgreSQL audit history is out of scope for MVP.
- **PostgreSQL backup is out-of-band (managed service)** — PostgreSQL backup and restore is handled by the managed PostgreSQL service (e.g. Azure managed PostgreSQL), not by orbital. Orbital does not back up or restore PostgreSQL. Post-MVP, coordinating DGraph and PostgreSQL backups into a consistent point-in-time snapshot is a future effort.
- **`ORBITAL_EXPORT_DIR` must be set to a PVC-backed path in AKS** — default is `./subgraph-exports` (ephemeral container filesystem). Export zip artifacts stored there are lost on pod restart, causing all completed export jobs to go stale. In `deploy/dev/deploy.yaml`, set `ORBITAL_EXPORT_DIR=/scratch-exports/zips` to reuse the existing `orbital-scratch-exports` PVC. No new PVC needed.
- **Template `data-*` URL attributes must NOT include `{{.BasePath}}`** — JS code prepends `BASE` (= `window.ORBITAL_BASE` = `{{.BasePath}}`) to all URL constructions. If a template attribute already includes `{{.BasePath}}`, the result is double-prefixed (`/orbital/orbital/...`) and produces 404 on AKS. Rule: template data attributes contain only the path (`/servers/{{.ID}}`); JS always adds `BASE`. Exception: HTMX declarative attributes (`hx-get`, `hx-post`) are rendered server-side and must include `{{.BasePath}}` since HTMX does not go through the JS `BASE` variable.
- **DataTables `stateSave: true` on all main page tables** — persists page length, search, sort, and page position in localStorage across navigations. Applied to inventory, datacenter, server list, and audit log tables. Embedded per-tab tables (e.g. `dc-servers-table`) are excluded — they reinit on every tab load.
- **sessionStorage for API data cache, localStorage for UI state** — API response data (e.g. inventory rows) is cached in `sessionStorage` (clears on tab close, always fresh on new session). UI preferences and state (tab positions, filter selections, DataTables state) live in `localStorage` (persists across sessions). The distinction: data copies → sessionStorage; user preferences → localStorage.
- **Inventory table uses sessionStorage cache + `searchCols` pre-filter** — on page revisit, inventory rows are fed from `sessionStorage` at DataTables init time (`data: initialData`), eliminating the ajax flash. The saved type filter is passed as `searchCols` so the filtered state is the first and only draw — no second redraw. Reload button clears the cache, empties the table visually (`clear().draw()`), then refetches. `populateTypeDropdown()` is called after data is available (not in `initComplete`) to handle the first-visit case where `initComplete` fires before ajax data arrives.
- **Page titles use `<Page> | Orbital` pattern** — `head.gohtml` renders `{{.PageTitle}} | Orbital` for all pages with a distinct title. The inventory/home page (where `PageTitle` equals `"Orbital"`) renders as just `Orbital`. All handlers set `PageTitle` via the page data struct.
- **`k8sAvailable` flag gates in-cluster restore** — `rest.InClusterConfig()` is attempted at startup; `k8sAvailable = true` only if it succeeds. The Restore page always shows the manual kubectl runbook; the stored-backup restore section is hidden when `k8sAvailable` is false.
- **Helm chart `backups.full.enabled` gates PVC mount on scratch DGraph** — the DGraph Helm chart only mounts a `backups.volume` PVC when `backups.full.enabled || backups.incremental.enabled` is true. To keep `orbital-scratch-exports` mounted without running actual backup jobs, set `backups.full.enabled: true` with a never-firing cron schedule (`"0 0 31 2 *"`). Do not set it to `false` or the PVC silently disappears and exports fail with "no json.gz found".

## Example Data / Seeding

Example GraphQL mutation files live in `examples/`. Each file seeds one data center (namespace + DC + racks + servers) into DGraph. Run with `make seed` (requires orbital running with migrations applied).

**Seeding rules — learned from practice:**
- `addNamespace` takes a single object (not array): `addNamespace(input: { name: "..." }, upsert: true)`
- Cross-type references must use `orbId`, not `name`, since `orbId` is the `@id` field. Example: `dataCenter: { orbId: "ns:dc-name" }`, `rack: { orbId: "ns:rack-name" }`. Using `{ name: "..." }` fails with "field orbId cannot be empty" because DGraph treats it as a new object with no orbId.
- `orbId` format convention: `"<namespace>:<entity-name>"` — e.g. `"alaska-dot:alaska-dot-galleon"`, `"alaska-dot:Rack-1"`, `"alaska-dot:GRTLY24"`
- All ConfigItem nodes require `orbId`, `name`, `namespace`, and `createdBy`/`createdAt`
- Run `addNamespace` → `addDataCenter` → `addRack` → `addServer` in that order within a single mutation batch
- DGraph upsert never deletes stale nodes — if a node is removed from seed data (e.g. rack renamed), add an explicit `deleteRack`/`deleteServer` mutation to `seed.sh` before seeding
- `hostname` and `rackPosition` on `Server` are **design intent** fields set by the admin (not populated by orb scan). Hostname convention: `r{rack:02d}-u{position:02d}.{datacenter}` — e.g. `r01-u17.alaska-dot-cruiser`
- **`make seed-aks-clean` for clean-slate AKS seeding** — runs `drop_all` on DGraph blue before seeding. Use when AKS has stale/accumulated nodes from previous seeds. `make seed-aks` does NOT drop first. `seed-dgraph.sh --clean` accepts the flag directly.
- **Full seed produces 1,351 config items** — 9 DC + 24 Rack + 188 Server + 155 IdracSettings + 106 StorageController + 313 StorageVolume + 368 StorageDevice + 188 IPAddress. This is the expected count after a clean seed with all current files in `examples/seed/`.

## E2E Tests (Playwright)

Tests live in `e2e/`. Run with `make test-e2e` (requires orbital running on `:8001`).

**Auth setup:** `e2e/global-setup.ts` logs in as `admin@armada.ai` / `admin` once via the browser UI, saves the session cookie to `e2e/.auth.json`. All tests reuse this state via `storageState` in `playwright.config.ts`. The `.auth.json` file is gitignored and regenerated automatically on each test run.

**Test conventions:**
- Use `data-testid` attributes on elements that need stable selectors — not CSS utility classes or layout-driven selectors, which break when styling changes
- Assert against values read from the page rather than hardcoded seed data (e.g. read server count from the summary table, then assert row count matches) — hardcoded counts break when seed data changes

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
