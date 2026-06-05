# Auth Reference

Read this before: OIDC flow changes, CLI login, keychain, session handling, bearer token validation, authz work.

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
- `RequireRole(db, minRole)` — Echo middleware; checks mutating HTTP methods (POST/PUT/PATCH/DELETE), passes GET through; 403 for insufficient role
- `RequireAdmin` — convenience wrapper around `RequireRole` with `minRole = admin`
- `RoleAtLeast(actual, minimum user.Role) bool` helper in `authz.go`

**Admin UI:**
- `/users` — admin-only page; button group per row for R/D/A role assignment; last-admin guard returns 409 to prevent locking yourself out

**Readonly UI gating:**
- `CanMutate bool` in `layout.Base` — set from the authenticated user's role
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

