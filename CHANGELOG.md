# Changelog

Notable changes to **orbital** (cloud), **orb** (edge), and **orbctl** (CLI).

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are
[semantic](https://semver.org/), tagged per component:

| Component | Tag prefix |
|---|---|
| orbital (server) | `v*` |
| orb (edge) | `orb/v*` |
| orbctl (CLI) | `cli/v*` |

**This file is the source of truth**, not the GitHub release page. Orbital targets air-gapped
deployments — an operator holding the tarball and no network connection must still be able to read
what changed. GitHub Release bodies are generated from this file, never the other way round.

> Releases before **v0.0.31** (2026-08-12) predate this changelog. For that history see
> `git log` and `git tag --sort=-creatordate`; the delivery timeline is in `ROADMAP.md`.

---

## [Unreleased]

### Fixed
- **Three browser-login failures redirected to the wrong page under a base path.**
  `invalid_state`, `no_id_token` and the new nonce refusal redirected to
  `/?error=…` while every other login error used `basePath + "/?error=…"`. On a
  deployment served under `/orbital` those three bounced to the cluster root, so
  the user saw someone else's 404 rather than orbital's sign-in message.

### Changed
- **Browser-login error codes joined the registry.** `invalid_state`,
  `no_id_token` and `invalid_nonce` were raw strings while every neighbouring
  refusal used a registry code, so the login modal rendered them bare instead of
  as `error — hint`. Now `INVALID_STATE`, `NO_ID_TOKEN` and `INVALID_NONCE`, each
  with an operator-readable explanation and a row in `ERROR-RESPONSES.md`.
- **~22 comments citing `ADR 0NN` now cite the domain docs.** `docs/decisions/`
  was removed on 2026-07-07 and the pointers went with it, leaving code that
  referred readers to files that had not existed for months — ADR 012 in the
  divergence paths, ADR 010 in the app-principal paths, 007 in the schema-version
  note, 002 in the delete handler. Two citations were *removed* rather than
  repointed: `DIVERGENCE.md` cited the ADR it had absorbed, and a `UI.md` bullet
  about shared JS navigation cited an ADR about shared ConfigItem handlers in Go —
  a wrong pointer is worse than a dangling one. The runbook mention stays, since
  it explains the removal and how to recover the file from git.

- **CSRF protection on cookie-authenticated API calls no longer depends on the
  client attaching a token** (`internal/auth/csrf.go`). The token guard added in
  the auth refactor required `X-CSRF-Token` on every non-GET request to
  `/graphql` and `/api/v1`. `shared.js` attached it for `fetch` and for htmx, but
  not for the jQuery XHR behind DataTables — so **v0.0.46 returned 403 on the
  Data Centers, Servers, Clusters and Network list pages**. A token has to be
  wired into every client transport, and this app has three.

  The guard now reads what an honest client already sends. A stated
  `Origin` (or `Referer`) is authoritative — same host passes, another host is
  refused. If a request states neither *and* carries a content type only an HTML
  form can produce (`application/x-www-form-urlencoded`, `multipart/form-data`,
  `text/plain`), it is refused; that backstop is what makes the first rule's
  fail-open safe. `application/json` is unreachable from a cross-site form, and a
  cross-origin `fetch` sending it triggers a CORS preflight that fails because
  orbital configures no CORS policy — the same approach as Apollo Server's
  `csrfPrevention`. No client-side code is involved, so a future transport cannot
  forget it.

  The order is deliberate: **htmx encodes as `application/x-www-form-urlencoded`
  by default**, and two `hx-post` attributes target `/api/v1`, so testing content
  type ahead of `Origin` would have 403'd every htmx mutation — the same failure
  as the token, from the other direction.

  **Bearer callers stay exempt** — orbctl, AEP Fleet Commander and cb-bundler are
  unaffected, as before. **Login, logout and register are unaffected**: they POST
  form-encoded to `root` UI routes outside this group and keep their hidden
  `csrf` field. Requests with neither `Origin` nor `Referer` are allowed
  deliberately — a browser always sends `Origin` cross-origin, so their absence
  means the call did not come from a browser form. `SameSite=Lax` is unchanged.

  `ORBITAL_CONFIG.csrfToken` and the `fetch`/htmx wrappers in `shared.js` are
  removed. See `docs/reference/AUTH.md` § CSRF on cookie-authenticated API calls.

### Added
- **`Rack.uHeight`** — rack height in units (`schema/VERSION` → **v11**, apply
  per cluster). Editable from the DataCenter page and shown as **Height (U)**;
  unset renders as an em dash, never `0`. Nullable by design — 42U is common but
  not universal, so orbital records it rather than assuming it.

  **Do not compute a rack's valid unit range as `1..uHeight`** — that needs
  `startingUnit` and `descUnits`, which orbital does not model yet. See
  `DGRAPH.md` § Schema rules.

- **`Server.uHeight`** — how many rack units a server occupies (`schema/VERSION`
  → **v12**, apply per cluster). Pairs with `Rack.uHeight`: how many units the
  rack has, versus how many a server takes. `Float` and nullable — `0` means a
  rack-mounted device that consumes no unit, which is not the same as unset.
  Sizes vary from 1U to 6U, so there is no default. Editable in the config editor
  and shown on the server page.

- **`version` is shown in the Metadata panel** on the Server, Data Center, Cluster
  and Network Device tabs. It is the value a caller needs to guard a write
  (`"version": <n>` in the mutation's variables → `409 MVCC_CONFLICT` on a
  concurrent edit), and it was the one ConfigItem field the UI never surfaced.
- **`Server.serialNumber`** is editable in the config editor, shown on the server
  detail tab, and carried in audit diffs — added to `FormFields` and
  `BeforeFields` in `internal/configitems/registry.go` and to the `GetServer`
  query. Without the registry entry the editor silently dropped the key: the save
  reported success and bumped `version`, but nothing was written. See the two new
  rows in `docs/planning/debt.md`.
- **`Server.serialNumber`** (`schema/VERSION` → `v10`). Holds Redfish
  `ComputerSystem.SerialNumber` verbatim, alongside the existing `serviceTag`.
  The two are **not** interchangeable: an R450 reports `DLP6K74` for both, while
  an R650 reports `SKU=CFRHDX3` and `SerialNumber=MXFC400359006Z` — confirmed
  against live iDRAC 7.20.10.05. Added so consumers can compute serial-number
  drift per server, which was impossible while orbital stored only the service tag.

  `serviceTag` remains the `orbId` natural key and is unchanged; `serialNumber`
  is a plain attribute and must never be used for identity. **Requires a DGraph
  schema alter on deploy** (`deploy/README.md` § 8) — the field is
  indexed `@search(by: [hash])`, so servers can be looked up by serial.

### Changed
- **Local DGraph export directories moved from `/tmp/orbital-test-*` to
  `.local/exports/{blue,scratch,test}`.** **Every developer must recreate their
  stack once** (`make down && make up`, then `make seed`) — until they do, their
  containers stay bound to the old paths while orbital reads the new ones, which
  produces exactly the failure this change removes.

  `/tmp` was the wrong home on two counts. macOS prunes it periodically, and
  removing an export *directory* under a running container leaves a stale bind
  mount: the mount was resolved at container start, so the container holds the
  old inode and the two sides stop agreeing about that path. Re-creating the
  directory on the host does not repair it — only restarting the container does,
  which is why `make up`'s `mkdir` could never help. The symptom landed much
  later as a backup failing with *"no json.gz found after export"*, and it was
  diagnosed as a product bug more than once. Second, `/tmp` is shared between
  checkouts: two clones of orbital clobbered each other's exports.

  `.local/` is already gitignored and already the home for local-only files, so
  ignored artifacts now live in the ignored tree rather than inside tracked
  `deploy/local/`. Paths are relative in both places but spelled differently —
  compose resolves against its own directory (`../../.local/exports/blue`), Go
  against the process working directory (`./.local/exports/blue`).

  Two incidental fixes carried along: the containerized orbital no longer needs a
  path-identity mount (`/tmp/x:/tmp/x`) now that it sets
  `DGRAPH_SCRATCH_EXPORT_DIR` explicitly, and the constant `blueExportDir` —
  which pointed at the *test* alpha's directory, not blue's — is now
  `testAlphaExportDir`.

- **`docs/auth.md` rewritten for the provider list.** The integrator-facing guide
  still described Entra: MSAL libraries, tenant GUIDs,
  `api://<client>/user_impersonation` scopes, an On-Behalf-Of code sample, and
  `orbctl login` as the primary way to get a token. Every one of those produces a
  token current orbital rejects, and the CLI's login is broken pending redesign.
  It now says what an integrator actually needs: that orbital is a resource server
  which issues nothing, the four things to ask the operator for (issuer, the
  client id whose `azp` must match, the required audience, the group-to-role
  mapping), the three caller shapes (client credentials as an app principal,
  delegated for a backend acting for its own users, PKCE for a human), how a role
  is derived, and the errors they will actually hit. Provider-neutral throughout —
  no vendor is named, because the adopter's IdP is theirs. 176 → 120 lines.

- **The sign-in button no longer says "Sign in with Microsoft".** It reads
  `Sign in with {ORBITAL_OIDC_DISPLAY_NAME}`, default **`SSO`**, with a neutral
  icon; `static/logo/microsoft.svg` is deleted. The button named a vendor orbital
  no longer trusts — the deployed IdP is Keycloak — but the fix is not to hardcode
  the new one: the adopter's IdP is theirs, so an operator sets `Okta`,
  `Keycloak`, `Entra ID` or whatever their users recognise. Follows ArgoCD's
  `oidc.config.name` and Grafana's generic-OAuth `name`, both of which default to
  something provider-neutral. If browser login ever accepts several providers,
  the same field scales to one button each — the Grafana and GitLab shape.

  An adopter who wants a branded button sets `ORBITAL_OIDC_ICON_URL` to an image
  **they** host or mount; empty renders a neutral glyph. **Orbital ships no vendor
  logos on purpose** — "Sign in with Microsoft" and its Google equivalent are
  specified brand treatments with mandated wording and dimensions, and shipping
  those marks in an open-source repo hands a trademark obligation to every adopter
  and fork. The operator has the relationship with their IdP, so they supply the
  asset and the label. Gitea takes the same approach with its per-source icon.

- **BREAKING — `trustedService` is now `delegatedAuthorization`, `claimValidationRules`
  is removed, and unrecognised fields fail startup.** Three changes to
  `ORBITAL_AUTH_PROVIDERS`, which shipped in v0.0.44; each needs a config edit.

  `"trustedService": { "assignedRole": "admin" }` becomes
  `"delegatedAuthorization": { "role": "admin" }`, with behaviour unchanged. The old
  name described what the caller *is* while its two siblings describe what orbital
  *does*, and "trusted" named nothing — every provider in the list is trusted. The
  new name is RFC 8693 §1.1's: this is delegation rather than impersonation, since
  both identities survive (the human in the audit actor, the client in
  `acting_client`), and only *authorization* is delegated — orbital still
  authenticates the token itself against the issuer's JWKS. The inner key is `role`
  because the parent already supplies the qualifier, the same reason Kubernetes
  writes `issuer.url` and RFC 8693 puts `sub` inside `act`.

  **`claimValidationRules` is removed.** It existed to anchor `azp`; `clientID`
  superseded that job and nothing else ever used it — both its tests still asserted
  the `azp` case. Use `clientID` for `azp` and `issuer.audiences` for `aud`; a rule
  on either was redundant at best, and `{"claim": "aud", …}` in particular rejected
  every Keycloak token, because the comparison is string-only while Keycloak issues
  `aud` as an array.

  **Unrecognised fields now fail startup.** `encoding/json` drops what it does not
  know, so without this both changes above would silently disarm a working config
  instead of refusing it — and `client_id`, the OAuth spelling of `clientID`, would
  leave an entry matching every client of its issuer. Kubernetes decodes
  `AuthenticationConfiguration` strictly for the same reason.

  **Roll the config and the image together.** A v0.0.44 orbital reading a
  `delegatedAuthorization` config ignores the field and then refuses to start
  (*"set defaultRole, roleMapping or trustedService"*), and this build refuses a
  `trustedService` config by name.

### Removed
- **BREAKING — the single-issuer bearer path is gone, and with it
  `ORBITAL_APP_TOKEN_ALLOWED_APPIDS`.** Bearer verification now exists only
  through `ORBITAL_AUTH_PROVIDERS`: **a deployment with API auth enabled and no
  provider list refuses to start** rather than falling back to a second path.
  `ORBITAL_OIDC_*` is unchanged and still drives the browser login flow — orbital
  is an OAuth *client* there and a resource server here, and only the second one
  moved.

  The allowlist existed because the old path trusted exactly one
  `(issuer, client)` pair, so "which application minted this token" needed a
  separate global list. A provider list is keyed on `(iss, azp)`, so the entry
  **is** the allowlist: a client-credentials caller authenticates iff some entry's
  `clientID` matches its `azp`, and is refused before any role logic otherwise.
  Its one lasting rule survives in `AUTH.md` — an unset allowlist must never mean
  "allow everything" — and under the provider list an empty config cannot be
  permissive, since there is no entry to match. Pinned by
  `TestProviderSet_AppTokenIsGatedByClientID`, which asserts both the listed and
  the unlisted case.

  **`deploy/base` no longer names an identity provider at all.** The issuer,
  client id, token URL and provider list moved to the overlays, because base must
  not point an adopter's deployment at someone else's IdP. `dev-netbox` carries
  them for both the orbital container and the in-pod cb-bundler; an overlay
  without a provider list will not start.

- **BREAKING — `ORBITAL_AUTH_MODE=external-jwt` is gone**, along with
  `ORBITAL_JWT_ISSUER`, `ORBITAL_JWT_AUDIENCE`, `ORBITAL_JWT_CLIENT_ID` and
  `ORBITAL_JWT_DEFAULT_ROLE`. `ORBITAL_AUTH_PROVIDERS` supersedes it: a
  `delegatedAuthorization` entry is the same behaviour (every valid token gets one
  role, no user row) keyed on `(iss, azp)` rather than globally, so the
  dual-issuer fallback the mode carried for AAD service and orbctl tokens becomes
  one more entry in the list. **A deployment still setting these vars will not
  start** — envconfig ignores a variable with no matching field, so they
  contribute nothing, and with no provider list the fail-closed guard refuses
  startup rather than serving unauthenticated. Migration is one provider entry;
  the shape is in `docs/reference/AUTH.md` § `delegatedAuthorization`.

  One convention the mode owned is recorded in AUTH.md rather than lost: an
  auth failure never logs an identity decoded from an unverified token.
  `ProviderSet` logs the issuer, reason and `request.id` only.

  `make run-orbital-aep` is removed with it — it hardcoded an internal Keycloak
  host and client into a build file. `make run-orbital` sources
  `deploy/local/orbital.env` (gitignored) when present, and
  `deploy/local/orbital.env.example` now carries provider-agnostic placeholders
  plus a commented `ORBITAL_AUTH_PROVIDERS` example of both shapes. It also sets
  `ORBITAL_API_AUTH_ENABLED=true`, without which `ORBITAL_DEV=true` bypasses
  bearer verification and a provider list looks broken when it is simply not
  being consulted.

### Security
- **State-changing API calls authenticated by session cookie now require an
  `X-CSRF-Token` header.** A cookie is an ambient credential — the browser
  attaches it to any request to this origin, including one a third-party page
  caused — so authentication alone never proved the user intended the call.
  Until now the entire defence was `SameSite=Lax` on the session cookie: a real
  control, but one attribute, and setting `SameSite=None` (to embed orbital's UI
  in another product's frame, say) would have made every mutation forgeable with
  nothing in the code to notice. **Bearer callers are exempt and must be** — a
  token is not ambient, and an API client has no session to fetch a token from.
  Reads are untouched.

  Client-side the header is added by a single `fetch` wrapper in `shared.js`,
  which `orbital.js` and `orb.js` import, plus an `htmx:configRequest` listener
  for htmx's own XHRs — rather than at ~25 call sites, so one added later is
  covered without anyone remembering. Same-origin requests only; the token is
  never attached to a third-party URL. Verified live: a cookie-authenticated
  `POST /api/v1/backup` returns **403** without the header and **202** with it,
  while `GET` is unaffected. Pinned by `TestRequireCSRFOnCookieAuth`, including
  the bearer exemption and a wrong-token case.

### Changed
- **`ORBITAL_AUTH_PROVIDERS` is a privilege grant, not just an identity list** —
  documented, not changed. Any app (client-credentials) caller whose `azp`
  matches an entry is `dev`-equivalent on every mutating route, so listing a
  client grants it write access to everything a dev can write. Reviewed
  2026-09-22 and kept: per-client scoping is cheap to add later (a role on the
  entry, the shape `delegatedAuthorization.role` already uses) and no second
  machine caller needs it yet. `AUTH.md` § App callers now states the blast
  radius instead of leaving it in a code comment.

- **Browser login now uses PKCE, binds the ID token with a `nonce`, and compares
  `state` in constant time.** OAuth 2.1 makes PKCE mandatory for **all** clients,
  confidential included — the client secret proves which application is redeeming
  the code, the verifier proves it is the same party that started the flow.
  orbctl had been doing this correctly while orbital's own login did not. The
  `nonce` is what makes a replayed ID token detectable: a token lifted from
  another login attempt verifies perfectly on signature, issuer, audience and
  expiry, and nothing else catches it. **An ID token with a missing or mismatched
  nonce is refused** (`?error=invalid_nonce`), the absent case explicitly, so an
  implementation that skipped the check when the claim is absent cannot pass.
  Pinned by `TestOIDCLogin_SendsPKCEChallengeAndNonce`,
  `TestOIDCCallback_NonceMismatchIsRefused` and
  `TestOIDCCallback_MissingNonceIsRefused`.

  The three values are stored and cleared as **one** record (`auth.OIDCLogin`) —
  clearing the state while leaving a verifier or nonce behind is how a stale
  value gets reused on a later attempt. Round-tripped by
  `TestOIDCLogin_StateVerifierAndNonceRoundTripTogether`, which asserts all three
  rather than just the state: a verifier lost in transit turns the exchange into
  an unexplained 400 at the IdP, and a lost nonce silently disables the replay
  check.

- **Rate limiting is enabled in `deploy/base`.** `ORBITAL_RATE_LIMIT_ENABLED`
  defaults to `false` in code, which is right for local dev and wrong for every
  real deployment: the mechanism existed, attached a tighter bucket to
  `POST /user/login`, and was never switched on — so the one credential-guessing
  surface orbital has ran unguarded. Local password login is the break-glass path
  and cannot be disabled, which is what makes it worth guarding. Verified: at
  `ORBITAL_LOGIN_RATE_LIMIT_RPS=1` a burst of six attempts returns
  `200 200 429 429 429 429` with `Retry-After: 1`.

  Per-pod and in-memory, so the ceiling is `RPS x replicas`. It slows one source;
  it deliberately does **not** lock accounts — lockout on a break-glass path is a
  denial-of-service anyone can trigger with an admin's email address.

- **The OIDC callback no longer logs claims at INFO on every login.**
  `oidc.go` logged email, name and `preferred_username` unconditionally, while
  `AUTH.md` documented claim logging as debug-only and off by default. The
  `logger.Debug("oidc id token claims", …)` twenty lines below it — the one the
  docs describe — is unchanged.

### Fixed
- **A human bearer token could impersonate a service account and gain write
  access.** Orbital labels machine callers `app:<azp>` in `user_name`, and both
  `ResolveUser` and `RequireRole` classified callers by prefix-matching that
  string. For a human, `user_name` is the token's `name` claim — which Keycloak
  lets the end-user edit in their own account console — so a token whose name
  began `app:` took the app-principal branch: provisioning skipped, the
  provider's role never applied, and `RequireRole` granting dev-equivalent
  access. A readonly user could write.

  The verifier now records the principal kind on the request context
  (`auth.MarkAppPrincipal` / `auth.IsAppPrincipal`), where no token claim can
  reach it, and both branches read that. `AppPrincipalPrefix` remains as the
  display label for audit records, which is all it was ever for. Kubernetes
  solves this by RESERVING its `system:` prefix, which it must because it is
  stateless and the username string is the identity; orbital has a context and
  can carry the fact instead — the same reasoning that rejected `usernamePrefix`
  for identities.

  Pinned by `TestRequireRole_ForgedAppNameIsNotAnAppPrincipal` (integration —
  the grant happens at `RequireRole`, which a nil-db unit test short-circuits
  before reaching) and `TestProviderSet_HumanTokenCannotForgeAnAppPrincipal`,
  with the real client-credentials path kept as the positive control. Both were
  run against the previous implementation and fail there.

## [v0.0.44] - 2026-09-20

### Added
- **Orbital accepts bearer tokens from multiple identity providers.**
  `ORBITAL_AUTH_PROVIDERS` takes a JSON array of providers, each with its own
  issuer, audiences, claim validation and role handling. A token selects its
  provider by the `(iss, azp)` pair, which must be unique, so exactly one provider
  ever attempts cryptographic validation and there is no "try each until one
  accepts" fallback. Keying on the pair rather than the issuer alone lets one
  Keycloak realm host several clients orbital treats differently. A token whose
  `azp` matches no entry is refused, which makes `clientID` the app-token gate —
  `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` is ignored when the provider list is set, and
  orbital warns rather than leaving it silently inoperative. Unset, everything
  behaves as before; setting it alongside `ORBITAL_AUTH_MODE` is a startup error.

  Each provider says where its callers' roles come from: `defaultRole` (orbital's
  users table owns roles; a new user is created with it and existing users keep
  theirs), `roleMapping` (the provider's group claim owns roles, re-derived at
  every login; no matching group is denied rather than dropped to readonly), or
  `trustedService` (a caller whose authorization happens upstream: every valid
  token gets the assigned role and no user row is provisioned). `trustedService`
  combines with neither of the others and setting none of the three is a startup
  error; `defaultRole` alongside `roleMapping` is legal and is described below.
  `trustedService` replaces `ORBITAL_AUTH_MODE=external-jwt` and its
  `ORBITAL_JWT_DEFAULT_ROLE` — same behaviour, named as the deliberate trust
  delegation it is rather than a default nobody overrode.

  Orbital keys users by email, so one address cannot be shared across providers —
  a second provider asserting an existing address is refused rather than
  inheriting the row.

  `defaultRole` may be set alongside `roleMapping`, where it is the floor for a
  token whose groups match nothing — without it an unmatched login is refused
  (Grafana's `role_attribute_strict`). `users.role_source` records whether the
  provider or an admin last set a role: a matched group always wins, but the floor
  applies only to users the provider already owned, so an admin's promotion is not
  reverted at the next login. The users page renders provider-set roles read-only
  with provenance and leaves locally-set ones editable — Grafana and NetBox
  overwrite manual changes silently, which is the behaviour their users complain
  about.

  Role changes driven by a provider write an audit event naming the provider and
  the causing group — the transition only, not every login. The users page shows
  provider-owned roles read-only with their provenance, because an editable field
  that reverts at the next login is worse than one that says who owns it.

  Client-credentials tokens are treated as app principals: no user row, gated by
  `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` as before. Design and rationale in
  `docs/reference/AUTH.md` § Multiple identity providers.

  Browser sign-in uses the same mapping as bearer callers, so one identity from
  one provider cannot end up with two different roles depending on whether it
  arrived with a cookie or a token. A refused sign-in now shows a reason —
  `NO_ROLE_MAPPED` or `IDENTITY_INCOMPLETE`, rendered as `error — hint` per
  `ERROR-RESPONSES.md` — where it previously bounced silently to the home page.
  `ORBITAL_LOG_LEVEL=debug` logs the decoded ID token claims (never the raw
  token) for diagnosing a mapping, since the ID token arrives back-channel and
  is otherwise invisible to an operator.

  Audit events gain `acting_client`, recording the OAuth client that presented
  the token when it differs from the human in `actor` — so a request made by a
  trusted upstream service on a user's behalf is distinguishable from that user
  acting directly. Empty when the caller is their own actor. Inferred from the
  verified `azp`; RFC 8693's `act` claim supersedes it when token exchange lands.

  `users` gains a nullable `issuer` column recording which provider owns each
  row. A provider only ever resolves rows it owns — a login for an address owned
  by a local account or another provider is refused with `IDENTITY_CONFLICT`
  rather than claiming the row. Without that, any configured provider could mint
  a token for an existing address and inherit that user's role, including the
  local break-glass admin. Local password accounts have no issuer, which is what
  keeps break-glass representable and unclaimable. The one exception is a row with
  neither an issuer nor a password — unowned and never a local account — which the
  first provider to resolve it claims, so users predating the column are not
  locked out of SSO.

- **`ORBITAL_API_AUTH_ENABLED` splits API authentication out of `ORBITAL_DEV`.** `ORBITAL_DEV` bundled
  three unrelated switches — template hot-reload, the API bearer-auth bypass, and permission to use
  the placeholder session key — so you could not verify bearers locally without also giving up
  hot-reload, and the startup log blamed `dev:true` for auth being off. The new variable is
  hierarchical, not independent: **unset it follows `!ORBITAL_DEV`**, so no existing deployment
  changes. Set explicitly it wins, making `ORBITAL_DEV=true` + `ORBITAL_API_AUTH_ENABLED=true` a
  valid combination for the first time. An explicit `false` disables auth in every auth mode;
  an inherited `false` does not, because `external-jwt` never consulted `ORBITAL_DEV` and quietly
  dropping auth there would be a regression. Auth resolving to enabled with no usable verifier now
  **refuses startup** — the fail-closed guard keys on intent rather than on `ORBITAL_DEV`. Both auth
  log lines carry `decided_by`, so the deciding setting is stated rather than inferred.
  `ORBITAL_DEV` keeps only what its name implies.

### Changed
- **BREAKING (deployment) — orbital trusts Keycloak only; Entra is gone from
  `deploy/base`.** The UI's OIDC client, the bearer providers and cb-bundler's
  client-credentials grant all point at one Keycloak realm. cb-bundler needed no
  code change — it already took `ORBITAL_TOKEN_URL` and `ORBITAL_TOKEN_SCOPE`
  overrides for non-Entra providers; both are now set, and the scope matters
  because Keycloak rejects Entra's `api://{clientID}/.default` form.
  **`orbctl login` is broken by this** and is tracked in
  `docs/planning/backlog.md` — it builds Entra URLs by string concatenation
  instead of using OIDC discovery.

### Removed
- **BREAKING — device-code browser SSO is gone.** `ORBITAL_OAUTH2_DEVICE_CODE` (which defaulted to
  `true`), `GET /auth/device`, `POST /auth/device/poll` and `pages/device-code.gohtml` are all
  removed; the login modal now always uses Authorization Code via `GET /auth/login`. **A deployment
  relying on device code must register a redirect URI with its IdP and set
  `ORBITAL_OIDC_REDIRECT_URL` to match** — byte-for-byte, since OAuth compares it exactly.
  Device code existed to work around one Azure AD constraint (orbital sits behind an Internal Load
  Balancer on private DNS, and Entra would not accept a redirect URI it could not resolve). It was
  vendor-locked in the worst way: the endpoint was built by string surgery on the Azure URL shape,
  `TrimSuffix(issuer, "/v2.0") + "/oauth2/v2.0/devicecode"`, which 404s against any other provider —
  so the `true` default silently broke browser SSO for anyone pointing orbital at a non-Entra IdP.
  Keycloak accepts `http://` and private-hostname redirect URIs, so the original constraint does not
  exist there. It had a single consumer, orbital's own login modal, which is a browser and therefore
  always had the standard flow available. **orbctl is unaffected** — it uses Authorization Code +
  PKCE with a loopback listener (RFC 8252) and never called these endpoints.

## [v0.0.42] - 2026-09-17

### Added
- **`DataCenter` gained a `model` field, typed as the `DataCenterModel` enum**
  (`Beacon`, `Cruiser`, `Triton`, `Leviathan`; `schema/VERSION` → `v9`). An enum
  rather than a free string, so the set of Galleon models is declared in the schema
  and reachable by introspection — a client renders a dropdown without orbital
  publishing a separate list. The field is optional; existing data centers read back
  `null` until it is set. DGraph enforces the enum at the GraphQL layer only, so a
  `dgraph live` bulk load can still store an off-list value, which then reads back
  as `null` **alongside an `errors` entry** rather than failing the query — a client
  that reads `data` and ignores `errors` cannot tell unset from corrupt. Query,
  introspection and update examples are in `docs/api-cheatsheet.md`.

- **Orbital now reports when DGraph is running an older schema than the build ships.** It logs a
  `WARN` at startup naming every missing declaration, and the Schema page states whether the applied
  schema matches. Orbital still does not *apply* the schema — that remains a manual deploy step —
  but forgetting it used to produce no signal at all until users hit 404s or silently truncated
  pages (v0.0.25 queried `retentionDays` against a DGraph on v3; every cluster 404'd, 2026-07-27).
  The Schema page previously labelled the shipped `schema/VERSION` as the "Active Version" beside
  DGraph's real SDL, so it could caption a v8 graph "v9".

### Changed
- **BREAKING — `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` empty now DENIES every app-only bearer token.**
  It previously skipped the allowlist check when empty, so any AAD app token bound to orbital's
  audience was accepted regardless of which application minted it — the same shape AWS eliminated
  from IAM/GitHub-Actions OIDC trust policies after it was found exploitable. The wildcard is now
  explicit: set `*` for the old permissive behaviour. The default is no longer orbital's own app id
  and is now empty, so **a deployment that authenticates any service with client credentials must
  list its application ids or publish will 401** on the legacy single-issuer path. It is inert where
  `ORBITAL_AUTH_PROVIDERS` is set (each entry's `clientID` is the gate), which is why
  `deploy/base/deploy.yaml` no longer sets it. User (non-app) tokens are unaffected.
- **Job tables gained a `status` index** (`export_jobs`, `backups`, `restore_jobs`). Every job
  trigger runs a conflict check filtering `status IN (pending, running)`, which previously scanned
  the whole table. Applied by the boot migration; additive, no data change.
- **`ORBITAL_OIDC_ISSUER_URL` and `ORBITAL_OIDC_CLIENT_ID` no longer have code defaults.** They
  shipped defaulted to Armada's AAD tenant and app id — public identifiers rather than secrets, but
  a default that silently points another organisation's deployment at our identity provider. Empty
  disables SSO cleanly (password login is unaffected), so nothing breaks at zero configuration.
  **A deployment relying on those defaults must now set both explicitly, or orbctl bearer tokens
  will 401** — `deploy/base/deploy.yaml` already sets them. For local SSO, copy
  `deploy/local/orbital.env.example` to `deploy/local/orbital.env`; `make run-orbital` sources it
  when present and runs normally when it is absent.
- **`orbId` is now unique across every ConfigItem type, enforced by DGraph** (`schema/VERSION` → `v8`).
  It was `@id`, which DGraph scopes *per implementing type* — a `Rack` and a `Server` could hold the
  same `orbId`, and only the `<kind>-` prefix convention kept them apart. A collision breaks reads
  (`getConfigItem` returns *"A list was returned, but GraphQL was expecting just one item"*) and makes
  one of the two nodes vanish silently from every diff, export preview and change-request base
  capture, because `internal/graphdiff` keys its snapshot by `orbId`. Now `@id(interface: true)`, and
  a second type reusing an `orbId` is refused.

  **The constraint is not retroactive** — applying the schema succeeds with duplicates already stored,
  without scanning or rejecting them. Audit an existing graph before applying it and after any bulk
  import; the query is in `docs/reference/DGRAPH.md` § orbId convention. Orbital does not apply the
  DGraph schema at startup, so this reaches a running cluster only when the schema is applied.

### Removed
- **`orb scan` is gone from the orb CLI.** It never scanned anything — it slept, printed a
  hardcoded "Found 3 BMC interfaces" and a fake progress bar, then reported "Scan complete".
  An operator running it against real hardware would have believed a discovery had happened.
  The feature remains post-MVP (`docs/reference/ORB.md` § orb scan); only the placeholder
  command is removed.

## [v0.0.41] - 2026-09-16

### Added
- **Orbital runs safely at any replica count.** Previously `deploy/base/deploy.yaml` carried
  `replicas: 1` with a written warning not to raise it, and nothing enforced that — a one-line
  kustomize patch deployed a broken configuration with no error. Every long-running job (export,
  backup, restore) is now *claimed* with a conditional `UPDATE` so exactly one runner executes it,
  proves liveness through a refreshed `heartbeat_at`, and is *fenced* on `locked_by` so a runner
  that stalls and resumes aborts instead of racing its replacement. Job admission runs under a
  transaction-scoped advisory lock, and a divergence report is claimed before it is applied rather
  than after — the previous order let two replicas both apply a report, which silently drops
  operator resolutions via the supersede branch. No new dependency, no worker tier, no leader to
  configure: coordination is PostgreSQL, which orbital already requires.
  See `deploy/README.md` § High availability and `docs/reference/OCI.md` § Job leases.
- `GET /api/v1/export/jobs/:id` now returns `lockedBy` and `heartbeatAt`, so an operator
  investigating a stuck or reaped job can tell which pod is running it.
- `ORBITAL_JOB_HEARTBEAT_INTERVAL` (`10s`), `ORBITAL_JOB_STALE_AFTER` (`60s`) and
  `ORBITAL_JOB_ORPHAN_GRACE` (`1h`) govern job liveness.
- **Schema migration is serialized across replicas.** Migration runs in every replica at boot, so
  replicas starting together raced the same DDL — and the failure is fatal. This was not
  theoretical: a **fresh install at `replicas: 2` crashlooped one pod on 4 of 4 attempts**, because
  an empty database means both pods try to create every table (`create "orbs" table: duplicate key
  value violates unique constraint "pg_type_typname_nsp_index"`). It is now taken under a
  PostgreSQL advisory lock — the same approach Rails, Flyway and Alembic use — with a bounded retry
  so a holder that died mid-migration produces a diagnosable error rather than a silent hang.
  `ORBITAL_MIGRATION_LOCK_TIMEOUT` (`5m`) bounds the wait.

- **Staleness is now two signals, and only the author can clear the one that matters.**
  `stale` means at least one change object's `version` no longer matches its node — a fact about
  what the *author* proposed. `subtreeChanged` means the reviewed scope moved without any change
  object going out of date, typically an edit to an owned child — a fact about what the *reviewer*
  looked at. Both block merge. Previously these were one flag, which let a reviewer clear, in one
  click, a proposal written against a value that had since moved: approving re-anchored the request,
  `stale` went false, and the merge wrote an outdated value over someone else's edit. The two facts
  have different remedies, so a single flag could only ever offer the wrong one to somebody.
  **Re-approving no longer clears `stale`** — the author rebases, by `PATCH`ing the changeset with
  the current `version` or dropping the object. A request can now be `approved` *and* `stale`, in
  which case `availableActions` drops `merge` and offers `edit` to the author. A rebase dismisses
  approvals **even when the graph has not moved**, because the reviewer approved a different
  proposal — the same rule as GitHub's "dismiss stale approvals on push", with the version vector
  standing in for the commit sha. An item sent without a `version` can never be item-stale and is
  guarded by the scope anchor alone, exactly as before.
- **The concurrency precondition is `version`, not `ifVersion`** *(breaking; renamed before any
  external client adopted it).* A precondition named after the node's own field is what Kubernetes
  (`resourceVersion`), Google Cloud (`etag`), Firestore, DynamoDB, Hibernate and plain SQL all do;
  `if`-prefixing is an HTTP **header** idiom (`If-Match`) that reads wrong on a body field. It could
  not collide with the `version` orbital stamps, because a changeset rejects `version` inside `set`
  outright and on `/graphql` the two sit at different nesting levels — the same separation
  Kubernetes relies on between `metadata` and `spec`. Applies everywhere the token appears:
  `/graphql` variables, change-request items, and `DELETE /api/v1/config-items/{type}/{id}?version=`.
  **A client still sending `ifVersion` is refused `400` naming the new field** rather than having its
  guarantee silently dropped. A mutation that declares its own `$version` is also refused — orbital
  adds the predicate itself, and a hand-written one gets no conflict detection.
- **Change-request preconditions are now one concept, not two — `before` was removed.**
  An item's optional precondition is `version`, the entity's `version` as you read it: the same
  token `/graphql` mutations accept, meaning the same thing. The field-level `before` a client used
  to send is gone. It existed because server-side version stamping was unreliable on one write path,
  and that is fixed — every writer now stamps — so it had become a second concurrency vocabulary for
  a question already answered. **Field-level protection is unchanged at merge**, where it comes from
  the ancestor orbital records itself (`base_values`) rather than from anything a client asserts:
  a merge still refuses per field, naming the field, and still drops already-satisfied fields from
  the write. **What this costs:** a creation-time refusal is now entity-grained, so a third party
  editing a *different* field of the same entity while you compose refuses your proposal where
  `before` would have accepted it — reload and propose again. It also retires the whole-value
  false-conflict on composite JSON fields that `before` could not express. Orbital's editor sends
  `version` per item on the propose path; a create sends none, since there is no version to match.
- **A stale change request now says which entity moved.**
  `base_hash` anchors a request to the version vector of its scope, so it has always refused a
  merge whose intent moved — but it is a fingerprint, so it could only ever say "something
  changed", leaving an operator to go and find out what. Requests now also store the vector that
  hash is computed from, and a stale merge returns `409 MVCC_CONFLICT` with a `problems[]` entry
  per moved entity carrying its orbId and both versions. This applies to every request, including
  ones whose author sent no precondition, and the error still wraps the same sentinel so the
  status, code and existing clients are unchanged. The vector is re-captured wherever the hash is
  — create, amend, the rebase on approval, the rebase after a partial merge — so re-reviewing a
  stale request still clears it in one click. A merge also now carries the version it planned
  against into each write, so an edit landing between planning and the write refuses that item
  rather than overwriting it.
- **A change-request item can carry `version` — the same concurrency token `/graphql` accepts.**
  `POST /api/v1/change-requests` items take an optional `version`: the entity's `version` as you
  read it. If the entity has moved since, the request is refused with `409 MVCC_CONFLICT` naming
  the item and both versions, instead of being accepted and only failing at merge — where
  `base_hash` refuses wholesale and can never say which entity moved. It is entity-level where
  the existing `before` is field-level, and the two stay distinguishable: an entity-level problem
  carries no `field`. Honoured on `op: delete`, which `before` skips by construction — a delete is
  where a stale precondition costs most, since it destroys work with no diff to recover it from.
  One per item, never per field. Omission stays legal and leaves the item guarded by the scope
  anchor alone. Supplying it against an entity that does not exist is refused at validation rather
  than ignored — a caller that asked for a check and silently did not get one is worse off than
  one that never asked. Duplicate orbIds in a changeset were already refused for ordering reasons;
  the message now also gives the second reason, which is that two preconditions on one entity
  cannot both hold.
- **Change Requests — maker-checker approval for config mutations (Spike 36, backend).**
  `POST /api/v1/change-requests` proposes a set of ConfigItem changes as target end-state;
  a peer other than the author approves it; `POST .../merge` applies it MVCC-guarded.
  `GET .../diff` returns a flat, render-ready diff of current intent vs the proposal.
  Protected classes are declared via `/api/v1/approval-policies` — **one policy per
  namespace**, covering either every type or a named list — and are **opt-in**: with no
  policy, nothing changes. Enforcement is per policy; there is no global switch.
  Notable properties: a changeset is validated against the *deployed* schema at creation, so a
  proposal that could never apply never reaches a reviewer; staleness and approval validity are
  derived on every read rather than stored, so no hook can miss a write; a partially applied
  merge stays open with a recorded per-item attempt and is safe to retry without re-approval
  unless someone else wrote to a covered entity.
  **Enforcement is live**: a mutation on a protected class is refused with
  `403 APPROVAL_REQUIRED` and a hint pointing at the change-request endpoint, unless the
  caller's role is in the policy's `bypass_roles` — in which case the write goes through
  and is logged as a *privileged write*. The check sits in the single function every
  DGraph write passes through, so it covers `/graphql` and internal dispatch alike;
  merging an already-approved change request is the one exemption. A divergence
  **Accept** on a protected class now returns `202 Accepted` with a `changeRequestId`
  and leaves the entry pending instead of mutating intent.
  `GET /api/v1/change-requests?status=` is **repeatable and OR-ed** (`status=merged&status=rejected&status=closed`);
  an unrecognised value is refused with `400` rather than silently matching everything.
  Every change-request response carries an **`effect`** — how many entities and fields it
  changes, and the field with its before/after when there is exactly one — so a client can
  render a queue row without walking the changeset. It reports what the request would DO, not
  what it says:
  the delta is computed once at creation against the same snapshot the staleness anchor comes
  from, so a client that posts a complete end-state (a reconcile flow) still gets `1 field`
  when one field differs.
  **A proposal now carries only the fields the author actually changed.** The editor previously
  sent an entity's whole scalar payload, so a one-field edit opened a request claiming six
  fields: the diff was right, every count derived from the payload was not, and merging would
  have written all six.
  **UI**: a *Change Control* section with the review queue and an admin policies page;
  a review view showing the diff, the approvals (including "approved an earlier version"
  when one no longer counts) and only the actions the caller may actually take; and the
  config editor relabels **Save → Propose change** on a protected class, opening a change
  request instead of writing, and leaving you on the entity — where a banner names the
  request and links to it. A caller whose role may bypass the policy gets **both** actions —
  *Propose change* as the primary and *Save directly* beside it, so holding the bypass role no
  longer means losing access to review. An entity with a change already
  in flight says so before you start editing it, including when the change targets an owned
  child such as its iDRAC or maintenance window — named by request id, which never goes stale.
  Field marks now cover a server's own fields on the Server Summary table, not only its
  maintenance panel, and sit in a column of their own.
  **A detail tab whose panel holds a proposed field now carries a dot**, so a change to a
  server's iDRAC or maintenance window is visible without clicking through every panel —
  red when two requests inside disagree. A proposal that already matches current state
  does not light it.
  The review queue shows three tabs — *Needs my review* (hidden for roles that cannot approve),
  *Open*, *Closed* — and lists each request by **title**, with a narrow column giving the change's
  size (`1 field`, `12 fields`) beside it.
  The review view follows the server detail page's shape: the title, then a row of rounded
  actions, then the diff in a wide panel with a Details summary beside it and one Activity panel
  below, with status rendered the same way as on the queue. Its activity is one chronological
  table rather than separate reviews and merge-attempt lists. The queue remembers which tab you
  last opened.
  **The proposer now writes the title.** The propose footer carries a title input, prefilled with
  the entity name and replaceable in one gesture; leaving it alone or clearing it stores that same
  entity name. The generated title no longer appends a field count — the queue's `Change` column
  states that, and requests created before this carry the old `<entity> · N fields` form.
  Titles are capped at 255 characters — the length the `title` column always enforced, which until
  now surfaced as a 500 instead of a 400 — and an open request can be **renamed in place** from
  the review page, which patches the title alone and
  leaves the changeset, the staleness anchor and every approval untouched.
  **A review view now warns when another active request proposes the same field**, naming and
  linking each competing request and the fields it overlaps on, and distinguishing a competitor
  proposing a *different* value — whichever merges first wins the field, and the other is refused
  until re-reviewed — from one proposing the *same* value, where one of them merges as a no-op.
  Until now the collision was invisible until a merge, at which point the other request silently
  went stale and its approvals stopped counting.
- **Config-item edits are guarded against concurrent writes again.** The editor sends
  `version`, so saving a form someone else has changed underneath you is refused with
  `409` and a reload prompt instead of silently overwriting their edit. This had been lost
  since 2026-06-20, when the per-page edit modals — each of which passed `version` — were
  replaced by the shared editor module, which did not carry it forward. Nothing failed in
  between: the check is opt-in server-side, so a client that omits it looks exactly like one
  that declined it. Covers a data center, cluster, network device, server and its iDRAC and
  maintenance settings.

- **Approval policies can govern every namespace.** A policy is now created with either a
  namespace or `allNamespaces: true`; the second covers every namespace, **including data
  centers onboarded after the policy was written**. Resolution is **fallback**: a namespace
  with its own policy uses that one instead, even when it is weaker, and a namespace whose
  policy is **disabled** is not gated at all. So an all-namespaces policy is a **default,
  not a floor**. Exactly one policy still governs any write, so "which policy did this?"
  keeps a single answer. The policies page marks a namespace row that is overriding the
  global one.
- **Artifact-to-artifact compare** — `GET /api/v1/export/compare?from=&to=` returns the
  desired-state delta between any two published artifacts of a data center, pulled by immutable
  digest and diffed in memory. Surfaced as a **Compare** tab on Publish History with linkable
  `?from=&to=` URLs.
- **Export preview + guarded apply** — `POST /api/v1/export/preview` returns the content diff
  between current intent and the last published artifact; `expectedContentHash` on
  `POST /api/v1/export` rejects with `409 MVCC_CONFLICT` when intent moved since the preview.
- **Filters on `GET /api/v1/oci/artifacts`** — `dc` (data-center orbId), `status`, `limit`.
  Without them the endpoint was capped at 100 rows across all data centers, so a busy data center
  pushed others out of the response entirely.
- **Divergence badge** in the navigation, showing unresolved divergence entries.
- **`GET /api/v1/proposed-changes?orbId=…`** — what is proposed for a set of entities, keyed by
  orbId and indexed by field, so a client can overlay proposals onto entities read from
  `/graphql` with a map lookup rather than inverting the change-request list itself. Reads
  PostgreSQL only, so it is safe on every page load. Powers per-field marks on the server
  detail page: a field with a change in flight names the proposed value, its author and a link
  to the review; several proposals show a count, and ones that disagree are called out.
- **Change requests are identified as `<namespace>-<number>`** (`colo-42`) instead of a UUID —
  per-namespace numbering, so each data center counts from 1 and an id says which site it is
  about. Accepted and returned everywhere, including URLs. The queue lists the id first, titles
  each request by what it changes (`server-CWJHDX3 · maintenance.enabled → true`) rather than
  which entity it touches, and shows relative age.
- **Approval-policy administration is audited** — create, update and delete write `management`
  audit events carrying before/after and an explicit flag when enforcement stopped. Previously a
  write that *bypassed* a policy was recorded while changing the policy itself left no trace.
- **`?orbId=` is repeatable on `/api/v1/change-requests` and `/api/v1/audit-log`** (max 128),
  matching requests or events touching any of them — so a view can ask about an entity and
  everything it owns in one call.
- **`ORBITAL_CHANGE_CONTROL_ENABLED`** removes the change-control feature entirely — queue,
  policies page, endpoints and nav — for adopters running their own change management. Deletes
  no data; see `docs/reference/CONFIG.md`.
- **`ServerMaintenance` config item** (schema v6) — maintenance-window flag per server.
- **Request payload-size guard and rate limiting** on the API surface.
- **Audit events record where a write came from.** Every event now carries `eventSource`
  (`graphql`, `rest`, or `internal` for work done by a background job), `sourceIpAddress`, and
  `requestId` — all three exposed through `GET /api/v1/audit-log` and indexed, so "what else
  happened in that request" and "what came from that address" are answerable without scanning.
  All three are **omitted rather than blank** when there is no HTTP request behind the event: a
  scheduled backup or an internally dispatched write reports `internal` and no address, because an
  empty string in a column an operator filters on reads as a recorded fact rather than as "not
  applicable". Existing events keep rendering with the fields absent; no backfill.
  `userAgent` is deliberately NOT added — it stays on session-boundary events
  (`loginSuccess`/`loginFailed`/`logout`) where orbital already records it.


- **`limit` and `offset` on `GET /api/v1/change-requests`.** `total` always counts every match,
  never the page. Both are opt-in: a request with neither returns exactly what it did before.
- **The change-request queue is now a sortable, searchable, paged table** like every other list in
  the UI — 25 rows a page, a page-length menu and a search box. It fetches the 200 most recent
  matches and **says so** when more matched ("Showing the 200 most recent of 531"), rather than
  showing a prefix that looks like the whole list.

### Changed
- **A job interrupted by a restart is now failed by a reaper on a ticker, not at the next boot.**
  The startup sweep failed *every* pending or running job on the assumption that a process starting
  meant none could be alive — true at one replica, destructive at two, where a second pod booting
  would mark the first pod's in-flight restore failed while it was still executing `drop_all`. The
  visible difference at `replicas: 1` is that an interrupted job is marked failed a minute or so
  later rather than instantly, and its error now names the runner that died.
- Rate limiting and the database connection pool are **per pod**, so their effective values scale
  with replica count. Divide `ORBITAL_RATE_LIMIT_RPS` by the expected number of replicas; see
  `docs/reference/CONFIG.md` § Multi-replica notes.
- **A config-item edit no longer half-saves.** Saving a server (or cluster, or data center) can touch
  several entities at once, and every mutation was dispatched in parallel — so if one failed, the
  others had already been written while the message read "Conflict — please reload and try again".
  A save now checks every entity for concurrent edits **before writing anything** and refuses the
  whole save if any moved, naming them; the remaining writes go one at a time, so a failure stops
  the rest. If something does fail partway, the message says which entities were saved. The
  approval check also now covers every type in the edit tree rather than the top-level one, which
  removes a case where a policy on a child refused *after* the parent had been written.


- **The change-request queue no longer computes staleness, and is ~130x faster.** Listing requests
  derived each row's staleness from the graph — 1,222 DGraph queries and 1.21s for one unfiltered
  page of 507 requests, scaling linearly with the queue. The list now answers from PostgreSQL
  alone: **0 DGraph queries, 0.009s.** `stale`, `subtreeChanged`, `staleEntities` and
  `missingTargets` are **omitted from list items** — absent rather than `false`, because the
  endpoint no longer asks the question; read a missing `stale` as "open the request to find out".
  `GET /api/v1/change-requests/{id}` is unchanged and still reports all four. This follows GitHub,
  where `mergeable` is returned by the single-PR endpoint and never by the list. One accepted
  divergence: a list item's `approvals` counts decisions cast against the current changeset
  revision, while the detail view additionally requires the scope hash to match.


- **`GET /api/v1/change-requests` filters `orbId` and `namespace` in SQL** (jsonb
  containment, GIN-indexed) instead of after rendering each row. Rendering derives
  staleness, so the old order paid several DGraph round-trips per row before discarding
  it — fine for a queue page, ruinous for the pending-change lookup that now runs on
  every detail view. A lookup matching nothing costs zero DGraph calls.
- **New filter value `status=active`** — not-terminal (open plus approved). Neither
  existing value answers "does this entity have a change in flight", because `approved`
  is derived and `status=open` excludes it.
- **Divergence Accept no longer needs the report to carry a type.** An entry whose
  `type_name` is empty used to fail with "update intent manually"; orbital now resolves
  the type from the entity's `orbId` (`@id` on the `ConfigItem` interface, so globally
  unique). The remaining failure — an `orbId` orbital has never seen — reports that
  instead, since it is a different problem with a different remedy.
- **Audit tables renamed** `events` → `audit_events` (with `audit_event_resources`,
  `audit_event_resource_types`); ent types and handler renamed to match. The API path
  `/api/v1/audit-log` is unchanged. Rationale in `docs/reference/AUDIT.md`.
- **Audit retention is now operator-configured** rather than an assumed 12 months. Orbital ships no
  prescribed period.
- **`ROADMAP.md` reduced to a one-page status view**; spike definitions moved to
  `docs/planning/backlog.md` and technical debt to `docs/planning/debt.md`.

### Fixed
- **A cascade delete can no longer destroy a record that changed while it was being planned.**
  Deleting a data center, server or cluster collects the whole owned subtree and then removes it;
  the removal carried no concurrency check at all, and the `?version=` precondition covers only the
  top-level entity, check-then-act, with planning and the approval check inside the window. An edit
  to any child in that window was destroyed silently — `DELETE`, the irreversible operation, had a
  weaker guarantee than `update`, which returns `409` in the same situation. The delete is now a
  compare-and-swap over the entire planned set: if anything moved, **nothing** is deleted and the
  `409 MVCC_CONFLICT` names which records changed. Unchanged when nothing has moved.


- **A governed namespace no longer locks out clients that name their orbId variable something
  other than `orbId`.** The approval gate resolves the governing policy from the orbIds a mutation
  names, and it could only see them as literals or behind a variable called exactly `orbId`. Any
  other spelling — `$clusterOrbId`, or a compound mutation, which *cannot* have two variables of
  one name — resolved to no namespace and was refused `400 VARIABLE_FORM_REQUIRED`, telling the
  caller to use the variable form they were already using. Three shapes now resolve:
  `orbId: { eq: $anyName }`, `orbId: { in: [...] }` with literals and variables mixed, and a whole
  filter object behind `filter: $anyName`. Resolution descends only through the `orbId` key, so a
  filter on another field is never mined for a resource id; and it only ever *adds* orbIds, so the
  gate can become stricter but never laxer — the fail-closed default is unchanged. The same lookup
  feeds the audit log, so these mutations now record their `resource_ids` and are findable through
  `GET /api/v1/audit-log?resource_id=` instead of landing with an empty one.
  **`update` mutations are covered too.** The write pre-flight resolved its target by variable name
  to bump `version` and stamp the audit before-state, so a renamed-variable update was refused
  before the gate ever saw it — correctly, since it could not otherwise have been stamped. It now
  resolves the selector and the patch by reference as well, and a renamed update is stamped,
  version-guarded and diffed identically to the `$orbId`/`$set` spelling. A shape that still cannot
  be pinned to exactly one row — two orbIds, an `in:` list, an inline literal, or a filter behind a
  variable — is refused exactly as before: resolution can turn a refusal into a fully stamped
  write, never into an unstamped one.
- **The audit log no longer renders an empty diff when the patch variable is not called `set`.**
  The field differ read after-values from `variables["set"]` and otherwise fell back to the whole
  variables map, which for `set: $patch` intersects `{orbId, patch}` with the before-state, matches
  nothing, and renders an audit row with **no changes at all** — while the write itself lands
  normally, with no error anywhere. The differ and the stamper now resolve the patch variable
  through one helper, so they cannot disagree about which map holds the new values. Audit events
  persist the mutation query alongside its variables, so historical rows render correctly too.
- **A merged change request no longer tells you to re-approve it.** Merging bumps the version
  vector by definition, so the scope-moved signal fired on every terminal request and the detail
  view rendered "Changed since review. Re-approve to merge." above a request already applied. The
  terminal branch cleared the other two staleness flags and not this one.
- **A multi-entity change request now lists what it did.** The record on a merged request rendered
  one aggregate row — "2 entities / 2 fields" — because it was built from the queue-row summary,
  which carries an orbId only when there is exactly one. The response now carries `record`: one
  entry per change object with its fields, the value each had when last reviewed, and whether it
  applied. Derived from the stored changeset, so every request already in the database renders
  correctly with no backfill.
- **The review table's Note column rendered raw markup** (`&lt;span class="is-family-monospace"&gt;`)
  inside the conflict explanation — the value formatter returns escaped HTML and the whole sentence
  was being escaped a second time around it.
- **Concurrent writes to one entity could be silently lost even when the caller asked for a
  concurrency check.** `version` was compared in Go against a snapshot fetched by a separate
  request, then the mutation was written with a filter on `orbId` alone — two DGraph transactions,
  so a writer committing in between was overwritten with no trace. Reproduced in the test suite:
  12 concurrent writers all reported success against the same starting version, and 11 writes
  vanished while `version` advanced once. Orbital now injects `version: { eq: $version }` into the
  mutation's own filter, so the comparison happens inside the write; the loser gets
  `409 MVCC_CONFLICT` instead of a success it did not get. Requires `schema/VERSION` **v7**, which
  adds `@search` to `ConfigItem.version` — **apply the schema before rolling out the code**; the
  wrong order refuses mutations with a clear message rather than writing anything. A mutation whose
  shape cannot carry the predicate is now refused rather than sent unguarded, which also means an
  `version` sent with a create is rejected instead of quietly dropped. Change-request merge
  inherits the guard. Repeated transaction aborts — more likely now that writers contend on the same
  predicate — are retried, and a persistent one surfaces as `503 WRITE_CONTENTION`, deliberately
  distinct from `MVCC_CONFLICT`: the request is still valid, the store was busy.
  The same change closes a second hole: a supplied `version` that orbital could not evaluate —
  a failed pre-read, or a mutation touching more than one type — used to be dropped and the write
  reported success. It is now either enforced by the database or refused, so there is no path where
  a caller asks for a concurrency check and silently does not get one.
- **The cascade-delete endpoint bypassed approval policies entirely.**
  `DELETE /api/v1/config-items/{type}/{id}` planned its cascade and wrote to DGraph directly, so it
  never passed through the function where the approval check lives. Measured on the same entity,
  same policy and same caller seconds apart: `updateDataCenter` was refused `403 APPROVAL_REQUIRED`
  while the `DELETE` returned `200` and removed it. The delete now asks the same policy question,
  with the same status, code and hint the GraphQL path returns — and it asks it about every type the
  cascade would remove, read from the planned nodes rather than declared per branch, so a policy
  protecting only an owned child refuses the parent delete that would take it. The same endpoint
  also accepts `?version=`, so a delete confirmed from a dialog that has been sitting open is
  refused `409` rather than landing on whatever the entity has since become; the delete modal sends
  the version it displayed and keeps itself open on a conflict. Omitting it stays unconditional, and
  a namespace with no policy is unaffected.
- **A divergence Accept wrote intent without bumping `version`, so it was invisible to change-request staleness.**
  `base_hash` is a hash of the scope's `orbId@version` vector — a write that does not move a
  version does not move the hash. An open change request covering the accepted entity kept
  reporting `stale: false`, an approval cast *before* the Accept kept counting, and the merge
  proceeded against a state nobody had reviewed. The Accept was in the audit log throughout, so
  nothing looked wrong; it was found only by asking why an approved request still looked fresh.
  Two causes, both fixed. The **write pre-flight** — the before-fetch, the
  `version`/`updatedAt`/`updatedBy` stamping and the opt-in `version` check — lived in the
  `/graphql` handler, which internal dispatchers do not go through, so each was left to the
  caller and both callers forgot a different one (the change-request merge had passed an
  incomplete before-state, rendering raw variables where a field diff belongs). All three now
  live in `writeToDGraph`, the single function every write already passes through for the
  approval-policy check, so no caller opts in and none can forget. And Accept dispatched its
  mutation with a `$filter` object, while the row to stamp is resolved from the `orbId`
  variable — it now uses the canonical `update{Kind}($orbId, $set)` shape every other writer
  uses. An Accept consequently bumps the version, stamps the acting identity on the node, and
  carries a field-level diff in the audit log; Reject and Ignore still touch nothing.
  A caller-supplied before-state is now a *fallback* merged under the fetch rather than a
  replacement for it, which keeps the diff for fields outside a type's `BeforeFields` selection.
- **A change request could overwrite an edit it never reviewed, and could merge as a no-op.**
  Staleness was entity-level: `base_hash` fingerprints a version vector, so it answered "did
  anything in scope move" but never "what was it", and a write that changed a value without
  bumping `version` — a direct DQL write, a restore, `make seed` — left it matching. A changeset
  item can now carry **`before`**, the values the caller read, and orbital records the ancestor
  (`base_values`) for the fields the changeset touches. Every field then resolves to *applies*,
  *already satisfied*, or *conflicts*: a conflict refuses the whole merge before anything is
  applied, naming the field; an already-satisfied field is dropped from the write, so merging a
  request someone else beat you to costs no version bump and writes no audit row for a change that
  changed nothing. The write is narrowed to match the guard — a field-level check paired with a
  whole-`set` write would push stale values over other people's edits. Orbital's own editor now
  sends `before` for the scalars it changes, so an edit landing while someone has a modal open is
  refused at creation and named rather than silently absorbed. A refused action renders the
  per-field detail it already receives — a merge conflict now says which field moved, from what,
  to what, instead of "state moved since you read it".
- **A merged change request logged no field diff in the audit trail.** Merge read only `version`
  plus the fields it was about to clear, so the audit event's `before` never contained the fields
  being *set* — the diff had nothing to intersect and the row fell back to dumping raw mutation
  variables, while the same edit saved directly through `/graphql` showed `-false / +true`. Merge
  now reads the scalar fields it writes, so a merged change and a direct save produce the same
  audit row.
- **The Config Item inventory went permanently empty once a namespace filter was saved.** With
  `stateSave: true`, DataTables restores saved state *during* the constructor, so `initComplete`
  ran before `const inventoryTable` had bound and threw a TDZ `ReferenceError` out of the
  constructor — taking the data fetch with it. Only reproduced once both a saved DataTables state
  and a saved namespace filter existed, which is why a fresh profile looked fine. `initComplete`
  now uses `this.api()` rather than the outer binding.
- **The Config Item inventory could also show "No data available in table" indefinitely.** A page loaded
  while the graph was empty — between `make up` and `make seed`, or before a restore finished —
  cached `[]` in `sessionStorage`, and because `"[]"` is a truthy string every later load treated
  it as a cache hit and skipped the fetch. Servers and Data Centers looked fine because they always
  fetch. An empty cached list is now a cache miss.
- **`/api/v1/audit-log` silently truncated an over-cap `orbId` filter**, so a resource panel whose
  subtree exceeded the limit dropped its overflow children with no signal — a Server audit tab
  looked complete and was not. Both filters now refuse over the cap instead of narrowing the
  answer, and the cap is sized above a real subtree (measured at 35 orbIds for a populated
  server; it was 32).
- **JSON-editor field clearing** — clearing a field is now a DGraph `remove`; `set: null` was a
  silent no-op, so cleared fields never persisted.
- **Handler integration suite** — 10 failures and an intermittent flake. Tests called handlers
  directly, bypassing Echo's error handler, so the JSON error envelope they asserted on was never
  rendered.
- **Stale end-to-end test fixtures** — specs referenced pre-migration server orbIds and failed
  after the `server-<serial>` convention landed.
- **Removed a dead restore-availability flag** and documentation for an `ORBITAL_RESTORE_BACKEND`
  environment variable that does not exist.
