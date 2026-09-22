# Backlog

The definitions behind `ROADMAP.md`'s one-line entries.

**One line per entry: the question, and where the reasoning lives.** A spike that needs paragraphs gets a doc in [`docs/spikes/`](../spikes/) — that is what the directory is for. Long-form rationale for entries trimmed on 2026-09-15 is in git history.

**Spike lifecycle** (see `CLAUDE.md`): a question here → a design doc in `docs/spikes/` once someone works on it → decisions fold into `docs/reference/<DOMAIN>.md` when it ships → the spike doc is **deleted**. A doc in `docs/spikes/` means "open question," never "settled."

---

## Spikes — open questions

| Spike | Status | Question |
|---|---|---|
| [One audit event per entity](../spikes/spike-audit-event-per-entity.md) | §4 + `request_id` shipped; §§1–3 design-only | Should an event describe one entity rather than a whole request, so `?orbId=` is exact and `changes` is single-entity by construction? |
| [Advisory enforcement](../spikes/spike-advisory-enforcement.md) | Not started | Should a policy have a middle state — evaluate and record, but do not block? **Verdict "yes" withdrawn 2026-09-17** — a lockout is not possible (disabling is never gated) and the retrospective dry run is already an audit-log filter. Ergonomics, not safety. |
| [Approval on publish (`export.publish`)](../spikes/spike-approval-on-publish.md) | Not started | Should publishing an artifact require approval? **Verdict: yes** — publish is the last reversible point. |
| [Spike 27 — Atlas migrations](../spikes/spike-27-atlas-migrations.md) | **Design complete** | How does orbital evolve the Postgres schema against legacy data without crashlooping the deploy? |
| [Spike 37 — GraphQL AST parse](../spikes/spike-37-graphql-ast-parse.md) | Not started | Should the `/graphql` proxy parse mutations with a real AST instead of the regex pile the approval gate now depends on? |
| Should the queue show staleness, and from where? | Not started | The list is PostgreSQL-only since 2026-09-15 and no longer computes it. Options: leave it off (GitHub's shape), denormalise `current_hash` onto the row invalidated at the `writeToDGraph` chokepoint, or compute it asynchronously. → [CHANGE-CONTROL.md](../reference/CHANGE-CONTROL.md) |
| Key user identities on `(iss, sub)`, not email | Not started | Orbital keys `users` by email, which OIDC states is mutable, reusable across end-users, and falsifiable — "the only guaranteed unique identifier is the combination of the iss claim and the sub claim". Consequence today: one address cannot be shared across providers, so a second provider asserting it is refused `IDENTITY_CONFLICT`. The conventional fix is an identity-link table keyed `(issuer, subject)` — Keycloak `federated_identity`, Auth0 account linking, django-social-auth `usersocialauth` — giving one person N provider links. → [AUTH.md](../reference/AUTH.md) |
| orbctl still hardcodes Entra URLs | Not started | `internal/orbauth/auth.go:74,168` builds `https://login.microsoftonline.com/{tenant}/oauth2/v2.0/...` by string concatenation, so `orbctl login` breaks now that orbital trusts only Keycloak. Same vendor-lock pattern that got device-code removed. Fix is OIDC discovery instead of URL construction. Deliberately out of scope for the Keycloak cutover — orbctl is a developer tool, not a service path. |
| Consume RFC 8693 token exchange (`act` claim) | Not started | A trusted upstream service (AEP FC) authorizes its own users and calls orbital on their behalf. Orbital infers the actor from the verified `azp` and stamps it as `acting_client`; RFC 8693 carries it in a signed `act` claim that nests across a chain, where `azp` names only the last client. Keycloak already advertises `urn:ietf:params:oauth:grant-type:token-exchange`. → [AUDIT.md](../reference/AUDIT.md) |
| Spike 23 — Audit existing tests | Not started | Which tests guard a real regression, and which are theater to delete? Output: a delete list with one-line justifications. |
| [Spike 26 — Multiple identity providers](../spikes/spike-26-auth-providers.md) | **Shipped and validated** (2026-09-22) | `ORBITAL_AUTH_PROVIDERS` — a list of providers, each mapping its own group claim to orbital roles. Decisions live in [AUTH.md](../reference/AUTH.md); the spike file is now only the upstream survey and deliberation. Group-driven roles verified end to end against the dev realm: floor → `readonly` with `role_source=provider`, `orbital-admin` → `admin`, `providerRoleChange` audit event read back through `GET /api/v1/audit-log`, users page read-only naming the issuer. Verified through the browser path; the bearer transport against a real realm is untested (same `RoleMapper`, different entry point). |
| Spike 29 — Publish-action metrics | Not started | What should orbital emit around publish/export? Publish is async, so HTTP metrics capture only the `202`. Reconcile with Spike 21. |
| Spike 28 — In-pod bundler service auth | Post-MVP | Should the co-located cb-bundler use AAD client-credentials (built) or loopback trust (documented)? The docs contradict each other — fix that regardless. |
| Spike 9b — Valkey cache-aside | Not started | What caching fits read-heavy graph queries, and does orbital degrade correctly when Valkey is down? |
| Spike 19 — `orb scan` | Post-MVP | How does an operator seed orbital from a live Galleon without hand-authoring YAML? |
| Spike 35 — Before/after capture, undo, change feed | Not started | Should orbital persist a full per-node snapshot — and does that make undo and an intent change feed fall out? |
| Network topology rollout | Data migration, not a spike | Remaining DCs need NetworkDevice/Adapter/Interface seeded from NetBox + Redfish. → [network-model.md](../network-model.md) |

**Closed spikes** — decisions live in the domain docs, deliberation in git (`git log docs/spikes/`):
**30** pre-export change preview · **31** guarded apply + selective revert · **33** ConfigItem ownership unification · **34** audit log convention · **change-request lifecycle in the audit log** — verdict: stays in the approval tables, no audit events → [DGRAPH.md](../reference/DGRAPH.md), [CHANGE-CONTROL.md](../reference/CHANGE-CONTROL.md), [AUDIT.md](../reference/AUDIT.md)

---

## Production readiness (pre-GA)

- **Deployment** — ingress architecture, dedicated hostnames, TLS, internal vs external exposure.
- **CI/CD** — `.github/` exists with no workflow files; build, test and image publish are all manual.
- **AKS smoke suite** — `make smoke-aks` is shallow; expand to the real post-deploy checks.
- **Security hardening** — clear the Hi items in [`debt.md`](debt.md) before staging or prod exposure.
- **Perf & cost** — benchmark DGraph query latency under realistic load; size AKS node SKUs.

---

## Post-MVP enhancements

| Item | Notes |
|---|---|
| Open change requests as tabs | Like servers/clusters/datacenters — a CR becomes a workspace rather than a queue row. Asked for 2026-09-01. |
| Amend a change request's changeset from the UI | The rename half shipped 2026-09-01; editing the changeset itself does not exist. |
| Catch an accidental duplicate proposal at creation | Two open requests can target the same entity and field with no warning. Trigger: 2026-08-31. |
| Semantic text colours fail WCAG AA on light backgrounds | `has-text-warning` / `has-text-success` on white. Affects status text in dense tables. |
| Divergence observation audit (forensics) | Record what was observed, not just the resolution. |
| Migrate password hashing to Argon2id | Local-auth accounts only; OIDC is unaffected. |
| Migrate enum-like String fields to GraphQL enums | Schema change — coordinate with a `schema/VERSION` bump. |
| Swagger response-model sweep | ~65 `{object} map[string]string` annotations render as empty objects. → [CLAUDE.md](../../CLAUDE.md) settled decision |
| SDL syntax highlighting on the Schema page | Cosmetic. |
| Refactor bundler URL config away from the `name=url` env DSL | Also tracked in [`debt.md`](debt.md). |

---

## External integration dependencies

| System | Status |
|---|---|
| **Atlas UI** | Consumer of orbital's Topology API; coordinate on query shape. |
| **PLM** | Vendor selection in progress — design behind a Go interface, out of v1 scope. |
| **ITSM** | Vendor selection in progress — same. |
