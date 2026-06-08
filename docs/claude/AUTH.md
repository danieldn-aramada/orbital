# Auth Reference

Read this before: OIDC flow changes, CLI login, keychain, session handling, bearer token validation, authz work.

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

**orbital-cli uses Authorization Code + PKCE (not device code)** — the CLI runs on the user's local machine, can open a browser directly, and can bind a local redirect server on a random port. It doesn't have the private DNS / redirect URI problem. Conditional Access policies that block device code flows apply to orbital-cli, not to the orbital web server.

## Bearer token validation

- `/api/v1/graphql` registered on both `e.Any("/graphql")` (session auth, for browser) and `api.Any("/graphql")` (bearer auth, for CLI/API clients).
- **Azure AD app must set `requestedAccessTokenVersion: 2`** in the app manifest (`api.requestedAccessTokenVersion: 2`). Default `null` produces v1 tokens with `iss: "https://sts.windows.net/..."` which does not match go-oidc v2 discovery issuer.
- **Bearer token audience is the bare client GUID** — Azure AD v2 sets `aud` to bare GUID (e.g. `5fc832f6-...`), not `api://5fc832f6-...`. Configure `go-oidc` with `cfg.OIDCClientID` directly, not `"api://"+cfg.OIDCClientID`.

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

## orbital-cli credential storage

- **Login flow**: Authorization Code + PKCE. Opens browser automatically with a local redirect server on a random port. No private DNS issue — runs on the user's machine where the private VPN DNS resolves correctly.
- **Keychain** (macOS only, CGo + Security framework directly — not `go-keyring`): stores `{refresh_token, name, email}` JSON blob. Uses `kSecAttrAccessibleWhenUnlockedThisDeviceOnly` — locked when device is locked, not synced to iCloud. No Touch ID — requires Apple code signing entitlements (`errSecMissingEntitlement = -34018`) not available for unsigned CLIs.
- **File** (`~/.orbital/credentials.json`, mode 0600): stores access token + expiry only. Azure AD JWTs are ~6KB, exceeding go-keyring's 4096-byte macOS `security -i` limit.
- **Subcommands read file only** — never touch keychain, never silently refresh. If access token expired → exit with "run `orbital login`". Only `orbital login` does the keychain read + refresh token exchange.
- **JSON blob in keychain** (not separate entries) — atomic, avoids multiple keychain prompts, easy to version. Same pattern as GitHub CLI, Azure CLI.

## orbauth shared package

- `internal/orbauth/` — PKCE flow, token exchange, refresh, Store interface, FileStore, KeychainStore. Both `orb` and `orbital-cli` import it. Neither CLI contains auth logic directly.
- `orb login` uses plain FileStore at `~/.orb/credentials.json` (stores full credentials including access token). Different from `orbital-cli` which splits keychain (refresh token) + file (access token).

## `orbital get datacenter` CLI

- Resolves identifiers in order: `0x`-prefix → DGraph UID, contains `:` → orbId, otherwise tries orbId then name.
- POSTs to `/api/v1/graphql` with `Authorization: Bearer` header.

