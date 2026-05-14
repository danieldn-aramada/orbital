# Roadmap

## Development Timeline

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'doneTaskBkgColor': '#22c55e', 'doneTaskBorderColor': '#16a34a', 'activeTaskBkgColor': '#3b82f6', 'activeTaskBorderColor': '#2563eb', 'taskBkgColor': '#e5e7eb', 'taskBorderColor': '#d1d5db', 'taskTextColor': '#6b7280', 'taskTextDarkColor': '#fff'}}}%%
gantt
    dateFormat YYYY-MM-DD
    axisFormat %b %Y

    Section Completed
    Req Gather & Solution Eval (DCIM, PLM, ITSM)    :done, 2026-01-01, 2026-03-04
    Req Gather (Digital Twin in Atlas)              :done, 2026-03-04, 2026-04-10
    Research & Technology Selection                 :done, 2026-04-10, 2026-04-14
    Architecture Design                             :done, 2026-04-14, 2026-05-08

    Section Current
    Prototyping                   :active, 2026-04-14, 2026-05-27
    
    Section Upcoming
    MVP                           :2026-05-27, 2026-06-27
    General Availability          :2026-06-27, 2026-07-27
```

**Note:** All future dates are subject to change.


## Current Phase: Prototyping

Architecture Design is complete (closed 2026-05-06). Prototyping continues through 2026-05-27; spike scope is now stable.

Goal of prototyping is learning, not shipping. Each spike below is a question to answer, not a feature to build. Results from these spikes define the MVP.

| # | Spike | Key Question | Owner | Status | Depends On |
|---|---|---|---|---|---|
| 1 | AKS Deployment Validation | Can we deploy orbital and DGraph on AKS and reach a working baseline? | Daniel | ✅ Done (4/20) | — |
| 2 | Orb CLI structure | What is the right command structure for the orb binary — flags or subcommands? | Daniel | ✅ Done (4/22) | — |
| 3 | PostgreSQL / ent data model | What is the right schema for orbital's operational data in PostgreSQL? | Daniel | ✅ Done (5/5) | — |
| 4 | Web UI scaffold | Can we build the orbital management UI with HTMX and Go templates? What pages does it need? | Daniel | ✅ Done (5/6) | — |
| 5 | Authentication | How do we implement JWT bearer auth in orbital for Atlas UI consumers? | Daniel | ✅ Done (5/8) | — |
| 6 | DGraph backup to S3-compatible storage | What is the right DGraph backup strategy, including deduplication and retention? | Daniel | ✅ Done (5/9) | — |
| 7 | Air-gap sync round-trip | Does orbital's config export work reliably as a complete, importable payload for orb? | — | 🔄 In progress | — |
| 8 | Authorization | How do we restrict mutations to authorized roles using Azure AD App Roles + DGraph @auth, and how do we test authz offline? | — | 🔄 In progress | Spike 5 |
| 9 | DGraph performance and cost | Does DGraph hold up at scale, and what does it cost on AKS? | — | Not started | — |
| 14 | Production deployment | What does a repeatable, production-ready AKS deployment look like — image build pipeline, secrets management, in-cluster PostgreSQL, embedded assets, and port-forward vs ingress for dev? | Daniel | 🔄 In progress | — |
| 15 | AKS smoke test suite | How do we validate critical user flows after each AKS dev deployment? | — | Not started | 14 |
| 10 | DGraph operations | Can our team operate DGraph on AKS without prior experience? | — | Not started | — |
| 11 | Schema migration — build vs runbook | Do we need automation or is a runbook sufficient? | — | ❌ Out of scope (MVP) | Spike 10 |
| 12 | Orb import API | What is the right API contract for orb's local config import endpoint? | — | Not started | — |
| 13 | Divergence reports | How does orbital surface real divergence between intended and actual state, and how does an admin resolve it? | — | Not started | 17 |
| 17 | DGraph blue restore from backup | How do we restore DGraph blue from a known-good backup, and how does orbital coordinate the sequence safely? | — | Not started | 6 |
| 16 | Seed iDRAC settings and storage devices | What does real iDRAC and storage data look like, and does the schema cover all fields we need to seed it for representative data centers? | — | Not started | — |
| 18 | Observability | What does useful monitoring look like for orbital and DGraph in AKS — metrics, dashboards, and alerts for API latency, DGraph query performance, and job health? | — | Not started | 1 |

---

### Spike 1. AKS Deployment Validation ✅
**Question:** Can we deploy orbital and DGraph on AKS and reach a working baseline?

**Context:** First end-to-end deployment of the stack — validates that orbital, DGraph Alpha, DGraph Zero, and supporting networking can run together in our shared AKS dev environment.

**Completed:** April 20, 2026 — orbital and DGraph deployed in AKS dev. GraphQL endpoint reachable. NetworkPolicy applied to restrict DGraph access to orbital only.

### Spike 2. Orb CLI structure ✅
**Question:** What is the right command structure for the orb binary — flags or subcommands?

**Completed:** April 22, 2026

**What was built:**
- `cmd/orb/` entry point with Cobra root command
- Subcommand split: `orb start` (long-running edge service), `orb scan` (BMC/inventory discovery), `orb export` (graph export), `orb import` (config load)
- `internal/cli/` package with shared CLI scaffolding and output utilities (`internal/cli/out/`)
- Confirmed: single binary, subcommand-driven is the right model for the edge deployment context

### Spike 3. PostgreSQL / ent data model ✅
**Question:** What is the right schema for orbital's operational data in PostgreSQL?

**Completed:** May 5, 2026

**What was built:**
- `ent/` schema covering all orbital operational entities: `users`, `orbs`, `namespaces`, `backups`, `export_jobs`, `registry_artifacts`
- `ent generate` workflow; all CRUD methods code-generated
- Schema migrations managed by ent's `migrate` package against local PostgreSQL
- Confirmed: PostgreSQL via ent is the right approach for all managed-service operational data; DGraph holds only graph/config data

### Spike 4. Web UI scaffold ✅
**Question:** Can we build the orbital management UI with HTMX and Go templates? What pages does it need?

**Completed:** May 6, 2026

**What was built:**
- HTMX + Go templates (`web/templates/`) with Bulma CSS, server-side rendering
- Pages: datacenter management, backups, operations (export jobs + edge delivery), audit log, divergence reports, schema
- Shared layout components: navbar, sidebar menu, delete/edit modals, table partials
- All page JS in `web/static/app.js` — no inline scripts
- Playwright E2E test suite (`e2e/`) with global auth setup (`e2e/global-setup.ts`), `make test-e2e` target, `data-testid` conventions
- Example GraphQL seed files (`examples/seed/`) for 5 data centers (Alaska DOT Cruiser, Alaska DOT Galleon, Houston, Seattle, Colo) with real rack names, hostnames, and rack positions sourced from Netbox; all servers updated with `manufacturer: "Dell"` and Redfish-standard model names (`PowerEdge R650`, `PowerEdge XE9680`, etc.)
- Schema additions: `KubernetesCluster`, `EksaConfig`, `IPAddress` types; `id: ID` on `ConfigItem` interface; `oobIP: IPAddress` on `Server` (was string). IP address modeling settled as GraphQL-only hub pattern with typed back-refs; DQL `~predicate` for cross-type IP queries.
- Servers page: cross-DC server list DataTable (columns: Data Center, OOB IP, Hostname, Service Tag, Model, Rack) with tab persistence (`localStorage.serverTabs`)
- Server detail view: double-click server row in DC tab → replaces DC tab content with server detail; back button restores DC tab view (`ShowDCBack` / `dcCtx=1` pattern)
- Server detail tabs: iDRAC settings, Storage (controllers + devices), Config Profile (formatted JSON)
- Server edit modal: JSONEditor for all scalar fields (hostname, manufacturer, model, OOB MAC, rack position, service tag); follows same pattern as DC edit modal; post-save reload targets correct parent (DC tab or Servers page tab) via `data-reload-url`/`data-reload-target`
- HTMX + JS library pattern settled: window bridge for ES modules (`window.JSONEditor`); lazy init on first Edit click for components requiring a visible container
- DataTables UI: compact one-row toolbar (page length + buttons in `topStart`), Bulma-wrapped page-length select (`dtWrapLengthSelect()`), smaller pagination buttons via CSS

**Success criteria:**
- ✅ Server-rendered UI with HTMX for dynamic updates (no full SPA)
- ✅ Pages for all major operational areas
- ✅ E2E tests validate core flows against a live stack

### Spike 5. Authentication ✅
**Question:** How do we implement auth in orbital?

**Completed:** May 8, 2026

**What was built:**
- OIDC Authorization Code Flow via `go-oidc/v3` + `golang.org/x/oauth2`
- Azure AD as the IdP (tenant `8f231c2a-9551-4b40-be17-5b24afe5e890`)
- Session-based auth via `gorilla/sessions` cookie; CSRF token in same cookie
- User auto-provisioned on first OIDC login (no password hash = SSO-only account)
- Local email/password login retained for dev/seed user (`admin@armada.ai`)
- Name and email stored in session at login — no DB query per request
- `password_hash` is nullable in PostgreSQL; nil = OIDC-only user

### Spike 6. DGraph backup to S3-compatible storage ✅
**Question:** What is the right backup strategy for DGraph, and how do we handle deduplication and retention?

**Completed:** May 9, 2026

**What was built:**
- `POST /api/v1/backups` — triggers async backup job; returns job ID
- `GET /api/v1/backups` / `GET /api/v1/backups/:id` — list and status
- `GET /api/v1/backups/:id/download` — presigned URL (15 min TTL)
- `DELETE /api/v1/backups/:id` — removes record and S3 object
- `POST /api/v1/backups/test-connection` — validates storage credentials
- SHA-256 checksum dedup — skips upload if graph unchanged since last backup
- Retention enforcement — prunes oldest completed backups beyond `ORBITAL_S3_RETENTION_COUNT`
- Azure Blob Storage auto-detected by `.blob.core.windows.net`; uses Shared Key auth. All other endpoints use AWS SDK with path-style addressing.
- Backup zip named `orbital-<version>-<timestamp>.zip`

**Context:** Orbital is the authoritative intent store for the fleet — if DGraph data is lost, no configuration exports can be produced and no modular data centers can be onboarded. DGraph community edition only has the export mutation (`json.gz` + `schema.gz`), which produces full snapshots. PostgreSQL is handled by the managed service layer and is not orbital's concern.

**v1 approach — admin-initiated full snapshots with checksum dedup:**
An admin triggers a backup via the UI or API. Orbital runs DGraph's export mutation, computes a checksum, compares against the last successful backup record — if identical, skips the upload. Otherwise uploads `json.gz` + `schema.gz` to S3-compatible storage and records the result in PostgreSQL. Backup is async: the trigger returns a job ID; a status endpoint tracks progress.

**Storage:** Any S3-compatible backend (AWS S3, Cloudflare R2, MinIO, Azure Blob via S3 API). Configured at startup via environment variables — not user-selectable at runtime.

**S3 configuration (env vars):**
- `ORBITAL_S3_ENDPOINT` — custom endpoint for S3-compatible providers; empty = AWS S3
- `ORBITAL_S3_REGION`
- `ORBITAL_S3_BUCKET`
- `ORBITAL_S3_ACCESS_KEY`
- `ORBITAL_S3_SECRET_KEY`
- `ORBITAL_S3_PREFIX` — optional path prefix within the bucket (e.g. `orbital/backups/`)
- `ORBITAL_S3_RETENTION_COUNT` — max number of backups to retain; oldest deleted after upload (default: 30)

If S3 is not configured, the backup UI button is disabled with a visible explanation.

**PostgreSQL `backup_records` table:**
| Column | Type | Notes |
|---|---|---|
| `id` | serial PK | |
| `status` | varchar | `pending`, `running`, `completed`, `failed` |
| `initiated_by` | int FK → users | |
| `initiated_at` | timestamptz | |
| `completed_at` | timestamptz | nullable |
| `s3_path` | text | full S3 URI of the uploaded archive; nullable until complete |
| `schema_version` | text | orbital schema version at time of backup |
| `checksum` | text | SHA-256 of `json.gz`; used for dedup |
| `size_bytes` | bigint | nullable until complete |
| `error` | text | nullable; populated on failure |

**Post-v1 — incremental and dedup:**
DGraph community has no native incremental backup. Options to evaluate once real data volumes are known from Spike 9:
- DGraph enterprise binary incremental backups
- Frequent full snapshots with storage-side versioning and lifecycle rules
- Export diff against previous snapshot (complex — only if snapshot sizes become a real problem)

**Success criteria:**
- ✅ `POST /api/v1/backups` triggers an async backup job; returns job ID
- ✅ `GET /api/v1/backups` lists backup history from PostgreSQL
- ✅ `GET /api/v1/backups/:id` returns job status
- ✅ `GET /api/v1/backups/:id/download` returns a presigned S3 URL (valid for a short TTL) for admin download
- ✅ Checksum dedup: if graph unchanged since last backup, upload is skipped and job completes with status `skipped`
- ✅ Retention: after a successful upload, backups exceeding `ORBITAL_S3_RETENTION_COUNT` are deleted from S3
- ✅ End-to-end validated: trigger backup, confirm archive appears in storage, confirm record in PostgreSQL

### Spike 7. Air-gap sync round-trip 🔄
**Question:** Does orbital's config export work reliably as a complete, importable payload?

**Context:** Orbital must expose a data center-scoped export endpoint (`POST /api/v1/datacenters/{id}/export`) that returns a `json.gz` + `schema.gz` pair for that data center's subgraph. This is not a raw pass-through of DGraph's export mutation — orbital must partition the graph by data center. In deployments using `configbundle`, its Bundle Generator calls this endpoint to produce a ConfigBundle. This spike builds the endpoint and validates the export is reliable and loadable. OCI artifact publishing (pushing signed exports to a registry for edge consumers to pull) is also in scope here.

**What's been built (as of 5/9):**
- `POST /api/v1/datacenters/{id}/export` — async job; queries blue DGraph, loads subgraph into scratch DGraph, triggers native export, packages into `json.gz` + `schema.gz` zip
- Export jobs globally serialized (scratch DGraph is shared state)
- Per-job scratch export directories via DGraph `destination` parameter (`/dgraph/export/<jobID>/`)
- `GET /api/v1/export/jobs`, `GET /api/v1/export/jobs/:jobId`, `GET /api/v1/export/jobs/:jobId/download`
- `DELETE /api/v1/export/jobs/:jobId` — removes record, zip, and scratch dir
- Stale detection on list: marks jobs whose scratch file no longer exists
- OCI publish: `POST /api/v1/export/jobs/:jobId/publish` — pushes signed OCI artifact to configured registry (oras-go v2 + cosign, air-gap safe, `TlogUpload: false`)
- OCI artifacts: `GET /api/v1/oci/artifacts`, `GET /api/v1/oci/artifacts/:id`
- `GET /api/v1/oci/public-key` — distributes signing public key to edge consumers
- `POST /api/v1/oci/test-connection` — validates registry credentials
- Edge Delivery UI page — registry status, test connection, published artifacts table

**Remaining:**
- Orb receives and loads the `json.gz` into local DGraph and serves graph offline after import
- Validate export sizes are reasonable (USB/manual transfer reference point)

**Success criteria:**
- ✅ Implement `POST /api/v1/datacenters/{id}/export` — returns scoped `json.gz` + `schema.gz`
- ⬜ Orb receives and loads the `json.gz` into local DGraph (simulating what `configbundle`'s edge agent does)
- ⬜ Orb serves the graph correctly offline after import
- ✅ Validate export can be published as a signed OCI artifact for edge pull
- ⬜ Validate export sizes are reasonable (reference point: USB/manual transfer)

### Spike 8. Authorization 🔄
**Question:** How do we restrict mutations to authorized roles, and how do we test authz offline?

**Context:** Authentication (Spike 5) is complete — orbital knows who the user is. Authorization is the next layer: what they are allowed to do. Two mechanisms will work together: Azure AD App Roles for role assignment, and DGraph's native `@auth` directive for enforcing mutation access at the graph layer.

**Approach settled:**
- **Azure AD App Roles** (not group GUIDs) — define roles like `orbital-admin` and `orbital-viewer` in the Azure app manifest. App Roles appear in the JWT `roles` claim as strings, not GUIDs, and can be included in a custom namespace claim that DGraph's `@auth` can read.
- **DGraph `@auth` directive** — add `@auth(add/update/delete: { rule: ... })` to each type in `schema/v1.graphql`. Mutation rules check the `roles` claim; query requires only a valid JWT (`ClosedByDefault: true`). Field-level auth is not supported by DGraph and will not be attempted.
- **Go middleware** — route-group-level role checks in Echo for REST endpoints. GraphQL endpoint protected at HTTP layer (role check); DGraph `@auth` is defense-in-depth.
- **Offline JWT testing** — integration tests generate and sign JWTs locally using a test RSA key pair. DGraph's `# Dgraph.Authorization` in the test schema points to the test public key (not Azure JWKS). No network call required. This pattern allows authz integration tests to run fully offline and in CI.

**What's been built (as of 5/11):**
- Bearer token validation working end-to-end: `go-oidc/v3` validates Azure AD v2 JWT (signature, issuer, audience, expiry) via JWKS auto-discovery
- `/api/v1/graphql` registered on the bearer-protected `api` group — all API calls to GraphQL require a valid token
- Azure AD app manifest fix documented: `requestedAccessTokenVersion: 2` required for v2 tokens; v1 tokens (`iss: https://sts.windows.net/...`) rejected by go-oidc v2 discovery
- Audience fix: v2 access tokens carry bare client GUID in `aud`, not `api://<guid>` — orbital now uses `cfg.OIDCClientID` directly
- `orbital get datacenter <name|orbId|id>` and `orbital get datacenters` CLI commands — both use bearer token from `~/.orbital/credentials.json` to call `/api/v1/graphql`; validated against live Azure AD tokens
- macOS keychain rewritten to CGo + Security framework (`SecItemAdd`/`SecItemCopyMatching`/`SecItemDelete`) without Touch ID ACL (`errSecMissingEntitlement -34018` on unsigned binaries); uses `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`

**Remaining:**
- Azure AD App Roles defined in app manifest; assign `orbital-admin` and `orbital-viewer` to users/groups
- DGraph schema updated with `@auth` directives on all mutable types; `ClosedByDefault: true`
- Go middleware role enforcement on REST mutation endpoints
- Offline JWT integration tests (local test RSA key pair, no Azure AD call)

**Success criteria:**
- ✅ Bearer token validation end-to-end with real Azure AD tokens
- ✅ `/api/v1/graphql` protected by bearer middleware
- ⬜ Azure AD App Roles defined in app manifest; `orbital-admin` and `orbital-viewer` roles assignable
- ⬜ DGraph schema updated with `@auth` directives; `ClosedByDefault: true` active
- ⬜ Orbital Go middleware enforces role checks on REST mutation endpoints
- ⬜ Integration tests sign JWTs with a local test key — authz enforced without hitting Azure AD
- ⬜ A user with `orbital-viewer` role cannot perform mutations via GraphQL or REST
- ⬜ A user with `orbital-admin` role can perform all operations

### Spike 9. DGraph performance and cost
**Question:** Does DGraph hold up at realistic scale for graph traversal queries, and what does it cost to run on AKS?

**Context:** There are unsubstantiated reports of high CPU usage under unknown conditions. This spike reproduces and characterizes that before any optimization work begins.

**Success criteria:**
- Define a realistic query mix: expected patterns from the digital twin UI (deep traversals — DataCenter → Servers → StorageControllers → StorageDevices), read/write ratio, and target dataset size for v1
- Seed DGraph with a representative dataset and benchmark query latency under increasing concurrency
- Identify which specific queries are expensive and whether they correlate with the reported CPU spikes
- Determine if Valkey caching is sufficient mitigation or if DGraph is a hard bottleneck
- Map peak CPU/memory profile to an AKS node SKU and produce a cost estimate for v1 workload

### Spike 10. DGraph operations
**Question:** Can our team operate DGraph reliably on AKS without prior experience?

**Context:** The team has strong Go/Java and PostgreSQL experience but no DGraph operational background. Schema migrations, backup/restore, and cluster behavior during restarts are all unknowns. This spike must be completed before building any automation around these processes.

**Success criteria:**
- ⬜ Perform a full backup and restore cycle on AKS — validate data integrity after restore
- ~~Apply a schema change to a live DGraph instance~~ — out of scope for MVP; schema changes require a redeployment (same model as PostgreSQL migrations via ent)
- ✅ DGraph pod recovery on AKS — pod deletion + StatefulSet recreation validated today (2026-05-12)
- ~~Evaluate blue/green deployment viability~~ — deferred, not a blocker for MVP
- ⬜ Produce a runbook covering restore and schema change

### Spike 11. Schema migration — build vs runbook
**Question:** Do we need a built-in schema migration tool in orbital, or is a well-maintained runbook sufficient?

**Context:** The architecture calls for orbital to own schema versioning and apply changes to DGraph automatically on startup. But this is non-trivial to build correctly. Spike 10 (DGraph operations) will reveal how painful schema changes are in practice — this spike uses those findings to decide whether automation is worth the investment or whether operational discipline (runbooks, manual apply, version tracking in PostgreSQL) is good enough for the foreseeable future.

**Do not start until Spike 10 is complete.**

**Success criteria:**
- Assess the real operational cost of manual schema migrations based on Spike 10 findings
- Determine if the frequency and risk of schema changes justifies building automation
- If yes — produce a design doc for the migration tool (not code)
- If no — produce a runbook that covers schema apply, rollback, and version tracking in PostgreSQL

### Spike 12. Orb import API
**Question:** What is the right API contract for orb's local config import endpoint?

**Context:** In deployments using `configbundle`, config reaches orb via the edge agent calling orb's local `/import` API with the `json.gz` payload — not by orb polling orbital directly. Orb has no direct connection to orbital; the delivery mechanism is the deployment layer's concern. This spike defines and validates that local API contract between the delivery layer and orb.

**Success criteria:**
- Define the `/import` API: endpoint, payload format, auth model (local loopback — what, if any, auth is appropriate)
- Validate that orb correctly loads the `json.gz` into local DGraph and serves it offline after import
- Confirm the import is idempotent and safe to re-run on the same or newer payload
- Confirm behaviour on a stale or older payload (should orb reject, warn, or accept?)
- Produce an API design doc covering the endpoint contract

### Spike 15. AKS smoke test suite
**Question:** How do we validate critical user flows work after each AKS dev deployment?

**Context:** Local Playwright tests (`e2e/`) run against `localhost:8001` with email/password auth. AKS deployments need a separate post-deploy smoke test that can run from any machine with `kubectl` access — no public URL required, no SSO complexity.

**Approach:**
- `e2e/smoke/` directory — separate from the main e2e suite, purpose-built for AKS validation
- Port-forward orbital to `localhost:8001` before running (same pattern as `make seed-aks`)
- Authenticate via local login (`admin@armada.ai / admin`) — same `global-setup.ts` pattern, avoids Azure AD redirect interception
- `make smoke-aks` target: port-forwards orbital, runs smoke suite, tears down

**Flows to cover:**
- Login (local)
- Export subgraph end-to-end (trigger → poll until completed → download)
- Edit a data center field and verify it saves
- Trigger a backup and verify it appears in the backup list
- OCI test connection

**Success criteria:**
- ⬜ `make smoke-aks` runs from any machine with `kubectl` access to the dev cluster
- ⬜ Tests use `data-testid` selectors — not CSS classes or layout-driven selectors
- ⬜ Suite completes in under 2 minutes
- ⬜ Clear pass/fail output — usable as a manual post-deploy gate

**Not in scope:**
- SSO / Azure AD login automation
- CI pipeline integration (can be added later once suite is stable)
- Full regression coverage — smoke only, critical paths only

### Spike 14. Production deployment 🔄
**Question:** What does a repeatable, production-ready AKS deployment look like?

**Context:** Spike 1 validated that the stack can run on AKS at all. This spike makes it repeatable and production-grade — image build pipeline, proper secrets management, self-contained binary, and a clear deploy sequence.

**What's been built (as of 2026-05-12):**
- `deploy/dev/deploy.yaml` — full Deployment + Service with all env vars wired from `orbital-secrets`
- `deploy/dev/secrets.yaml` (gitignored) — single K8s Secret covering all orbital secrets including cosign key
- `deploy/dev/postgres.yaml` — in-cluster PostgreSQL StatefulSet with 5Gi PVC (Azure managed PostgreSQL `pg_hba.conf` blocks unencrypted connections from pod CIDR without admin credentials)
- `deploy/dev/ingress.yaml` — nginx Ingress placeholder (skipped for now — using port-forward)
- `deploy/charts/values-dev-scratch.yaml` — DGraph scratch instance (no Ratel, smaller disk)
- Two DGraph helm releases: `dgraph-blue` (live) and `dgraph-scratch` (export only); scratch uses `--set serviceAccount.create=false` to avoid SA conflict
- `deploy/README.md` — step-by-step AKS dev deploy guide

**Remaining:**
- Switch Go templates from `template.ParseFiles` (runtime disk reads) to `//go:embed` (binary-embedded) — removes `COPY web/` from Dockerfile, makes binary self-contained
- Dockerfile currently copies `web/` and `schema/` at runtime; embed eliminates this
- CI/CD pipeline: GitHub Actions (or Azure DevOps) to build, tag, and push image on merge to main
- Decide on image tagging strategy (semver vs git SHA)
- Validate port-forward workflow for OIDC login end-to-end in AKS dev
- Seed DGraph schema on first deploy (currently requires `make seed` pointed at cluster)

**Success criteria:**
- ⬜ `//go:embed` replaces all `template.ParseFiles` calls — Dockerfile has no `web/` or `schema/` COPY
- ⬜ CI pipeline builds and pushes image on merge; version injected via ldflags
- ⬜ `kubectl apply -f deploy/dev/` brings up a working orbital in a clean namespace
- ⬜ OIDC login works end-to-end via port-forward
- ⬜ DGraph schema applied automatically on first boot (orbital startup applies schema via admin API)

### Spike 16. Seed iDRAC settings and storage devices
**Question:** What does real iDRAC and storage data look like for our modular data center servers, and does the current schema cover everything we need?

**Context:** The `IdracSettings`, `StorageController`, `StorageDevice`, and `StorageVolume` types exist in the schema and the server detail UI already renders iDRAC and Storage tabs — but no seed data exists for these types. Without representative data, the tabs are always empty in dev, which makes UI and export validation unreliable. We also want to confirm the `IdracSettings` fields (`firmwareVersion`, `sshEnabled`, `osToIdracPassThroughEnabled`, `usbManagementPortEnabled`) are sufficient, or if we need to add more (e.g. `ipAddress`, `networkProtocols`, `activeSessions`).

**Scope:**
- Gather real or realistic iDRAC settings for the seed servers (at minimum one per data center — alaska-dot-cruiser and alaska-dot-galleon have real hardware data available from BMC discovery)
- Identify any missing schema fields on `IdracSettings` and add them (safe schema change — new nullable fields only)
- Gather storage controller and device data (model, serial number, capacity, WWN) for the same servers
- Add `addIdracSettings`, `addStorageController`, and `addStorageDevice` mutations to the existing seed files
- Confirm the server detail Storage and iDRAC tabs render correctly after seeding

**Success criteria:**
- ⬜ At least one data center seed file includes iDRAC settings and storage devices for all seeded servers
- ⬜ `IdracSettings` schema covers the fields needed — any additions are documented as a versioned schema change
- ⬜ `make seed` and `make seed-aks` both apply the updated seed data cleanly (upsert safe)
- ⬜ Server detail iDRAC and Storage tabs render the seeded data correctly in dev

### Spike 17. DGraph blue restore from backup
**Question:** How do we restore DGraph blue from a known-good backup, and how does orbital coordinate the sequence safely?

**Context:** DGraph community edition has no native restore API — only an export mutation. Restore requires `drop_all` + `dgraph live` loader. Orbital cannot invoke `dgraph live` directly via HTTP. This spike builds the mechanism and validates it end-to-end. Restore is a prerequisite for any risky write to blue DGraph (import subgraph, bulk upsert) — the safety net is: backup blue → perform risky write → if bad, restore from that backup.

**Settled design decisions:**

**Two restore paths:**

**Path 1 — From a local file (manual, kubectl):** Admin has a backup zip on their machine and `kubectl` access. They copy files into the DGraph Alpha pod and run `dgraph live` directly. The DGraph Alpha pod already has the `dgraph` binary — no extra tooling needed. Documented as a runbook on the `/restore` page in the orbital UI.

**Path 2 — From a stored backup (automated, orbital):** Admin clicks Restore on a completed backup record in the Backups UI. Orbital uses `client-go` to exec into the DGraph Alpha pod — no sidecar, no Kubernetes Job, no extra image. The DGraph Alpha pod's existing `dgraph` binary handles the live import. Orbital:
1. Generates a presigned S3 URL (or Azure SAS URL) for the backup zip — same `blobStorage.presignURL()` used by the download endpoint, 15 min TTL, validated by the storage backend
2. Execs into the DGraph Alpha pod: `curl <presigned-url> -o /tmp/backup.zip && unzip /tmp/backup.zip -d /tmp/restore`
3. Execs: `curl -s -X POST localhost:8080/alter -d '{"drop_all": true}'`
4. Execs: `dgraph live --files /tmp/restore/data.json.gz --schema /tmp/restore/schema.gz --alpha localhost:9080`
5. Streams stdout/stderr, tracks status in PostgreSQL as a restore record

**client-go in-cluster auth:** `rest.InClusterConfig()` reads the service account token Kubernetes automatically mounts into every pod. No extra credentials needed. Orbital's ServiceAccount needs a `Role` + `RoleBinding` in the DGraph namespace granting `get`/`list` on `pods` and `create` on `pods/exec`. DGraph Alpha pod discovered at runtime by label selector (`app.kubernetes.io/component=alpha,app.kubernetes.io/instance=dgraph-blue`) — no hardcoded pod name.

**Local dev fallback:** `InClusterConfig()` fails outside Kubernetes. Orbital detects this at startup (`k8sAvailable` flag). The Restore page always shows the manual runbook; the stored-backup restore section shows a message explaining it requires in-cluster deployment.

**Partial failure:** Non-atomic — `drop_all` wipes blue before `dgraph live` loads data. If `dgraph live` fails, blue is left empty. No automatic rollback. Admin must trigger another restore. Warning displayed prominently on the Restore page.

**Blocking:** Restore is blocked if any backup job or export job is `pending` or `running`. Backup and export jobs are blocked if any restore job is `pending` or `running`. All three job types check each other before starting — reject immediately with a clear error.

**Exec timeout:** Configurable via `ORBITAL_RESTORE_TIMEOUT` env var, default 10 minutes. Chosen to give quick failure feedback on a small dataset while leaving headroom for larger graphs. If timeout is exceeded, the exec is cancelled and the restore job is marked `failed`.

**Downtime window:** `drop_all` is instantaneous and irreversible; blue is empty until live load completes. Acceptable for a break-glass operation — not zero-downtime.

**Orbital responsibility:** Coordinates the sequence. Before any risky write to blue (import subgraph, divergence apply), orbital: (1) triggers a backup, (2) waits for completion, (3) proceeds. If the write goes wrong, admin triggers restore from the Restore page.

**Success criteria:**
- ✅ Manual restore runbook documented on `/restore` page — local file path
- ⬜ `client-go` added as dependency; `rest.InClusterConfig()` attempted at startup; `k8sAvailable` flag set
- ⬜ Orbital ServiceAccount `Role` + `RoleBinding` in DGraph namespace: `get`/`list` pods, `create` pods/exec — added to `deploy/dev/`
- ⬜ `ORBITAL_RESTORE_TIMEOUT` env var in config (default 10m)
- ⬜ `restore_jobs` ent table: `id`, `status` (`pending`/`running`/`completed`/`failed`), `backup_id` (FK → backups), `initiated_by`, `started_at`, `completed_at`, `log` (stdout/stderr), `error`
- ⬜ `POST /api/v1/restore` — creates restore job; blocked if any backup/export/restore job is pending or running
- ⬜ Restore job runner: generates presigned URL → execs curl+unzip into DGraph Alpha pod (discovered by label selector) → execs drop_all → execs dgraph live; respects `ORBITAL_RESTORE_TIMEOUT`
- ⬜ `GET /api/v1/restore` — list all restore jobs (permanent, never deleted)
- ⬜ `GET /api/v1/restore/:id` — restore job status
- ⬜ Restore page (`/restore`) — restore jobs table + backup selector dropdown + trigger button (stored backup section hidden when `k8sAvailable` is false); manual kubectl runbook always shown
- ⬜ Backup and export job triggers reject with 409 if any restore job is pending or running
- ⬜ End-to-end validated on AKS: trigger backup → wipe blue manually → restore via UI → confirm data intact

### Spike 13. Divergence reports
**Question:** How does orbital surface real divergence between intended state (orbital blue DGraph) and actual state (orb's local graph), and how does an admin resolve it?

**Context:** Orbital is transport-agnostic — it does not know or care how a divergence payload arrives. For the prototype we showcase S3 as the concrete transport (same pattern as OCI for subgraph export). Orb writes a full graph snapshot (`json.gz` + `schema.gz`) to a known S3 location. An admin manually triggers orbital to fetch and process the snapshot.

**Settled design decisions:**

**Payload format:** Orb writes a full graph snapshot — the same `json.gz` + `schema.gz` format used by the subgraph export. No separate report format. Reuses the existing scratch DGraph mechanism.

**Fetch model:** On-demand only (no polling). Admin clicks "Fetch" in the UI. Orbital downloads the snapshot, loads it into scratch DGraph, diffs each node by `orbId` against blue DGraph, and stores the diff in PostgreSQL as a divergence report.

**Diff direction:** Blue DGraph = intended state, orb snapshot = actual state. Every diff entry records both values explicitly ("intended: X, actual: Y"). Three diff categories: field value changed, node exists in orb but not in orbital (discovered resource), node exists in orbital but not in orb (missing/decommissioned).

**Resolution:** A human admin must resolve every divergence report before any changes are written to orbital. The admin reviews field-level diffs and chooses: accept individual fields (orb's value adopted into orbital), accept all, or reject all. Accepted changes are written as GraphQL mutations against blue DGraph. Resolved reports are marked `resolved` in PostgreSQL — orbital never auto-applies changes from orb.

**Scratch DGraph contention:** Importing an orb snapshot uses scratch DGraph. An import is blocked if an export job is pending or running, and vice versa. Serialized via the same global lock mechanism as export jobs.

**Orb registration:** New PostgreSQL table `orb_registrations` — one row per orb deployment. Stores the orb's namespace, name, and S3 location where it writes snapshots (key prefix). Future: public key for snapshot signature verification (not in scope for this spike).

**S3 key convention:** `divergence/{namespace}/{orb-name}/{timestamp}.zip` — same bucket as backups, separate prefix. Orbital reads from the S3 location registered for the orb.

**Transport agnosticism preserved:** S3 is one concrete transport, not the only one. The fetch-and-diff logic is independent of how the snapshot arrived — a future intake API or file upload would feed the same pipeline.

**Success criteria:**
- ⬜ `orb_registrations` ent table: namespace, orb name, S3 snapshot prefix, created/updated timestamps
- ⬜ Orb registration UI — admin registers an orb's S3 location
- ⬜ `POST /api/v1/orbs/:id/fetch-snapshot` — downloads snapshot from S3, loads into scratch DGraph, diffs against blue, stores result in PostgreSQL; blocked if export in progress
- ⬜ Divergence report stored in PostgreSQL: status (`pending_review`, `resolved`), per-node diffs (field name, intended value, actual value, diff type)
- ⬜ Divergence reports UI — list reports, show field-level diffs, accept/reject controls
- ⬜ Accept writes GraphQL mutations against blue DGraph; report marked `resolved`
- ⬜ Scratch DGraph wiped after import (same as export)

### Spike 18. Observability
**Question:** What does useful monitoring look like for orbital and DGraph in AKS — what metrics matter, how do we expose them, and what do we alert on?

**Context:** We are seeing DGraph performance unknowns (Spike 9) and want ongoing visibility into API latency, DGraph query times, and job health (backup/export/restore) — not just a one-time benchmark. Orbital runs in AKS alongside DGraph; the cluster already has Prometheus available via Azure Monitor.

**Scope:**
- **Orbital API metrics** — request latency (p50/p95/p99), error rate, and request count per endpoint; expose via `/metrics` (Prometheus format) using Echo middleware
- **DGraph metrics** — DGraph alpha already exposes Prometheus metrics at `:8080/debug/prometheus_metrics`; scrape it via a ServiceMonitor or annotation
- **Job health metrics** — counters for backup/export/restore job completions and failures; expose from orbital's `/metrics` endpoint
- **Dashboards** — Grafana dashboards (or Azure Monitor workbooks) for: API latency by endpoint, DGraph query latency, job success/failure rate
- **Alerts** — API error rate > threshold, DGraph memory > 80%, restore job failure

**Key questions to answer:**
- Does Echo's default Prometheus middleware give enough granularity, or do we need custom histograms?
- Does DGraph's built-in `/debug/prometheus_metrics` cover query latency, or do we need application-level timing around our GraphQL proxy calls?
- What is the right scrape interval and retention for this workload?

**Success criteria:**
- ⬜ `GET /metrics` on orbital returns Prometheus-format metrics (request latency histogram, error counter, job counters)
- ⬜ DGraph alpha metrics scraped by Prometheus (via annotation or ServiceMonitor)
- ⬜ At least one Grafana dashboard covers: orbital API p95 latency, DGraph query latency, job health
- ⬜ At least one alert defined: high error rate or DGraph memory pressure
- ⬜ Validated end-to-end: trigger a backup and a restore; confirm job metrics appear in the dashboard

---

## MVP Definition

> Working draft — final scope will be confirmed once spikes complete.

### Orbital (cloud)
- GraphQL Topology API — proxy DGraph with auth, rate limiting, and caching
- Schema management — versioned schema apply with backwards compatibility validation on startup
- Export API — `POST /api/v1/datacenters/{id}/export` returning scoped `json.gz` + `schema.gz`
- Orb registry — register, authenticate, and revoke orbs
- Audit log — record all config mutations with actor and timestamp ✅
- Backup — DGraph and PostgreSQL backup to Azure Blob with tracked records

### Orb (edge)
- Local DGraph — hold a complete copy of its data center's intended state, fully offline
- Config import — load `json.gz` from export API or file (air-gap)
- Drift reporting — observe actual state, compare to intended state, report the gap to orbital
- Discovery — scan local BMC and inventory APIs; export discovered graph for orbital import

### Explicitly out of scope for v1
- Network infrastructure config items (owned externally)
- PLM and ITSM integrations — design TBD, vendor selection in progress
- Multi-DGraph instance per data center

---

## Technical Debt

| Item | Notes |
|---|---|
| Switch templates + schema to `//go:embed` | `web/templates/templates.go` and `config.go` (`ORBITAL_SCHEMA_PATH`) read from disk at runtime. Replace with `//go:embed` so the binary is self-contained. Tracked in Spike 14. |
| Switch DGraph DQL calls to `dgo` client | `internal/handler/export.go` uses raw HTTP calls to `/query`, `/mutate`, `/alter`. Replace with `dgraph-io/dgo` (gRPC-based official Go client) for idiomatic usage and proper transaction management. |
| Audit mutation detection: regex → `vektah/gqlparser` AST | `extractOperations` and `extractResourceIDs` in `internal/handler/graphql.go` use regex on the raw query string. Fragile for edge cases (string literals containing type names, non-standard filter shapes). Replace with `vektah/gqlparser` (the parser underlying gqlgen) for proper AST walking. New dependency — add when regex causes real problems. |

---

## External Integration Dependencies

These are integration touchpoints that orbital must support but does not own. Vendor selection and design are being driven by other teams. Orbital's API-first design should remain flexible enough to accommodate them — no orbital work is blocked on these, but MVP scope may be affected by their timelines.

| System | Role | Status |
|---|---|---|
| **Atlas UI** | Customer-facing digital twin — queries orbital via GraphQL to visualize modular data center topology | Integration approach defined. Atlas calls orbital; orbital proxies DGraph. |
| **Product Lifecycle Management (PLM)** | Source of bill of materials for data center hardware — orbital may query PLM to enrich or validate configuration items | Vendor evaluation in progress by another team. Integration design TBD. |
| **IT Service Management (ITSM)** | Links customer support tickets to configuration changes in the data center — ITSM may call orbital to correlate incidents with config state | Vendor evaluation in progress by another team. Integration design TBD. |
