# Testing Strategy

Orbital's testing strategy is grounded in three principles: test behavior not implementation, use real services in the integration layer, and make tests honest about what they actually verify.

**Current state:** ~36 Playwright E2E tests passing across 5 spec files (+ 1 smoke spec). 74 Go test functions across 16 test files. Integration tests cover all async pipelines (export, backup, OCI publish, restore), the login handler, OIDC callback flow, audit event write-through, and export JSON API endpoints. Unit tests cover session management, CSRF, bearer token validation, GraphQL proxy logic (isMutation, MVCC, audit suppression), config validation, OCI naming, JWT claims parsing, and GraphQL orbId extraction. E2E workflow tests exercise export and backup end-to-end through the UI.

**Last reviewed:** 2026-05-19 (post T.15--T.17; previous score 6.5/10).

---

## Test Pyramid

```
         +-------------------+
         |  E2E (Playwright) |  Full stack, browser automation, UI behavior
         +-------------------+
       +-----------------------+
       |   Integration tests   |  Real DGraph, PostgreSQL, MinIO, OCI registry
       +-----------------------+
     +---------------------------+
     |       Unit tests          |  Pure logic only, no external dependencies
     +---------------------------+
```

### Layer 1 -- Unit tests

Pure functions with no external dependencies. Fast (< 1s total). Run on every save.

**What belongs here:**
- OCI publisher: `RepoForDC`, `NextTag`, `extractZip`, `PublicKeyFingerprint`
- Backup: `writeZip`, checksum computation, `enforceRetention` (with mock `blobStorage` -- the interface already exists at `handler/backup.go:33`)
- GraphQL proxy: `toFloat64` edge cases, `extractResourceIDs` (the three-source orbId logic)
- Config: prod-safety validation (HMAC key, OIDC issuer, OCI registry defaults)
- Any pure transformation or validation function

**What does NOT belong here:** anything that touches DGraph, PostgreSQL, S3, or the filesystem. Those go in integration tests.

**Run with:** `make test-unit`

---

### Layer 2 -- Integration tests

Test against real services running in Docker. Slower (seconds per test) but honest -- no mocked DGraph responses, no fake SQL, no stubbed HTTP servers for external services.

**What belongs here:**
- Export pipeline end-to-end: seed namespace + DC -> trigger export -> verify zip contents and artifact path
- Backup pipeline: trigger backup -> verify MinIO object + PostgreSQL record + checksum
- OCI publish: trigger export -> publish -> verify manifest in local OCI registry + cosign signature verifiable with the dev key
- Restore pipeline: backup -> restore -> verify DGraph subgraph matches original
- API endpoint tests: HTTP-level tests against live orbital handlers with real backing services

**Run with:** `make test-integration`

---

### Layer 3 -- E2E (Playwright)

Browser automation against a fully running orbital. Tests what the user actually experiences.

**Philosophy:** test behavior, not implementation. A test should describe what a user does and what they see -- not whether a specific JS function was called.

**What belongs here:**
- All page-level user flows: navigate, interact, verify visible outcome
- Async pipeline flows triggered from the UI: click "Run Backup", wait for status to show "Completed", verify the new row appears in the backup table
- Cross-page flows: create DC -> drill down -> verify content in tab
- Error states: trigger a failure, verify the error message is shown

**What does NOT belong here:** anything that can be verified at the integration layer. E2E tests are for the full user experience, not for catching business logic bugs.

**Run with:** `make test-e2e` (requires orbital running on `:8001`)

---

## Test Infrastructure

### Docker Compose for tests

Test services are merged into the main `deploy/local/docker-compose.yml` using dedicated test ports. No separate compose file -- the test stack starts alongside dev services.

| Service | Test port | Dev port | Notes |
|---------|-----------|----------|-------|
| DGraph Zero | 15080, 16080 | 5080, 6080 | |
| DGraph Alpha | 18080, 19080 | 8080, 9080 | |
| PostgreSQL | 5433 | 5432 | DB name: `orbital_test` |
| MinIO | 9100, 9101 | -- | S3-compatible; bucket: `orbital-test` |
| OCI Registry (Zot) | 5100 | -- | No auth; ephemeral storage |

**Makefile targets:**
```makefile
test-stack-up:
    docker compose -f deploy/local/docker-compose.yml up -d --wait

test-stack-down:
    docker compose -f deploy/local/docker-compose.yml down -v

test-unit:
    go test -short ./...

test-integration: test-stack-up
    go test -tags integration ./...

test-e2e:
    npx playwright test

test: test-unit test-integration test-e2e
```

### Test helper package

`internal/testutil/` provides shared setup/teardown helpers:

| File | Helpers |
|------|---------|
| `dgraph.go` | `ResetDGraph` (drop-all + schema apply), `SeedMinimal` (one Namespace + DC) |
| `db.go` | `NewTestDB` (ent client to test PostgreSQL, tables truncated via `t.Cleanup`) |
| `jobs.go` | `WaitForJob` (poll job status until terminal state or timeout) |
| `storage.go` | MinIO/S3 test helpers |

---

## Test Isolation

### DGraph isolation strategy

One shared DGraph instance per test run. Drop-all + schema-apply + minimal seed at the start of the integration test suite (`TestMain`). Individual tests within the suite share state -- they must not depend on each other's mutations or clean up after themselves.

**Exception -- restore tests:** Restore performs `drop_all` on DGraph, which wipes everything. Restore tests need their own isolated DGraph instance or must run last in the suite.

### PostgreSQL isolation strategy

Use `ent/enttest` backed by the test PostgreSQL instance. Truncate all tables in `TestMain` before the suite runs. Individual tests that create records should use `t.Cleanup` to delete them, or rely on the next suite-level truncation.

### Playwright isolation strategy

Tests share the same orbital instance and seeded DGraph. Tests that mutate data (e.g., edit a DC name) must restore original values in cleanup (`afterEach`) or use a value unlikely to conflict with other tests. The global `?fresh=1` URL parameter (which clears `localStorage` tab state) should be used at the start of any test that cares about tab state.

---

## Async Pipeline Testing

Export, backup, OCI publish, and restore are all async -- triggered by an HTTP call, run in a goroutine, polled for completion.

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

- **`data-testid` attributes are required** on any element a test interacts with or asserts against. No structural CSS selectors (`table > tbody > tr:first-child`).
- **One spec file per page/feature area** in `e2e/` (e.g., `e2e/backups.spec.ts`, `e2e/export.spec.ts`).
- **No fixed `sleep()` calls.** Use `waitFor`, `waitForResponse`, or `waitForSelector` with an explicit timeout.
- **Seed data is the source of truth.** Assert against values read from the page, not hardcoded constants. If the seed data changes, tests adapt automatically.
- **Auth state is global** -- `e2e/global-setup.ts` logs in once and saves the session. All specs reuse this cookie. Do not log in per test.
- **Cross-browser:** Chromium only for now (internal admin tool). Add Firefox if users report rendering differences.

---

## CI Pipeline

Deprioritized for now (solo dev, prototyping phase). When ready, three jobs in dependency order:

```
unit-tests (fast, no Docker)
    |
integration-tests (Docker Compose services)
    |
e2e-tests (full orbital stack + Playwright)
```

**`unit-tests`:** Runs on every push. No services needed. Fails fast.

**`integration-tests`:** Runs on every push. GitHub Actions `services:` block starts DGraph, PostgreSQL, MinIO, and the OCI registry. Runs `go test -tags integration ./...`. Caches Go modules.

**`e2e-tests`:** Runs on push to `main` and on PRs. Builds orbital, starts the full test Docker Compose stack, runs `make run-orbital` in the background, then runs `npx playwright test`. Uploads Playwright HTML report as an artifact on failure.

**`lint`:** Runs in parallel with `unit-tests`. `golangci-lint run ./...`.

---

## Coverage Map

### Well covered

| Area | Test files | Notes |
|------|-----------|-------|
| Backup pipeline | `handler/backup_integration_test.go` | Trigger, MinIO verify, checksum, retention |
| Export pipeline | `handler/export_integration_test.go`, `handler/export_test.go` | Full end-to-end with DGraph seed |
| OCI publish pipeline | `handler/oci_integration_test.go`, `oci/publisher_test.go` | Publish, verify manifest, cosign signature |
| Restore pipeline | `handler/restore_integration_test.go` | Validation, graceful K8s failure mode |
| GraphQL orbId extraction | `handler/graphql_test.go` | Three-source extraction logic |
| Config validation | `config/config_test.go` | Prod-safety checks |
| OCI naming | `oci/publisher_test.go` | `RepoForDC`, `NextTag` |
| JWT claims parsing | `orbauth/auth_test.go` | Bearer token validation |

### Newly covered (T.11--T.17)

| Area | Test files | Notes |
|------|-----------|-------|
| Session management, CSRF | `auth/session_test.go` (10 tests) | Full roundtrip: set/get/clear session, CSRF create/validate/idempotent, OIDC state set/get/clear |
| Bearer token validation | `auth/bearer_test.go` (6 tests) | Local OIDC httptest server with RSA key pair; valid/expired/wrong-audience/UPN-fallback |
| Login handler | `handler/login_test.go` (7 integration tests) | CSRF failure, wrong password, unknown email, SSO account, success + HX-Redirect, logout valid/invalid CSRF |
| GraphQL proxy handler | `handler/graphql_handler_test.go` (9 tests) | isMutation, hasGQLErrors, extractOperations (pure), proxy passthrough, mutation proxy, ifVersion stripping, MVCC conflict/match, GQL error audit suppression |
| OIDC callback handler | `handler/oidc_test.go` (6 integration tests) | Local httptest OIDC provider with RSA key, token endpoint, JWKS; login redirect, state validation (missing/wrong), new user provisioning, existing user reuse, empty email rejection |
| Audit event write-through | `handler/graphql_event_test.go` (2 integration tests) | Mutation writes audit event with correct fields (operations, resourceTypes, resourceIds); GQL errors suppress audit event |
| Export JSON API | `handler/export_api_test.go` (5 integration tests) | List empty/populated, Status happy path, invalid UUID 400, unknown UUID 404 |
| E2E export workflow | `e2e/export.spec.ts` | Trigger export, wait for 202, wait for completion, verify job row + download button |
| E2E backup workflow | `e2e/backups.spec.ts` | Conditional skip if S3 unconfigured, trigger backup, wait for terminal state, verify status + download |

### Not covered

| File | Lines | Risk | Notes |
|------|-------|------|-------|
| `handler/datacenter.go` | ~279 | Medium | CRUD handlers for data center config items. Partially covered by E2E datacenter tests but no handler-level tests. |
| `handler/server.go` | ~346 | Medium | Server CRUD handlers. Partially covered by E2E. |
| `handler/event.go` (List, diff rendering) | ~398 | Medium | `writeAuditEvent` now tested via `graphql_event_test.go`. Remaining: `List` handler (pagination, filtering, HTMX fragment rendering), `buildDiffHTML`, `buildVarSummary`, `lineDiff`. |
| `handler/export.go` (Trigger, Download, runExport) | ~807 | Medium | `List` and `Status` now covered by `export_api_test.go`. Remaining: `Trigger` (conflict detection, audit write), `Download`, `runExport` async pipeline (covered separately by `export_integration_test.go`). |
| `handler/inventory.go` | ~127 | Medium | Inventory/discovery handlers. |
| `server/server.go` | ~296 | Medium | Echo server setup, middleware wiring, route registration. |
| `handler/graphql.go` (remaining) | ~389 | Low-medium | `fetchBeforeByOrbID` and `toFloat64` edge cases remain. Core proxy, audit write-through, and MVCC are now covered. |
| `handler/ui.go` | -- | Low | Template rendering, covered indirectly by E2E. |
| `internal/graph/` | -- | Low | DGraph client wrappers, covered indirectly by integration tests. |

---

## Known Issues

1. **Test isolation is fragile.** All integration tests share `testDB` and `testDcID` with no per-test cleanup. Works due to implicit ordering but will break if tests are parallelized or reordered. Acceptable for now; revisit if flaky tests appear.

2. **`time.Sleep(2s)` after DGraph schema apply.** Used in `testutil/dgraph.go` as a heuristic wait for schema propagation. A DGraph readiness check (query a known type, retry on failure) would be more reliable. Low priority -- has not caused flakes yet.

3. ~~**`TestTempCleanup` is a no-op.**~~ Fixed in T.11 -- deleted.

4. ~~**`stringSliceEqual` reimplements stdlib.**~~ Fixed in T.11 -- replaced with `slices.Equal`.

5. ~~**Hardcoded rack count in E2E.**~~ Fixed in T.11 -- reads count from page dynamically.

---

## Completed Steps (T.1 -- T.10)

| Step | Status | Notes |
|------|--------|-------|
| T.1 DGraph client interface design | **Reclassified as tech debt** | Not a testing blocker. Integration tests proved HTTP-level handler testing works without a typed DGraph client interface. |
| T.2 Test Docker Compose + Makefile | **Done** | Test stack merged into `deploy/local/docker-compose.yml`. All Makefile targets work. |
| T.3 Test helper package | **Done** | `internal/testutil/` with `dgraph.go`, `db.go`, `jobs.go`, `storage.go`. |
| T.4 DGraph client implementation | **Reclassified as tech debt** | Not needed for testing. Handlers tested at HTTP level. |
| T.5 Unit tests | **Done** | `oci/publisher_test.go`, `handler/graphql_test.go`, `handler/export_test.go`, `config/config_test.go`, `orbauth/auth_test.go`. |
| T.6 Integration tests (DGraph + export) | **Done** | `handler/export_integration_test.go`, `handler/graphql_test.go`. |
| T.7 Integration tests (backup, OCI, restore) | **Done** | `handler/backup_integration_test.go`, `handler/oci_integration_test.go`, `handler/restore_integration_test.go`. |
| T.8 CI GitHub Actions | **Deprioritized** | Solo dev, prototyping phase. Revisit before MVP. |
| T.9 data-testid attributes | **Done** | Added to 5 page templates. |
| T.10 Playwright expansion | **Done** | `navigation.spec.ts`, `backups.spec.ts`, `export.spec.ts`, `restore.spec.ts`. All 34 tests passing. |

---

## Completed Steps (T.11 -- T.17)

These steps have been completed. Detailed definitions are preserved for historical reference.

---

### T.11 -- Fix test quality issues (Sonnet, ~1 session)

Clean up structural issues found in the Opus review. Small, self-contained fixes.

**Tasks:**
- Delete `TestTempCleanup` from `handler/export_test.go` -- it tests a stdlib guarantee (`t.TempDir()` cleanup) and verifies nothing about orbital.
- Replace `stringSliceEqual` with `slices.Equal` in `handler/graphql_test.go` -- remove the custom helper, import `slices`, use the stdlib function.
- Fix `toHaveCount(4)` hardcoded rack count in `e2e/datacenter.spec.ts` -- read the count from the page dynamically, matching the pattern used for server counts in the same file.

**Files:** `internal/handler/export_test.go`, `internal/handler/graphql_test.go`, `e2e/datacenter.spec.ts`

---

### T.12 -- Auth handler tests (Sonnet, ~1 session) -- prerequisite for Spike 11

The auth layer has zero test coverage. Spike 11 (Authorization) is the next priority and builds directly on top of `login.go`, `oidc.go`, and `internal/auth/`. These must be tested before Spike 11 implementation begins.

**`handler/login.go` tests:**
- CSRF validation failure returns 403
- Wrong password returns 401 with correct error message
- Account flagged for SSO returns redirect to OIDC flow
- Successful login creates session cookie with expected claims

**`handler/oidc.go` tests:**
- Callback with valid authorization code exchanges for tokens and creates session
- Invalid state parameter returns 400
- Missing ID token in provider response returns 500
- Missing email claim in ID token returns appropriate error

**`internal/auth/` tests:**
- CSRF generation + validation roundtrip (generate token, validate it, validate a tampered one fails)
- Session set/get/clear lifecycle (set session values, read them back, clear, verify gone)
- Bearer token validation (valid token, expired token, wrong signing key, missing claims)

**Approach:** Use `httptest.Server` for the OIDC provider mock. Use `net/http/httptest` for handler-level tests. No real OIDC provider needed.

**Files:** `internal/handler/login_test.go` (new), `internal/handler/oidc_test.go` (new), `internal/auth/*_test.go` (new)

**Prerequisite for:** Spike 11 (App Roles)

---

### T.13 -- GraphQL proxy handler tests (Sonnet, ~1 session)

The GraphQL proxy (`handler/graphql.go`) is the most critical untested code in orbital. Every query and mutation flows through it. The proxy logic, rate limiting, caching, audit log integration, and error translation are all untested. A regression here is a total system outage.

**Approach:** Use `httptest.Server` as a mock DGraph backend. Tests verify the handler's behavior at the HTTP level without needing a real DGraph instance.

**Tests to write:**
- **Proxy passthrough:** query is forwarded verbatim to DGraph, response is returned to client unchanged
- **Mutation audit trail:** mutation is proxied, audit event is written to PostgreSQL with correct actor, resource, and orbId
- **Rate limiting:** requests exceeding threshold return 429
- **Error translation:** DGraph 4xx/5xx responses are translated to correct HTTP status for the client
- **`isMutation` classification:** table-driven tests for query vs mutation detection (queries, mutations, fragments, whitespace variations)

**Files:** `internal/handler/graphql_proxy_test.go` (new)

---

### T.14 -- E2E workflow tests (Sonnet, ~1 session)

The current E2E specs for backups, restore, and export are structure smoke tests -- they verify page elements exist but never trigger an actual workflow. The datacenter-edit spec proves the right pattern: trigger action, wait for async completion, verify UI state. Extend this pattern to operational workflows.

**Keep existing structure tests as baseline.** Add workflow tests on top, do not replace them.

**`e2e/backups.spec.ts` -- add workflow test:**
- Trigger backup via the UI button
- Wait for "Completed" status to appear in the job table: `await expect(page.getByTestId('backup-job-status').first()).toHaveText('Completed', { timeout: 30_000 })`
- Verify download link appears for the completed backup

**`e2e/export.spec.ts` -- add workflow test:**
- Select a data center from the dropdown
- Trigger export via the UI button
- Wait for "Completed" status in the job table
- Verify download link appears for the completed export

**Prerequisites:**
- Add `data-testid` attributes on job status cells and action buttons (add as part of this task)
- Ensure test DGraph has seeded data sufficient for a real export

**Files:** `e2e/backups.spec.ts`, `e2e/export.spec.ts`, relevant `.gohtml` templates (for new `data-testid` attributes)

---

## Summary

| Step | Status | Who | Priority |
|------|--------|-----|----------|
| T.1 DGraph client interface | Reclassified (tech debt) | -- | -- |
| T.2 Test Docker Compose + Makefile | Done | -- | -- |
| T.3 Test helper package | Done | -- | -- |
| T.4 DGraph client implementation | Reclassified (tech debt) | -- | -- |
| T.5 Unit tests | Done | -- | -- |
| T.6 Integration tests (DGraph + export) | Done | -- | -- |
| T.7 Integration tests (backup, OCI, restore) | Done | -- | -- |
| T.8 CI GitHub Actions | Deprioritized | Sonnet | Revisit pre-MVP |
| T.9 data-testid attributes | Done | -- | -- |
| T.10 Playwright expansion | Done | -- | -- |
| T.11 Fix test quality issues | Done | Sonnet | Deleted no-op test, replaced custom helper with stdlib, fixed hardcoded count |
| T.12 Auth handler tests | Done | Sonnet | 10 session tests, 6 bearer tests, 7 login integration tests |
| T.13 GraphQL proxy handler tests | Done | Sonnet | 3 pure + 6 handler tests with mock DGraph |
| T.14 E2E workflow tests | Done | Sonnet | Export + backup workflow tests with data-testid attributes |
| T.15 OIDC callback handler tests | Done | Sonnet | 6 integration tests with local httptest OIDC provider |
| Makefile fix (test-integration seed) | Done | Sonnet | Runs `bash scripts/seed.sh` after integration tests to restore DGraph for E2E |
| T.16 Audit event write-through test | Done | Sonnet | 2 integration tests: mutation writes event, GQL error suppresses event |
| T.17 Export JSON API endpoint tests | Done | Sonnet | 5 integration tests: List empty/populated, Status happy/400/404 |

---

## Evaluation Report (2026-05-19, post T.15--T.17)

### Overall score: 7.5 / 10

Previous score was 6.5. The 1-point improvement reflects closing the three highest-priority gaps from the last evaluation: OIDC callback (was the #1 risk), audit event write-through (#2), and export API endpoints. The Makefile fix for post-integration seeding also improves day-to-day reliability of the test workflow.

### What improved since last evaluation

**T.15 -- OIDC callback handler tests (+0.5)**
This was the single highest-risk gap. `handler/oidc_test.go` adds 6 integration tests using a well-engineered local OIDC provider: `newOIDCProvider` spins up an httptest server with `/.well-known/openid-configuration`, `/jwks` (RSA public key), and `/token` (returns a signed JWT). The `TokenClaims` map is mutable per-test, allowing each test to configure different email/name claims before calling `Callback`. Tests cover: login redirect to IdP with state in session cookie, missing session state returns `error=invalid_state`, wrong state returns `error=invalid_state`, new user is provisioned in PostgreSQL (verified by querying ent), existing user is reused without creating duplicates, and empty email claim returns `error=no_email`. Both user provisioning tests use `t.Cleanup` to delete test users. This is the complete happy and error path for OIDC -- the only untested branch is the `no_id_token` redirect (when `token.Extra("id_token")` is not a string), which is a minor edge case.

**Makefile fix -- test-integration seed (+0.1)**
`test-integration` now runs `bash scripts/seed.sh` after the integration test run. This restores DGraph seed data that integration tests may have mutated, preventing false failures when E2E tests run next. Small but fixes a real day-to-day friction point.

**T.16 -- Audit event write-through test (+0.3)**
`handler/graphql_event_test.go` adds 2 integration tests. `TestGraphQL_MutationWritesAuditEvent` sends a mutation through the GraphQL `Handle` method with a mock DGraph backend (returning a successful response) and a real PostgreSQL test DB. It uses a polling helper (`pollForEvent`) to wait up to 5 seconds for the goroutine-spawned audit event to appear, then asserts `operations`, `resourceTypes`, and `resourceIds` fields. `TestGraphQL_GQLErrorsSuppressAuditEvent` verifies the negative case: GQL errors in the DGraph response result in no audit event after a 300ms wait. This closes the gap where the audit write-through path was only tested for suppression but never for actual writes. The polling pattern is the right approach for testing goroutine-based side effects.

**T.17 -- Export JSON API endpoint tests (+0.1)**
`handler/export_api_test.go` adds 5 integration tests for the `Export.List` and `Export.Status` JSON API handlers. Tests verify: empty DB returns `[]` (not null), populated DB returns jobs with required fields (`jobId`, `dataCenter`, `status`, `createdAt`), Status returns correct fields for a known job, invalid UUID returns 400, and unknown UUID returns 404. The `newExportListHandler` helper passes empty strings for DGraph URLs (since List/Status don't use them) and `t.TempDir()` for export directories. Tests properly clean up created ExportJob records. Note: these test the JSON API layer only -- the actual export pipeline (Trigger + runExport) was already covered by `export_integration_test.go`.

### Remaining gaps (ordered by risk/criticality)

1. **`handler/datacenter.go` (~279 lines) -- Medium risk.** CRUD handlers for data center config items. Partially covered by E2E datacenter tests, but no handler-level tests exercise validation, error responses, or edge cases directly.

2. **`handler/server.go` (~346 lines) -- Medium risk.** Server CRUD handlers. Same situation as datacenter.go.

3. **`handler/event.go` (List, diff rendering) -- Medium risk.** `writeAuditEvent` is now tested. Remaining: the `List` handler (pagination, filtering by orbId/resource_type, HTMX fragment rendering), `buildDiffHTML`, `buildVarSummary`, and `lineDiff`. These are ~250 lines of presentation logic. Lower risk because audit writes (the compliance-critical path) are covered.

4. **`handler/export.go` (Trigger, Download) -- Medium risk.** `List` and `Status` are now tested. `Trigger` (conflict detection with existing running jobs, audit event write) and `Download` (file streaming, non-completed job returns 404) have no focused handler tests. The full async pipeline is covered by `export_integration_test.go`.

5. **`handler/inventory.go` (~127 lines) -- Medium risk.** Inventory/discovery handlers.

6. **`server/server.go` (~296 lines) -- Medium risk.** Echo server setup, middleware wiring, route registration.

7. **`graphql.go` remaining paths -- Low risk.** `fetchBeforeByOrbID` and `toFloat64` edge cases. Core proxy, MVCC, and audit write-through are all now covered.

8. **CI pipeline (T.8) -- Low risk for now, high risk pre-MVP.** No automated test runs on push.

### Updated score breakdown

| Category | Score | Notes |
|----------|-------|-------|
| Unit test quality | 7 / 10 | Unchanged. Session, CSRF, bearer, isMutation, hasGQLErrors, extractOperations, extractResourceIDs, config validation, OCI naming all well-tested. The local OIDC server with RSA keys (reused across bearer_test.go and oidc_test.go) is excellent infrastructure. No new pure-function tests added in this round. |
| Integration test coverage | 7.5 / 10 | Up from 6.5. All async pipelines covered. Full auth path now covered (login + OIDC callback). Audit write-through tested with real PostgreSQL and polling. Export JSON API (List, Status) tested. The two biggest remaining gaps are datacenter/server CRUD handlers and the export Trigger/Download endpoints. |
| E2E test coverage | 6 / 10 | Unchanged. ~36 tests across 5 specs. Workflow tests for export and backup are solid. No new E2E tests in this round. |
| Test isolation / reliability | 7 / 10 | Up from 6.5. The Makefile fix (seed after integration tests) prevents cross-suite contamination. OIDC tests use t.Cleanup for user deletion. The `pollForEvent` helper is a clean pattern for goroutine-based side effects. The `time.Sleep(2s)` after DGraph schema apply remains the main reliability concern. |
| **Overall** | **7.5 / 10** | Up from 6.5. The three highest-priority gaps from the previous evaluation (OIDC callback, audit write-through, export API) are all closed. Auth is now comprehensively tested from login through OIDC to session management. The remaining gaps are medium-risk CRUD handlers and presentation logic. |

### Next priorities (T.18+)

---

### T.18 -- DataCenter and Server handler tests (Sonnet, ~1 session)

CRUD handlers for the two most-used config item types. Partially covered by E2E but no focused handler-level tests.

**Tests to write (datacenter.go):**
- Create data center with valid input returns 201
- Create with missing required fields returns 400
- Update existing data center returns 200
- Delete data center returns 200
- Get non-existent data center returns 404

**Tests to write (server.go):**
- Similar CRUD coverage

**Approach:** Integration tests with real test DGraph + PostgreSQL, using `testutil` helpers.

**Files:** `internal/handler/datacenter_test.go` (new), `internal/handler/server_test.go` (new)

---

### T.19 -- Export Trigger/Download + Event List handler tests (Sonnet, ~1 session)

Fill in the remaining export and event handler coverage. `Trigger` has conflict detection logic (409 when a job is already running) and audit event writes. `Download` streams the artifact file and returns 404 for non-completed jobs. `Event.List` has pagination, filtering, and HTMX fragment rendering.

**Tests to write (export.go):**
- Trigger when no job is running creates job and returns 202
- Trigger when a job is already running returns 409
- Trigger when a restore is running returns 409
- Download for completed job with artifact file returns the zip
- Download for non-completed job returns 404

**Tests to write (event.go):**
- List with default pagination returns events ordered by timestamp desc
- List with orbId filter returns matching events only
- List with resource_type filter returns matching events only
- List with limit/offset returns correct page

**Files:** `internal/handler/export_trigger_test.go` (new), `internal/handler/event_list_test.go` (new)

---

### T.20 -- CI pipeline (Sonnet, ~1 session) -- pre-MVP blocker

Set up GitHub Actions with three jobs: unit tests (no Docker), integration tests (Docker Compose services), E2E tests (full stack + Playwright). This is already designed in the CI Pipeline section above -- just needs implementation.

**Files:** `.github/workflows/test.yml` (new)
