# Spike 11: Authorization — Design Document

## Summary

Spike 11 adds role-based access control to orbital. The design uses Azure AD App Roles as the identity source, Echo middleware as the **primary enforcement layer**, and DGraph `@auth` directives as **defense-in-depth only**. Session-authenticated users (web UI) get roles from PostgreSQL; bearer-authenticated users (CLI/API) get roles from the JWT `roles` claim.

---

## Decision 1: Azure AD App Roles

**Decision:** Two roles for MVP: `orbital-admin` and `orbital-viewer`. No `orb` role in Azure AD — orb identity uses Ed25519 keys, not Azure AD tokens.

| Role | Azure AD App Role value | Grants |
|---|---|---|
| Admin | `orbital-admin` | All operations: read, mutate, backup, restore, export, publish, manage |
| Viewer | `orbital-viewer` | Read-only: queries, list endpoints, download artifacts |

**Azure AD manifest snippet** (applied by infra team, not by code):

```json
"appRoles": [
  {
    "allowedMemberTypes": ["User"],
    "displayName": "Orbital Admin",
    "description": "Full access to orbital configuration management",
    "isEnabled": true,
    "value": "orbital-admin",
    "id": "<generate-guid>"
  },
  {
    "allowedMemberTypes": ["User"],
    "displayName": "Orbital Viewer",
    "description": "Read-only access to orbital data",
    "isEnabled": true,
    "value": "orbital-viewer",
    "id": "<generate-guid>"
  }
]
```

**How roles reach orbital:**

- **Bearer tokens (CLI/API):** Roles appear in the JWT `roles` claim as `["orbital-admin"]`. Already extracted by `bearer.go` — `c.Set("roles", claims.Roles)`.
- **Session auth (web UI):** OIDC callback extracts roles from the ID token `roles` claim and stores in the session cookie. For local dev users (email/password), a `role` column on the PostgreSQL `users` table (default: `orbital-admin` for seeded admin user).

**Why not groups:** App Roles are explicit, auditable, and appear directly in the JWT. Azure AD group GUIDs require a Graph API call to resolve — adds latency and breaks in air-gap/offline scenarios.

---

## Decision 2: DGraph `@auth` Directives

**Decision:** Use a **static service token with `ClosedByDefault: true`** for defense-in-depth. Do NOT add per-role `@auth` directives to individual types for MVP.

**Rationale:**
1. Orbital proxies all GraphQL through Go handlers. Clients never reach DGraph directly. Echo middleware is the authoritative enforcement layer.
2. Per-role DGraph `@auth` requires orbital to mint or forward JWTs to DGraph on every request — unnecessary complexity.
3. Air-gap deployments (orb) won't have Azure AD. DGraph `@auth` tied to Azure AD JWTs would break edge scenarios.
4. Per-type `@auth` rules are hard to test locally and add schema maintenance overhead.

**What we will do (service token only):**

1. Add `ORBITAL_DGRAPH_AUTH_SECRET` env var to config (32+ char HMAC secret).
2. Orbital mints a **single static internal JWT** at startup with `{"ROLE": "orbital-admin"}`, signed with the secret.
3. All proxied DGraph requests include `X-Orbital-Auth: <token>` header.
4. Add `# Dgraph.Authorization {"Header":"X-Orbital-Auth","Namespace":"https://orbital.armada.ai/jwt/claims","Algo":"HS256","VerificationKey":"<secret>","ClosedByDefault":true}` to the schema.
5. All types get a minimal `@auth` rule requiring the `orbital-admin` role. Since orbital always sends the admin-level internal JWT, all operations pass — the point is to block direct DGraph access without the token.

**Per-role DGraph `@auth` enforcement is deferred** until there is a concrete reason to enforce at the DGraph layer. Echo middleware handles it.

---

## Decision 3: Echo Middleware Role Enforcement

**Decision:** Two new middleware functions in `internal/auth/roles.go`:

```go
RequireRole("orbital-admin")            // 403 if user lacks the role
RequireAnyRole("orbital-admin", "orbital-viewer")  // 403 if user has neither
```

Reads roles from `c.Get("roles")` — already set by `bearer.go` for API clients. Session middleware must also populate this from the session cookie.

**Route-to-role mapping:**

| Route | Method | Required role |
|---|---|---|
| `/graphql` (mutations) | POST | `orbital-admin` |
| `/graphql` (queries) | POST | `orbital-viewer` |
| `/api/v1/inventory` | GET | `orbital-viewer` |
| `/api/v1/datacenters/:id/export` | POST | `orbital-admin` |
| `/api/v1/export/jobs` | GET | `orbital-viewer` |
| `/api/v1/export/jobs/:jobId/publish` | POST | `orbital-admin` |
| `/api/v1/export/jobs/:jobId` | DELETE | `orbital-admin` |
| `/api/v1/backups` | POST | `orbital-admin` |
| `/api/v1/backups` | GET | `orbital-viewer` |
| `/api/v1/backups/:id` | DELETE | `orbital-admin` |
| `/api/v1/backups/test-connection` | POST | `orbital-admin` |
| `/api/v1/restore` | POST | `orbital-admin` |
| `/api/v1/restore` | GET | `orbital-viewer` |
| `/api/v1/events` | GET | `orbital-viewer` |
| Web UI pages | GET | `orbital-viewer` |
| Login/logout/callback, `/static/*` | * | none |

**GraphQL mutation gate:** The `/graphql` endpoint is shared. Inside `graphql.go` Handle, after `isMutation()` check: if mutation and user lacks `orbital-admin`, return 403. Queries pass for any authenticated user.

**Admin implies viewer:** Use `RequireAnyRole("orbital-admin", "orbital-viewer")` for read routes so admins don't need both roles assigned.

**Session role population (web UI):**

1. `oidc.go` callback: extract `roles` from ID token claims → store in session as `"roles"`.
2. `login.go` (local auth): read `role` from PostgreSQL `users` table → store in session.
3. Session middleware (`server.go`): read `roles` from session → `c.Set("roles", roles)`.
4. PostgreSQL: add `role` column to `users` ent schema (default `"orbital-admin"`).

---

## Decision 4: Offline JWT Integration Tests

**Approach:** Generate RSA key pair at test init. Serve a local JWKS endpoint via `httptest.Server`. Build `BearerVerifier` pointed at the test server.

**Test utilities** (`internal/auth/` — test-only files):
- `generateTestKeyPair()` — RSA 2048-bit
- `mintTestJWT(claims, key)` — signs JWT with test key
- `startTestJWKS(pubKey)` — `httptest.Server` serving `.well-known/openid-configuration` + `/jwks`

**Test cases:**

*Bearer verification:*
- Valid token, `orbital-admin` role → passes, roles on context
- Valid token, `orbital-viewer` role → passes, roles on context
- Valid token, no roles → passes auth, fails role middleware
- Expired token → 401
- Wrong signing key → 401
- No Authorization header → falls through to session check

*Role middleware:*
- Admin + admin-required route → 200
- Viewer + admin-required route → 403
- Admin + viewer-required route → 200
- No roles + any protected route → 403

*GraphQL mutation gate:*
- Mutation + admin role → proxied
- Mutation + viewer role → 403
- Query + viewer role → proxied

**Key detail:** Test JWKS server must serve a valid OpenID Connect discovery document at `/.well-known/openid-configuration` with `jwks_uri` pointing to itself — `go-oidc` performs discovery.

---

## Decision 5: Cross-Cutting Concerns

**Audit log:** Add user roles to audit event `details` JSON. Modify `writeEvent` to include `"roles": [...]` from context. Do not audit 403 rejections in the mutation log.

**Orb identity:** Orbs use Ed25519 keys, not Azure AD tokens. No changes to orb auth in this spike. Role middleware must not break future unauthenticated orb endpoints.

**Web UI role gating:** Pass `UserRole` to all template render calls. Use `{{if eq .UserRole "orbital-admin"}}` to hide mutation controls (edit buttons, backup/restore/export triggers) for viewers. Cosmetic only — middleware is the real gate.

---

## Implementation Plan

| Step | Scope | Notes |
|---|---|---|
| 1 | PostgreSQL role column + session role propagation | ent schema, login.go, oidc.go, session middleware |
| 2 | Role middleware + route wiring | `internal/auth/roles.go`, server.go groups, graphql.go mutation gate |
| 3 | DGraph service token defense-in-depth | config, startup JWT mint, schema header, minimal @auth |
| 4 | Offline JWT integration tests | httptest JWKS server, bearer + role + mutation gate tests |
| 5 | Audit log role enrichment + UI role gating | writeEvent, template UserRole |
| 6 | AKS validation | Azure AD app manifest (infra), smoke test admin/viewer tokens |

---

## Risks and Open Questions

1. **OIDC access token roles:** Azure AD puts App Roles in both ID tokens and access tokens — verify the access token (used by CLI) includes `roles`. If not, CLI flow needs adjustment. **Verify early in Step 1.**
2. **Local dev without OIDC:** When `ORBITAL_OIDC_CLIENT_SECRET` is unset, API auth is disabled (current behavior). Role enforcement on the API group is also skipped in this mode. Log a warning at startup.
3. **DGraph `@auth` schema compatibility:** Adding `# Dgraph.Authorization` header and `@auth` to types is a schema change. Must be applied to both blue and scratch DGraph instances. Malformed auth header makes DGraph reject all queries — test carefully.
4. **Role hierarchy:** Two flat roles for MVP. `RequireAnyRole` for reads is explicit and readable. Add hierarchy only if more roles are introduced.
5. **Two GraphQL endpoints (`/graphql` and `/api/v1/graphql`) should be consolidated.** Currently the UI calls `/graphql` (root group, session auth) and the CLI calls `/api/v1/graphql` (API group, bearer auth). This is an artifact of Echo route group design, not a principled decision. Best practice is a single endpoint with middleware that accepts either session cookie or bearer token — whichever the client presents. The UI should be migrated to `/api/v1/graphql` and the bare `/graphql` route removed. The auth middleware for the `/api/v1` group should try bearer first, fall back to session cookie, and reject if neither. **Consolidate as part of Step 2 (route wiring).**

---

## Settled Decisions (for CLAUDE.md / AUTH.md after implementation)

- Echo middleware is the primary authorization enforcement layer. DGraph `@auth` is defense-in-depth only.
- Two roles for MVP: `orbital-admin`, `orbital-viewer`. No hierarchy — use `RequireAnyRole` for read endpoints.
- Orb identity is not part of Azure AD roles. Orb authorization is Spike 16 scope.
- DGraph defense-in-depth uses a static service token (not per-user JWTs). `ClosedByDefault: true` blocks direct DGraph access. Per-role DGraph `@auth` deferred.
- Local dev without OIDC config skips role enforcement on API routes. Startup warning is logged.
