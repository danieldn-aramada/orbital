# OCI, Export, Backup & Restore Reference

Read this before: export job work, OCI publish/signing, backup/restore, Swagger annotations.

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
- **Tag strategy:** monotonic `v{n}` tags per data center repo, derived from count of existing `registry_artifacts` rows. `:latest` updated on every successful publish.
- `registry_artifact.datacenter_name` stores DC name at publish time — denormalized for display, avoids DGraph lookup on every artifact list. Default `""` allows migration on existing rows.

## Backup

- **Trigger:** `POST /api/v1/backups` → async job.
- **Flow:** DGraph native export mutation on blue → `json.gz` written to host-side volume mount (`DGRAPH_EXPORT_DIR`, default `./dgraph-exports`) → SHA-256 checksum → skip upload if unchanged since last backup (dedup) → package `data.json.gz` + `schema.gz` into zip → upload to S3 → clean export dir → enforce retention count.
- **Azure Blob Storage** auto-detected by `.blob.core.windows.net` in endpoint; uses Shared Key auth (not AWS Signature V4). All other S3-compatible endpoints use AWS SDK with path-style addressing.
- **Backup zip naming:** `orbital-<version>-<timestamp>.zip` (e.g. `orbital-v0.1.0-20260509T135041Z.zip`). Version from `internal/version.Version` via ldflags.
- **Retention:** `ORBITAL_S3_RETENTION_COUNT` prunes oldest completed backups.

## Restore

- `pending → running → completed → failed`. Jobs are permanent — never deleted.
- **Blocked if:** any backup or export job is pending/running (409). Backup and export are also blocked if any restore job is pending/running — all three job types check each other.
- **Restore scope is DGraph only (MVP)** — PostgreSQL operational data (audit logs, events, job history) is not restored. PostgreSQL backup is out-of-band via the managed PostgreSQL service (Azure). Post-MVP: coordinate DGraph and PostgreSQL backups into a consistent point-in-time snapshot.
- **Mechanism:** `dgraph-live` idle pod (`deploy/dev/dgraph-live.yaml`, runs `sleep infinity`, mounts `orbital-restore-pvc`). Orbital execs into it by label selector `app.kubernetes.io/name=dgraph-live` to run `dgraph live`. Alpha pod hit only via HTTP (`/alter` for `drop_all`, `/admin/schema` to re-apply schema). Idle pod stays resident — exec is instant, no Kubernetes Job startup delay.
- `k8sAvailable` flag — `rest.InClusterConfig()` attempted at startup. `k8sAvailable = true` only if it succeeds. Restore page always shows manual kubectl runbook; stored-backup restore section hidden when `k8sAvailable` is false.
- `ORBITAL_RESTORE_TIMEOUT` env var (default 10m).

## Swagger

- Regenerated via `make docs` (runs `swag init -g cmd/orbital/main.go -o docs`). Both `make build-orbital` and `make run-orbital` depend on this — docs always up to date.
- Swagger tag names: `backup graph`, `export subgraph`, `oci`.
