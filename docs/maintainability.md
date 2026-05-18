# Orbital Maintainability Plan

A step-by-step technical debt and improvement roadmap based on a full codebase audit (May 2026). Work through phases in order — each phase unblocks the next.

## Executive Summary

- **Zero Go tests.** Every change is manual QA. Complex async pipelines (export, backup, restore, OCI publish) have no automated verification.
- **Three security/correctness bugs exist today:** an unauthenticated audit log endpoint, missing goroutine timeouts that can wedge jobs permanently, and no startup reaper for jobs interrupted by crashes.
- **`internal/handler/` is a 3,560-line god package** mixing HTTP routing, business logic, DGraph client calls, file I/O, and zip creation — with 22+ bare `http.Post` calls and no injectable seams for testing.
- **`web/static/app.js` is a 2,418-line monolith** with no module system, multiple duplicate event listener registrations (double-firing Escape/modal/toast handlers), and 8 magic-delay `setTimeout` patterns.
- **Several data-integrity gaps:** backup retention can orphan S3 objects, restore never verifies the backup checksum before `drop_all`, and a failed cosign sign leaves an unsigned artifact in the OCI registry with no rollback.

---

## Phase 1: Security & Correctness

> Fix these before MVP. Each item is small and independent.

### 1.1 Events endpoint bypasses bearer auth ⚠️

**Problem:** `internal/server/server.go:245` registers `/api/v1/events` on the `root` group instead of the `api` group. This skips the bearer auth middleware, making the entire audit log publicly accessible.

**Fix:** Move the route registration from `root.GET("/api/v1/events", ...)` to `api.GET("/events", ...)`.

**File:** `internal/server/server.go:245`
**Effort:** 5 min

---

### 1.2 No timeouts on async goroutines

**Problem:** `Export.runExport` (`handler/export.go:312`), `BackupHandler.runBackup` (`handler/backup.go:451`), and `Publisher.Publish` (`oci/publisher.go:73`) all use `context.Background()` with no timeout. If DGraph or S3 hangs, the goroutine blocks indefinitely and the job stays in `running` state forever. (Restore already has a timeout — no change needed there.)

**Fix:** Replace `context.Background()` with `context.WithTimeout(context.Background(), N)`. Reasonable values: 30 min for export, 30 min for backup, 10 min for OCI publish. Make these configurable via `config.go` alongside `RestoreTimeout`.

**Files:**
- `internal/handler/export.go:312`
- `internal/handler/backup.go:451`
- `internal/oci/publisher.go:73`
- `internal/config/config.go` — add `ExportTimeout`, `BackupTimeout`, `OCIPublishTimeout`

**Effort:** 30 min

---

### 1.3 No stuck-job reaper on startup

**Problem:** If the process crashes while a job is `pending` or `running`, that job stays in that state permanently. There is no startup reconciliation.

**Fix:** On startup (in `server.New()`), query all three job tables (`ExportJob`, `Backup`, `RestoreJob`) for rows with `pending` or `running` status and update them to `failed` with error `"interrupted: server restarted"`.

**Files:**
- New `internal/handler/reaper.go` — `ReconcileStaleJobs(ctx, db)` function
- `internal/server/server.go` — call `ReconcileStaleJobs` after DB init

**Effort:** 1 hr

---

### 1.4 Insecure session HMAC key in production

**Problem:** `config.go` defaults `SessionHMACKey` to `"local-dev-hmac-key-change-in-prod"`. If an operator forgets to set this in production, all sessions are signed with a well-known key.

**Fix:** In `config.New()`, if `!cfg.Dev` and `cfg.SessionHMACKey == "local-dev-hmac-key-change-in-prod"`, return an error that halts startup.

**File:** `internal/config/config.go`
**Effort:** 15 min

---

### 1.5 Hardcoded Azure AD tenant in `orbauth`

**Problem:** `internal/orbauth/auth.go:21-23` defines `TenantID`, `ClientID`, and `Scope` as package-level constants. The CLI can only authenticate against one specific Azure AD tenant.

**Fix:** Move these three values to `config.go` and pass them into `orbauth` as parameters. `OIDCIssuerURL` and `OIDCClientID` already exist in config — wire them into the orbauth functions.

**Files:**
- `internal/orbauth/auth.go` — remove constants, accept via parameters
- `internal/config/config.go` — ensure fields are present
- `cmd/orbital-cli/` — pass config values through

**Effort:** 45 min

---

### 1.6 Backup retention can orphan S3 objects

**Problem:** `enforceRetention` in `handler/backup.go:658` deletes the DB record regardless of whether the S3 `deleteObject` call succeeded. Failed S3 deletes are only logged as warnings, creating phantom objects with no corresponding DB record.

**Fix:** Only delete the DB record if the S3 delete succeeded (or the key was empty, indicating no remote object to delete).

**File:** `internal/handler/backup.go:641-663`
**Effort:** 15 min

---

### 1.7 Restore never verifies backup checksum

**Problem:** The backup process computes and stores a SHA-256 checksum in PostgreSQL, but `RestoreHandler.runRestore` never checks the downloaded backup against it. A corrupted or tampered backup reaches `drop_all` (the point of no return) unverified.

**Fix:** After downloading the backup zip and before calling `extractBackupZip`, hash the downloaded file and compare against `bk.Checksum`. Fail the job with a clear error if they don't match.

**File:** `internal/handler/restore.go` (between lines 367 and 374)
**Effort:** 30 min

---

## Phase 2: Testing Foundations

> The single highest-leverage investment. See `docs/testing-strategy.md` for the full strategy, test pyramid, infrastructure design, and Playwright conventions. The items below are the structural prerequisites — do them in order.

### 2.1 Design DGraph client interface — **Opus required first**

The DGraph client abstraction is the key enabler for all integration testing. The interface shape is a design decision (transport-level vs. semantic-level) with long-term consequences — get it wrong and tests are shallow; get it right and integration tests meaningfully verify behavior.

**Do a short Opus session (15–20 min) to design and commit the interface before Sonnet implements it.** See `docs/maintainability.md` — "What Needs Opus vs. Sonnet" section.

Once designed, Sonnet implements `internal/dgraph/client.go` and wires it into the seven handler files that currently hold a raw `dgraphURL`:
- `internal/handler/export.go`
- `internal/handler/backup.go`
- `internal/handler/restore.go`
- `internal/handler/graphql.go`
- `internal/handler/datacenter.go`
- `internal/handler/server.go`
- `internal/handler/inventory.go`

This also fixes: no timeouts on DGraph calls, no connection pooling, 22+ duplicated `http.Post` call sites.

**Effort:** 15–20 min Opus (design) + 4–6 hr Sonnet (implementation + wiring)

---

### 2.2 Test infrastructure — Docker Compose + helpers

Before writing any tests, the test stack needs to exist. See `docs/testing-strategy.md` for the full service list and port assignments.

**Files to create:**
- `deploy/test/docker-compose.yml` — DGraph, PostgreSQL, Valkey, MinIO, OCI registry (CNCF Distribution)
- `internal/testutil/dgraph.go` — `ResetDGraph`, `SeedMinimal`
- `internal/testutil/db.go` — `NewTestDB`
- `internal/testutil/jobs.go` — `WaitForJob`
- `Makefile` — add `test-stack-up`, `test-stack-down`, `test-unit`, `test-integration`, `test-e2e`, `test` targets

**Effort:** 2–3 hr

---

### 2.3 Unit tests — pure logic (no Docker required)

Write table-driven unit tests for pure functions first — these need nothing running and give immediate value.

| File | Covers |
|------|--------|
| `internal/oci/publisher_test.go` | `RepoForDC`, `NextTag`, `extractZip`, `PublicKeyFingerprint` |
| `internal/handler/backup_test.go` | `writeZip`, checksum, `enforceRetention` (mock `blobStorage`) |
| `internal/handler/restore_test.go` | Checksum verification (after additional-findings.md A.4) |
| `internal/handler/graphql_test.go` | `toFloat64` edge cases, `extractResourceIDs` |
| `internal/config/config_test.go` | Prod-safety validation |

**Note:** `blobStorage` is already an interface (`backup.go:33-38`). Write a simple in-memory mock for the backup/restore tests.

**Effort:** 4–6 hr

---

### 2.4 Integration tests — DGraph, export, backup, OCI, restore

See `docs/testing-strategy.md` steps T.6 and T.7 for the full scope. Requires 2.1 (client) and 2.2 (infrastructure) first.

Key flows to cover:
- Export pipeline end-to-end: seed → export → verify zip contents
- Backup: trigger → verify MinIO object + PostgreSQL record + checksum
- OCI publish: export → publish → verify manifest + cosign signature in local registry
- Restore: backup → restore → verify DGraph state (isolated DGraph instance)

**Run with:** `go test -tags integration ./...`
**Effort:** 6–8 hr

---

### 2.5 CI pipeline

Add GitHub Actions workflow with four jobs: `lint`, `unit-tests`, `integration-tests`, `e2e-tests`. Integration and e2e jobs spin up the test Docker Compose stack. Playwright HTML report uploaded as artifact on failure.

**File:** `.github/workflows/ci.yml`
**Effort:** 1–2 hr

---

## Phase 3: Backend Structural Cleanup

> These reduce duplication and complexity. Do alongside feature work — no need to batch.

### 3.1 Extract `currentUser()` helper

**Problem:** The pattern of extracting the current user's display name from the Echo context appears verbatim in at least 7 locations:

```go
userName, _ := c.Get("user_name").(string)
if userName == "" {
    userName, _ = c.Get("user_email").(string)
}
```

Found in: `export.go:133`, `backup.go:299`, `restore.go:220`, `graphql.go:98`, `datacenter.go:211`, `server.go:237`, `ui.go:85`.

**Fix:**
```go
// internal/handler/helpers.go
func currentUser(c echo.Context) string {
    if v, _ := c.Get("user_name").(string); v != "" { return v }
    v, _ := c.Get("user_email").(string)
    return v
}
```

Replace all 7+ instances.

**File to create:** `internal/handler/helpers.go`
**Effort:** 30 min

---

### 3.2 Session store created per-request

**Problem:** `auth.newStore(keys)` calls `sessions.NewCookieStore()` on every HTTP request — both in the server middleware (`server.go:43-54`) and in every `Get*`/`Set*`/`Validate*` function in `session.go`. gorilla/sessions is designed with a singleton store.

**Fix:** Create the `*sessions.CookieStore` once in `server.New()` and pass it into auth functions that need it.

**Files:**
- `internal/auth/session.go` — accept `*sessions.CookieStore` instead of re-creating it
- `internal/server/server.go` — create store once, inject it

**Effort:** 1 hr

---

### 3.3 OCI push rollback on signing failure

**Problem:** In `oci/publisher.go`, `doPush()` calls `pushArtifact` then `sign`. If `pushArtifact` succeeds but `sign` fails, the unsigned artifact is already in the OCI registry while the DB record is marked `failed`. No cleanup occurs.

**Fix:** When `sign` returns an error, attempt to delete the pushed manifest from the registry using the digest returned by `pushArtifact`. If the delete also fails, log a warning with the digest so the operator can clean up manually. Either way, mark the artifact `failed`.

**File:** `internal/oci/publisher.go`
**Effort:** 1-2 hr

---

### 3.4 Add ent edge from `RegistryArtifact` to `ExportJob`

**Problem:** `ent/schema/registry_artifact.go:16` stores `export_job_id` as a plain UUID field with no ent `Edge`. This means no cascading delete (deleting an export job leaves orphan artifact rows) and no type-safe graph traversal.

**Fix:** Add an ent edge from `RegistryArtifact` → `ExportJob` and its back-edge, then regenerate.

**Files:**
- `ent/schema/registry_artifact.go` — add `Edges()` with edge to `ExportJob`
- `ent/schema/export_job.go` — add back-edge
- Run `go generate ./ent`

**Effort:** 1 hr

---

### 3.5 Delete empty placeholder packages

**Problem:** Three packages contain only a bare package declaration — no types, no functions, no logic:
- `internal/discovery/discovery.go`
- `internal/discovery/bmc/bmc.go`
- `internal/drift/drift.go`

**Fix:** Delete these files and their parent directories.

**Effort:** 5 min

---

### 3.6 Remove dev-mode artificial sleeps

**Problem:** `handler/server.go:203` and `handler/datacenter.go:162` both have `time.Sleep(150 * time.Millisecond)` guarded by `if h.dev`. These were presumably added for UI skeleton-loader testing but slow down local development for no benefit.

**Fix:** Delete both blocks.

**Files:**
- `internal/handler/server.go:202-204`
- `internal/handler/datacenter.go:161-163`

**Effort:** 5 min

---

### 3.7 Fix Go version in `go.mod`

**Problem:** `go.mod:3` declares `go 1.25.5`. Go 1.25 does not exist — this is a typo or artifact.

**Fix:** Run `go version` to confirm the actual version in use and update `go.mod` to match.

**File:** `go.mod:3`
**Effort:** 5 min

---

## Phase 4: UI / Frontend Cleanup

> Reduces fragility and eliminates double-firing event listeners. Do alongside feature work.

### 4.1 Extract DataTables button config function

**Problem:** The Excel/CSV/Copy/ColVis/Reload button configuration is copy-pasted across 4 DataTable initializations in `app.js` at lines 653, 761, 858, and 2211. Each copy is ~8 lines of near-identical object literals.

**Fix:** Extract a `dtButtons(reloadId)` helper function that returns the buttons array, parameterized by the reload button element ID. Replace all 4 call sites.

**File:** `web/static/app.js`
**Effort:** 30 min

---

### 4.2 Consolidate inline scripts into app.js

**Problem:** Three template components contain inline `<script>` blocks that duplicate or conflict with logic in app.js:

- `web/templates/components/login-modal.gohtml:69-103` — defines `openModal`, `closeModal`, `closeAllModals`, registers a `DOMContentLoaded` listener AND an Escape key handler. app.js line 1267 also registers modal click handlers; app.js line 1289 also registers an Escape handler. **Result: double-firing.**
- `web/templates/components/todo-toast.gohtml:1-28` — defines `displayTodoToast`, registers a `DOMContentLoaded` listener for `.todo` clicks. app.js line 337 also registers a `.todo` click delegation. **Result: double-firing.**
- `web/templates/components/report-issue-modal.gohtml:41-51` — registers its own Escape handler. app.js line 1289 also has one. **Result: double-firing.**

**Fix:** Move any logic from those inline blocks that isn't already covered into app.js, then remove all three `<script>` blocks from the templates. Where logic is duplicated, keep only the app.js version.

**Files:**
- `web/templates/components/login-modal.gohtml`
- `web/templates/components/todo-toast.gohtml`
- `web/templates/components/report-issue-modal.gohtml`
- `web/static/app.js`

**Effort:** 2 hr

---

### 4.3 Add `data-testid` attributes to key UI elements

**Problem:** Only one `data-testid` exists in the entire UI (`data-testid="app-version"` in `menu.gohtml:64`). All Playwright selectors rely on fragile structural/CSS selectors that break when layout changes.

**Fix:** Add `data-testid` attributes to elements that Playwright tests need stable hooks for. Prioritize in this order:
1. `web/templates/fragments/datacenter-tab.gohtml` — summary table, server rows, edit button, reload button
2. `web/templates/fragments/server-tab.gohtml` — server detail table, edit button
3. `web/templates/components/navbar.gohtml` — nav items, user menu
4. Remaining templates as e2e tests are written

**Convention:** Use `data-testid="<noun>-<action>"` format (e.g., `data-testid="dc-edit-btn"`, `data-testid="server-row"`).

**Effort:** 1-2 hr (initial batch); ongoing as tests are added

---

### 4.4 Remove SCSS dead code

**Problem:** `web/sass/main.scss:507-658` contains ~150 lines of commented-out `.app-menu` code — an entire duplicate of the real app-menu block.

**Fix:** Delete lines 507-658.

**File:** `web/sass/main.scss`
**Effort:** 5 min

---

## Phase 5: Post-MVP Polish

> Quality improvements for after the MVP milestone. Do in any order.

### 5.1 Expand E2E test coverage

Currently only the data center tab and data center edit flow are covered (~10% of UI). Add Playwright specs for:
- Servers page (table load, drill-down to server tab)
- Inventory/home page (DataTable, type filter)
- Backups page (trigger, poll, download link, delete)
- Restore page (backup select, trigger, poll, log modal)
- Export page (trigger, poll, download, publish flow)
- Signed Artifacts / Edge Delivery page
- Audit Log page (expandable rows)
- Schema page

Prerequisite: Phase 4.3 `data-testid` attributes on each page before writing that page's spec.

**Directory:** `e2e/`

---

### 5.2 Split app.js into per-feature modules

The 2,418-line monolith should be split into one file per feature area. Each module handles its own `DOMContentLoaded` registration, page-specific DataTable init, and HTMX afterSwap handling. The shared utilities (timestamp formatting, tab management, skeleton loaders) become a `utils.js`.

Suggested split:
```
web/static/js/
  utils.js        # formatTimestamp, relativeTime, fetchWithMinDelay, tab management
  inventory.js    # home page DataTable
  datacenter.js   # DC list, DC detail tabs, DC edit modal
  server.js       # server list, server detail tabs, server edit modal
  backup.js       # backup page
  export.js       # export page + OCI publish
  artifacts.js    # signed artifacts / edge delivery
  audit.js        # audit log DataTable
  restore.js      # restore page
  main.js         # DOMContentLoaded orchestration, shared event listeners
```

Consider using esbuild (zero-config, fast, adds one dev dependency) to bundle into a single `app.js` for production.

Prerequisite: Phase 4.2 (inline script consolidation) should be done first.

---

### 5.3 Replace JS skeleton loaders with server-rendered templates

`showDatacenterSkeleton()` (app.js:343-408) and `showServerSkeleton()` (app.js:410-473) build entire HTML layouts as JavaScript template literals. These can drift out of sync with the actual server-rendered HTML structure.

**Fix:** Create Go template fragments for skeletons (`web/templates/fragments/datacenter-skeleton.gohtml`, `server-skeleton.gohtml`) and serve them from the initial HTMX swap, rather than injecting them via JS. Remove the JS functions.

---

### 5.4 Incremental handler package decomposition

The `internal/handler/` package at 3,560 lines is the long-term target for decomposition. Work incrementally:

**Step 1 (extract storage abstraction):** The `blobStorage` interface (`backup.go:33-38`) is already defined. Move it and its two implementations (`s3Storage`, `azureStorage`) to `internal/storage/`. This also resolves the duplication where `backup.go:74` and `restore.go:69` both independently detect Azure vs. S3 by checking the endpoint URL.

**Step 2 (extract export domain logic):** Move the subgraph extraction logic (`fetchNamespaceSubgraph`, `doExport`) from `handler/export.go` into `internal/export/`. The handler becomes a thin coordinator.

**Step 3 (extract backup domain logic):** Same for `doBackup`, `writeZip`, `enforceRetention` → `internal/backup/`.

After each step, the handler file becomes a thin HTTP adapter; the extracted package becomes independently testable.

---

## What Needs Opus vs. Sonnet

Most of this plan is execution work — Sonnet can implement every item in Phases 1–4 directly from the specs above. Two items are exceptions where Opus thinking is needed *before* Sonnet writes code:

### Spike 8 (Authorization) — Opus design required, not in this plan

Spike 8 is the current MVP blocker and the highest-risk design decision in the codebase. It is intentionally not covered here. DGraph `@auth` directives are evaluated at query time by DGraph itself — not by Echo middleware — which means the GraphQL proxy layer must pass through the right JWT claims in a specific format. The mapping from Azure AD App Roles to DGraph `@auth` rules, the offline JWT validation strategy for air-gap scenarios, and the interaction with the existing session/bearer auth split all require an Opus design session before any implementation. Getting this wrong means either silent over-permissioning or hard-to-debug 403s.

**Do not start Spike 8 on Sonnet without a settled design.**

### DGraph client interface shape (item 2.1) — Opus design recommended

Item 2.1 is the single highest-leverage item in the plan — it unlocks all Go testing. But the interface sketch in this document is intentionally rough. There's a meaningful design choice between:

- **Transport-level** interface (pass DQL/GraphQL strings, get raw bytes back) — easy to define, but tests only verify that the right string was sent, not that the right thing happened
- **Semantic-level** interface (typed methods per operation like `QueryNamespaceSubgraph`) — tests are more meaningful, but the interface grows with every new query

Getting this wrong creates a worse testing situation than the current one. Before Sonnet implements 2.1, spend 15–20 minutes with Opus designing the interface shape against Orbital's actual query diversity.

---

## Observability Gap (not yet addressed)

The async jobs (export, backup, OCI publish, restore) run in goroutines with no trace ID, no structured log correlation, and no way for an operator to understand what happened mid-job beyond polling the final status. When an export fails at step 7 of 12 after 8 minutes, there is no way to identify the failing step without reading raw server logs and correlating by timestamp.

**Recommended pattern (not yet implemented):** Thread a `job_id` field through every `slog` call made during a job's execution. No new dependencies required — just consistent field usage:

```go
log := slog.With("job_id", jobID, "job_type", "export")
log.Info("starting subgraph fetch", "namespace", ns)
// ...
log.Error("dgraph query failed", "step", "fetchScalars", "err", err)
```

This is low-effort, high-impact, and should be done as part of item 2.1 (when the DGraph client is introduced) so the log fields flow naturally through the client calls. It is not a separate work item — fold it into the async timeout work in item 1.2 and the client work in item 2.1.

---

## Do Not Touch Before MVP

**Item 5.4 (handler package decomposition) should not be started until after the MVP milestone.** Every feature addition between now and MVP touches the handler package. Decomposing it mid-sprint adds coordination overhead with no user-visible benefit. The right time is after the MVP cut, when the feature surface has stabilized.

---

## Implementation Order

**Session 1 — Quick wins (~1 hr combined):**
Items 1.1, 1.6, 3.5, 3.6, 3.7, 4.4

**Session 2 — Correctness (~3 hr combined):**
Items 1.2 (+ fold in job_id logging), 1.3, 1.4, 1.7, 3.1

**Opus design session — before Sessions 3-4:**
DGraph client interface shape (item 2.1, 15–20 min). See `docs/testing-strategy.md` for full context. Also: Spike 8 authorization design (separate session).

**Sessions 3-4 — Testing foundation (~12-14 hr combined):**
Item 2.2 (infrastructure) in parallel with 2.3 (unit tests, no Docker needed) → then 2.1 Sonnet implementation → then 2.4 (integration tests) → then 2.5 (CI). Full detail in `docs/testing-strategy.md`.

**Ongoing alongside feature work:**
Items 1.5, 3.2, 3.3, 3.4, 4.1, 4.2, 4.3

**Post-MVP:**
Items 5.1-5.4 (5.4 strictly post-MVP)
