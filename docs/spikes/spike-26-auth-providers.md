# Spike 26 — Orbital accepts multiple identity providers

**Status:** Implemented and documented (2026-09-20). The settled decisions now
live in `docs/reference/AUTH.md` § Multiple identity providers — read that, not
this. This file remains only for the deliberation behind the decisions and the
upstream survey; delete it once nothing references it.

Validated end to end 2026-09-20 against the dev Keycloak realm through the real
browser flow: a client role mapped to admin, promoted a readonly user, recorded
the issuer, wrote one audit event naming provider and group, and rendered the
users page read-only for that row while local accounts stayed editable.

Two things only live testing caught, both now fixed: a service-account token
provisioning a user row, and provider-ownership being inferred from
usernamePrefix. Eleven synthetic tests passed through both.

Orbital does NOT need Keycloak's "Direct access grants" for any of this — it is a
resource server and never requests tokens. That toggle would only have been a
convenience for scripted testing, and ROPC is discouraged by OAuth 2.1 anyway.

This describes what orbital's auth should be. Everything about orbital's auth
other than multi-provider support is out of scope here.

*Fold into `docs/reference/AUTH.md` as each phase lands, and delete this file when
the last one does (CLAUDE.md § Spike docs). Supersedes the older framing of Spike
26 — "move orbctl + orbital off AAD-specific claims" — which is the same problem
stated narrowly: per-provider claim mapping is how orbital stops being
AAD-specific.*

**Already shipped in this direction** (2026-09-20, before implementation began):
device-code browser SSO removed, which was vendor-locked to Entra by string
surgery on the issuer URL; and `ORBITAL_API_AUTH_ENABLED` split out of
`ORBITAL_DEV`. Both were prerequisites for a provider-agnostic auth stack.

## The model

Orbital is three things at once, and keeping them apart is most of the model:

- The web UI is an OAuth client. It logs users in itself, so it holds a client
  secret. Like NetBox.
- orbctl is a public client. Authorization code with PKCE, no secret. Like
  kubectl.
- The API is a resource server. It verifies tokens it did not issue, so it has no
  secret and no login flow of its own. Like the kube-apiserver.

The client secret is not a deviation from the Kubernetes model. It belongs to a
component Kubernetes does not have. This doc is about the third one.

How the API works:

- Orbital trusts a configured list of providers, not one.
- A token names its provider in `iss`, and that selects exactly one entry. Issuer
  URLs are unique, so exactly one authenticator ever attempts cryptographic
  validation. Never try each until one accepts.
- Verification is offline against that provider's published keys: signature,
  `iss`, `aud`, `exp`.
- Orbital owns authorization. The provider asserts identity and group
  membership; orbital's operator decides what that membership is worth. A
  provider never names an orbital role.
- Each provider is in exactly one of two modes, below.
- Local password login always works, so a broken provider cannot lock everyone
  out.

### The two modes

Mode A — `defaultRole`. Orbital's users table owns roles.

- A new user is created with `defaultRole`.
- An existing user keeps whatever the table says, login after login. Admin edits
  stick.

Mode B — `roleMapping`. Groups own roles, re-derived at every login.

- A matching group gives its role. Most privileged rule wins.
- No matching group means access is denied. No user created, nothing granted.
- `defaultRole` is not used in this mode.

Exactly one of the two per provider. Both is a startup error; so is neither.

`defaultRole` therefore means one thing only: the role a NEW user is created
with, in mode A. Never a fallback for unmatched groups, never an overwrite of a
role already in the table.

Worked example, mode A, provider carrying no orbital groups:

| user | in orbital already | after IdP login |
|---|---|---|
| joe@email.com | dev | dev |
| bob@email.com | admin | admin |
| mark@email.com | no | created readonly |

## Config shape

One env var, `ORBITAL_AUTH_PROVIDERS`, holding a JSON array. Empty or unset means
"use the existing single-issuer env vars", so every current deployment keeps
working untouched. The two are mutually exclusive — setting both is a startup
error, not a merge.

This follows orbital's own convention rather than importing Kubernetes's. Config
here is env vars via envconfig, orbital reads no config file at all today, and a
list of objects already has a precedent: `ORB_CONSUMERS` is a JSON array in an
env var with a custom `Decode` implementing `envconfig.Decoder`
(`internal/orbconfig/config.go:28`). Same shape, stdlib parsing, no new
dependency and no new mechanism.

    ORBITAL_AUTH_PROVIDERS='[
      {
        "issuer": {
          "url": "https://login.microsoftonline.com/<tenant>/v2.0",
          "audiences": ["5fc832f6-843e-4207-93dd-b3c3a77c06f2"]
        },
        "claimMappings": { "username": { "claim": "preferred_username" } },
        "defaultRole": "readonly"
      },
      {
        "issuer": {
          "url": "https://keycloak.example.com/realms/aep",
          "audiences": ["account"],
          "certificateAuthority": "/etc/orbital/pki/keycloak-ca.pem"
        },
        "claimValidationRules": [
          { "claim": "azp", "requiredValue": "aep-fleet-commander" }
        ],
        "claimMappings": {
          "username": { "claim": "email" },
          "groups":   { "claim": "groups" }
        },
        "roleMapping": [
          { "group": "orbital-admins",     "role": "admin" },
          { "group": "platform-engineers", "role": "dev" }
        ]
      }
    ]'

In a manifest it is a block scalar, which reviews fine in a diff:

    - name: ORBITAL_AUTH_PROVIDERS
      value: |
        [ … ]

Field notes:

- `claimMappings.groups.claim` names which claim carries membership, because
  providers disagree: `groups`, `roles`, `wids`, a namespaced URI.
- `claimValidationRules` is how azp anchoring is expressed. Keycloak defaults to
  `aud: account` for every client in a realm, so `aud` alone proves only "some
  client in this realm". Per provider rather than global, so a second provider
  can differ.
- `certificateAuthority` from day one — an adopter running a private IdP behind a
  private CA is realistic, and the alternative is trusting a CA store we do not
  control. A path, mounted like any other file.
- Take from the Kubernetes schema: issuer, audiences, CA, claim validation with
  claim/requiredValue, username mapping. Leave: CEL expressions, uid and extra
  mappings, audienceMatchPolicy. Add when something needs them.

Startup validation: JSON parses, issuer URLs unique and https, audiences
non-empty, exactly one of `defaultRole` or `roleMapping` per provider. Fail to
start on any of these — a malformed auth config must not run partially applied.
Do NOT require the provider to be reachable at startup: discovery failure logs
and retries, so a provider outage cannot stop orbital serving what does not need
it. A token whose `iss` matches nothing gets 401, with the unmatched issuer
logged server-side rather than returned to the caller.

What this costs: no comments. You cannot annotate why `platform-engineers` maps
to dev inside the value. The manifest around it can carry that, and the change
lands in git with a commit message. Judged not worth a new dependency plus
orbital's first config file — and a file would need a restart to apply anyway,
given the decision below, which is the worst of both.

## Two supporting rules

Role changes in mode B are audited — the transition, not the evaluation, naming
the provider and the causing group ("dev to admin, from group orbital-admins via
keycloak"). Today only admin-initiated changes write an event
(`users.go:150`); the provisioning paths (`authz.go:192`, `oidc.go:126`) write
nothing, so without this a user silently becomes admin with no trace. Auditing
every login instead of every change would be noise that trains people to ignore
the record.

Break-glass is documented, not built. Local password login is not gated on SSO —
in `login-modal.gohtml` the `{{if .OIDCEnabled}}` block wraps only the Microsoft
button and the divider, and the email/password form sits outside it. So an admin
can always sign in and repair a broken mapping. This needs naming in
docs/reference/AUTH.md and the deploy docs; an undocumented recovery path is
barely better than none during an incident. Keep it distinct from
`ORBITAL_ADMIN_EMAILS`, which is bootstrapping — how the first admin gets in, not
how you get back in.

## Why, where it would otherwise be relitigated

JSON in an env var rather than a config file. Orbital's config is env vars via
envconfig and it reads no config file today; a list of objects already has a
working precedent in `ORB_CONSUMERS`, which is a JSON array decoded by a custom
`envconfig.Decoder`. That costs no dependency and introduces no mechanism.
`ORBITAL_BUNDLER_URLS` is not a counter-example: it is a hand-rolled `name=url`
micro-DSL, and the lesson there is do not invent a format, not do not put
structured data in an env var.

Unique issuer URLs, selection by `iss`. "Try each verifier until one accepts"
lets an attacker choose which provider to attack, so the weakest configured one
sets the security level. Kubernetes makes that unrepresentable via the uniqueness
rule; this copies it.

Deny rather than fall back to readonly. A provider may have thousands of users. A
mapping enumerates which of them may use orbital; defaulting everyone else to
readonly silently grants read of the whole infrastructure graph to every employee
in the company. An adopter who wants a floor writes it as a rule — a group
everyone is in, mapped to readonly — which is explicit and reviewable in a diff.

Mode B re-derives at every login. Otherwise revocation silently fails: remove bob
from `orbital-admins` and, if his stored role were kept, he is still admin at his
next login with nothing to show the removal did not take. The corollary is that
the provider also wins over a local promotion — a dev in a group mapped to admin
becomes admin. That is what "the provider owns roles" means.

No third mode, and no sync flag. An earlier draft had `sync: true|false` copied
from NetBox's GROUP_SYNC_ENABLED and Grafana's skip_org_role_sync. It only bought
a seed-then-drift mode, which produces a role nobody can explain: ask why someone
has dev and the answer is "a group they were in at some point, unless an admin
changed it since, and there is no way to tell which". It also reopens the
revocation hole. Those upstream flags exist because their settings are always
present in global config; a per-provider block can simply be absent, so structure
expresses the same choice without a flag.

Mutual exclusion enforced at startup. NetBox makes the same two mechanisms
exclusive — `REMOTE_AUTH_DEFAULT_PERMISSIONS` is "only operational when
REMOTE_AUTH_ENABLED is True and REMOTE_AUTH_GROUP_SYNC_ENABLED is False" — but
resolves the conflict by silently ignoring the unused setting. We refuse at
startup instead. A setting that is present and inoperative is how someone comes
to believe it is doing something it is not.

## How this aligns with upstream

Multiple providers is the convention, not a workaround:

- Kubernetes: `AuthenticationConfiguration` lists up to 64 JWT authenticators,
  each with its own issuer, audiences, CA and claim mappings. Alpha 1.29, beta
  1.30, stable 1.34. Issuer URLs must be unique; selection is by `iss`. Hot
  reloaded. Mutually exclusive with the older `--oidc-*` flags, which are removed
  in 1.35.
- Vault: the JWT/OIDC method mounts once per provider at different paths, each
  with its own config. Selected by the path the caller hits, not by the token.
- NetBox: `REMOTE_AUTH_BACKEND` is "String or iterable" — "provide a string for a
  single backend, or an iterable for multiple backends, which will be attempted
  in the order given".
- Dex, as ArgoCD uses it: the broker alternative — federate upstream providers
  and have the resource server trust exactly one issuer. Declined for orbital
  (another component in the login path, and telling an adopter who already runs
  an IdP to also operate a broker is adding to their stack), but an adopter may
  still put one in front themselves.
- Grafana is the counter-example: several providers of different types, but never
  two of the same type — define two `[auth.generic_oauth]` blocks and only the
  last wins, reported since 2017. Their guidance is to run separate instances.
  Evidence of what a one-of-each config shape costs later.

Orbital owning authorization is universal: Kubernetes has RBAC objects, Grafana
has Grafana roles, NetBox groups and permissions, ArgoCD a policy CSV, Vault
policies. None let the provider name an internal role.

Per-provider mapping follows Kubernetes `claimMappings` and Grafana
`role_attribute_path`. NetBox is the weaker precedent — its group settings are
global, so an adopter with two providers cannot map them differently.

`defaultRole` meaning "role for a new user" follows NetBox's
`REMOTE_AUTH_DEFAULT_GROUPS`: "the list of groups to assign a new user account
when created". Creation only; existing users untouched.

Deny on no match follows Grafana's `role_attribute_strict = true`, which "denies
user access if no role or an invalid role is returned" and is documented as
disabling the default role assignment.

Most privileged wins follows Grafana, explicitly, when its role mapping and org
mapping disagree.

Break-glass follows everyone: ArgoCD keeps a local admin account as documented
break-glass, Grafana keeps local admin login wired alongside SSO, Kubernetes
never relies on OIDC alone (x509 client certs and the admin kubeconfig), Vault
has the root token and generate-root.

## Upstream survey: role ownership when an IdP and a local table disagree

Researched 2026-09-21, when deciding whether orbital should track who set a
user's role. The finding is that **nobody solves this well**, which is an
opportunity rather than a gap to copy.

**Grafana.** With role sync configured, its own docs state: *"any changes of user
roles and organization membership made manually in Grafana will be overwritten on
next user login."* The only escape is `skip_org_role_sync`, a **global** switch —
either the IdP owns every user's role or the local table does. No per-user
distinction. It generates recurring issues (grafana#68827) because the revert is
**silent**: an operator edits a role, it appears to save, and it is gone after the
user next signs in with nothing explaining why.

**NetBox.** Same shape. `REMOTE_AUTH_GROUP_SYNC_ENABLED` is global, and with it on
a manually added group and the Superuser flag are both removed at next login.
`REMOTE_AUTH_DEFAULT_PERMISSIONS` is documented as inoperative while group sync is
on — the two mechanisms are made exclusive rather than reconciled.

**Kubernetes.** Not comparable: stateless, so there is no stored role to disagree
with.

**Neither product tracks provenance per user.** The convention is: pick one owner
per provider and accept the other side's edits are discarded.

### Where orbital differs deliberately

Two improvements, separable — the first is the one that answers the complaint:

1. **Ownership is visible.** A provider-owned role renders read-only with its
   provenance ("dev, from group platform-engineers"). Grafana offers an editable
   control that silently reverts; that is the actual defect in the reports, not
   the policy behind it.
2. **Ownership is per user, not per provider** (`users.role_source`). A matched
   group always wins — being added to `orbital-admin` is explicit. The
   `defaultRole` floor applies only to users the provider already owned, so an
   admin promoting someone the mapping never covered is not reverted.

Point 2 is NOT conventional and must not be defended as such. It exists because
the two cases are otherwise indistinguishable — "removed from a group" and "never
in a group, promoted locally" produce identical inputs — and each alternative
breaks one: an authoritative floor reverts the admin's promotion; a seed-only
floor leaves a removed user's role in place forever.

**It earns its place only while the UI shows the difference.** A hidden per-user
rule is worse than either blanket rule, because nobody can predict what their next
login does. If the users page ever stops distinguishing locally-set from
provider-set roles, delete the column and follow Grafana.

## The one deliberate deviation

In mode B the users page shows the role read-only with its provenance — "dev,
from group platform-engineers". In mode A it stays editable, because there the
table really is authoritative.

This one is ours. Flagging it because everything else here is deliberately
borrowed:

- NetBox leaves the field editable with group sync on. The edit saves and is
  silently removed at next login — add an account to a group and set the
  Superuser flag by hand, and both are gone when that user signs in again. No
  greying out, no warning. Their documented remedy is a config flag, not a UI
  affordance.
- Grafana: could not verify either way. Their user-management docs cover role
  sync and `skip_org_role_sync` and say nothing about disabled states or
  indicators. An earlier draft asserted Grafana already does this; that came from
  a search summary rather than documentation, and is withdrawn.

So the upstream state of the art is a field that accepts an edit and quietly
discards it. Beating that is cheap: the provider, group and rule are already in
hand at the moment the role is derived. The alternative — match upstream, leave
it editable, accept the same confusion — is recorded so the choice is visible
rather than assumed.

## Migration

- The single-issuer env vars keep working unchanged. No existing deployment has
  to do anything.
- File and env vars are mutually exclusive; setting both is a startup error.
  Kubernetes does exactly this, and merging two sources of truth for who may
  authenticate is how they drift.
- Kubernetes deprecated its flags in 1.30 and removes them in 1.35. Worth
  copying the direction, not committing to a date.

## Decisions

1. Format — a JSON array in `ORBITAL_AUTH_PROVIDERS`, decoded with a custom
   `envconfig.Decoder`, exactly as `ORB_CONSUMERS` already does. Empty means "use
   the single-issuer env vars". No config file and no new dependency.

   An earlier draft proposed a YAML file following Kubernetes
   `--authentication-config`, and argued that a list of objects does not fit in
   an env var, citing `ORBITAL_BUNDLER_URLS` as the burn. That argument was
   wrong: BUNDLER_URLS is a hand-rolled `name=url` micro-DSL, while ORB_CONSUMERS
   is JSON with a real parser and has caused no trouble. The lesson from
   BUNDLER_URLS is do not invent a format, not do not put structured data in an
   env var. Orbital reads no config file today, and this is not the feature to
   introduce one for.

2. Restart to apply, not hot reload. Kubernetes reloads because you cannot
   casually restart an API server; orbital can, and since 2026-09-16 it rolls
   with `maxUnavailable: 0`, so a config change costs a zero-downtime rollout.
   Hot reload would also add a failure mode — bad config arriving at runtime
   rather than at boot, and replicas disagreeing about who may authenticate mid
   propagation. This decision and decision 1 reinforce each other: an env var
   requires a restart anyway, so the pair is coherent. A mounted file plus hot
   reload is the other coherent pair, and is more machinery than adding a
   provider justifies.

3. A token whose `iss` matches no provider gets a generic 401. The unmatched
   issuer is logged server-side at WARN. Enumerating configured issuers to an
   unauthenticated caller is free information disclosure, and the operator has
   the log. This was already stated under Config shape; recorded here so it is
   not reopened.

4. Identities stay keyed by email, and a provider may only resolve rows it owns
   (`IDENTITY_CONFLICT` otherwise). A `usernamePrefix` was built and removed the
   same day: prefixing is what stateless systems do because they have nowhere to
   store an identity link, while orbital stores users and should key on
   `(iss, sub)` via a link table instead — the Keycloak / Auth0 /
   django-social-auth model. Filed in backlog.md; the ownership rule holds the
   line until then.


7. Service-account tokens are app principals, not users. A client-credentials
   token is a valid caller — the in-pod cb-bundler authenticates this way — but
   there is no human behind it, so it must not land in the users table with a
   role.

   This is not hypothetical: validating phase 1 against the dev Keycloak, a real
   service-account token provisioned `service-account-armada-orbital` as a
   readonly user. Keycloak creates a real user object for a service-enabled
   client, so `preferred_username` is populated while `email` is absent — meaning
   whether the bug fires depends on which claim the provider maps. A
   config-dependent trap rather than a clean failure.

   Orbital already has the concept: `AppPrincipalPrefix`, no provisioning, and
   the `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` allowlist as the authorization gate.
   `ProviderSet` preserves it. Upstream agrees on the shape — Kubernetes gives
   service accounts their own `system:serviceaccount:` namespace, Grafana and
   ArgoCD make them a separate principal type. None let one become a row in the
   human user table.

5. Switching a provider from mode A to mode B rewrites existing users' roles at
   their next login. Documented, not guarded. It is exactly what configuring a
   mapping asks for, and the audit events from the supporting rule above make it
   observable — each change names the provider and the causing group, so an
   operator can see what moved and when.

6. `ORBITAL_ADMIN_EMAILS` applies at provisioning, in mode A only. In mode B the
   groups are authoritative and a second mechanism that can grant admin outside
   the mapping would defeat the mapping. Mode B needs no bootstrap anyway —
   whoever is in the admin group is admin from the first login.

   Because the setting is global while modes are per-provider, a deployment may
   legitimately have it apply to one provider and not another. So: log a warning
   at startup naming the providers that ignore it. A warning rather than an
   error, and rather than silent inoperability, which is the NetBox behaviour
   criticised above.

## Implementation notes

Order, smallest useful pieces first:

1. Config parsing and validation. Decode `ORBITAL_AUTH_PROVIDERS`, apply the
   startup checks under Config shape, and build the provider list. No behaviour
   change yet — the single-issuer env-var path still serves.
2. Issuer-keyed verifier selection. Replace the fixed two-verifier routing with a
   lookup over the list. This is the point at which multiple providers actually
   work.
3. The two role modes, plus `usernamePrefix` and app-principal handling.
4. Audit events on role transitions.
5. Users page: read-only with provenance for mode B rows.
6. Docs — `docs/reference/AUTH.md` for the settled decisions, `deploy/README.md`
   for the break-glass path, `docs/reference/CONFIG.md` for the new env var.

Each of 1 through 5 is independently shippable and leaves the system working.

## Related

- Backlog Spike 26, provider-portable identity, is currently framed as moving
  orbctl and orbital off AAD-specific claims. Narrower than this; worth
  reconciling once the shape is agreed.
- docs/reference/AUTH.md is the settled-decisions home. Nothing moves there until
  decided.

## Sources

- https://kubernetes.io/docs/reference/access-authn-authz/authentication/
- https://github.com/kubernetes/enhancements/tree/master/keps/sig-auth/3331-structured-authentication-configuration
- https://opensource.microsoft.com/blog/2025/05/08/jwt-it-like-its-hot-a-practical-guide-for-kubernetes-structured-authentication/
- https://developer.hashicorp.com/vault/docs/auth/jwt
- https://netboxlabs.com/docs/netbox/configuration/remote-authentication/
- https://grafana.com/docs/grafana/latest/setup-grafana/configure-access/configure-authentication/generic-oauth/
- https://github.com/grafana/grafana/issues/10209
- https://dexidp.io/
- https://www.cncf.io/blog/2026/09/08/kubernetes-access-via-an-identity-provider-public-client-not-confidential/
- https://zitadel.com/blog/the-broken-promise-of-oidc
