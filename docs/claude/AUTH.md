# Auth Reference

Read this before: OIDC flow changes, CLI login, keychain, session handling, bearer token validation, authz (Spike 8) work.

## Session auth (orbital web)

- Sessions use gorilla/sessions cookie store with HMAC-SHA256 (`ORBITAL_SESSION_HMAC_KEY`) and AES-256 (`ORBITAL_SESSION_ENCRYPTION_KEY`).
- **Session encryption key must be exactly 32 bytes** — gorilla/sessions silently fails to decode sessions with the wrong key length. Orbital validates this at startup and refuses to start if misconfigured.
- Local login: email/password against PostgreSQL `users` table, bcrypt cost 12. Always available for dev.
- OIDC/SSO: Azure AD via OpenID Connect. Enabled when `ORBITAL_OIDC_ISSUER_URL` and `ORBITAL_OIDC_CLIENT_SECRET` are both set. Disabled with a startup warning if the secret is missing.

## Bearer token validation

- `/api/v1/graphql` registered on both `e.Any("/graphql")` (session auth, for browser) and `api.Any("/graphql")` (bearer auth, for CLI/API clients).
- **Azure AD app must set `requestedAccessTokenVersion: 2`** in the app manifest (`api.requestedAccessTokenVersion: 2`). Default `null` produces v1 tokens with `iss: "https://sts.windows.net/..."` which does not match go-oidc v2 discovery issuer.
- **Bearer token audience is the bare client GUID** — Azure AD v2 sets `aud` to bare GUID (e.g. `5fc832f6-...`), not `api://5fc832f6-...`. Configure `go-oidc` with `cfg.OIDCClientID` directly, not `"api://"+cfg.OIDCClientID`.

## Authorization — Spike 8 (in progress)

- **Roles:** `orbital-admin`, `orbital-viewer` defined as App Roles in Azure AD app manifest. Appear in JWT `roles` claim as strings. Do not use Azure AD group GUIDs as the authz primitive.
- **DGraph `@auth` directives** — `@auth(add/update/delete)` on each type restricts mutations to authorized roles. `ClosedByDefault: true` requires valid JWT for all operations. Field-level authz not supported by DGraph — do not attempt.
- **Echo middleware** is the primary enforcement layer for REST mutation endpoints. DGraph `@auth` is defense-in-depth, not primary.
- **Integration tests** — generate and sign JWTs locally with a test RSA key pair. No Azure AD network call required in tests or CI.

## orbital-cli credential storage

- **Login flow**: Authorization Code + PKCE. Device Code flow was rejected — Conditional Access policies can block it. Auth Code + PKCE opens browser automatically with a local redirect server on a random port.
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
