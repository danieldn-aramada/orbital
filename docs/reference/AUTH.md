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

- **Single GraphQL endpoint at `/graphql`.** Consolidated 2026-06-09 from the prior split (`root.Any("/graphql")` for session, `api.Any("/graphql")` for bearer). The split caused asymmetric authz: the bearer path had route-level `RequireRole(RoleDev)` which blocked readonly callers from running queries (which use POST), and the session path had NO route-level auth enforcement at all (leaking data to anonymous users). Now one path with the provider set's `RequireAuth()` (accepts session OR bearer) and `ResolveUser`; mutations enforced at the handler. The endpoint sits at `/graphql` rather than `/api/v1/graphql` — GraphQL is not URL-versioned (GitHub/GitLab/NetBox/Apollo convention); `/api/v1/` is reserved for REST endpoints where version semantics matter. Orb registers its own `/graphql` for symmetry.
- **Mutation authorization is enforced in the handler, not on the route.** `internal/handler/graphql.go` calls `isMutation(req.Query)` and, when true, checks `RoleAtLeast(role, RoleDev)`. The handler is the sole authoritative gate for write authz on the GraphQL endpoint — do not re-add `RequireRole(db, RoleDev)` to the route or you will block readonly POST queries.
- **`isMutation` is the security boundary, not a heuristic.** It must defend against bypasses (leading `#`-comments, multi-operation requests, string-literal smuggling, block strings). Strengthened 2026-06-09 to strip comments and string literals before matching `\bmutation\b`. See `internal/handler/graphql.go` and `TestIsMutation` cases. If you add features to the GraphQL parser path, add adversarial cases to that test.
- **Bearer verification exists only through `ORBITAL_AUTH_PROVIDERS`** (see below). The single-issuer path built from `ORBITAL_OIDC_ISSUER_URL` was removed — `ORBITAL_OIDC_*` now configures the browser login flow alone, where orbital is an OAuth *client* rather than a resource server. With API auth required and no provider list, orbital refuses to start rather than serving unauthenticated.
- **A verifier checks signature, issuer and audience — never `scp`/`scope`.** Authorization is orbital's, enforced downstream by `RequireRole` against `users.role` or a provider-assigned role. Adding scope checks would duplicate that and tie orbital to one provider's conventions.

### App callers are gated by the provider entry's `clientID`

An **app-only** (client-credentials) caller carries a client identity
(`azp`/`appid`) and no `email`. The in-pod cb-bundler is the worked example: it
mints a token to call `/graphql` during publish. Such a caller gets no user row
and `dev`-equivalent access at `RequireRole`.

**Whether a caller is an app principal is a fact the verifier records, never
something derived from its name.** `auth.MarkAppPrincipal` sets it on the request
context and `auth.IsAppPrincipal` is the single definition of the question, used
by both `ResolveUser` (skip provisioning) and `RequireRole` (grant the tier).
`AppPrincipalPrefix` ("app:") is the **display label only** — it gives the audit
record something to show when there is no email.

**Do NOT classify by `strings.HasPrefix(user_name, AppPrincipalPrefix)`.** That
was the shape until 2026-09-21 and it was forgeable: `user_name` for a human is
the token's `name` claim, which in Keycloak the end-user edits in their own
account console. A human token whose name claim began `app:` took the
app-principal branch in `ResolveUser` — skipping provisioning *and* the
provider's role — and was then granted dev-equivalent write access by
`RequireRole`. Pinned by `TestRequireRole_ForgedAppNameIsNotAnAppPrincipal` and
`TestProviderSet_HumanTokenCannotForgeAnAppPrincipal`, both of which fail against
the prefix implementation.

Kubernetes uses a username prefix for the same purpose (`system:serviceaccount:`)
and gets away with it only because it **reserves** the namespace and is
stateless — there the username string IS the identity. Orbital has a request
context and can carry the fact explicitly, which is the same reason
`usernamePrefix` was rejected for identities (see § Identities are keyed by email).

**The gate is the provider entry whose `clientID` matches the token's `azp`, and
nothing else.** A token from an unlisted client is refused during provider
selection, before any role logic runs, so **listing a client IS the allowlist** —
per provider rather than one global list, and the same mechanism that selects the
entry. Pinned by `TestProviderSet_AppTokenIsGatedByClientID`, both halves: a
listed client authenticates as an app principal, an unlisted one gets 401 and no
identity.

**A deployment whose services authenticate with client credentials MUST list
those clients**, or publish 401s. There is no wildcard and no default.

**An app caller is `dev`-equivalent, and that is the whole of its authorization.**
`RequireRole` grants dev to any app principal, so **listing a client in
`ORBITAL_AUTH_PROVIDERS` grants it write access to everything a dev can write** —
there is no per-client scoping. cb-bundler needs far less than that. Decided
2026-09-22 to keep it and say so rather than build scoping nobody has asked for:
the alternative is a role on the provider entry for app callers (the shape
`delegatedAuthorization.role` already uses for user-bearing tokens), which is
cheap to add when a second machine caller needs something narrower. **Treat the
client list as a privilege grant, not an identity list.**

*This replaced `ORBITAL_APP_TOKEN_ALLOWED_APPIDS`, a global list of application
ids on the removed single-issuer path. Its one lasting rule is kept here: "I have
not configured this" and "I intend to allow everything" must never be the same
input. That equivalence is a documented, exploited failure mode rather than a
theoretical one — AWS IAM trust policies omitting the `sub` condition accepted
tokens from ANY GitHub Actions workflow (Datadog Security Labs, 2023), and AWS
now refuses to create such a policy at all. Under the provider list an empty
config cannot be permissive: there is no entry to match, so nothing
authenticates.*

**User tokens are unaffected.** The app-principal branch requires an absent
`email`, so a human caller carrying an `azp` claim is never treated as a service.

## Multiple identity providers (`ORBITAL_AUTH_PROVIDERS`)

Orbital's API is a resource server: it verifies bearer tokens it did not issue,
holds no client secret, and has no login flow of its own — the same role the
kube-apiserver plays. The browser UI is a separate thing (a confidential OAuth
client, which is what `ORBITAL_OIDC_CLIENT_SECRET` is for) and orbctl a third (a
public client using authorization code + PKCE). Keep the three apart; only the
first is what this section describes.

- **Trust is a LIST of providers, not one, and not a mode.** `ORBITAL_AUTH_PROVIDERS`
  is a JSON array decoded by an `envconfig.Decoder`, the same shape `ORB_CONSUMERS`
  uses. It is the ONLY source of bearer verification: unset, there is none, and a
  deployment with API auth enabled refuses to start rather than serve
  unauthenticated. It superseded both `ORBITAL_AUTH_MODE=external-jwt` and the
  single-issuer path built from `ORBITAL_OIDC_ISSUER_URL` — removed rather than
  left as a second source of truth for who may authenticate.
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
- **Decoding is strict: an unrecognised field fails startup.** `encoding/json`
  drops what it does not know, which in an auth config means a constraint the
  operator wrote silently not applying — `trustedService` kept its old spelling
  after the rename and would have quietly stopped granting anything, and
  `client_id` (the OAuth spelling of `clientID`) would leave an entry matching
  every client of its issuer. Kubernetes decodes `AuthenticationConfiguration`
  strictly for the same reason. **Do not relax this to make a migration easier** —
  a config that half-applies is the failure this prevents.
- **`issuer.audiences` is any-of, and it must name orbital — never `account`.**
  RFC 7519 §4.1.3 and RFC 9068 §4: the token must carry at least one listed value,
  and that value has to identify this resource server. `account` is Keycloak's
  stock audience for every client in a realm (`default-roles-<realm>` → account
  roles → the audience-resolve mapper), so listing it makes the check vacuous and
  leaves `clientID` carrying the whole load.
- **A Keycloak client that is also the resource server needs its OWN Audience
  mapper.** `AudienceResolveProtocolMapper` deliberately SKIPS the client named in
  `azp`, so `armada-orbital`'s own tokens never carry `aud: armada-orbital` from
  the built-in mapper — only tokens from *other* clients holding its roles do
  (which is why AEP's did and the cb-bundler's did not). Fix is one Audience mapper
  on that client's dedicated scope, *Included Client Audience* = itself. Verified
  against the dev realm 2026-09-21; do not "fix" a missing audience by re-adding
  `account`.
- **Orbital does not verify `azp` on the browser ID token.** go-oidc says so at
  `verify.go:295` ("This check DOES NOT ensure that the ClientID is the party to
  which the ID Token was issued"), and orbital adds nothing. Tolerable because the
  ID token arrives back-channel from the code exchange and is never accepted as an
  inbound credential, and OIDC Core §3.1.3.7 step 4 only asks for it when `aud`
  holds more than one value. The BEARER path does check `azp` — structurally, via
  the provider entry's `clientID`.
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
provider, and the same mechanism that selects the entry. There is no second,
global allowlist to keep in sync (see § App callers above).

### `delegatedAuthorization`: the decision happens upstream

A caller whose own authorization layer decides what its users may do. `AEP FC`
authenticates and authorizes through `organization-svc`, then calls orbital on a
user's behalf; it does not configure users in orbital or Keycloak.

    "clientID": "aep-fleet-commander",
    "delegatedAuthorization": { "role": "admin" }

Every valid token gets that role, on every request, and **no user row is
provisioned** — the caller is a service acting for its own users, not an orbital
user, so it gets the same treatment as an app principal for the same reason.

**The name is RFC 8693 §1.1's, and both halves are load-bearing.** That section
separates *impersonation* (the resource server cannot tell client from user) from
*delegation* (both identities survive) — orbital keeps the human in `user_email`
and the client in `acting_client`, so this is delegation. And only
*authorization* is delegated: orbital still authenticates the token itself
against the issuer's JWKS. The inner key is `role`, not `assignedRole`, because
the parent already supplies the qualifier — the same reason k8s writes
`issuer.url` and RFC 8693 puts `sub` inside `act`.

It was previously `ORBITAL_JWT_DEFAULT_ROLE` in the removed `external-jwt` mode —
a default that happened to apply to everyone, which reads like an unset value
rather than a decision. Renamed from `trustedService` 2026-09-21: every provider
in the list is trusted, so the word named nothing, and it described what the
caller *is* while its two siblings describe what orbital *does*.

**What it costs, stated plainly:** orbital's own authorization is not the control
for these callers, so if the upstream service fails open, orbital does too. What
limits the damage is `acting_client` on audit events (see `AUDIT.md`), which is
how the log distinguishes "AEP acting as someone" from that person acting
directly.

**These callers are invisible on the Users page, so that page does not answer
"who has admin".** No row is provisioned, so `/users` lists only the identities
orbital itself authorizes — complete for those, and silent about delegated ones.
The authoritative answers live elsewhere: the provider list says which clients are
delegated and to what role, and the audit log says who actually acted. **Do NOT
"fix" this by provisioning rows for delegated callers** — orbital would then hold
a role it does not own, cannot revoke, and would not correct when the upstream
service changes its mind. **The standard answer is RFC 8693 token exchange**, where the IdP signs
an `act` claim instead of orbital inferring one from `azp` — filed in
`backlog.md`. `azp` names only the last client in a chain and does not nest;
`act` does. Treat the current shape as the interim it is.

### Roles: `delegatedAuthorization` is exclusive, `defaultRole` and `roleMapping` compose

Orbital owns authorization. A provider asserts identity and group membership;
orbital's operator decides what that membership is worth. **A provider never
names an orbital role** — `roleMapping` is written by the operator.

Three settings decide where a caller's role comes from, and the legal
combinations are not symmetric:

| | `defaultRole` alone | `roleMapping` alone | `roleMapping` + `defaultRole` |
|---|---|---|---|
| Who owns the role | orbital's users table | the provider's groups | the provider's groups |
| New user | created with `defaultRole` | created with the matched role | matched role, else the floor |
| A group matches | n/a | applies at every login, overwriting a local edit | same |
| No group matches | n/a | **denied** — 401, no user created | **floor**, but only for a row this provider already owns |
| Later logins | table wins; admin edits stick | re-derived every login | re-derived every login |
| Users page | role editable | read-only, with provenance | read-only, provenance reads `(no group matched — defaultRole)` |

Plus `delegatedAuthorization`, which owns the role wholesale and stores nothing.

**`delegatedAuthorization` cannot be combined with either of the others, and a
provider carrying none of the three is a startup error** (`AuthProviders.Validate`).
Pairing `delegatedAuthorization` with a setting that writes to the users table would be
two answers to one question; a provider with none of them would accept tokens it
could do nothing with. NetBox makes its equivalent pair exclusive
(`REMOTE_AUTH_DEFAULT_PERMISSIONS` is "only operational when
REMOTE_AUTH_GROUP_SYNC_ENABLED is False") but resolves the conflict by silently
ignoring the unused setting; orbital refuses at startup instead, because a
setting that is present and inoperative is how someone comes to believe it is
doing something it is not.

**`defaultRole` alongside `roleMapping` is legal and means "map, else this" —
the floor.** Pinned by
`TestAuthProviders_DefaultRoleAlongsideRoleMappingIsTheFloor`, and the
`dev-netbox` overlay ships it on the `armada-orbital` provider. Grafana exposes
the same pair as `role_attribute_path` plus `role_attribute_strict = false`.
**Drop `defaultRole` to make an unmatched login a refusal** — that is the choice
the pairing expresses, so do NOT "simplify" the two into one setting.

- **`defaultRole` alone means "the role a NEW user is created with", never a
  fallback for unmatched groups.** Copied from NetBox's
  `REMOTE_AUTH_DEFAULT_GROUPS` wording. So a user who already has a role in
  orbital keeps it when their token carries nothing that matches — that is the
  setting working, not a bug.
- **`defaultRole` as a floor is the opposite: authoritative, not a seed.** It is
  applied at every login, so a user removed from their mapped group is demoted to
  the floor rather than keeping what the mapping last gave them — otherwise
  revocation would silently fail. It applies only to rows whose `role_source` is
  already `provider` (`providerRoleApplies`), so an admin's local promotion of
  someone the mapping never covered is not reverted. A new user provisioned under
  the floor is created at the floor role and marked provider-owned.
- **A mapping with no floor denies on no match rather than falling back to
  readonly.** A provider may have thousands of users; a mapping enumerates which
  of them may use orbital. An implicit readonly floor would silently grant read of
  the whole infrastructure graph to every employee — which is why the floor has to
  be written down to exist. Grafana's `role_attribute_strict` is the same rule.
- **A mapping re-derives at every login, so the provider wins in both
  directions.** Losing a group demotes; gaining one promotes, overwriting a local
  edit. That is what "the provider owns roles" means, and it is why the users page
  must not offer an edit there. NetBox behaves identically and documents it.
- **Most privileged rule wins** when several groups match (Grafana does the same).
- **`ORBITAL_ADMIN_EMAILS` is inert on the login path wherever a `roleMapping` is
  configured** — including when the floor fires, because the floor arrives as a
  provider-derived role like any other. A second mechanism that could grant admin
  outside the mapping would defeat the mapping. **It is not inert at startup:**
  `ReconcileAdminEmails` promotes a listed existing row to admin on every boot,
  and that promotion is local (`role_source` stays local), so the floor will not
  revert it — a matched group will, at that user's next login.

### Who owns a role is tracked per user, and the UI must say so

`users.role_source` records whether the provider or an admin last set a role.
`NULL` counts as local, so a role predating the column sticks.

- A **matched group always wins**. Being added to `orbital-admin` is explicit and
  overrides an earlier local edit.
- The **`defaultRole` floor applies only to users the provider already owned**, so
  an admin promoting someone the mapping never covered is not reverted at their
  next login.
- An admin setting a role through the users API marks it `local`.

**This is NOT conventional and must not be defended as such.** Grafana's own docs
say manual role changes *"will be overwritten on next user login"*, with
`skip_org_role_sync` as a global all-or-nothing escape; NetBox's
`REMOTE_AUTH_GROUP_SYNC_ENABLED` is the same shape. Neither tracks provenance per
user. Orbital does because the two cases are otherwise indistinguishable —
"removed from a group" and "never in a group, promoted locally" produce identical
inputs — and each alternative breaks one of them.

**The column earns its place only while the users page distinguishes the two.**
A provider-set role renders read-only with provenance; a locally-set one stays
editable. A hidden per-user rule is worse than either blanket rule, because nobody
can predict what their next login does. **If that UI distinction is ever removed,
delete the column and follow Grafana** — the silent revert those products are
criticised for is what an invisible version of this would be.

Survey and reasoning: [`spike-26-auth-providers.md`](../spikes/spike-26-auth-providers.md)
§ Role ownership when an IdP and a local table disagree — point-in-time evidence,
not normative.

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
`email` claim**. Such a caller is marked an app principal by the verifier
(`auth.MarkAppPrincipal`), gets `AppPrincipalPrefix + appid` as its **display**
name, an empty email, and no provisioning.

**The gate is the provider entry's `clientID`.** A token whose `azp` matches no
entry is refused before any of this runs, so listing the client IS the
allowlist — per provider rather than one global list, and the same mechanism that
selects the entry. Pinned by `TestProviderSet_AppTokenIsGatedByClientID`.

**This is not hypothetical and the trap is config-dependent.** Validating against
the dev Keycloak, a real service-account token provisioned
`service-account-armada-orbital` as a readonly user: Keycloak creates a real user
object for a service-enabled client, so `preferred_username` is populated while
`email` is absent — meaning the bug only fires for providers that map username to
`preferred_username`. Eleven synthetic tests passed while it happened. Kubernetes
gives service accounts their own `system:serviceaccount:` namespace and Grafana
and ArgoCD make them a separate principal type; none let one become a row in the
human user table.

### Auth failures never log a purported identity

`ProviderSet` logs the issuer, the reason and `request.id` on a refusal — never
an email or `sub` decoded from the token. Claims on an unverified token are
attacker-controllable, and writing them into the log as identity is a
log-forging vector. **Do NOT "improve" this by decoding claims for the failure
log.** The cost is real and accepted: an operator cannot grep failed sign-ins by
email, only successful ones. The removed external-jwt verifier drew the line
differently — identity after the signature verified, never before — which is the
shape to copy if this is ever revisited.

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
> `delegatedAuthorization` plus an inferred `acting_client`, and token exchange is the
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

## CSRF on cookie-authenticated API calls

`internal/auth/csrf.go`, wired into `apiAuth` in `internal/server/server.go`, so it covers
`/graphql` and `/api/v1`. On any non-GET/HEAD/OPTIONS request **not** carrying a bearer token, the
request must vouch for its own origin:

1. **A stated `Origin` (or, failing that, `Referer`) is authoritative** — same host passes, any
   other host is refused. Browsers set `Origin` on every non-GET request, so this is the path
   essentially all browser traffic takes.
2. **If it states neither, and carries a content type only an HTML form can produce**
   (`application/x-www-form-urlencoded`, `multipart/form-data`, `text/plain`) — refuse. A browser
   form post always states an `Origin`, so a request with none is normally a non-browser client;
   this backstop is what makes step 1's fail-open safe rather than merely convenient. Strip
   `Origin` and a cross-site form still does not get through.

`application/json` is unreachable from a cross-site form, and a cross-origin `fetch` sending it
triggers a CORS preflight that fails because orbital configures no CORS policy. Compare Apollo
Server's `csrfPrevention` and OWASP's non-simple-request defence for API endpoints.

**Order matters, and it is not the obvious one.** Content-type-first was the first cut, and it was
wrong: **htmx encodes as `application/x-www-form-urlencoded` by default**, and two `hx-post`
attributes target `/api/v1` (`backup/test-connection`, `divergence/test-connection`). A
content-type test applied ahead of the Origin check 403s every htmx mutation — the same class of
breakage as the token it replaced, arrived at from the other direction. Do not reorder these.

**Bearer callers are exempt from both** — orbctl, AEP Fleet Commander and cb-bundler. A bearer is
not an ambient credential, so CSRF does not apply; gating them on browser headers they never send
would break every API client. The check is `strings.CutPrefix(Authorization, "Bearer ")`.

**Absent `Origin` *and* `Referer` is allowed for a non-form content type, deliberately.** A
cross-origin POST from a browser always carries `Origin`, so a request with neither did not come
from a browser form and is not the threat. This keeps cookie-authenticated scripts working
(`scripts/e2e-divergence.sh` logs in with a cookie jar, then POSTs JSON to `/api/v1/export` with
no `Origin`). A header that *is* present but opaque (`null`) or unparseable is refused, not waved
through — present-but-unusable is not the same as absent.

**HTML form routes are outside this group and unchanged.** `POST /user/login` and
`POST /user/logout` are `root.POST` routes that keep the synchronizer-token pattern — a hidden
`csrf` field validated by `auth.ValidateCSRF`. Check 1 would refuse them, and must never reach
them. `GetOrCreateCSRF`/`ValidateCSRF` exist for that path only.

### Why not a CSRF token on the API

**It was a token, from the auth refactor until v0.0.46, and the token is what broke.** A token has
to be attached by every client transport. Orbital has three — `fetch`, htmx's XHR, and the jQuery
XHR behind DataTables. `shared.js` wrapped the first two; nobody wrapped the third, so every
DataTable POST arrived without the header and v0.0.46 took out the Data Centers, Servers, Clusters
and Network pages at once. Nothing failed at build time; it surfaced as a 403 in production.

Checking a property the honest client **already needs** — a JSON content type to call a JSON API,
a same-origin `Origin` the browser sets itself — leaves nothing for the next transport to forget.
That is the whole reason for the shape. Do not reintroduce a header the client has to be taught to
send.

**`SameSite=Lax` remains** and remains a real control. This is defence in depth: set
`SameSite=None` to embed orbital's UI in another product's frame and check 2 is what still stands.

## Session cookie Secure flag

- **`ORBITAL_COOKIE_SECURE` (default `true`) is the only source of truth for the cookie's `Secure` attribute** — decoupled from the old `ORBITAL_DEV` because that flag bundled unrelated dev behaviour. `ORBITAL_DEV` was itself split and removed on 2026-09-23 for the same reason ([CONFIG.md](./CONFIG.md)); this setting was simply first. Do NOT re-derive `Secure` from any mode flag — pinned by `auth.TestCookieSecure_FollowsConfig`.
- **AKS dev sets `ORBITAL_COOKIE_SECURE=false`** because Istio is HTTP-only there; otherwise the browser silently drops the cookie and every login appears to fail. Remove the override once TLS lands.

## orbauth shared package

- `internal/orbauth/` — PKCE flow, token exchange, refresh, `Store` interface, `FileStore` (the only implementation). Both `orb` and `orbctl` import it. Neither CLI contains auth logic directly.
- `orb login` (at `~/.orb/credentials.json`) and `orbctl login` (at `~/.orbital/credentials.json`) now follow identical patterns — single file, full credentials including refresh token.

## `orbctl get datacenter` CLI

- Resolves identifiers in order: `0x`-prefix → DGraph UID, contains `:` → orbId, otherwise tries orbId then name.
- POSTs to `/graphql` with `Authorization: Bearer` header.

