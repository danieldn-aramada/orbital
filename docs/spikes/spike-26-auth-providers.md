# Spike 26 — upstream survey and deliberation behind multi-provider auth

> **Evidence companion to [`AUTH.md`](../reference/AUTH.md) § Multiple identity
> providers — start there for what orbital actually does.** Everything settled
> lives in that document; this file keeps only the landscape research behind the
> decisions and the arguments that were had on the way, including three
> reversals worth not repeating.
>
> **This is NOT a description of orbital.** Where it names a target, orbital's
> shipped shape may fall short of it deliberately (see § Interim, not the
> destination) and AUTH.md wins on every point of fact.
>
> Point-in-time as of **2026-09-21**. Vendor behaviour and documentation quotes
> are dated; nothing here is normative. Kept because "did anyone check how other
> products do this" is a question that comes back, usually from someone new, and
> reproducing the survey is expensive.

## What the survey was for

Orbital's API is a resource server: it verifies bearer tokens it did not issue.
The question was whether trusting a *list* of identity providers — each with its
own claim mapping and its own answer to who owns a user's role — is a
conventional shape or an invention, and how comparable products resolve the
conflict between an IdP's assertion and a role stored locally.

The short answer: the list is entirely conventional, and the conflict is the
part **nobody solves well**.

## Multiple providers is the convention, not a workaround

- **Kubernetes.** `AuthenticationConfiguration` carries up to 64 JWT
  authenticators, each with its own issuer, audiences, CA and claim mappings.
  Alpha 1.29, beta 1.30, stable 1.34. Issuer URLs must be unique and selection is
  by `iss` — "try each until one accepts" is made unrepresentable, because it
  would let a caller aim at whichever configured provider is weakest. Hot
  reloaded. Mutually exclusive with the older `--oidc-*` flags, removed in 1.35.
- **Vault.** The JWT/OIDC method mounts once per provider at different paths,
  each independently configured. Selection is by the path the caller hits rather
  than by the token.
- **NetBox.** `REMOTE_AUTH_BACKEND` is "String or iterable" — "provide a string
  for a single backend, or an iterable for multiple backends, which will be
  attempted in the order given."
- **Dex (as ArgoCD uses it).** The broker alternative: federate upstream
  providers and have the resource server trust exactly one issuer. Declined —
  it is another component in the login path, and telling an adopter who already
  runs an IdP to also operate a broker adds to the stack orbital is supposed to
  slot into. An adopter may still put one in front themselves.
- **Grafana is the counter-example.** It supports several providers of different
  types but never two of the same type: define two `[auth.generic_oauth]` blocks
  and only the last wins, reported since 2017 (grafana#10209). Their guidance is
  to run separate instances. This is the evidence for what a one-of-each config
  shape costs later.

**Orbital owning authorization is universal.** Kubernetes has RBAC objects,
Grafana its own roles, NetBox groups and permissions, ArgoCD a policy CSV, Vault
policies. None lets the provider name an internal role.

## Role ownership when an IdP and a local table disagree

Researched 2026-09-21, when deciding whether orbital should track *who* set a
user's role. The finding is that nobody solves this well, which is an
opportunity rather than a gap to copy.

- **Grafana.** With role sync configured, its own docs state: *"any changes of
  user roles and organization membership made manually in Grafana will be
  overwritten on next user login."* The only escape is `skip_org_role_sync`, a
  **global** switch — either the IdP owns every user's role or the local table
  does. It generates recurring issues (grafana#68827) because the revert is
  **silent**: an operator edits a role, it appears to save, and it is gone after
  that user next signs in, with nothing explaining why.
- **NetBox.** Same shape. `REMOTE_AUTH_GROUP_SYNC_ENABLED` is global, and with it
  on, a manually added group and the Superuser flag are both removed at the next
  login. `REMOTE_AUTH_DEFAULT_PERMISSIONS` is documented as inoperative while
  group sync is on — the two mechanisms are made exclusive rather than
  reconciled, and the unused one is ignored silently.
- **Kubernetes.** Not comparable: stateless, so there is no stored role to
  disagree with. This is also why its `usernamePrefix` guidance does not
  transfer — see the reversals below.

**Neither product tracks provenance per user.** The convention is to pick one
owner per provider and accept that the other side's edits are discarded.

Two places orbital deliberately departs, both recorded in AUTH.md as rules:
ownership is **visible** (a provider-owned role renders read-only with the
causing group, rather than offering an edit that silently reverts), and ownership
is **per user** rather than per provider. The second is **not conventional and
must not be defended as such** — it exists because "removed from a group" and
"never in a group, promoted locally" produce identical inputs, and each blanket
rule breaks one of them. It earns its place only while the UI shows the
difference; if that ever goes, delete the tracking and follow Grafana.

## Break-glass, and service accounts

**Break-glass follows everyone.** ArgoCD keeps a local admin account as
documented break-glass, Grafana keeps local admin login wired alongside SSO,
Kubernetes never relies on OIDC alone (x509 client certs, the admin kubeconfig),
Vault has the root token and generate-root. Nothing here was invented.

**A service account is a separate principal type everywhere.** Kubernetes gives
them their own `system:serviceaccount:` namespace; Grafana and ArgoCD make them a
distinct principal. None lets one become a row in the human user table.

## Deliberation worth not repeating

**A YAML config file was proposed and reversed.** The first draft followed
Kubernetes `--authentication-config` and argued a list of objects does not fit in
an env var, citing `ORBITAL_BUNDLER_URLS` as the burn. That argument was wrong:
`BUNDLER_URLS` is a hand-rolled `name=url` micro-DSL, while `ORB_CONSUMERS` is
JSON with a real parser and has caused no trouble. The lesson from `BUNDLER_URLS`
is *do not invent a format*, not *do not put structured data in an env var*.
Orbital reads no config file, and this was not the feature to introduce one for.
The pairing matters: an env var needs a restart anyway, which is coherent with
restart-to-apply. A mounted file plus hot reload is the other coherent pair, and
is more machinery than adding a provider justifies.

**A `sync: true|false` flag was designed and dropped.** Copied from NetBox's
`GROUP_SYNC_ENABLED` and Grafana's `skip_org_role_sync`, it only bought a
seed-then-drift mode, which produces a role nobody can explain: ask why someone
has `dev` and the answer is "a group they were in at some point, unless an admin
changed it since, and there is no way to tell which". It also reopens the
revocation hole. Those upstream flags exist because their settings are always
present in global config; a per-provider block can simply be absent, so structure
expresses the same choice without a flag.

**A `usernamePrefix` was built and removed the same day.** Prefixing is what
**stateless** systems do: Kubernetes recommends a per-authenticator prefix
because it has no user table and nowhere to store an identity link, so the prefix
is the only way to keep two providers' subjects apart. Orbital stores users, so
it can store the link; prefixing papers over the wrong primary key while making
every identifier uglier. Worse, provider ownership was briefly *inferred* from
the prefix, which is a string convention standing in for a stored fact.

**The same mistake was still live one layer down, and was fixed 2026-09-21.**
Machine callers are labelled `app:<azp>` in `user_name`, and two branches
classified callers by prefix-matching that label. For a human, `user_name` is the
token's `name` claim — editable by the end-user in Keycloak's account console —
so a token whose name began `app:` was treated as a service: provisioning
skipped, the provider's role never applied, dev-equivalent write access granted.
The fix is the same shape as the `usernamePrefix` reversal: the verifier records
the fact on the request context, where no claim can reach it, and the string goes
back to being a display label. **Kubernetes' `system:serviceaccount:` is not a
counter-example** — it works only because Kubernetes *reserves* the namespace and
is stateless, so the username string genuinely is the identity. Reach for a
prefix as a carrier of meaning and check that premise first.

**One claim was withdrawn, and the withdrawal is the point.** An earlier draft
asserted that Grafana already renders a synced role read-only. That came from a
search summary rather than from Grafana's documentation, which covers role sync
and `skip_org_role_sync` and says nothing about disabled states. Unverifiable
either way, so it was withdrawn — the deviation is recorded as ours rather than
borrowed.

**Keycloak's "Direct access grants" is not needed for any of this.** Orbital is a
resource server and never requests tokens; the toggle would only be a convenience
for scripted testing, and ROPC is discouraged by OAuth 2.1 anyway.

**Green synthetic tests missed three real defects.** Live testing against the dev
realm caught two: a service-account token provisioning
`service-account-armada-orbital` as a readonly user, and provider ownership being
inferred from `usernamePrefix`. The third — the forgeable `app:` prefix above —
survived until someone asked what the constant was *for*, because every test that
touched it constructed the verifier's output by hand and so could only confirm
the classification it already assumed. Every synthetic test was green through all
three. The
service-account trap was *config-dependent* — Keycloak creates a real user object
for a service-enabled client, so `preferred_username` is populated while `email`
is absent, meaning the bug only fires for providers that map username to
`preferred_username`.

## Interim, not the destination

Two things in the shipped shape are stopgaps. Both are filed in
[`backlog.md`](../planning/backlog.md), and neither should be read as the
intended end state:

1. **Identities are keyed by email.** OIDC is explicit that the only guaranteed
   unique identifier for an end-user is `(iss, sub)`: an issuer may reuse an
   email across end-users over time, an email may change, and
   `email`/`preferred_username` are mutable and falsifiable. What holds the line
   today is an ownership rule, not the key. The target is an identity-link table
   keyed `(issuer, subject)` — one user row, N provider links — which is what
   Keycloak's own `federated_identity`, Auth0's account linking and
   django-social-auth's `usersocialauth` all do.
2. **`acting_client` is inferred from `azp`.** When a trusted upstream service
   calls on a user's behalf, the actor is recorded from the verified `azp`. The
   standard mechanism is RFC 8693 token exchange, where the IdP signs an `act`
   claim: `azp` names only the last client in a chain and does not nest, `act`
   does. Keycloak already advertises the token-exchange grant.

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
