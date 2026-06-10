# Auth Reference

Read this before: OIDC flow changes, CLI login, session handling, bearer token validation, authz work.

## Design framing — requirements vs. opinions

User identity and per-user audit are hard requirements for orbital. That requirement *forces* one thing only: tokens must carry a signed assertion of user identity. Everything else in this doc is a chosen opinion with named alternatives.

| Decision | Status | Alternatives we did not pick |
|---|---|---|
| Tokens must carry signed user identity | **Forced** by audit requirement | None |
| OIDC as the token format | **Defensible default**, not forced | SAML assertions; mTLS + signed user-context header; custom IdP |
| Azure AD as the IdP | **Operational choice**, not in code | Okta, Auth0, Keycloak — orbital's bearer middleware is IdP-agnostic; swap is config-only |
| JIT user provisioning on first login | **Opinion** | Pre-provisioned only (admin creates row first); directory sync from AD groups via SCIM/Graph |
| Local `users.role` column as authz source of truth | **Opinion** (forced shape, not existence) | Azure AD App Roles in JWT claims (requires AD admin permissions we lack); external policy engine (OPA, Cedar, SpiceDB); AD group lookup at login |
| Three-role model (readonly/dev/admin) | **Opinion** | Finer-grained RBAC matrix; ABAC (per-namespace, per-resource); two-role read/write |
| Orbital is the authoritative authz decision point | **Opinion** | External policy-as-code engine consulted per request — gains policy provenance and what-if simulation, costs an external dependency |

**Why this matters:** "audit is core → SSO is required" is defensibly true but understates the design surface. The honest framing is: *given user identity + audit are non-negotiable, we chose OIDC + local role table + JIT provisioning + three-role RBAC. Each link is a defensible choice, not an inevitability.* If audit ever grows from "who did what" into "why was this allowed" (regulatory reproducibility, policy provenance), revisit the last row.

## Settled Decisions

- **DGraph `@auth` directives are out of scope** — all queries go through the Go server; clients never reach DGraph directly. Authorization is enforced entirely at the Go middleware layer. DGraph `@auth` adds no security value given the network topology. Do not re-add it.
- **Authorization model: three-role local table (`readonly < dev < admin`)** — `role` enum on `users` ent schema: `readonly` (default), `dev`, `admin`. `ORBITAL_ADMIN_EMAILS` (comma-separated): on first OIDC/device-code login, matching emails get promoted to `admin`. `RoleAtLeast(actual, minimum user.Role) bool` in `authz.go` is the canonical comparison helper. `RequireRole(db, minRole)` checks mutating methods (POST/PUT/PATCH/DELETE), passes GET through; `RequireAdmin` is a wrapper. GraphQL mutations require `dev` minimum (not admin). Azure AD App Roles deferred — requires Application Administrator permissions. If available later, extract `roles` claim at login and override local role; local table stays as source of truth.
- **Admin role management UI at `/users`** — server-side rendered Go template (not DataTables). Button group per row (R/D/A), active role highlighted+disabled. Self-row fully disabled. Last-admin guard: `PUT /api/v1/users/:id/role` returns 409. Operation is idempotent (same role → 200 with no DB write).
- **Readonly UI gating: `CanMutate bool` on `layout.Base`** — `true` for dev and admin. Pages gate action forms behind `{{if .CanMutate}}...{{else}}{{template "access-required" .}}{{end}}`. Pure-action pages (Restore): entire content gated. Mixed pages (Export, Backup, Signed Artifacts): list/table always visible, action form gated.
- **`can_mutate` is derived from the session cookie, not a DB lookup** — computed in the global session middleware in `server.go` via `RoleAtLeast`. No DB call on GET requests. `RequireRole` is DB-backed on mutating methods (security enforcement boundary). `can_mutate` is a UI display hint only. Role changes take effect on next login. Do not re-add a separate `SetCanMutate` middleware or per-route `can_mutate` wiring.
- **`ORBITAL_OIDC_DEVICE_CODE` defaults to `true`** — device code is the only viable browser SSO for this deployment (private DNS/ILB, no publicly resolvable redirect URI). Set `false` only if deploying on a public URL with a registered redirect URI. No auto-open: Azure AD's v1 `deviceauth` endpoint doesn't support `?otc=` pre-fill.
- **`POST /auth/device/poll` sends `device_code` in the JSON body** — not as a query parameter. Query params appear in application logs, proxy logs, and browser history. `device_code` is a short-lived credential. Handler uses `c.Bind()`. Route is `POST`, not `GET`.
- **`ResolveUser` middleware bridges bearer token auth to the user table** — wired after `RequireAuth()` and before `RequireRole()` in `server.go`. When `user_id` is 0 (bearer path: JWT validated but no session), finds or provisions the user by email from JWT claims. Without this, all bearer token requests were always-403. Do not remove or reorder it.

## Session auth (orbital web)

- Sessions use gorilla/sessions cookie store with HMAC-SHA256 (`ORBITAL_SESSION_HMAC_KEY`) and AES-256 (`ORBITAL_SESSION_ENCRYPTION_KEY`).
- **Session encryption key must be exactly 32 bytes** — gorilla/sessions silently fails to decode sessions with the wrong key length. Orbital validates this at startup and refuses to start if misconfigured.
- Local login: email/password against PostgreSQL `users` table, bcrypt cost 12. Always available for dev.
- OIDC/SSO: Azure AD via OpenID Connect. Enabled when `ORBITAL_OIDC_ISSUER_URL` and `ORBITAL_OIDC_CLIENT_SECRET` are both set. Disabled with a startup warning if the secret is missing.

## Device code browser SSO

Activated by `ORBITAL_OIDC_DEVICE_CODE=true`. The login modal shows a "Sign in with Microsoft" button that uses the device code flow instead of the standard Authorization Code redirect.

**Why device code for browser SSO** (not Authorization Code + PKCE):

- **Azure AD private DNS limitation** — Orbital runs behind an Internal Load Balancer in AKS and uses private DNS names (e.g. `orbital.devnew.armada.internal`) that only resolve on the VPN. Azure AD's Authorization Code flow requires a redirect URI that Azure AD can validate — a private DNS name either fails registration or silently misdirects. Device code has no redirect URI at all.
- **No HTTPS requirement** — Authorization Code + PKCE requires the redirect URI to be an HTTPS endpoint registered with Azure AD. Orbital's Go server receives plain HTTP (TLS is terminated at the Istio ingress layer). Device code needs no redirect URI, so TLS on the server process is irrelevant to the OAuth handshake.
- **No App Roles available** — We don't have Application Administrator permissions on the Azure AD tenant, so we can't create custom App Roles. This ruled out JWT claim-based authz regardless of flow. Authorization is enforced via the local `role` column on the `users` table (see Authorization section below).

**Endpoints:**
- `GET /auth/device` — initiates the flow, returns `device_code`, `user_code`, `verification_uri`, `verification_uri_complete`
- `GET /auth/device/poll` — polls the token endpoint; returns 202 (pending), 200 (complete, sets session), or 4xx (error)
- Standalone page rendered at `/auth/device`; includes JS poller

**Auto-open variant** — On page load the JS poller immediately opens `verification_uri_complete` (which embeds the user_code) in a new tab. This eliminates the manual copy-paste UX of classic IoT device code flows. The original tab continues polling and redirects when the token arrives. A fallback manual link is shown if the popup is blocked.

**orbctl uses Authorization Code + PKCE (not device code)** — the CLI runs on the user's local machine, can open a browser directly, and can bind a local redirect server on a random port. It doesn't have the private DNS / redirect URI problem. Conditional Access policies that block device code flows apply to orbctl, not to the orbital web server.

## Bearer token validation

- **Single GraphQL endpoint at `/graphql`.** Consolidated 2026-06-09 from the prior split (`root.Any("/graphql")` for session, `api.Any("/graphql")` for bearer). The split caused asymmetric authz: the bearer path had route-level `RequireRole(RoleDev)` which blocked readonly callers from running queries (which use POST), and the session path had NO route-level auth enforcement at all (leaking data to anonymous users). Now one path with `bv.RequireAuth()` (accepts session OR bearer) and `ResolveUser`; mutations enforced at the handler. The endpoint sits at `/graphql` rather than `/api/v1/graphql` — GraphQL is not URL-versioned (GitHub/GitLab/NetBox/Apollo convention); `/api/v1/` is reserved for REST endpoints where version semantics matter. Orb registers its own `/graphql` for symmetry.
- **Mutation authorization is enforced in the handler, not on the route.** `internal/handler/graphql.go` calls `isMutation(req.Query)` and, when true, checks `RoleAtLeast(role, RoleDev)`. The handler is the sole authoritative gate for write authz on the GraphQL endpoint — do not re-add `RequireRole(db, RoleDev)` to the route or you will block readonly POST queries.
- **`isMutation` is the security boundary, not a heuristic.** It must defend against bypasses (leading `#`-comments, multi-operation requests, string-literal smuggling, block strings). Strengthened 2026-06-09 to strip comments and string literals before matching `\bmutation\b`. See `internal/handler/graphql.go` and `TestIsMutation` cases. If you add features to the GraphQL parser path, add adversarial cases to that test.
- **Azure AD app must set `requestedAccessTokenVersion: 2`** in the app manifest (`api.requestedAccessTokenVersion: 2`). Default `null` produces v1 tokens with `iss: "https://sts.windows.net/..."` which does not match go-oidc v2 discovery issuer.
- **Bearer token audience is the bare client GUID** — Azure AD v2 sets `aud` to bare GUID (e.g. `5fc832f6-...`), not `api://5fc832f6-...`. Configure `go-oidc` with `cfg.OIDCClientID` directly, not `"api://"+cfg.OIDCClientID`.
- **`internal/auth/bearer.go` validates signature, issuer, and audience only — not `scp`/`scope`.** Authorization is enforced downstream by `RequireRole` against the local `users.role` column. Adding scope-based checks would duplicate logic and tie us to Azure AD specifics.

## Third-party API clients

Orbital is an OAuth **resource server**, not an identity provider. Client applications authenticate with Azure AD directly and present the resulting JWT to orbital. Orbital does not issue tokens, proxy auth, or expose a device-code endpoint for external clients — `/auth/device` is for orbital's own browser UI.

**Integrator quickstart (give this to client teams):**

```
Tenant ID:         <Azure AD tenant GUID>
Orbital client ID: <ORBITAL_OIDC_CLIENT_ID value>
Scope to request:  api://<orbctlent-id>/user_impersonation
                   (fallback: api://<orbctlent-id>/.default)
API endpoint:      https://<orbital-host>/graphql
Auth header:       Authorization: Bearer <access_token>
Token version:     v2 (issuer https://login.microsoftonline.com/<tenant>/v2.0)
```

**OAuth flow** — orbital doesn't care which flow produces the token, only that the resulting JWT has the right issuer + audience. Common topologies:

- **Frontend → client backend → orbital** — backend uses **On-Behalf-Of (OBO)** to mint an `aud=orbital` token from the inbound `aud=client-api` token. Preserves user identity (`preferred_username` flows through), so `ResolveUser` maps to the right row and audit logs show the real user. Requires the backend be a confidential client (secret or cert), and admin consent granted on its App Registration's API permission to orbital's `user_impersonation` scope. The OBO exchange itself is between the client and Azure AD — orbital sees only the final token. See Microsoft's [OBO docs](https://learn.microsoft.com/azure/active-directory/develop/v2-oauth2-on-behalf-of-flow). The frontend can be a SPA, native app, or server-rendered UI — only the backend's role matters here.
- **Public client → orbital direct** — Authorization Code + PKCE if the client has a registerable HTTPS redirect URI; device code if it's behind private DNS / VPN-only (same constraint that pushed orbital's own UI to device code). No exchange, client requests `aud=orbital` upfront. "Public client" covers SPAs, native desktop, and mobile apps — anything that cannot keep a secret.
- **Headless service / scheduled job (no user identity)** — `grant_type=client_credentials` with `scope=api://<orbctlent-id>/.default`. Resulting token has no `preferred_username`, so `ResolveUser` cannot map it. Avoid until we add explicit service-account provisioning. Use OBO for any human-driven action, even from a backend.

**Scope policy** — orbital's App Registration exposes `user_impersonation` (Azure AD's default scope name when adding a delegated scope via "Expose an API"). Orbital does not validate `scp` — the scope name is consent-clarity only, not an authz boundary. `.default` works as a fallback but is discouraged because it requires admin consent and bundles all consented permissions opaquely.

**Pre-authorizing client apps** — the "Authorized client applications" section in "Expose an API" is left empty by default. Each client must request user (or admin) consent the first time. To skip the consent dialog for a specific trusted integrator, add their App Registration's client ID with the `user_impersonation` scope checked. Reserve this for fully trusted internal clients — leaves consent explicit and revocable for everyone else.

**First-call provisioning** — when a JWT arrives with an email not in the `users` table, `ResolveUser` middleware (see `internal/handler/authz.go`) auto-provisions a row with `role: readonly`. Admins listed in `ORBITAL_ADMIN_EMAILS` get promoted on first login. Tell integrators: their users will hit 403 on mutating calls until an admin promotes them via `/users`.

**Convenience endpoint to expose later** — a `GET /api/v1/whoami` would let integrators verify their token works and see the role orbital assigned, without making a mutating call. Not in scope today, but cheap to add if multiple integrators ask.

## Authorization

Role enforcement is done entirely at the Go middleware layer. DGraph `@auth` directives are out of scope — clients never reach DGraph directly (all queries go through the Go server), so DGraph-level authz adds no security value. Azure AD App Roles are deferred — we lack Application Administrator permissions on the tenant. If App Roles become available later, extract the `roles` claim at login and override the local role; the local table stays as source of truth.

**Role model:**
- `role` column on `users` ent schema: enum `admin` / `dev` / `readonly`, default `readonly`
- `ORBITAL_ADMIN_EMAILS` (comma-separated env var): on first OIDC or device-code login, matching emails are promoted to `admin`; all other users get `readonly`

**Middleware:**
- `RequireRole(db, minRole)` — Echo middleware; checks mutating HTTP methods (POST/PUT/PATCH/DELETE), passes GET through; 403 for insufficient role. DB-backed because it is a security enforcement boundary.
- `RequireAdmin` — convenience wrapper around `RequireRole` with `minRole = admin`
- `RoleAtLeast(actual, minimum user.Role) bool` helper in `authz.go`

**Admin UI:**
- `/users` — admin-only page; button group per row for R/D/A role assignment; last-admin guard returns 409 to prevent locking yourself out

**Readonly UI gating:**
- `CanMutate bool` in `layout.Base` — derived from `auth.UserSession.Role` in the global session middleware; no DB call on GET requests
- Role is baked into the session cookie at login. Role changes take effect on next login; `PUT /api/v1/users/:id/role` response includes a `"note"` field telling callers to re-login.
- Sessions predating the `user_role` key default to `"readonly"` — safe fallback.
- Amber access-required banner shown on privileged pages for readonly users

## orbctl credential storage

- **Login flow**: Authorization Code + PKCE. Opens browser automatically with a local redirect server on a random port. No private DNS issue — runs on the user's machine where the private VPN DNS resolves correctly.
- **Single-file storage**: `~/.orbital/credentials.json`, mode 0600. Stores the full `Credentials` blob — access token, refresh token, expiry, name, email — as JSON. Matches the pattern used by `aws`, `gcloud`, `kubectl`, `terraform`, `az`. `orb login` uses the same model at `~/.orb/credentials.json`.
- **Subcommands silently refresh expired access tokens.** `orbauth.GetCredentials()` (and the thin `GetToken()` wrapper) is the canonical chokepoint — every subcommand reads through it. If the cached access token has > 60s remaining, returns immediately. If expired but a refresh token is on disk, exchanges it with AAD and saves the rotated credentials before returning. Only when refresh fails or no refresh token exists does the user see "run `orbctl login`". This reverses the prior "subcommands read file only, never refresh" behavior (which forced redundant `orbital login` invocations for any expired session) and matches `gcloud`, `gh`, `aws`, `az` conventions.
- **Concurrent CLI invocations are not file-locked.** If two subcommands hit the refresh path simultaneously, the second exchange may fail because AAD rotated the refresh token after the first call. Probability is low (users rarely parallelize CLI subcommands); accepted for v1. Add a flock around `getCredentialsFromStore` if it becomes a real problem.
- **No OS keychain integration.** Removed 2026-06-09. The previous implementation (CGo + macOS Security framework via `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`) bought defense against one narrow threat (stolen Mac with FileVault disabled and disk pulled offline) at the cost of ~360 lines of platform-specific code, CGo cross-compile pain (required `clang -arch x86_64` for darwin/amd64), and a larger binary. Touch ID was off the table without Apple code-signing entitlements (`errSecMissingEntitlement = -34018`). Industry baseline for OAuth-token CLIs is mode-0600 file storage — keychain integration is the exception, justifiable only when biometric UX is actually available. Reconsider only if (1) orbctl gets signed and notarized, or (2) regulatory requirement specifies at-rest encryption beyond FileVault.

## Why orbctl uses hand-rolled OAuth instead of MSAL

`docs/auth.md` recommends external service developers use Microsoft's MSAL library for OAuth (especially On-Behalf-Of). orbctl does NOT use MSAL — it implements PKCE + refresh directly in `internal/orbauth`. This is deliberate, not laziness:

- **Different audiences, different tools.** MSAL is recommended for external service authors because OBO is intricate and not worth writing by hand. orbctl implements PKCE + refresh, which is simple OAuth — well-defined RFC, ~250 lines of working Go code today. Writing it ourselves avoids a ~2-3 MB binary-size increase (real cost for a brew-distributed CLI).
- **`-v` UX** (printing the access token as `export ORBITAL_TOKEN=<value>` after login) is custom and easier to add to our thin OAuth layer than to wrap around MSAL's account-management abstractions.
- **No OBO** in orbctl — it's a public client doing user-delegated PKCE, not a confidential client brokering tokens on behalf of others. The MSAL feature set largely doesn't apply.

**Trigger conditions to reconsider** migrating to MSAL `public.Client`:
1. Auth code grows beyond ~400 lines (currently ~250 in `internal/orbauth/`)
2. Hit an edge case `orbauth` mishandles (e.g., concurrent-refresh races requiring proper token cache coordination, weird clock-skew handling)
3. Multi-tenant support becomes a real requirement (account switching, multiple identity providers)
4. Need to share auth code across multiple Go clients (then publishing a vendor-maintained library wrapper makes sense)

Until one of those triggers fires, the recommendation split (MSAL for service authors, `orbauth` for the CLI) is deliberate.

## Session cookie Secure flag

- **`ORBITAL_COOKIE_SECURE` (default `true`) is the only source of truth for the cookie's `Secure` attribute** — decoupled from `ORBITAL_DEV` because that flag bundles unrelated dev behavior. Do NOT revert to `Secure: !cfg.Dev` — pinned by `auth.TestCookieSecure_FollowsConfig`.
- **AKS dev sets `ORBITAL_COOKIE_SECURE=false`** because Istio is HTTP-only there; otherwise the browser silently drops the cookie and every login appears to fail. Remove the override once TLS lands.

## orbauth shared package

- `internal/orbauth/` — PKCE flow, token exchange, refresh, `Store` interface, `FileStore` (the only implementation). Both `orb` and `orbctl` import it. Neither CLI contains auth logic directly.
- `orb login` (at `~/.orb/credentials.json`) and `orbctl login` (at `~/.orbital/credentials.json`) now follow identical patterns — single file, full credentials including refresh token.

## `orbctl get datacenter` CLI

- Resolves identifiers in order: `0x`-prefix → DGraph UID, contains `:` → orbId, otherwise tries orbId then name.
- POSTs to `/graphql` with `Authorization: Bearer` header.

