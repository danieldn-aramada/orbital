# OCI, Export, Backup & Restore Reference

Read this before: export job work, OCI publish/signing, backup/restore, Swagger annotations.

## Settled Decisions

- **Orbital is the sole OCI producer** — no downstream system needs registry write credentials. Orbital calls bundlers, bundles all layers, signs once, pushes once. ConfigBundle is a bundler — it queries Orbital's GraphQL and returns layers; it never pushes directly to ACR.
- **Bundler URLs are per-request, not server-side config** — callers supply `{"bundlers": ["url"]}` in the publish request body. A future migration to named server-side bundlers is tracked in a code comment in `publisher.go`. Do not pre-emptively add server-side bundler config.
- **Bundling is all-or-nothing** — if any bundler fails (non-2xx, timeout, size exceeded), the publish job fails and nothing is pushed to ACR. No partial pushes. Clients can retry without bundlers for a raw-export-only artifact.
- **OCI annotation carries orbId, not DGraph UID** — `com.armada.orbital.datacenter-id` is set from `export_jobs.datacenter_orb_id`. DGraph UIDs (`0x...`) must never appear in OCI artifact annotations. Fix at the source in orbital's publisher — not with compensating post-import DGraph queries in orb.
- **Export trigger is `POST /api/v1/export` with `{"orbId":"..."}` in the request body** — not a path segment. `fetchDCInfo` queries DGraph by orbId (`getDataCenter(orbId: $orbId)`), never by DGraph UID. The UID is internal and unstable.
- **Export jobs show `createdBy` (email), not `startedAt`** — `createdBy` populated from `actorFromContext` at job creation. `startedAt` removed from API response and UI; remains on ent schema for internal tracking only.
- **`RestoreBackend` has one implementation: `SubprocessRestoreBackend`** — runs `dgraph live` as a subprocess via `exec.CommandContext`. No `ORBITAL_RESTORE_BACKEND` env var, no idle pod, no shared PVC, no RBAC. The dgraph binary version in the Dockerfile must match the DGraph Alpha version deployed — rebuild orbital if you upgrade Alpha.
- **Backup checksum dedup was dropped — rely on retention only** — DGraph's JSON export is not byte-deterministic (same data → different bytes across runs). SHA-256 differs on every run even with no data changes. Do not re-implement checksum-based dedup. `ORBITAL_BACKUP_RETENTION_COUNT` is the correct bound on storage growth.
- **Backup retention is count-based; `skipped` status reserved for future** — do not repurpose `skipped` for dedup. Each completed backup has a unique S3 key, so pruning the DB record + S3 object together never orphans another record.
- **`ORBITAL_BACKUP_SCHEDULE` is a cron expression** — standard 5-field spec (e.g. `"0 0 * * *"`). Empty = no schedule on startup (local dev default). Validated via `robfig/cron/v3`. `ORBITAL_BACKUP_RETENTION_DAYS` default is 14 (not 30).

## Export job lifecycle

`pending → running → completed → stale`

- **Stale detection** — on export job list page load, orbital checks scratch file existence for each completed job and marks stale if missing.
- **Delete** removes the PostgreSQL record, export zip, and the job's scratch directory.
- **Export and publish are separate actions** — publish never happens automatically on export. Publish button appears on completed jobs. Re-publishing is allowed and creates a new `registry_artifacts` row (full audit trail).
- **Globally serialized** — scratch DGraph is shared state; only one export job may be pending or running at a time. Returns 409 if another is in progress.

## OCI publishing

- **Libraries:** `oras.land/oras-go/v2` for pushing, `github.com/sigstore/cosign/v2` for signing. Do not use the cosign binary — SDK used directly in-process.
- **Credentials:** `ORBITAL_OCI_USERNAME`/`ORBITAL_OCI_PASSWORD` are env-only. Never store in PostgreSQL. Signing private key configured via `ORBITAL_OCI_SIGNING_KEY_PATH` (unencrypted file), env/file-only, never a form field. **Signing is mandatory** — publish fails if key not configured.
- **Air-gap safe:** `TlogUpload: false` — no Sigstore network calls. Signature stored as OCI referrer. Public key distributed via orb onboarding response (primary, air-gap) and `GET /api/v1/oci/public-key` (secondary).
- **Artifact format:** `artifactType: application/vnd.orbital.subgraph.v1`, two layers:
  - `data.json.gz` — `application/vnd.orbital.subgraph.data.v1+gzip`
  - `schema.gz` — `application/vnd.orbital.subgraph.schema.v1+gzip`
  - Manifest annotations use `com.armada.orbital.*` prefix.
- **Per-layer producer attribution:** every layer carries the annotation `com.armada.orbital.producer` set at push time. Orbital's own graph layers use `orbital`; bundler-returned layers use the friendly name from `ORBITAL_BUNDLER_URLS` (e.g. `configbundle-bundler=http://...`). Orb reads this on import and displays it. Layers without the annotation render as `(unannotated)` (legacy artifacts). Do NOT invent producer names downstream — the OCI annotation is the source of truth.
- **Tag strategy:** monotonic `v{n}` tags per data center repo, derived from count of existing `registry_artifacts` rows. `:latest` updated on every successful publish.
- `registry_artifact.datacenter_name` stores DC name at publish time — denormalized for display, avoids DGraph lookup on every artifact list. Default `""` allows migration on existing rows.

## Backup

- **Trigger:** `POST /api/v1/backups` → async job.
- **Flow:** DGraph native export mutation on blue → `json.gz` written to host-side volume mount (`DGRAPH_EXPORT_DIR`, default `./dgraph-exports`) → package `data.json.gz` + `schema.gz` into zip → upload to S3 → clean export dir → enforce retention count.
- **Azure Blob Storage** auto-detected by `.blob.core.windows.net` in endpoint; uses Shared Key auth (not AWS Signature V4). All other S3-compatible endpoints use AWS SDK with path-style addressing.
- **Backup zip naming:** `orbital-<version>-<timestamp>.zip` (e.g. `orbital-v0.1.0-20260509T135041Z.zip`). Version from `internal/version.Version` via ldflags.
- **Retention:** `ORBITAL_S3_RETENTION_COUNT` prunes oldest completed backups.

## Restore

- `pending → running → completed → failed`. Jobs are permanent — never deleted.
- **Blocked if:** any backup or export job is pending/running (409). Backup and export are also blocked if any restore job is pending/running — all three job types check each other.
- **Restore scope is DGraph only (MVP)** — PostgreSQL operational data (audit logs, events, job history) is not restored. PostgreSQL backup is out-of-band via the managed PostgreSQL service (Azure).
- **Mechanism: `SubprocessRestoreBackend`** — runs `dgraph live` as a subprocess via `exec.CommandContext`. The `dgraph` binary is copied from `dgraph/dgraph:v25.3.1` into the orbital Docker image via multi-stage build. No idle pod, no shared PVC, no RBAC. `dgraph live` connects to Alpha/Zero via gRPC using `ORBITAL_DGRAPH_ALPHA_GRPC` / `ORBITAL_DGRAPH_ZERO_GRPC` (defaults: `localhost:9080` / `localhost:5080`).
- `ORBITAL_RESTORE_TIMEOUT` env var (default 10m).

## Swagger

- Regenerated via `make docs` (runs `swag init` for both orbital and orb). Run after changing any `@Router`, `@Tags`, or `@Summary` annotations.
- Swagger tag names: `backup graph`, `export subgraph`, `oci`.
