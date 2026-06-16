# 010 — Bundler service-to-orbital authentication

**Date:** 2026-06-12
**Status:** Accepted

## Context

Orbital invokes bundlers (cb-bundler today, others later) by POST to a per-request URL during artifact publish. Each bundler then turns around and queries orbital's `/graphql` for the data it needs to build its layer.

Orbital's `/graphql` is mounted under the `apiAuth` middleware chain (`internal/server/server.go`) which requires either:
- A valid session cookie (user logins via OIDC auth code flow), OR
- A valid bearer token verified against Azure AD's JWKS.

cb-bundler is neither a human session nor configured with credentials. Its requests currently arrive with no `Authorization` header and orbital returns **401**. As of 2026-06-12 this had never surfaced because the publish flow from the UI was not yet wiring bundlers (separate bug, fixed in the same week). Once that wiring landed, every bundler-enriched publish failed with `orbital query failed: graphql returned status 401` because cb-bundler has no token to send.

## Options considered

### A. Pre-shared service token

Generate a random hex string, store on both orbital and cb-bundler via the existing K8s Secret. Orbital's bearer verifier checks for that exact string as an alternative to OIDC validation.

- ✅ Trivial to implement (~30 lines, no network dependency)
- ✅ Works locally without Azure AD reachable
- ❌ Manual rotation; one shared secret across many callers can't distinguish them
- ❌ Synthetic actor identity (`service:bundler`) — we make up a string rather than provenance from an IdP
- ❌ Doesn't generalize when other services need similar access
- ❌ Diverges from standard enterprise patterns

### B. Localhost-only auth bypass

Skip auth on `/graphql` when the remote address is loopback AND the query is a read.

- ❌ Fragile (parse GraphQL queries to determine read vs mutation)
- ❌ Breaks the moment cb-bundler moves out of the orbital pod
- ❌ Subtle attack surface — anything that can co-locate gets unauthenticated read access
- Rejected.

### C. OAuth2 client credentials grant against existing orbital AAD app

cb-bundler obtains a JWT from Azure AD using the OAuth2 **client credentials** grant against the **existing** orbital app registration. No new app registration is required — the orbital app authenticates as itself, calling its own API.

- ✅ Standard enterprise pattern (RFC 6749 §4.4)
- ✅ Reuses the `ORBITAL_OIDC_CLIENT_SECRET` already in `orbital-secrets` — one secret, not two
- ✅ Tokens auto-rotate (60-min lifetime); cb-bundler refreshes
- ✅ Orbital's existing bearer verifier validates the JWT identically to user tokens (same JWKS, signature, issuer, audience checks)
- ✅ The `appid` claim provides provenance (which client called) for audit
- ✅ Generalizes to any future service that needs to call orbital — same client-credentials pattern, no new infrastructure
- ❌ Requires Azure AD reachable to mint tokens (impacts local dev — see consequences)
- ❌ Requires one-time Azure AD configuration: expose an API scope on the existing app + grant admin consent
- ❌ Orbital's bearer verifier needs to handle app-only token claim shape (no `email`/`oid` — has `appid`)

### D. Register a separate AAD app for cb-bundler

Same as C but with a dedicated app registration for cb-bundler.

- ✅ Cleanest separation of identity per service
- ❌ More moving parts (additional app reg, secret, lifecycle)
- ❌ Not warranted today — cb-bundler is the only consumer; one app's `appid` claim suffices for audit
- ❌ Wouldn't change the orbital-side verifier work (same claim handling needed either way)
- Deferred. Revisit if multiple distinct services emerge or per-caller scopes matter.

## Decision

**Adopt Option C: OAuth2 client credentials grant against the existing orbital Azure AD app.**

## Flow

```
                                                  ┌───────────────┐
   1. cb-bundler                                  │  Microsoft    │
   ──────────────────────────────────────────►   │  Entra (AAD)  │
   POST login.microsoftonline.com/{tenant}        │               │
       /oauth2/v2.0/token                          │   tenant:     │
                                                  │   {tenant_id} │
   grant_type=client_credentials                   │   app:        │
   client_id={orbital_app_client_id}              │   {client_id} │
   client_secret={orbital_oidc_client_secret}     │               │
   scope=api://{client_id}/.default                │               │
                                                  └──────┬────────┘
                                                         │
                                            JWT issued   │
                                            aud  = orbital app
                                            appid = orbital app (caller)
                                            exp  ≈ now + 60min
                                                         │
                  ◄──────────────────────────────────────┘
   2. cb-bundler caches JWT for ~50min (5-min safety margin)

   3. cb-bundler calls orbital:
   ──────────────────────────────────────►   ┌──────────────────────┐
   POST /graphql                              │  orbital              │
   Authorization: Bearer eyJ0eXAi...          │                       │
                                              │  bearer verifier      │
   4. Verifier:                               │  - JWKS signature ✓   │
      - fetch JWKS from Azure AD              │  - issuer ✓           │
        (cached)                              │  - audience ✓         │
      - validate signature                    │  - appid → actor      │
      - validate iss = login.microsoftonline. │                       │
        com/{tenant}/v2.0                     │  Allow.               │
      - validate aud = {client_id}            │                       │
      - extract appid → audit log actor       └──────────────────────┘
```

## Identity in audit logs

App-only tokens have:
- `appid` — caller's client_id (the orbital app, since cb-bundler authenticates AS the app)
- `oid` — service-principal object ID (sometimes; not user)
- No `email` / `preferred_username`

Orbital's bearer verifier maps these to a synthetic actor string: `app:<appid>`. Audit rows for bundler-driven mutations are attributed accordingly, distinct from human-driven ones (`<user-email>`).

## Azure AD configuration (one-time, per environment)

On the orbital app registration:

1. **Expose an API**: set Application ID URI to `api://{client_id}`. Add a scope (or rely on `.default` for app permissions).
2. **API permissions** → Add → "My APIs" → orbital → Application permissions → check the scope → **Grant admin consent**.

That's the entire AAD side. Same app reg, same client_secret. cb-bundler authenticates as the orbital app calling itself.

## Consequences

### Production (AKS)
- `ORBITAL_OIDC_TENANT_ID`, `ORBITAL_OIDC_CLIENT_ID`, `ORBITAL_OIDC_CLIENT_SECRET` get exposed to the cb-bundler sidecar via the existing K8s Secret. No new secret material.
- Token lifetime is Azure AD's default (60 min). Refresh handled by cb-bundler's HTTP client.
- Orbital verifier extended once; future services using the same pattern need no further orbital changes.

### Local dev
- cb-bundler must be able to reach `login.microsoftonline.com` to mint tokens. Offline dev is no longer trivial.
- Developers running `make run-bundler` locally need the orbital app's `ORBITAL_OIDC_CLIENT_SECRET` in their environment.
- This matches the existing pattern — devs already need `deploy/local/cosign.key` and other secrets to exercise the full pipeline locally.

### What this does NOT solve
- Per-caller authorization (which scopes can call what). All callers using the same app reg get the same permissions. Acceptable today with one consumer. Revisit when multiple services with distinct privilege need diverge.
- mTLS / mutual identity at the transport layer. Out of scope.

## App Caller Authorization (added 2026-06-12)

The original implementation handled app-token verification in `BearerVerifier` correctly, but two downstream middlewares — `ResolveUser` and `RequireRole` — still treated absent `user_email` / `user_id == 0` as "unauthenticated" and returned 401/403. That made every `/graphql` query from cb-bundler fail with 401.

### Decision — MVP policy

**Any app-only caller that passes the `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` allowlist is treated as `dev`-equivalent for authorization purposes.**

Rationale:
- The allowlist (`ORBITAL_APP_TOKEN_ALLOWED_APPIDS`) **is** the authorization gate. If an `appid` is on the allowlist, the operator has already decided that caller is trusted.
- All mutating API routes today require `dev` minimum. The only finer distinction is `admin`, which gates user-management endpoints — those have no app-caller use case in the MVP.
- Matches the existing "static allowlist is the authz mechanism for services" design contract (RFC 6749 §4.4 + Microsoft Entra v2 token shape).

### Implementation

Both `ResolveUser` and `RequireRole` branch early on `strings.HasPrefix(user_name, auth.AppPrincipalPrefix)`:

- `ResolveUser`: app callers skip the Postgres `users` table lookup (no row exists by design). `user_id` stays `0`.
- `RequireRole`: app callers bypass the `user_id` / role-table check. Grants access when `RoleAtLeast(user.RoleDev, minRole)`. Denies (403, logged with `reason=app_caller_below_required_role`) when minRole is `admin` or higher.

Use the exported `auth.AppPrincipalPrefix` constant in both places — do not introduce a second `"app:"` string literal.

### Best practice for later (post-MVP)

When Application Administrator permissions on the Azure AD tenant become available, migrate to **Microsoft Entra App Roles** for app-caller authorization:

1. Define App Roles on the orbital app registration (e.g., `Mutate.All`, `Publish.Bundler`, `Read.All`).
2. Assign App Roles to consuming service principals (cb-bundler gets `Publish.Bundler`, future services get scoped roles).
3. In `RequireRole`, when handling an app caller, check the `roles` claim from the JWT against the required role — not the local users table.
4. Retire `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` once all consumers are App Role-gated. The allowlist was a stand-in for App Roles; both shouldn't coexist long-term.

Microsoft's explicit guidance — [App roles and claims](https://learn.microsoft.com/en-us/entra/identity-platform/howto-add-app-roles-in-apps) — is that App Roles are the designed mechanism for app-only authz. Resource servers should *not* manage a separate identity table for service principals. Today's allowlist + dev-equivalent policy is the correct interim under the access constraint; App Roles is the destination.

### Anti-pattern to avoid

Do not provision a fake `users` row for app callers (e.g., `bundler@armada.internal`). It would let the user-token codepath flow through unchanged but introduces:
- Synthetic email addresses that look real to operators auditing the table
- Two sources of truth for service authz (allowlist + DB row)
- Drift risk when one is updated and the other isn't

AWS, GitHub, Stripe, and Microsoft Entra all use distinct actor-type discriminators (CloudTrail `userIdentity.type`, GitHub `actor_type`, Entra `userType=ServicePrincipal`) rather than fake user rows. Orbital's `app:<appid>` actor prefix is the same pattern with the type collapsed into the actor string.

## Related

- Orbital auth implementation: `internal/auth/bearer.go`, `internal/handler/authz.go`
- cb-bundler's existing token field: `configbundle/internal/bundler/orbital.go` `BearerToken`
