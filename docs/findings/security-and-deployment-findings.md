# Security and Deployment Findings

Findings from the May 2026 codebase audit covering authentication, session security, API exposure, deployment correctness, and operational gaps. Ordered by severity within each section. Cross-references to `docs/findings/maintainability.md` and `docs/findings/additional-findings.md` are noted where relevant.

---

## Critical — Fix Before Any Production Exposure

### S.1 GraphQL proxy is fully unauthenticated at `root` level ⚠️

**Problem:** `internal/server/server.go:249` mounts the GraphQL handler on the `root` group:

```go
root.Any("/graphql", gql.Handle)   // no auth
api.Any("/graphql", gql.Handle)    // bearer auth when OIDC configured
```

The same handler is mounted twice — once with no auth at all, once inside the `api` group. Because the root-level route matches first, an anonymous request to `/graphql` bypasses all authentication and reaches DGraph directly. This means the full GraphQL mutation surface — `deleteDataCenter`, `deleteServer`, `addServer`, `updateKubernetesCluster`, and any future types — is accessible to anyone on the network.

The original intent appears to be that GET requests to `/graphql` serve the GraphiQL browser UI. The handler should be split: serve the GraphiQL UI page only on `GET /graphql` (no auth or session-only), and route all POST mutations exclusively through the authenticated `api.Any("/graphql")` path.

**File:** `internal/server/server.go:249`
**Effort:** 30 min

---

### S.2 No Kubernetes liveness or readiness probes

**Problem:** `deploy/base/deploy.yaml` defines no `livenessProbe`, `readinessProbe`, or `startupProbe` on the orbital container. There are no `/health`, `/ready`, `/healthz`, or `/ping` endpoints in the server.

Consequences:
- Kubernetes considers the pod ready the instant the process starts — before the DB connection, DGraph connection, and schema version check have completed.
- If the application deadlocks or loses its database connection, Kubernetes continues routing traffic to it indefinitely.
- There is no automatic restart on application-level failure.

**Fix:** Add a minimal health endpoint that checks PostgreSQL connectivity (and optionally DGraph reachability):

```go
root.GET("/health", func(c echo.Context) error {
    if err := db.Ping(c.Request().Context()); err != nil {
        return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
    }
    return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
})
```

And add probes to the deployment manifest:

```yaml
livenessProbe:
  httpGet: { path: /health, port: 8001 }
  initialDelaySeconds: 10
  periodSeconds: 30
readinessProbe:
  httpGet: { path: /health, port: 8001 }
  initialDelaySeconds: 5
  periodSeconds: 10
```

**Files:** `internal/server/server.go`, `deploy/base/deploy.yaml`
**Effort:** 1 hr

---

## High — Fix Before MVP

### ~~S.3 CSRF not enforced on GraphQL mutations~~ — Not a real finding

**Closed 2026-06-09.** `SameSite: Lax` fully blocks cross-site CSRF on POST endpoints. The original finding claimed Lax "is not a reliable defence against CSRF via `fetch()` or `XMLHttpRequest`" — this is incorrect. `SameSite: Lax` cookies are not sent on cross-site subresource requests (fetch/XHR) regardless of `credentials: 'include'`; they are only sent on top-level same-site navigations. Since orbital's GraphQL endpoint only accepts POST, a CSRF attack from a malicious page cannot include the session cookie. No CSRF token on the GraphQL handler is needed.

---

### ~~S.4 Audit actor identity is forged by the client~~ — Fixed 2026-06-09

**Fixed:** `internal/handler/graphql.go` — removed `updatedBy` extraction from client-supplied variables entirely. `actor` is now always `actorFromContext(c)` (authenticated identity from session or bearer token). Client-supplied `updatedBy` is ignored.

---

### ~~S.5 Raw JWT ID token logged at INFO level~~ — Already gone

**Closed 2026-06-09.** The raw JWT log line does not exist in the current codebase. `internal/handler/oidc.go` logs only non-sensitive claims (email, name, preferred_username) after successful token verification. Finding was written against an older revision.

---

### ~~S.6 Session cookie missing `Secure` flag~~ — Fixed 2026-06-09

**Fixed:** `internal/auth/session.go` — added `Secure: !keys.Dev` to the session options. `SessionKeys.Dev` is set from `cfg.Dev` (`ORBITAL_DEV` env var, default `true`). Local dev keeps `Secure: false` (HTTP). All non-dev deployments (production, staging) get `Secure: true` — zero cost since Istio ingress terminates TLS and the browser URL is already HTTPS.

---

## Medium — Fix Before MVP

### S.7 No HTTP request body size limit

**Problem:** `io.ReadAll(c.Request().Body)` in `internal/handler/graphql.go:79` reads the entire body into memory with no limit. No Echo `BodyLimit` middleware is configured in `server.go`. A client can send an arbitrarily large request body to exhaust server memory.

**Fix:** Add Echo's built-in body limit middleware to the server setup, or add it to the specific routes that read bodies:

```go
// internal/server/server.go — add to e.Use() block
e.Use(middleware.BodyLimit("1MB"))
```

For the backup/restore/export triggers (which receive small JSON bodies), 1MB is generous. For the GraphQL proxy, a larger limit (e.g., 4MB) may be appropriate for bulk mutations.

**File:** `internal/server/server.go`
**Effort:** 15 min

---

### S.8 TOCTOU race in job creation

**Problem:** All three job trigger handlers use a check-then-create pattern that is not atomic:

1. Query for any existing `pending`/`running` job
2. (Race window)
3. Create new job

Two concurrent requests can both pass the check in step 1 and both proceed to step 3. For exports, this is dangerous: two concurrent export goroutines both manipulate scratch DGraph — wiping, loading, and exporting from the same instance simultaneously, corrupting each other's results.

**Affected handlers and lines:**
- Export: `handler/export.go:97-128`
- Backup: `handler/backup.go:274-309`
- Restore: `handler/restore.go:169-229`

**Fix options (in order of preference):**
1. Use a PostgreSQL advisory lock or row-level lock: wrap the check and create in a transaction using `FOR UPDATE` on the status query.
2. Use a unique partial index: `CREATE UNIQUE INDEX ON export_jobs (status) WHERE status IN ('pending', 'running')` — the DB rejects a second insert while one exists.
3. Use an in-process mutex (simpler, works for single-replica deployment — which matches the current deployment model): a `sync.Mutex` per job type in the handler struct, held for the duration of the check-and-create.

Option 3 is lowest effort and sufficient given orbital currently runs as a single replica.

**Files:** `internal/handler/export.go`, `internal/handler/backup.go`, `internal/handler/restore.go`
**Effort:** 1-2 hr

---

### S.9 Graceful shutdown orphans in-flight async jobs

**Problem:** `cmd/orbital/main.go:33` correctly catches SIGTERM/SIGINT and `server.go:280-296` drains HTTP requests with a 10-second timeout. However, background goroutines launched by `go h.runExport(job.ID)`, `go h.runBackup(job.ID)`, and `go h.runRestore(job.ID)` are not tracked or cancelled on shutdown.

When the process receives SIGTERM:
- HTTP server drains and exits normally.
- `main()` returns, terminating the process.
- Any in-progress goroutine is killed mid-operation.
- The job stays in `running` state in PostgreSQL forever (mitigated by maintainability.md item 1.3's startup reaper — but only after the next restart).
- **Worst case for restore:** if SIGTERM arrives between `drop_all` and the completion of `dgraph live`, DGraph is left wiped with no data.

**Fix:** Use a `sync.WaitGroup` to track active job goroutines. On shutdown, signal them via context cancellation and wait for them to finish before exiting. The goroutines already accept a context — passing a cancellable context rather than `context.Background()` (addressed in maintainability.md item 1.2) gives you the hooks needed here.

```go
// In server.go New(): create a WaitGroup
// In each trigger handler: wg.Add(1) before go runX(); defer wg.Done() inside goroutine
// In shutdown: cancel the shared context, then wg.Wait()
```

**Files:** `internal/server/server.go`, `internal/handler/export.go`, `internal/handler/backup.go`, `internal/handler/restore.go`
**Effort:** 2 hr — best done as part of maintainability.md item 1.2 (adding timeouts to goroutines), since both require threading a cancellable context through the same goroutines.

---

### S.10 Audit log missing key events

**Problem:** Several important security-relevant events are not recorded in the audit log:

| Missing event | File with no `writeAuditEvent` call |
|--------------|-------------------------------------|
| Login (success and failure) | `internal/handler/login.go` |
| OIDC login / first-time user provision | `internal/handler/oidc.go` |
| Logout | `internal/handler/login.go` |
| Restore trigger | `internal/handler/restore.go` |
| Backup deletion | `internal/handler/backup.go` (Delete handler) |
| Export job deletion | `internal/handler/oci.go` (DeleteJob) |

For a system that manages infrastructure configuration, the absence of login/logout events and restore events (which wipe and replace the entire graph database) from the audit trail is a significant compliance gap.

**Fix:** Add `writeAuditEvent` calls to each handler listed above.

**Effort:** 1-2 hr

---

### S.11 No uniqueness constraint on orb registration

**Problem:** `ent/schema/orb.go` defines no unique constraints on `datacenter_id` or `public_key`. Compare with `ent/schema/user.go:18` (`field.String("email")...Unique()`) and `ent/schema/namespace.go:20` (`field.String("name").Unique()`).

This means:
- The same orb can be registered multiple times for the same datacenter, creating multiple active keys.
- The same public key can be associated with multiple orb registrations.
- There is no DB-level enforcement of the 1:1 orb-to-datacenter relationship described in `CLAUDE.md`.

**Fix:** Add uniqueness to the ent schema:

```go
func (Orb) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("datacenter_id").Unique(),  // one active orb per DC
        index.Fields("public_key").Unique(),     // no duplicate keys
    }
}
```

**File:** `ent/schema/orb.go`; run `go generate ./ent`
**Effort:** 30 min

---

### S.12 No rate limiting on any endpoint

**Problem:** No rate limiting middleware is configured anywhere in `internal/server/server.go`. This affects:
- The unauthenticated GraphQL proxy (`root.Any("/graphql")`) — vulnerable to scraping and mutation flooding.
- The export and backup trigger endpoints — a client could trigger many concurrent jobs (mitigated by the serialization check, but adds unnecessary load).
- The login endpoint — vulnerable to credential stuffing.

Echo has a built-in rate limiter (`middleware.RateLimiter`). A simple in-memory per-IP rate limiter is sufficient for MVP:

```go
e.Use(middleware.RateLimiter(middleware.NewRateLimiterMemoryStore(20)))
```

For the login endpoint specifically, a tighter limit should be applied separately.

**File:** `internal/server/server.go`
**Effort:** 30 min

---

### S.13 RBAC infrastructure exists but is never enforced

**Problem:** `internal/auth/roles.go` defines `HasRole()` and `RolesFromContext()`. Bearer token validation (`internal/auth/bearer.go:73-78`) extracts the `roles` claim from Azure AD App Role assignments. But no handler or middleware ever calls `HasRole()`. Every authenticated user — regardless of their assigned roles — can access all operations including destructive ones (restore, backup delete, export delete).

This is the known Spike 8 gap. It is documented here for completeness because it is a live security gap in any staging/production deployment, not just a feature-in-progress. Until role enforcement is added, the auth system provides only authentication (who you are), not authorization (what you can do).

**Fix:** Spike 8 — requires an Opus design session first (see `docs/findings/maintainability.md`).

---

## Low — Address When Convenient

### S.14 OIDC nonce not used

Standard OIDC requires a `nonce` parameter to bind the ID token to the session and prevent token injection attacks. The `go-oidc` library supports this but it is not configured. The `state` parameter provides adequate CSRF protection for the flow itself; the nonce provides additional replay protection.

**File:** `internal/handler/oidc.go`
**Effort:** 30 min

---

### S.15 OIDC state comparison is not constant-time

`internal/handler/oidc.go:68` uses `storedState != c.QueryParam("state")` (regular string comparison) for the OIDC state check. The CSRF token validation in `session.go:162` correctly uses `subtle.ConstantTimeCompare`. Timing attacks on OIDC state are practically infeasible over HTTP, but the inconsistency is worth fixing for uniformity.

**File:** `internal/handler/oidc.go:68`
**Effort:** 5 min

---

### S.16 Bearer auth silently disabled when OIDC issuer is unreachable

`internal/server/server.go:83-84`: if `NewBearerVerifier()` fails (e.g., OIDC issuer unreachable at startup), the API auth middleware is skipped entirely with a warning log. The `api` group becomes unauthenticated. This is particularly relevant for air-gap deployments where the OIDC issuer may be unreachable.

**Recommended behavior:** Fail startup rather than silently degrading to unauthenticated mode. If OIDC is configured (`OIDCIssuerURL != ""`), a failure to reach it at startup should be fatal.

**File:** `internal/server/server.go:80-91`
**Effort:** 15 min

---

### S.17 One DGraph query uses string interpolation instead of parameterized variables

`internal/handler/export.go:451`:
```go
query := fmt.Sprintf(`{ getDataCenter(id: %q) { ... } }`, datacenterID)
```

`%q` provides Go-style quoting which prevents injection in practice, and DGraph validates the `ID!` type. However, this is the only DGraph call that uses string interpolation — all others use parameterized variables. It should be made consistent. If the DGraph client abstraction from maintainability.md item 2.1 is built first, this becomes natural to fix.

**File:** `internal/handler/export.go:451`
**Effort:** 15 min (fold into item 2.1)

---

### S.18 GET `/api/v1/export/jobs` mutates database

`internal/handler/export.go:162-181`: the list-jobs handler is a GET request that writes to PostgreSQL (marks completed jobs as `stale` when their artifact file is missing). This violates HTTP semantics — GET should be idempotent and safe. Caching proxies or overeager prefetch logic could trigger unexpected DB writes.

Additionally, the staleness check runs `os.Stat` for every completed job on every list request, and errors updating stale status are silently swallowed (`//nolint:errcheck`).

**Fix:** Move the stale-marking logic to a background job or to the job deletion handler. The list handler should only read.

**File:** `internal/handler/export.go:162-181`
**Effort:** 1 hr

---

## Deployment Notes

### D.1 Templates served from filesystem, not embedded

Templates and static assets are loaded at runtime from relative paths (`web/templates/`, `web/static/`, `schema/`). The Dockerfile (`Dockerfile:25-27`) copies these directories into `/app`, so this works correctly in the container as long as `WORKDIR /app` is used.

This is a known gap from Spike 14 (`//go:embed`). Until embedding is done, the binary cannot be run from an arbitrary working directory. Tests that start the server programmatically must either set the working directory to the repo root or mock the template layer.

**File:** `web/templates/templates.go`, `internal/server/server.go:119`

---

### D.2 Schema file named "demo" but is the only/production schema

~~`internal/config/config.go:27` defaults `ORBITAL_SCHEMA_PATH` to `schema/schema-demo.graphql`. There is no `schema/schema.graphql` — the demo schema is the production schema. The "demo" name is misleading for anyone new to the project and could cause operators to believe it is a non-production file.~~

**✅ Fixed 2026-06-07.** Renamed `schema/schema-demo.graphql` → `schema/schema.graphql`. Default in `config.go` updated to match. All references in `Makefile`, `scripts/seed-dgraph.sh`, tests, and docs updated.

---

## Summary Table

| ID | Finding | Severity | Effort |
|----|---------|----------|--------|
| S.1 | Unauthenticated GraphQL proxy on root group | Critical | 30 min |
| S.2 | No K8s liveness/readiness probes | Critical | 1 hr |
| ~~S.3~~ | ~~No CSRF on GraphQL mutations~~ | ~~High~~ | Not a real finding — SameSite: Lax blocks cross-site fetch/XHR |
| ~~S.4~~ | ~~Audit actor forged via client-supplied `updatedBy`~~ | ~~High~~ | ✅ Fixed 2026-06-09 |
| ~~S.5~~ | ~~Raw JWT logged at INFO level~~ | ~~High~~ | ✅ Already gone |
| ~~S.6~~ | ~~Session cookie missing `Secure` flag~~ | ~~High~~ | ✅ Fixed 2026-06-09 |
| S.7 | No HTTP body size limit (OOM risk) | Medium | 15 min |
| S.8 | TOCTOU race in job creation | Medium | 1-2 hr |
| S.9 | Graceful shutdown orphans async jobs | Medium | 2 hr |
| S.10 | Audit log missing login, OIDC, restore, delete events | Medium | 1-2 hr |
| S.11 | No uniqueness constraint on orb registration | Medium | 30 min |
| S.12 | No rate limiting on any endpoint | Medium | 30 min |
| S.13 | RBAC defined but never enforced | Medium | Spike 8 |
| S.14 | OIDC nonce not used | Low | 30 min |
| S.15 | OIDC state comparison not constant-time | Low | 5 min |
| S.16 | Bearer auth silently disabled on OIDC unreachable | Low | 15 min |
| S.17 | One DGraph query uses string interpolation | Low | 15 min |
| S.18 | GET handler mutates DB (stale job marking) | Low | 1 hr |
| D.1 | Templates loaded from filesystem (not embedded) | Note | Spike 14 |
| D.2 | Schema file named "demo" is the production schema | Note | 5 min |

### Recommended fix order (before any production/staging exposure)

1. **S.5** — Remove JWT log line (5 min, no risk)
2. **S.4** — Fix audit actor identity (15 min, no risk)
3. **S.6** — Add `Secure` cookie flag (15 min)
4. **S.7** — Add body size limit (15 min)
5. **S.12** — Add rate limiting (30 min)
6. **S.1** — Fix unauthenticated GraphQL root route (30 min)
7. **S.2** — Add health endpoint + K8s probes (1 hr)
8. **S.10** — Add missing audit events (1-2 hr)
9. **S.3** — CSRF on GraphQL (1-2 hr, after S.1 is fixed)
10. **S.8 + S.9** — TOCTOU race + shutdown safety (do together, 3-4 hr)
11. **S.11** — Orb uniqueness constraint (30 min)
12. **S.16** — Fail fast on OIDC unreachable (15 min)
