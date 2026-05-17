# Additional Audit Findings

Findings from the May 2026 codebase audit that are not covered in `docs/maintainability.md`. These are correctness bugs, security risks, and operational gaps discovered alongside the items in that plan. Each is actionable independently.

---

## Security & Sensitive Data

### A.1 Production infrastructure values baked as config defaults

**Problem:** `internal/config/config.go` defaults several fields to real, named production infrastructure. A deployment that forgets to set the corresponding environment variables will silently use these values:

| Line | Field | Default value |
|------|-------|---------------|
| 38 | `OIDCIssuerURL` | `https://login.microsoftonline.com/8f231c2a-9551.../v2.0` (real Azure AD tenant) |
| 39 | `OIDCClientID` | `5fc832f6-843e-...` (real Azure AD app registration) |
| 42 | `OCIRegistry` | `armadaeksatest.azurecr.io` (real ACR) |
| 44 | `OCIUsername` | `armadaeksatest` (real ACR admin username) |
| 31 | `S3Endpoint` | `https://armadagalleonbackups.blob.core.windows.net` (real Azure Blob) |
| 34 | `S3AccessKey` | `armadagalleonbackups` (real storage account name) |

A misconfigured deployment would: attempt OIDC auth against the production Azure AD tenant, push OCI artifacts to the production registry, and write backups to production Azure Blob storage — silently, without any error.

The `SessionHMACKey` / `SessionEncryptionKey` defaults are already flagged in maintainability.md item 1.4 as a prod-safety check. Extend that same pattern here: if `!cfg.Dev` and any of the above fields still hold their default values, fail startup with an explicit error naming the field.

**Fix:**
- `internal/config/config.go` — add prod-safety assertions for `OIDCIssuerURL`, `OCIRegistry`, `S3Endpoint`, and their credential companions alongside the HMAC key check in item 1.4.
- Alternatively, remove the production values from defaults entirely and use `required:"true"` envconfig tags so the field must be explicitly set. (This is the cleaner fix but requires coordinating the production deployment manifests to ensure all vars are set.)

**Effort:** 30 min

---

### A.2 Backup temp file contains full DGraph dump and persists on crash

**Problem:** `handler/backup.go:536-540` writes the backup zip to `os.TempDir()` and registers cleanup via `defer os.Remove(zipPath)`. A `defer` only runs on a normal function return, not on a process crash (SIGKILL, OOM kill, panic outside recovery). If orbital crashes between zip creation and upload, the full DGraph backup — all configuration items for all data centers — sits in the OS temp directory indefinitely.

On Linux, `os.TempDir()` is typically `/tmp`, which is not cleared between reboots unless `tmpfiles.d` is configured to do so. On AKS, pod restarts leave the previous container's `/tmp` behind until the pod is evicted.

**Fix:**
- Write the backup zip to a controlled directory (e.g., `ORBITAL_EXPORT_DIR`) rather than `os.TempDir()`, so it is on the same volume as other orbital artifacts and can be audited.
- On startup (as part of the stuck-job reaper in maintainability.md item 1.3), scan for orphaned backup zip files with no corresponding `completed` or `running` backup job and delete them.

**Effort:** 30 min

---

## Correctness Bugs

### A.3 MVCC optimistic lock silently passes on unexpected JSON types

**Problem:** `internal/handler/graphql.go:128-133` implements optimistic concurrency control by comparing the client-sent `ifVersion` against the current version fetched from DGraph:

```go
if int(toFloat64(before["version"])) != int(toFloat64(ifVersion)) {
    // return 409 conflict
}
```

`toFloat64()` (lines 376-387) handles `float64`, `int`, and `json.Number`. For any other type — `string`, `nil`, `bool`, unexpected JSON — it falls through and returns `0`.

**Silent false-positive scenario:** If DGraph returns the version as a `string` (e.g., `"3"`), `toFloat64` returns `0`. If the client sends `ifVersion` also as a string (e.g., `"2"`), that also becomes `0`. The comparison `0 != 0` is `false`, so the conflict check **passes** — a stale write proceeds undetected. Both parties wrote against different versions of the same node with no error.

The practical risk is low today because DGraph returns version numbers as `float64` in JSON and the GraphQL client sends them the same way. But it is a latent correctness hazard that could be triggered by a schema change, a client library update, or a DGraph version upgrade that changes numeric serialization.

**Fix:** Make `toFloat64` return `(float64, bool)` — return `false` for unknown types, and treat `(false, false)` as an error condition that returns 409. Alternatively, use `json.Number` throughout the MVCC path and compare as strings.

**File:** `internal/handler/graphql.go` lines 376-387 and 128-133
**Effort:** 45 min

---

### A.4 Failed export leaks scratch directory and leaves scratch DGraph dirty

**Problem:** `handler/export.go` creates a per-job scratch directory at step 6 of the export pipeline (`os.MkdirAll(jobScratchDir, 0o755)` line 389) and performs a `drop_all` on scratch DGraph at step 3 (line 363). On failure at any step from 3 onward, two things are left behind:

1. **Leaked scratch directory:** There is no `defer os.RemoveAll(jobScratchDir)` anywhere in `doExport` or `runExport`. The per-job directory (under `DGRAPH_SCRATCH_EXPORT_DIR`) persists indefinitely after a failed export. Across many retries, these accumulate.

2. **Dirty scratch DGraph:** If the export fails after `drop_all` (step 3) but before the data load completes (step 7), scratch DGraph holds either no data or a partial subgraph from the failed job. The next successful export will wipe it at step 3, so it self-heals — but until then, any consumer of scratch DGraph (if one exists) would see corrupted data.

**Fix:**
- Add `defer os.RemoveAll(jobScratchDir)` immediately after `os.MkdirAll` in `doExport`. This ensures cleanup on both success and failure. (See maintainability.md note on no-auto-cleanup: this is a scratch dir created specifically for this job's lifecycle, so deferred deletion is correct here — it is not a user-visible artifact.)
- For scratch DGraph state: no action needed beyond the self-healing behavior of the next export's `drop_all`. Document this as an accepted invariant.

**File:** `internal/handler/export.go` — add defer after line 389
**Effort:** 15 min

---

### A.5 OCI push and cosign sign use separate, independent credential stacks

**Problem:** Publishing an artifact uses two distinct authentication libraries against the same OCI registry in sequence:

1. **Push** (`publisher.go:237-252`): uses `oras.land/oras-go/v2/registry/remote/auth` with `orasauth.Credential{Username, Password}`
2. **Sign** (`publisher.go:188-219`): uses `github.com/google/go-containerregistry/pkg/authn` with `authn.AuthConfig{Username, Password}`

Both currently draw from the same `p.cfg.Username`/`p.cfg.Password` env vars, so they are identical **today**. The risk is divergence:
- Cosign also reads `~/.docker/config.json` and respects Docker credential helpers, which ORAS does not. A container environment where the Docker credential helper is configured but environment variables are not set could cause ORAS to fail while cosign succeeds, or vice versa.
- Token lifetimes differ: ORAS caches per-`newRepo()` call (no caching between jobs), cosign uses `go-containerregistry` whose token caching may differ. On a long job with a short-lived registry token, one succeeds and the other fails.
- If Azure ACR switches from admin-key auth to managed identity in the future, updating one library's credential path without the other would cause silent failures at sign time (the harder-to-diagnose path).

This is the root cause behind the scenario described in maintainability.md item 3.3 (unsigned artifact left in registry after sign failure).

**Fix:** Create a single `registryAuth` config that both ORAS and cosign consume. Ideally both paths should be initialized from the same credential source at startup — if using static username/password, wire it explicitly into both; if using managed identity, ensure both libraries draw from the same token source. Document the two paths in a comment at the point where `Publisher` is constructed so future credential changes update both.

**File:** `internal/oci/publisher.go` — comment at struct definition; update `NewPublisher`
**Effort:** 30 min (comment/documentation); 2-3 hr (proper unified credential source if switching to managed identity)

---

## Operational Gaps

### A.6 `orb scan` is entirely simulated — no real discovery occurs

**Problem:** `internal/cli/scan.go` is a UI demo, not a functional command. The full implementation:

```go
func runScan(cmd *cobra.Command, args []string) error {
    // Shows spinner for 3 seconds, prints "Found 3 BMC interfaces" (hardcoded)
    // Animates a progress bar labeled "Scanning storage devices" (fake ticks)
    // Prints "Scan complete"
    return nil
}
```

There are no network calls, no BMC/IPMI/Redfish library imports, no device enumeration, no output to any file or database. The number `3` is a literal. The command always succeeds and always produces the same output regardless of the environment.

An operator running `orb scan` on a modular data center would believe discovery completed successfully with 3 BMC interfaces found. If this output is used as the basis for any decision (e.g., "scan before registering orb with orbital"), they would be acting on fabricated data.

**Fix:** Either replace the stub with real discovery logic (the intended scope of Spike 2 per ROADMAP.md) or make the command clearly return an error: `return fmt.Errorf("orb scan: not yet implemented")`. The latter is safer than a fake success.

**File:** `internal/cli/scan.go`
**Effort:** 5 min (to make it fail honestly); Spike 2 effort for real implementation

---

### A.7 No database indexes on job status columns

**Problem:** `ent/schema/export_job.go`, `ent/schema/backup.go`, and `ent/schema/restore_job.go` define no `Indexes()` method — only primary keys are indexed. Every trigger handler runs a status query against these tables (`WHERE status IN ('pending', 'running')`) to detect conflicts before starting a new job. These are full table scans today.

As operational job history accumulates over months of production use, these scans degrade. At 10,000 export job rows (reasonable for a system running exports multiple times per day), an unindexed status query scans all 10,000 rows on every export trigger.

**Fix:** Add a composite index on `(status)` — or `(status, created_at)` to support the common pattern of "most recent running job" — to all three schemas.

```go
func (ExportJob) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("status"),
    }
}
```

Then run `go generate ./ent`.

**Files:**
- `ent/schema/export_job.go`
- `ent/schema/backup.go`
- `ent/schema/restore_job.go`

**Effort:** 30 min (schema change + regeneration + migration note)

---

## Relationship to `docs/maintainability.md`

These findings extend the maintainability plan but are kept separate because they are distinct in character — mostly correctness bugs and security risks rather than structural debt. The maintainability plan should be worked through in its stated order. The items here can be slotted in alongside the relevant phases:

| This doc | Natural pairing |
|----------|----------------|
| A.1 (prod config defaults) | Alongside maintainability item 1.4 (HMAC key check) |
| A.2 (backup temp file) | Alongside maintainability item 1.3 (startup reaper) |
| A.3 (MVCC false-positive) | Alongside maintainability item 2.1 (DGraph client abstraction) |
| A.4 (export scratch dir leak) | Alongside maintainability item 1.2 (goroutine timeouts) |
| A.5 (OCI dual credential path) | Alongside maintainability item 3.3 (OCI push rollback) |
| A.6 (orb scan is fake) | Alongside maintainability item 3.5 (delete dead packages) |
| A.7 (missing DB indexes) | Alongside maintainability item 3.4 (ent edge for RegistryArtifact) |
