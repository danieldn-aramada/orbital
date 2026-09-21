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

- **Approval bypass is NOT a role or a user capability.** `readonly < dev < admin` is unchanged and stays that way. Which roles may write a protected class directly is `approval_policy.bypass_roles` — a property of the policy, answered per protected class by an admin. If orbital ever needs real per-user permissions, that is Casbin, not more role tiers. See [`CHANGE-CONTROL.md`](./CHANGE-CONTROL.md).
- **DGraph `@auth` directives are out of scope** — all queries go through the Go server; clients never reach DGraph directly. Authorization is enforced entirely at the Go middleware layer. DGraph `@auth` adds no security value given the network topology. Do not re-add it.
- **Authorization model: three-role local table (`readonly < dev < admin`)** — `role` enum on `users` ent schema: `readonly` (default), `dev`, `admin`. `ORBITAL_ADMIN_EMAILS` (comma-separated): on first OIDC login, matching emails get promoted to `admin`. `RoleAtLeast(actual, minimum user.Role) bool` in `authz.go` is the canonical comparison helper. `RequireRole(db, minRole)` checks mutating methods (POST/PUT/PATCH/DELETE), passes GET through; `RequireAdmin` is a wrapper. GraphQL mutations require `dev` minimum (not admin). Azure AD App Roles deferred — requires Application Administrator permissions. If available later, extract `roles` claim at login and override local role; local table stays as source of truth. **If that day comes, use App Roles, not AD groups:** App Roles are explicit, auditable, and land directly in the JWT `roles` claim, whereas group GUIDs require a Microsoft Graph call to resolve — adding per-request latency and breaking in air-gapped/offline deployments. (Rationale carried over from the Spike 11 design before it was folded here; the rest of that spike's design — two flat roles `orbital-admin`/`orbital-viewer`, `RequireAnyRole`, and a DGraph `@auth` service token — was **superseded** by what actually shipped and is recoverable via `git log docs/spike-11-auth-design.md`.)
- **Admin role management UI at `/users`** — server-side rendered Go template (not DataTables). Button group per row (R/D/A), active role highlighted+disabled. Self-row fully disabled. Last-admin guard: `PUT /api/v1/users/:id/role` returns 409. Operation is idempotent (same role → 200 with no DB write).
- **Readonly UI gating: `CanMutate bool` on `layout.Base`** — `true` for dev and admin. Pages gate action forms behind `{{if .CanMutate}}...{{else}}{{template "access-required" .}}{{end}}`. Pure-action pages (Restore): entire content gated. Mixed pages (Export, Backup, Signed Artifacts): list/table always visible, action form gated.
- **`can_mutate` is derived from the session cookie, not a DB lookup** — computed in the global session middleware in `server.go` via `RoleAtLeast`. No DB call on GET requests. `RequireRole` is DB-backed on mutating methods (security enforcement boundary). `can_mutate` is a UI display hint only. Role changes take effect on next login. Do not re-add a separate `SetCanMutate` middleware or per-route `can_mutate` wiring.
- **`ORBITAL_OIDC_*` env var naming is vendor namespace, not protocol claim.** The runtime flows orbital uses today are OAuth 2.0 — Authorization Code for browser SSO, Authorization Code + PKCE (RFC 8252) for orbctl. Neither requests `openid` scope; no `id_token` is ever issued. The `OIDC_*` prefix on env vars reflects (a) Azure AD's "OpenID Connect" app-registration vocabulary, and (b) that bearer-token validation uses OIDC infrastructure (discovery → JWKS → signature/issuer/audience checks via go-oidc). Issuance is OAuth 2.0; validation is OIDC-flavored. Don't read `OIDC_` in a variable name as "we use id_tokens" — we don't.
- **Orbital does not model PIM (Privileged Identity Management) or any other elevation scheme** — it enforces `readonly < dev < admin` on whatever identity the token carries, full stop. PIM elevation is an IdP concern: the IdP grants a write-role token, orbital checks the role via `RequireRole`. Do NOT add PIM sessions, elevation windows, or approval state to orbital; a multi-writer client (AEP) layers that on top. Corollary: a client's "approval workflow" is satisfied by orbital's existing gates (role on write + `expectedContentHash` on publish) — see `OCI.md` § "Guarded Apply".
- **`ResolveUser` middleware bridges bearer token auth to the user table** — wired after `RequireAuth()` and before `RequireRole()` in `server.go`. When `user_id` is 0 (bearer path: JWT validated but no session), finds or provisions the user by email from JWT claims. Without this, all bearer token requests were always-403. Do not remove or reorder it.

## Session auth (orbital web)

- Sessions use gorilla/sessions cookie store with HMAC-SHA256 (`ORBITAL_SESSION_HMAC_KEY`) and AES-256 (`ORBITAL_SESSION_ENCRYPTION_KEY`).
- **Session encryption key must be exactly 32 bytes** — gorilla/sessions silently fails to decode sessions with the wrong key length. Orbital validates this at startup and refuses to start if misconfigured.
- Local login: email/password against PostgreSQL `users` table, bcrypt cost 12. Always available for dev.
- OIDC/SSO: Azure AD via OpenID Connect. Enabled when `ORBITAL_OIDC_ISSUER_URL` and `ORBITAL_OIDC_CLIENT_SECRET` are both set. Disabled with a startup warning if the secret is missing. **`ORBITAL_OIDC_ISSUER_URL` and `ORBITAL_OIDC_CLIENT_ID` have NO code defaults** — a baked-in tenant or client id silently points someone else's deployment at our IdP. A deployment must set them (`deploy/base/deploy.yaml` does); for local SSO copy `deploy/local/orbital.env.example` to `orbital.env`, which `make run-orbital` sources when present.

## Browser SSO: Authorization Code

The login modal's "Sign in with Microsoft" button goes to `GET /auth/login`, which is
Authorization Code against `ORBITAL_OIDC_ISSUER_URL`, returning to `ORBITAL_OIDC_REDIRECT_URL`.
That redirect URI must be registered with the IdP **byte-for-byte** — OAuth compares it exactly,
and it is sent twice (authorize, then again at token exchange, RFC 6749 §4.1.3).

**Device code (RFC 8628) was removed** — `ORBITAL_OAUTH2_DEVICE_CODE`, `GET /auth/device`,
`POST /auth/device/poll` and `pages/device-code.gohtml` are all gone. It existed as a workaround
for one Azure AD constraint: orbital sits behind an Internal Load Balancer on private DNS, and
Entra would not accept a redirect URI it could not resolve. Device code needs no redirect URI,
so it sidestepped the problem.

Three things ended it. The constraint was **vendor-specific** — Keycloak registers `http://` and
private-hostname redirect URIs without complaint, so the problem does not exist there. The
implementation was **vendor-locked**: the endpoint was built by string surgery on the Azure URL
shape (`TrimSuffix(issuer,"/v2.0") + "/oauth2/v2.0/devicecode"`), which 404s against any other
IdP, and it defaulted to `true`. And it had **one consumer** — orbital's own login modal, which
is a browser and therefore has the standard flow available to it. No comparable web UI
(Grafana, NetBox, Harbor, Rancher, Argo CD, Vault) uses device code for browser login; it is for
input-constrained devices and headless CLIs.

**orbctl is unaffected** — it uses Authorization Code + PKCE with a loopback listener
(`orbauth/auth.go`: `net.Listen("tcp","127.0.0.1:0")`, `code_challenge_method: S256`), which is
RFC 8252, the correct native-app pattern. It never called orbital's device endpoints.

## Bearer token validation

- **Single GraphQL endpoint at `/graphql`.** Consolidated 2026-06-09 from the prior split (`root.Any("/graphql")` for session, `api.Any("/graphql")` for bearer). The split caused asymmetric authz: the bearer path had route-level `RequireRole(RoleDev)` which blocked readonly callers from running queries (which use POST), and the session path had NO route-level auth enforcement at all (leaking data to anonymous users). Now one path with `bv.RequireAuth()` (accepts session OR bearer) and `ResolveUser`; mutations enforced at the handler. The endpoint sits at `/graphql` rather than `/api/v1/graphql` — GraphQL is not URL-versioned (GitHub/GitLab/NetBox/Apollo convention); `/api/v1/` is reserved for REST endpoints where version semantics matter. Orb registers its own `/graphql` for symmetry.
- **Mutation authorization is enforced in the handler, not on the route.** `internal/handler/graphql.go` calls `isMutation(req.Query)` and, when true, checks `RoleAtLeast(role, RoleDev)`. The handler is the sole authoritative gate for write authz on the GraphQL endpoint — do not re-add `RequireRole(db, RoleDev)` to the route or you will block readonly POST queries.
- **`isMutation` is the security boundary, not a heuristic.** It must defend against bypasses (leading `#`-comments, multi-operation requests, string-literal smuggling, block strings). Strengthened 2026-06-09 to strip comments and string literals before matching `\bmutation\b`. See `internal/handler/graphql.go` and `TestIsMutation` cases. If you add features to the GraphQL parser path, add adversarial cases to that test.
- **Azure AD app must set `requestedAccessTokenVersion: 2`** in the app manifest (`api.requestedAccessTokenVersion: 2`). Default `null` produces v1 tokens with `iss: "https://sts.windows.net/..."` which does not match go-oidc v2 discovery issuer.
- **Bearer token audience is the bare client GUID** — Azure AD v2 sets `aud` to bare GUID (e.g. `5fc832f6-...`), not `api://5fc832f6-...`. Configure `go-oidc` with `cfg.OIDCClientID` directly, not `"api://"+cfg.OIDCClientID`.
- **`internal/auth/bearer.go` validates signature, issuer, and audience only — not `scp`/`scope`.** Authorization is enforced downstream by `RequireRole` against the local `users.role` column. Adding scope-based checks would duplicate logic and tie us to Azure AD specifics.

### App Caller Authorization

`ORBITAL_APP_TOKEN_ALLOWED_APPIDS` gates **app-only** (client-credentials) bearer tokens — those carrying `appid`/`azp` and no `preferred_username`/`upn`. It runs *after* signature, issuer and audience have already verified, so it answers only "which application", never "is this token real".

| Value | Behaviour |
|---|---|
| unset / empty | **Every app token is rejected 401** |
| `*` (`auth.AppIDWildcard`) | Any app token bound to the audience is accepted |
| `id1,id2` | Only those application ids |

**Empty denies, and there is no default application id** *(changed 2026-09-17 — this reverses the previous behaviour, where empty skipped the check and the default was orbital's own app id).* "I have not configured this" and "I intend to allow everything" must not be the same input. That equivalence is a documented, exploited failure mode rather than a theoretical one: AWS IAM trust policies that omitted the `sub` condition accepted tokens from **any** GitHub Actions workflow — found in the wild by Datadog Security Labs in 2023 — and AWS now refuses to create such a policy at all. Vault's `bound_service_account_names` spells its wildcard `*` and is required; an Istio `AuthorizationPolicy` with no rules denies rather than allows. Orbital now matches that convention.

**A deployment that uses app tokens MUST set this, or publish breaks.** The in-pod cb-bundler mints a token **as the orbital app itself** (same client id) to call `/graphql` during publish, so `deploy/base/deploy.yaml` sets the orbital container's allowlist to that id. **Do NOT "clean up" the id out of the manifest the way it was cleaned out of `config.go`** — in code it was a wrong default, in the manifest it is the actual configuration.

**User tokens are unaffected.** The gate is inside the `email == "" && appID != ""` branch, so a human caller carrying an `appid` claim is never checked against it. Pinned by `TestAppTokenAllowlist_UserTokensUnaffected`, alongside `…_UnsetRejectsAppTokens`, `…_WildcardAcceptsAnyAppToken` and `…_SpecificListAcceptsListedRejectsOthers` — the negatives matter most here, since a permanently-allowing gate passes every positive test.

> **Superseded by `ORBITAL_AUTH_PROVIDERS`** (see § Multiple identity providers
> above). external-jwt still works and is still accurate as written, but it is
> the single-issuer path: one provider, one blanket role. The two are mutually
> exclusive — `config.New` refuses both. New deployments should use the provider
> list.

## Multiple identity providers (`ORBITAL_AUTH_PROVIDERS`)

Orbital's API is a resource server: it verifies bearer tokens it did not issue,
holds no client secret, and has no login flow of its own — the same role the
kube-apiserver plays. The browser UI is a separate thing (a confidential OAuth
client, which is what `ORBITAL_OIDC_CLIENT_SECRET` is for) and orbctl a third (a
public client using authorization code + PKCE). Keep the three apart; only the
first is what this section describes.

- **Trust is a LIST of providers, not one, and not a mode.** `ORBITAL_AUTH_PROVIDERS`
  is a JSON array decoded by an `envconfig.Decoder`, the same shape `ORB_CONSUMERS`
  uses. Unset means the legacy single-issuer path still serves, so no existing
  deployment changes. Setting it alongside `ORBITAL_AUTH_MODE` is a startup error
  — merging two sources of truth for who may authenticate is how they drift.
  **Do NOT move this to a config file**: orbital reads no config file, an env var
  needs a restart to apply which matches the restart-to-apply decision below, and
  `ORBITAL_BUNDLER_URLS` is not a counter-example (it is a hand-rolled `name=url`
  micro-DSL — the lesson there is do not invent a format, not do not put
  structured data in an env var).
- **A token selects its provider by `iss`, and issuer URLs are unique.** Enforced
  in `AuthProviders.Validate`. So exactly one provider ever attempts cryptographic
  validation. **Never implement "try each verifier until one accepts"** — that
  lets an attacker aim at whichever configured provider is weakest, and it is why
  Kubernetes makes issuer URLs unique in `AuthenticationConfiguration` rather than
  leaving ordering to the operator. The `iss` peek is unverified and only selects
  WHICH provider runs; that provider still checks the signature against its own
  JWKS, so a token claiming provider A's issuer but signed by B is refused
  (`TestProviderSet_TokenSignedByAnotherProviderIsRejected`).
- **`claimValidationRules` is how `azp` anchoring is expressed, per provider.**
  Keycloak issues `aud: account` to every client in a realm, so an audience check
  there proves only "some client in this realm" and `azp` is what identifies the
  caller. Per provider rather than global, so a second provider can differ.
- **Applying the config requires a restart.** Kubernetes hot-reloads because you
  cannot casually restart an API server; orbital rolls with `maxUnavailable: 0`,
  so a change costs a zero-downtime rollout. Reload would add a failure mode —
  bad config arriving at runtime rather than at boot, and replicas disagreeing
  about who may authenticate mid-propagation.
- **A provider that fails discovery does not block startup.** It is logged and
  its tokens are refused until a restart; the other providers keep working. An
  IdP outage must not stop orbital serving what does not need it.
- **An unmatched `iss` gets a generic 401.** The issuer is logged server-side and
  never echoed — an unauthenticated caller learns nothing about what is configured.

### Selection is keyed on `(iss, azp)`, not the issuer alone

One Keycloak realm hosts several clients that orbital must treat differently — a
trusted upstream service and a direct caller share an issuer but not a policy. So
a provider entry carries a `clientID` (the `azp` it matches), and the
**`(issuer.url, clientID)` pair must be unique**. An empty `clientID` matches any
client of that issuer as an issuer-wide default; a specific entry always wins, so
selection stays two exact lookups rather than an ordered scan.

Kubernetes keys on issuer alone because a cluster typically has one OIDC client.
That premise does not hold for a Keycloak realm, which is why this diverges —
**do not "simplify" it back to issuer-only**.

**`clientID` is also the app-token gate.** A token whose `azp` matches no entry is
refused before any role logic runs, so listing a client IS the allowlist — per
provider, and the same mechanism that selects the entry.
`ORBITAL_APP_TOKEN_ALLOWED_APPIDS` belongs to the legacy single-issuer path and is
**ignored** here; orbital warns at startup when it is set alongside
`ORBITAL_AUTH_PROVIDERS` rather than leaving it present and inoperative.

### Trusted services: authorization delegated upstream

A caller whose own authorization layer decides what its users may do. `AEP FC`
authenticates and authorizes through `organization-svc`, then calls orbital on a
user's behalf; it does not configure users in orbital or Keycloak.

    "clientID": "aep-fleet-commander",
    "trustedService": { "assignedRole": "admin" }

Every valid token gets that role, on every request, and **no user row is
provisioned** — the caller is a service acting for its own users, not an orbital
user, so it gets the same treatment as an app principal for the same reason.

**This is a deliberate trust delegation, and it is named so that is visible.** It
was previously `ORBITAL_JWT_DEFAULT_ROLE` — a default that happened to apply to
everyone, which reads like an unset value rather than a decision.

**What it costs, stated plainly:** orbital's own authorization is not the control
for these callers, so if the upstream service fails open, orbital does too. What
limits the damage is `acting_client` on audit events (see `AUDIT.md`), which is
how the log distinguishes "AEP acting as someone" from that person acting
directly. **The standard answer is RFC 8693 token exchange**, where the IdP signs
an `act` claim instead of orbital inferring one from `azp` — filed in
`backlog.md`. `azp` names only the last client in a chain and does not nest;
`act` does. Treat the current shape as the interim it is.

### Roles: three modes per provider, and only one at a time

Orbital owns authorization. A provider asserts identity and group membership;
orbital's operator decides what that membership is worth. **A provider never
names an orbital role** — `roleMapping` is written by the operator.

Plus `trustedService` above, which owns the role wholesale and stores nothing.

| | mode A — `defaultRole` | mode B — `roleMapping` |
|---|---|---|
| Who owns the role | orbital's users table | the provider's groups |
| New user | created with `defaultRole` | created with the matched role |
| Later logins | table wins; admin edits stick | re-derived every login; provider wins |
| No group matches | n/a | **denied** — no user created |
| Users page | role editable | read-only, with provenance |

**Exactly ONE of `defaultRole`, `roleMapping` or `trustedService` per provider.
Two or zero is a startup error** — they are three different answers to who owns
this caller's role, and a provider carrying more than one would have to pick,
silently.
NetBox makes the same pair exclusive (`REMOTE_AUTH_DEFAULT_PERMISSIONS` is "only
operational when REMOTE_AUTH_GROUP_SYNC_ENABLED is False") but resolves it by
silently ignoring the unused setting; orbital refuses instead, because a setting
that is present and inoperative is how someone comes to believe it is doing
something it is not.

- **`defaultRole` means "the role a NEW user is created with", never a fallback
  for unmatched groups.** Copied from NetBox's `REMOTE_AUTH_DEFAULT_GROUPS`
  wording. So a user who already has a role in orbital keeps it when their token
  carries nothing that matches — that is mode A working, not a bug.
- **Mode B denies on no match rather than falling back to readonly.** A provider
  may have thousands of users; a mapping enumerates which of them may use
  orbital. A readonly floor would silently grant read of the whole infrastructure
  graph to every employee. An adopter who wants a floor writes it as a rule — a
  group everyone is in, mapped to readonly — which is explicit and reviewable.
  Grafana's `role_attribute_strict` is the same rule.
- **Mode B re-derives at every login, so the provider wins in both directions.**
  Losing a group demotes; gaining one promotes, overwriting a local edit. That is
  what "the provider owns roles" means, and it is why the users page must not
  offer an edit there. NetBox behaves identically and documents it.
- **Most privileged rule wins** when several groups match (Grafana does the same).
- **`ORBITAL_ADMIN_EMAILS` applies in mode A only.** In mode B the groups are
  authoritative; a second mechanism that could grant admin outside the mapping
  would defeat the mapping. Mode B needs no bootstrap — whoever is in the admin
  group is admin from their first login.

### A role change takes effect at the NEXT login, not immediately

`SetUserSession` captures the role into the session cookie at sign-in, so a
provider-driven promotion or demotion lands when the user next authenticates —
not on their next request. "Re-derived at every login" means every login.

Worth knowing before an incident: revoking someone's admin group does **not**
end their current admin session. If that matters, the operator has to invalidate
the session, not just the group. NetBox and Grafana behave the same way; this is
the normal trade for session auth rather than an orbital quirk.

The bearer path has no such lag — there is no session, so every request carries
a token and the role is resolved from it each time.

### Debugging a mapping: claims are logged at DEBUG, tokens never

`ORBITAL_LOG_LEVEL=debug` logs the decoded ID token claims on the browser login
path. Off by default.

**Claims only, never the raw token.** A raw token is a bearer credential — anyone
with the log could replay it — while decoded claims cannot be replayed. GitLab
reached the same split for the same reason ([gitlab#345435](https://gitlab.com/gitlab-org/gitlab/-/issues/345435));
Grafana exposes the equivalent through `oauth.generic_oauth:debug`.

This exists because the ID token arrives **back-channel**, so an operator cannot
see it in the browser the way they can see a redirect. Without it, "no group
matched" is indistinguishable between a missing mapper, an unassigned role, a
claim of the wrong shape, and a name mismatch. The refusal log therefore also
names every claim the token carried and the groups claim's own value at WARN —
that is not debug-only, because a refusal nobody can diagnose is a refusal that
gets worked around.

### A provider only resolves rows it owns

`users.issuer` records which provider owns a row. A login whose issuer does not
match is refused `IDENTITY_CONFLICT`, and the row is left untouched. Without this,
any configured provider could mint a token for an existing address and inherit
that user's role — including the local break-glass admin, which is the account
that exists to survive a broken provider.

**One narrow claim path, and only one:** a row with **no issuer AND no password**
is claimed by the first provider that resolves it. Both halves are load-bearing —
no issuer means nobody owns it, no password means it was never a local account,
which is what keeps break-glass unclaimable however old the row is. Claiming
records ownership; it does not reassign a role.

That path exists because rows predating the `issuer` column have neither, so
without it every existing user is locked out of SSO until an admin backfills. It
stops firing permanently once a row has an issuer. Residual exposure: a deployment
configuring a second provider BEFORE its existing users have signed in once — an
unowned row goes to whichever provider asks first. First-come on a row nobody
owned, not theft from one that was.

**Do NOT widen it to "update the row when the issuer differs."** That is the whole
attack — it would let any configured provider take over an existing user,
including one holding a password. Pinned by
`TestIdentityOwnership_LocalAccountCannotBeClaimedByAProvider`,
`…SecondProviderCannotClaimAnotherProvidersUser`,
`…UnownedRowWithNoPasswordIsClaimedOnFirstLogin`, and the positive control
`…SameProviderResolvesItsOwnUserNormally` — three refusals would all pass against
an implementation that refused everything.

### Identities are keyed by email today, and that is the known weak point

`users.email` is the key, and OIDC is explicit that this is the wrong one: the
only guaranteed unique identifier for an end-user is `(iss, sub)`. An issuer may
reuse an email across different end-users over time, an end-user's email may
change, and `email`/`preferred_username` are mutable and can be falsified.

What holds the line in the meantime is the ownership rule above. Safe, but it
means
**one address cannot be shared across providers** — the same person reachable
through two IdPs can only use one.

**The fix is an identity-link table keyed `(issuer, subject)`**, which is what
every comparable product that stores users does: Keycloak's own
`federated_identity`, Auth0's account linking, django-social-auth's
`usersocialauth`. One user row, N provider links. Tracked in
[backlog.md](../planning/backlog.md).

**Do NOT reach for a username prefix instead.** One was built and removed
(2026-09-20). Prefixing is what STATELESS systems do — Kubernetes recommends a
per-authenticator `usernamePrefix` because it has no user table and nowhere to
store a link, so the prefix is the only way to keep two providers' subjects
apart. Orbital stores users, so it can store the link, and prefixing merely
papers over the wrong primary key while making every identifier uglier. Grafana
is the cautionary case at the other extreme: it supports neither, and its
documented answer is "make sure no user account overlaps between providers".

### Service accounts are app principals, not users

A client-credentials token is a valid caller — the in-pod cb-bundler authenticates
this way — but there is no human behind it, so it must not land in the users table
with a role. Detected as **carries a client identity (`azp`/`appid`) and no
`email` claim**, which is the generalisation of the test `BearerVerifier` already
applies. Such a caller gets `AppPrincipalPrefix + appid` as its name, an empty
email, and no provisioning.

**The gate is the provider entry's `clientID`, not `ORBITAL_APP_TOKEN_ALLOWED_APPIDS`.**
A token whose `azp` matches no entry is refused before any of this runs, so
listing the client IS the allowlist — per provider rather than one global list,
and the same mechanism that selects the entry. `ORBITAL_APP_TOKEN_ALLOWED_APPIDS`
belongs to the legacy single-issuer path and is **ignored** when
`ORBITAL_AUTH_PROVIDERS` is set; orbital logs a warning rather than leaving it
present and inoperative.

**This is not hypothetical and the trap is config-dependent.** Validating against
the dev Keycloak, a real service-account token provisioned
`service-account-armada-orbital` as a readonly user: Keycloak creates a real user
object for a service-enabled client, so `preferred_username` is populated while
`email` is absent — meaning the bug only fires for providers that map username to
`preferred_username`. Eleven synthetic tests passed while it happened. Kubernetes
gives service accounts their own `system:serviceaccount:` namespace and Grafana
and ArgoCD make them a separate principal type; none let one become a row in the
human user table.

### Break-glass when a provider breaks

**Local password login always works and is not gated on SSO.** In
`login-modal.gohtml` the `{{if .OIDCEnabled}}` block wraps only the Microsoft
button and the divider; the email and password form sits outside it. So an admin
with local credentials can always sign in and repair a broken provider or mapping.

This is the mechanism, deliberately — not special cases inside the authorization
path. ArgoCD keeps a local `admin` account as documented break-glass, Grafana
keeps local admin login wired alongside SSO, Kubernetes never relies on OIDC alone
(x509 client certs and the admin kubeconfig), Vault has the root token. **Do NOT
add "refuse to demote the last admin" or similar logic to the role path** — that
was considered and rejected as inventing a path upstream does not have.

`ORBITAL_ADMIN_EMAILS` is **not** this mechanism. It is bootstrapping — how the
first admin gets in. Break-glass is getting back in after the provider breaks.
If orbital ever gains an option to disable local password auth, the break-glass
procedure becomes "re-enable it" and must be documented when that option ships.

## External JWT mode (`ORBITAL_AUTH_MODE=external-jwt`)

An alternative auth stack that trusts bearer tokens issued by an external OIDC provider (e.g. AEP's Keycloak `aep-fleet-commander` client) instead of running orbital's own login flow. Intended for API-only integrations where an upstream service has already authenticated the user and is proxying to orbital.

**Config:**

| Var | Purpose |
|---|---|
| `ORBITAL_AUTH_MODE=external-jwt` | Enables this mode. Mutually exclusive with the normal OIDC + session flow. |
| `ORBITAL_JWT_ISSUER` | The OIDC issuer URL. JWKS discovered via `/.well-known/openid-configuration`. |
| `ORBITAL_JWT_AUDIENCE` | Required `aud` claim value. |
| `ORBITAL_JWT_CLIENT_ID` | Required `azp` claim value — the trust anchor. See below. |
| `ORBITAL_JWT_DEFAULT_ROLE` | Role every valid token maps to: `readonly`, `dev`, or `admin` (default **`readonly`** — least privilege, since this one value decides what every valid token may do). Leaving it unset logs a WARN naming the inherited tier. |

**What the middleware does per request:** if a `Bearer` token is present, validates signature via JWKS, checks `iss`/`aud`/`exp` (standard OIDC), checks `azp == ORBITAL_JWT_CLIENT_ID`, extracts `email`/`preferred_username`/`name`/`sub` onto context, sets `role` = configured default. No PostgreSQL provisioning — external users don't appear in orbital's `users` table; `RequireRole` reads `role` from context and grants directly. If NO bearer is present, it falls back to session-cookie auth (see below).

**UI still works — session fallback.** A browser can't attach a bearer to a plain page navigation, so external-jwt mode does NOT disable orbital's own login. The middleware accepts a bearer OR an authenticated session cookie. Humans sign in via local login (or OIDC if configured) and browse the UI with a session; AEP's proxied API calls carry a bearer. Session callers get their role from the DB (`RequireRole` looks it up by `user_id`) — the `ORBITAL_JWT_DEFAULT_ROLE` applies only to bearer callers. Login routes, session, and CSRF stay registered exactly as in the default mode.

**Internal services still work — AAD fallback (dual issuer).** external-jwt is **additive, not exclusive**: it ADDS Keycloak-user acceptance without removing the AAD bearer path that internal service callers depend on. The classic case is the in-pod cb-bundler, which calls back into orbital's `/graphql` during publish using an AAD client-credentials service token — if external-jwt replaced the AAD verifier, publish would 401 (regression fixed 2026-07-23). The middleware routes each bearer by its (unverified) `iss`: tokens from `ORBITAL_JWT_ISSUER` → Keycloak verifier (blanket role = `ORBITAL_JWT_DEFAULT_ROLE`); tokens from any other issuer → the AAD `BearerVerifier` fallback + `ResolveUser`. The `iss` peek only selects WHICH verifier runs; the chosen verifier still fully validates the signature. So orbital in external-jwt mode accepts **four** caller classes: Keycloak users (AEP, blanket role), AAD **service** tokens (in-pod bundler, via appid allowlist), AAD **user** tokens (**orbctl login** — verified via fallback, then given their real per-user `users.role`, NOT the blanket role), and session cookies (UI). orbctl compatibility requires `ORBITAL_OIDC_ISSUER_URL` + `ORBITAL_OIDC_CLIENT_ID` to be SET — they must match the AAD tenant + orbital app id that orbctl tokens carry. **Neither has a code default** (removed 2026-09-17, see above), so a deployment that does not set them explicitly builds no AAD fallback and **every orbctl token 401s**. `deploy/base/deploy.yaml` sets both; any new deployment path must too. Pinned by `TestExternalJWT_FallbackRoutesByIssuer` (Keycloak, AAD service, and AAD user sub-cases).

**Why `azp` is load-bearing (do not remove):** the demo config accepts `aud: "account"` because that's what Keycloak's built-in generic audience is, and we can't require the AEP team to add a custom orbital-scoped audience mapper to their `aep-fleet-commander` client for a demo. `aud` therefore doesn't identify *which* Keycloak client the token was minted for. Without the `azp` check, any token signed by the realm (including future clients registered by unrelated teams) would validate as an orbital admin. `azp` bounds trust to a specific client. RFC 9068 §4 explicitly requires client-id validation when authorization depends on client identity.

**Post-demo cleanup path:** if AEP adds an audience mapper that includes `orbital` in the token's `aud`, set `ORBITAL_JWT_AUDIENCE=orbital` and the `azp` check becomes redundant defense-in-depth (safe to keep, safe to relax). Do not remove the config field — it lets future integrations use the same fallback pattern.

**Non-goals of this mode:**
- Not for per-user role granularity *on bearer callers*. Every valid token maps to `ORBITAL_JWT_DEFAULT_ROLE`. If you need finer control, register a role mapper in the upstream OIDC provider and switch to the normal OIDC flow. (Session/UI users still get per-user roles from the DB.)
- Not silent SSO between AEP and orbital's UI. A human visiting orbital's UI directly logs in the normal way (local/OIDC); they do not inherit AEP's Keycloak session. Silent SSO would require registering orbital as a Keycloak client — deferred post-demo.
- Not production-safe as configured for the demo (all tokens → admin). Startup logs a WARN — heed it.

**Auth-failure logging attributes identity ONLY when the signature is valid.** An expired token (go-oidc checks `exp` *after* signature/iss/aud) and an `azp` mismatch (checked after `Verify()` succeeds) log `subject` (the stable, authoritative `sub`) + `user_email` — the claims are cryptographically authentic. Signature/issuer/malformed failures log the reason + source IP only, never the purported identity: those claims are attacker-controllable, and logging them as identity is a log-forging vector. Do NOT "simplify" this to always decode-and-log claims. Pinned by `TestExternalJWT_ExpiredToken_LogsIdentity`. Every auth-failure WARN carries `request.id` to correlate with the access-log line.

**The human identifier is email-preferred, matching `actorFromContext`.** Both the success path (`user_email` on context) and the failure logs use `externalJWTClaims.actorEmail()` — email, falling back to `preferred_username`. Same value everywhere so an operator grepping an email finds successful audit events AND auth failures. `preferred_username` alone is wrong here: OIDC does not guarantee it is unique or stable, and orbital keys identity on email.

**Regression guard:** `internal/auth/externaljwt_test.go` covers all reject paths (missing bearer, expired, wrong `aud`, wrong `azp`, missing `azp`) and the identity-logging convention. If you weaken any check, expect one of those tests to catch it.

## Third-party API clients

Orbital is an OAuth **resource server**, not an identity provider. Client applications authenticate with Azure AD directly and present the resulting JWT to orbital. Orbital does not issue tokens or proxy auth.

> **⚠️ Written for the Azure AD era and NOT updated for the Keycloak cutover
> (2026-09-20).** Orbital's deployed config now trusts Keycloak only, so the
> Entra-specific values below — tenant GUID, `api://…/.default` scopes, the
> `login.microsoftonline.com` issuer — would produce a token orbital rejects.
> The *topologies* below are still correct and provider-agnostic; only the
> concrete values are wrong. Use `ORBITAL_AUTH_PROVIDERS` (above) as the source
> of truth for what is actually trusted.
>
> Worth reading the OBO discussion with RFC 8693 in mind: Azure's
> On-Behalf-Of is the same delegation problem orbital now handles with
> `trustedService` plus an inferred `acting_client`, and token exchange is the
> standard version of it. See `docs/planning/backlog.md`.

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
- **Public client → orbital direct** — Authorization Code + PKCE, with a redirect URI registered with the IdP. No exchange, client requests `aud=orbital` upfront. "Public client" covers SPAs, native desktop, and mobile apps — anything that cannot keep a secret.
- **Headless service / scheduled job (no user identity)** — `grant_type=client_credentials` with `scope=api://<orbctlent-id>/.default`. Resulting token has no `preferred_username`, so `ResolveUser` cannot map it. Avoid until we add explicit service-account provisioning. Use OBO for any human-driven action, even from a backend.

**Scope policy** — orbital's App Registration exposes `user_impersonation` (Azure AD's default scope name when adding a delegated scope via "Expose an API"). Orbital does not validate `scp` — the scope name is consent-clarity only, not an authz boundary. `.default` works as a fallback but is discouraged because it requires admin consent and bundles all consented permissions opaquely.

**Pre-authorizing client apps** — the "Authorized client applications" section in "Expose an API" is left empty by default. Each client must request user (or admin) consent the first time. To skip the consent dialog for a specific trusted integrator, add their App Registration's client ID with the `user_impersonation` scope checked. Reserve this for fully trusted internal clients — leaves consent explicit and revocable for everyone else.

**First-call provisioning** — when a JWT arrives with an email not in the `users` table, `ResolveUser` middleware (see `internal/handler/authz.go`) auto-provisions a row with `role: readonly`. Admins listed in `ORBITAL_ADMIN_EMAILS` get promoted on first login. Tell integrators: their users will hit 403 on mutating calls until an admin promotes them via `/users`.

**Convenience endpoint to expose later** — a `GET /api/v1/whoami` would let integrators verify their token works and see the role orbital assigned, without making a mutating call. Not in scope today, but cheap to add if multiple integrators ask.

## Authorization

Role enforcement is done entirely at the Go middleware layer. DGraph `@auth` directives are out of scope — clients never reach DGraph directly (all queries go through the Go server), so DGraph-level authz adds no security value. Azure AD App Roles are deferred — we lack Application Administrator permissions on the tenant. If App Roles become available later, extract the `roles` claim at login and override the local role; the local table stays as source of truth.

**Role model:**
- `role` column on `users` ent schema: enum `admin` / `dev` / `readonly`, default `readonly`
- `ORBITAL_ADMIN_EMAILS` (comma-separated env var): on first OIDC login, matching emails are promoted to `admin`; all other users get `readonly`

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

