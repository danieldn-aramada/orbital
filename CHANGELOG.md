# Changelog

Completed spike detail. Each entry records what was built, the API contracts, key decisions, and what was validated. Ordered chronologically.

---

## Spike 1 · AKS Deployment Validation
**Completed:** April 20, 2026

First end-to-end deployment of orbital and DGraph on AKS. Validated that the architecture works in a real cloud environment, network isolation is correct, and the graph database recovers from pod restarts.

- ✅ Orbital and DGraph deployed in AKS dev namespace
- ✅ GraphQL endpoint reachable; NetworkPolicy restricts DGraph access to orbital only
- ✅ DGraph pod recovery validated — StatefulSet recreation after pod deletion works correctly

---

## Spike 2 · Orb CLI Structure
**Completed:** April 22, 2026

Established the orb binary structure. Confirmed that a single subcommand-driven binary is the right model for air-gap edge deployment — one artifact, clear separation between the long-running service and admin operations.

- ✅ `cmd/orb/` Cobra root command with subcommand split: `orb start`, `orb scan`, `orb export`, `orb import`
- ✅ `internal/cli/` shared scaffolding and output utilities (`internal/cli/out/`)
- ✅ Confirmed: single binary, subcommand-driven is the right model for edge deployment

---

## Spike 3 · PostgreSQL / ent Data Model
**Completed:** May 5, 2026 (tables added through May 14)

Established all operational data storage for orbital. PostgreSQL holds everything that is not a configuration item — job state, user accounts, audit history, artifact records. DGraph holds the graph.

- ✅ `users` — local accounts + OIDC-provisioned, nullable `password_hash` for SSO-only
- ✅ `orbs` — orb registry with Ed25519 public key and namespace association
- ✅ `namespaces` — tenancy boundary records
- ✅ `backups` — backup job records (status, checksum, S3 path, size, initiated by)
- ✅ `export_jobs` — export job records (status, datacenter, scratch dir path)
- ✅ `registry_artifacts` — OCI publish records (tag, digest, datacenter name, job FK)
- ✅ `events` — audit log (actor, operations, resource types/IDs, raw GraphQL payload, before-state diff)
- ✅ `restore_jobs` — restore job records (status, backup FK, stdout/stderr log, initiated by)
- ✅ `schema_versions` — applied DGraph schema version tracking
- ✅ `ent generate` workflow; all CRUD methods code-generated
- ✅ Schema migrations via ent `migrate` package against local PostgreSQL

---

## Spike 4 · Web UI
**Completed:** May 6, 2026 (additions through May 14)

Full management UI covering every orbital feature. Built with HTMX + Go templates + Bulma CSS — server-rendered, no SPA framework, no build step for the HTML layer.

- ✅ HTMX + Go templates with Bulma CSS, server-side rendering — no SPA
- ✅ Pages: Data Centers, Servers, Backups, Export Jobs, Signed Artifacts, Audit Log, Divergence Reports, Schema, Restore
- ✅ Shared components: navbar, sidebar, delete/edit modals, table partials
- ✅ All JS in `web/static/app.js`; all styles in `web/sass/main.scss`
- ✅ Data Centers: tab-per-DC view, drill-down to servers, inline edit modal with JSONEditor, before/after diff in audit tab
- ✅ Servers page: cross-DC DataTable (Data Center, OOB IP, Hostname, Service Tag, Model, Rack), tab persistence
- ✅ Server detail: iDRAC settings tab, Storage tab (controllers + devices), Config Profile tab, edit modal
- ✅ Export Jobs: trigger, poll, download, publish to OCI, stale detection, per-DC repo display
- ✅ Backups: trigger, list, download, restore trigger
- ✅ Restore: job table, backup selector, trigger button, manual kubectl runbook
- ✅ Audit log: full mutation history, before/after field diff (LCS), per-entity audit tabs on DC and server views
- ✅ Signed Artifacts: published OCI artifacts table
- ✅ DGraph schema: `KubernetesCluster`, `EksaConfig`, `IPAddress` types; IP hub pattern with typed back-refs
- ✅ 8 real-data seed files (Alaska DOT Cruiser, Alaska DOT Galleon, Houston, Seattle, Colo, Grayling, Livermore, 2F UAE) with Netbox hostnames and rack positions
- ✅ Playwright E2E test suite with global auth setup, `data-testid` conventions, `make test-e2e`
- ✅ Favicon: FA6 satellite-dish SVG with white rounded-rect background

---

## Spike 5 · Authentication
**Completed:** May 8, 2026

Full auth stack: OIDC SSO via Azure AD for browser sessions, PKCE + macOS keychain for the CLI, and bearer token validation for the API. Local email/password login retained for dev.

- ✅ OIDC Authorization Code Flow via `go-oidc/v3` + `golang.org/x/oauth2`; Azure AD as IdP
- ✅ Session-based auth via `gorilla/sessions` cookie; CSRF token in same cookie
- ✅ User auto-provisioned on first OIDC login (no password hash = SSO-only)
- ✅ Local email/password login for dev (`admin@armada.ai`)
- ✅ `orbital-cli`: Auth Code + PKCE login, macOS keychain (CGo + Security framework) stores refresh token, access token written to `~/.orbital/credentials.json`
- ✅ `orbauth` shared package — PKCE, token exchange, refresh, FileStore, KeychainStore — used by both `orb` and `orbital-cli`
- ✅ Bearer token validation end-to-end with real Azure AD v2 tokens (`go-oidc/v3` JWKS discovery)
- ✅ `/api/v1/graphql` registered on bearer-protected route group

---

## Spike 6 · DGraph Backup to S3
**Completed:** May 9, 2026

Async backup pipeline with SHA-256 deduplication and configurable retention. Supports both Azure Blob Storage and S3-compatible endpoints from the same code path. Validated end-to-end on AKS.

- ✅ `POST /api/v1/backups` — async backup job, returns job ID
- ✅ `GET /api/v1/backups`, `GET /api/v1/backups/:id` — list and status
- ✅ `GET /api/v1/backups/:id/download` — presigned URL (15 min TTL)
- ✅ `DELETE /api/v1/backups/:id` — removes record and S3 object
- ✅ `POST /api/v1/backups/test-connection` — validates storage credentials
- ✅ SHA-256 checksum dedup — skips upload if graph unchanged since last backup
- ✅ Retention enforcement — prunes oldest beyond `ORBITAL_S3_RETENTION_COUNT`
- ✅ Azure Blob Storage auto-detected by `.blob.core.windows.net`; Shared Key auth. All other endpoints use AWS SDK with path-style addressing.
- ✅ Backup zip named `orbital-<version>-<timestamp>.zip`
- ✅ Backups UI: status, size, download, trigger; blocked during restore (409)
- ✅ End-to-end validated on AKS: trigger → confirm in Azure Blob → restore → confirm data intact

---

## Spike 7 · DGraph Restore from Backup
**Completed:** May 14, 2026

Full restore pipeline via an idle `dgraph-live` pod that orbital exec's into. Handles the tricky coordination of stopping traffic, dropping data, loading from backup, and resuming — with full audit trail and conflict detection.

- ✅ `POST /api/v1/restore` — creates restore job; blocked if backup/export/restore in progress
- ✅ `GET /api/v1/restore`, `GET /api/v1/restore/:id` — list and status (jobs permanent, never deleted)
- ✅ Restore runner: downloads backup from S3 to shared PVC → `drop_all` against DGraph Alpha → `dgraph live` via exec into `dgraph-live` idle pod
- ✅ `dgraph-live` idle pod (`deploy/dev/dgraph-live.yaml`) — runs `sleep infinity`, mounts restore PVC, exec-ready instantly
- ✅ `client-go` in-cluster auth; `k8sAvailable` flag; Restore page hides stored-backup section when not in-cluster
- ✅ ServiceAccount `Role` + `RoleBinding` in DGraph namespace (`deploy/dev/rbac.yaml`)
- ✅ `ORBITAL_RESTORE_TIMEOUT` env var (default 10m)
- ✅ Restore UI: job table, backup selector, trigger, manual kubectl runbook always visible
- ✅ Backup and export triggers reject with 409 if restore job is pending/running
- ✅ End-to-end validated on AKS

---

## Spike 8 · AKS Dev Environment
**Completed:** May 18, 2026

Established a fully operational AKS dev environment to prototype against. All orbital services run in a single namespace with persistent storage, proper RBAC, and a step-by-step deploy guide so the environment can be reproduced from scratch.

- ✅ `deploy/dev/deploy.yaml` — Deployment + Service, env vars from `orbital-secrets`
- ✅ `deploy/dev/postgres.yaml` — in-cluster PostgreSQL StatefulSet with 5Gi PVC
- ✅ `deploy/dev/dgraph-live.yaml`, `deploy/dev/orbital-restore-pvc.yaml` — restore infrastructure
- ✅ `deploy/dev/rbac.yaml` — orbital ServiceAccount RBAC for pod exec
- ✅ `deploy/charts/values-dev-scratch.yaml` — DGraph scratch Helm values
- ✅ Two DGraph Helm releases: `dgraph-blue` (live) and `dgraph-scratch` (export only)
- ✅ `deploy/README.md` — step-by-step AKS dev deploy guide
- ✅ `scripts/seed-aks.sh` — port-forwards DGraph blue + scratch + zero, runs seed-dgraph.sh
- ✅ `scripts/seed-aks-postgres.sh` — port-forwards orbital-postgres, creates admin user
- ✅ `ORBITAL_EXPORT_DIR` set to PVC-backed `/scratch-exports/zips` — fixes export zips lost on pod restart
- ✅ AKS dev end-to-end validated: seed, export, backup, restore

---

## Spike 9 · Seed iDRAC and Storage Devices
**Completed:** May 15, 2026

Expanded the iDRAC schema with four new fields discovered during real hardware onboarding, and seeded all nine data centers with accurate hardware data. Navy Cruiser added as a new data center.

- ✅ 4 new `IdracSettings` fields added to schema: `ipmiEnabled`, `lockdownModeEnabled`, `dhcpEnabled`, `racadmEnabled`
- ✅ iDRAC seed files added for all data centers (Alaska DOT Cruiser, Galleon, Seattle, Houston, Grayling, Livermore, 2F UAE, Colo, Navy Cruiser)
- ✅ Server detail iDRAC tab renders all 8 fields correctly
- ✅ Navy Cruiser data center seeded (Rack-3, 16 servers, 16 OOB IPs)
