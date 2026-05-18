# Testing Strategy

Orbital's testing strategy is grounded in three principles: test behavior not implementation, use real services in the integration layer, and make tests honest about what they actually verify.

**Current state:** zero Go tests, two Playwright specs covering ~10% of the UI. Everything below is work to be done.

---

## Test Pyramid

```
         ┌───────────────┐
         │  E2E (Playwright) │  Full stack, browser automation, UI behavior
         └───────────────┘
       ┌─────────────────────┐
       │   Integration tests  │  Real DGraph, PostgreSQL, MinIO, OCI registry
       └─────────────────────┘
     ┌───────────────────────────┐
     │       Unit tests           │  Pure logic only, no external dependencies
     └───────────────────────────┘
```

### Layer 1 — Unit tests

Pure functions with no external dependencies. Fast (< 1s total). Run on every save.

**What belongs here:**
- OCI publisher: `RepoForDC`, `NextTag`, `extractZip`, `PublicKeyFingerprint`
- Backup: `writeZip`, checksum computation, `enforceRetention` (with mock `blobStorage` — the interface already exists at `handler/backup.go:33`)
- GraphQL proxy: `toFloat64` edge cases, `extractResourceIDs` (the three-source orbId logic)
- Config: prod-safety validation (HMAC key, OIDC issuer, OCI registry defaults)
- Any pure transformation or validation function

**What does NOT belong here:** anything that touches DGraph, PostgreSQL, S3, or the filesystem. Those go in integration tests.

**Run with:** `go test -short ./...`

---

### Layer 2 — Integration tests

Test against real services running in Docker. Slower (seconds per test) but honest — no mocked DGraph responses, no fake SQL, no stubbed HTTP servers for external services.

**What belongs here:**
- DGraph client: all query/mutation/alter operations against real DGraph
- Export pipeline end-to-end: seed namespace + DC → trigger export → verify zip contents and artifact path
- Backup pipeline: trigger backup → verify MinIO object + PostgreSQL record + checksum
- OCI publish: trigger export → publish → verify manifest in local OCI registry + cosign signature verifiable with the dev key
- Restore pipeline: backup → restore → verify DGraph subgraph matches original (isolated DGraph instance — see Test Isolation below)
- API endpoint tests: HTTP-level tests against live orbital handlers with real backing services

**Run with:** `go test -tags integration ./...`

---

### Layer 3 — E2E (Playwright)

Browser automation against a fully running orbital. Tests what the user actually experiences.

**Philosophy:** test behavior, not implementation. A test should describe what a user does and what they see — not whether a specific JS function was called.

**What belongs here:**
- All page-level user flows: navigate, interact, verify visible outcome
- Async pipeline flows triggered from the UI: click "Run Backup", wait for status to show "Completed", verify the new row appears in the backup table
- Cross-page flows: create DC → drill down → verify content in tab
- Error states: trigger a failure, verify the error message is shown

**What does NOT belong here:** anything that can be verified at the integration layer. E2E tests are for the full user experience, not for catching business logic bugs.

**Run with:** `make test-e2e` (requires orbital running on `:8001`)

---

## Test Infrastructure

### Docker Compose for tests

Create `deploy/test/docker-compose.yml` — a separate stack from the dev stack to avoid port conflicts:

| Service | Test port | Dev port | Notes |
|---------|-----------|----------|-------|
| DGraph Zero | 15080, 16080 | 5080, 6080 | |
| DGraph Alpha | 18080, 19080 | 8080, 9080 | |
| DGraph Alpha (scratch) | 18081, 19081 | 8081, 9081 | Only needed for export tests |
| PostgreSQL | 5433 | 5432 | DB name: `orbital_test` |
| Valkey | 6380 | 6379 | |
| MinIO | 9000, 9001 | — | S3-compatible; bucket: `orbital-test` |
| OCI Registry (Distribution) | 5000 | — | No auth; ephemeral storage |

**Makefile targets to add:**
```makefile
test-stack-up:
    docker compose -f deploy/test/docker-compose.yml up -d --wait

test-stack-down:
    docker compose -f deploy/test/docker-compose.yml down -v

test-unit:
    go test -short ./...

test-integration: test-stack-up
    go test -tags integration ./...

test-e2e:
    npx playwright test

test: test-unit test-integration test-e2e
```

### Test helper package

Create `internal/testutil/` with shared setup/teardown helpers:

```go
// ResetDGraph drops all data and re-applies the schema. Call at the start of each
// integration test suite (not between individual tests).
func ResetDGraph(t *testing.T, adminURL, schemaPath string)

// SeedMinimal seeds one Namespace + DataCenter so tests have a known starting state.
func SeedMinimal(t *testing.T, dgraphURL string) (namespaceID, dcID string)

// NewTestDB returns an ent client connected to the test PostgreSQL instance.
// Tables are truncated after the test via t.Cleanup.
func NewTestDB(t *testing.T) *ent.Client

// WaitForJob polls job status until it reaches a terminal state or times out.
// Returns the final status.
func WaitForJob(t *testing.T, db *ent.Client, jobID uuid.UUID, timeout time.Duration) string
```

---

## Test Isolation

### DGraph isolation strategy

One shared DGraph instance per test run. Drop-all + schema-apply + minimal seed at the start of the integration test suite (`TestMain`). Individual tests within the suite share state — they must not depend on each other's mutations or clean up after themselves.

**Exception — restore tests:** Restore performs `drop_all` on DGraph, which wipes everything. Restore tests need their own isolated DGraph instance. Run them against the scratch DGraph ports (`18081`) or in a separate test binary with `TestMain` that owns setup/teardown.

### PostgreSQL isolation strategy

Use `ent/enttest` backed by the test PostgreSQL instance. Truncate all tables in `TestMain` before the suite runs. Individual tests that create records should use `t.Cleanup` to delete them, or rely on the next suite-level truncation.

### Playwright isolation strategy

Tests share the same orbital instance and seeded DGraph. Tests that mutate data (e.g., edit a DC name) must restore original values in cleanup (`afterEach`) or use a value unlikely to conflict with other tests. The global `?fresh=1` URL parameter (which clears `localStorage` tab state) should be used at the start of any test that cares about tab state.

---

## Async Pipeline Testing

Export, backup, OCI publish, and restore are all async — triggered by an HTTP call, run in a goroutine, polled for completion.

**Pattern for integration tests:**

```go
// Trigger the job
resp := httpPost(t, "/api/v1/datacenters/"+dcID+"/export", nil)
require.Equal(t, 202, resp.StatusCode)
jobID := parseJobID(t, resp)

// Poll until complete
status := testutil.WaitForJob(t, db, jobID, 30*time.Second)
require.Equal(t, "completed", status)

// Assert the outcome
job := db.ExportJob.GetX(ctx, jobID)
require.NotNil(t, job.ArtifactPath)
assertValidExportZip(t, *job.ArtifactPath)
```

**Pattern for Playwright tests:**

```typescript
// Trigger via UI
await page.click('[data-testid="export-trigger-btn"]')

// Wait for the status cell to show "Completed"
await expect(page.locator('[data-testid="export-job-status"]').first())
  .toHaveText('Completed', { timeout: 30_000 })

// Assert the download link appeared
await expect(page.locator('[data-testid="export-download-btn"]').first()).toBeVisible()
```

---

## Playwright Conventions

- **`data-testid` attributes are required** on any element a test interacts with or asserts against. No structural CSS selectors (`table > tbody > tr:first-child`). See maintainability.md item 4.3 for the rollout plan.
- **One spec file per page/feature area** in `e2e/` (e.g., `e2e/backup.spec.ts`, `e2e/export.spec.ts`).
- **No fixed `sleep()` calls.** Use `waitFor`, `waitForResponse`, or `waitForSelector` with an explicit timeout.
- **Seed data is the source of truth.** Assert against values read from the page, not hardcoded constants. If the seed data changes, tests adapt automatically.
- **Auth state is global** — `e2e/global-setup.ts` logs in once and saves the session. All specs reuse this cookie. Do not log in per test.
- **Cross-browser:** Chromium only for now (internal admin tool). Add Firefox if users report rendering differences.

---

## CI Pipeline

Three jobs, in dependency order:

```
unit-tests (fast, no Docker)
    ↓
integration-tests (Docker Compose services)
    ↓
e2e-tests (full orbital stack + Playwright)
```

**`unit-tests`:** Runs on every push. No services needed. Fails fast.

**`integration-tests`:** Runs on every push. GitHub Actions `services:` block starts DGraph, PostgreSQL, MinIO, and the OCI registry. Runs `go test -tags integration ./...`. Caches Go modules.

**`e2e-tests`:** Runs on push to `main` and on PRs. Builds orbital, starts the full test Docker Compose stack, runs `make run-orbital` in the background, then runs `npx playwright test`. Uploads Playwright HTML report as an artifact on failure.

**`lint`:** Runs in parallel with `unit-tests`. `golangci-lint run ./...`.

---

## Actionable Steps

Work through these in order — each step unblocks the next. Sonnet/Opus annotation indicates which model should do the work.

---

### T.1 Design the DGraph client interface — **OPUS** (15–20 min design session)

This is the single most consequential design decision for testability. The interface shape determines whether integration tests can meaningfully verify behavior or just verify wire protocol. Decide between transport-level (pass DQL/GraphQL strings) vs. semantic-level (typed methods per operation) before Sonnet implements anything.

**Output:** A written interface definition committed to `internal/dgraph/client.go` as a documented `interface` type, ready for implementation.

**Prerequisite for:** T.4

---

### T.2 Test Docker Compose and Makefile targets — **SONNET**

Create `deploy/test/docker-compose.yml` with all six services (DGraph Zero + Alpha, PostgreSQL, Valkey, MinIO, OCI registry). Add `test-stack-up`, `test-stack-down`, `test-unit`, `test-integration`, `test-e2e`, and `test` targets to `Makefile`. Verify all services start healthy.

**Files:**
- `deploy/test/docker-compose.yml` (new)
- `Makefile` (update)

**Prerequisite for:** T.3, T.6, T.7, T.8

---

### T.3 Test helper package — **SONNET**

Create `internal/testutil/` with `ResetDGraph`, `SeedMinimal`, `NewTestDB`, and `WaitForJob`. Write a `TestMain` skeleton that other integration test packages can adopt.

**Files:**
- `internal/testutil/dgraph.go`
- `internal/testutil/db.go`
- `internal/testutil/jobs.go`

**Prerequisite for:** T.6, T.7

---

### T.4 DGraph client implementation — **SONNET** (after T.1)

Implement the interface from T.1 as a concrete `HTTPClient` with configurable timeout and a shared `*http.Client`. Wire it into all seven handler constructors that currently hold a raw `dgraphURL` field. Write `client_test.go` using `httptest.Server` to verify the HTTP layer.

**Files:** `internal/dgraph/client.go`, `internal/dgraph/client_test.go`, all handler files (see maintainability.md 2.1)

**Prerequisite for:** T.5 (DGraph tests), T.6

---

### T.5 Unit tests — pure logic — **SONNET**

Write table-driven unit tests for all pure functions. No Docker required.

| Test file | Functions to cover |
|-----------|-------------------|
| `internal/oci/publisher_test.go` | `RepoForDC`, `NextTag`, `extractZip`, `PublicKeyFingerprint` |
| `internal/handler/backup_test.go` | `writeZip`, checksum computation, `enforceRetention` (mock `blobStorage`) |
| `internal/handler/restore_test.go` | Checksum verification logic (after additional-findings.md A.4 is implemented) |
| `internal/handler/graphql_test.go` | `toFloat64` edge cases, `extractResourceIDs` |
| `internal/config/config_test.go` | Prod-safety validation checks |

---

### T.6 Integration tests — DGraph and export pipeline — **SONNET** (after T.1, T.2, T.3, T.4)

Write integration tests (build tag `integration`) for the DGraph client and the export pipeline.

**DGraph client tests (`internal/dgraph/client_test.go` — integration portion):**
- `GraphQL` round-trip: mutate a node, query it back, assert fields match
- `Alter`: apply schema, verify it was accepted
- `Query`: DQL query returns expected structure

**Export pipeline tests (`internal/handler/export_test.go`):**
- Seed one namespace + DC → trigger export → poll to completion → unzip artifact → assert `data.json.gz` and `schema.gz` present and non-empty
- Assert scratch DGraph is wiped at start of export (verify no stale data from previous run)
- Assert export fails cleanly when scratch DGraph is unreachable (timeout respected)

---

### T.7 Integration tests — async pipelines (backup, OCI, restore) — **SONNET** (after T.2, T.3)

These require the full test stack. Write with `WaitForJob` polling pattern.

**Backup tests (`internal/handler/backup_test.go` — integration portion):**
- Trigger backup → verify MinIO object exists at expected key → verify SHA-256 matches DB record
- Trigger identical backup → verify status is `skipped` (dedup logic)
- Trigger backup exceeding retention count → verify oldest MinIO object and DB record deleted

**OCI publish tests (`internal/oci/publisher_test.go` — integration portion):**
- Export → publish → verify manifest present in local OCI registry at expected tag
- Verify cosign signature verifiable with dev public key
- Verify `registry_artifacts` DB record shows `completed` with correct digest

**Restore tests (isolated — separate `TestMain` that owns its own DGraph):**
- Backup → restore → query DGraph → assert original namespace and DC are present
- Assert restore fails with checksum mismatch (after additional-findings.md A.4 is implemented)

---

### T.8 CI GitHub Actions workflow — **SONNET** (after T.5 has passing tests)

Create `.github/workflows/ci.yml` with four jobs: `lint`, `unit-tests`, `integration-tests`, `e2e-tests`. Use GitHub Actions `services:` for DGraph, PostgreSQL, MinIO, and OCI registry in the integration job. Upload Playwright HTML report on e2e failure.

**File:** `.github/workflows/ci.yml`

---

### T.9 Add `data-testid` attributes — **SONNET** (prerequisite for T.10)

See maintainability.md item 4.3. Do this page by page as Playwright specs are written for each page.

---

### T.10 Playwright test expansion — **SONNET** (after T.9 for each page)

One spec file per feature area. Use the async polling pattern for pipeline tests.

| Spec file | Coverage |
|-----------|---------|
| `e2e/servers.spec.ts` | Table load, drill-down to server tab, server edit |
| `e2e/inventory.spec.ts` | DataTable, type filter, session cache |
| `e2e/backup.spec.ts` | Trigger, poll to completion, download link, delete |
| `e2e/export.spec.ts` | Trigger, poll, download, publish flow |
| `e2e/restore.spec.ts` | Backup select, trigger, poll, log modal |
| `e2e/artifacts.spec.ts` | Table load, OCI connection test, public key display |
| `e2e/audit-log.spec.ts` | Table load, expandable child rows |
| `e2e/schema.spec.ts` | Version display, SDL content |

---

## Summary

| Step | Who | Unblocks |
|------|-----|---------|
| T.1 DGraph client interface design | **Opus** | T.4 |
| T.2 Test Docker Compose + Makefile | **Sonnet** | T.3, T.6, T.7, T.8 |
| T.3 Test helper package | **Sonnet** | T.6, T.7 |
| T.4 DGraph client implementation | **Sonnet** | T.5 (DGraph), T.6 |
| T.5 Unit tests — pure logic | **Sonnet** | T.8 (need passing tests first) |
| T.6 Integration tests — DGraph + export | **Sonnet** | — |
| T.7 Integration tests — backup, OCI, restore | **Sonnet** | — |
| T.8 CI workflow | **Sonnet** | — |
| T.9 data-testid attributes | **Sonnet** | T.10 (per page) |
| T.10 Playwright expansion | **Sonnet** | — |

**Only T.1 requires Opus.** Everything else is Sonnet implementation work once the interface is settled.
