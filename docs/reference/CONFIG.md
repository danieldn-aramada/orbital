# Configuration Reference

> **Audience:** anyone deploying orbital, or adding a config value to it.

Read this before adding an `envconfig` field to `internal/config/config.go`.

Every setting is an environment variable read once at startup. Local development needs **none** of them — `config.go` carries working defaults for the whole Docker Compose stack, which is what makes `make up && make run-orbital` work with no setup.

## Toggles, and the rule for adding one

Orbital keeps deliberately few. Every toggle declares **which kind it is**, because the two kinds have opposite lifetimes and confusing them is how a project ends up with thirty booleans nobody dares remove.

| Kind | Lifetime | Meaning |
|---|---|---|
| **Ops toggle** | Permanent | An operational lever for a *finished* feature — usually a kill switch for something on a hot path or a write path, so recovery from misbehaviour is a restart rather than a rollback. |
| **Maturity toggle** | **Temporary** | Something unfinished or unproven. **Must state its removal trigger.** A maturity toggle that outlives its trigger is a bug, not a feature. |

**The bar for adding one at all:**

> A feature toggle earns its place when the feature is **actively harmful** to some adopter — not merely unused by them.

Nav clutter and unused endpoints do not clear it. A *competing source of truth* does: orbital is adopted by teams at companies that already run their own DCIM, auth and change-management tools, and a second system answering the same question is worse than no system. Two further rules follow:

- **Toggles that interact are hierarchical, never independent booleans**, and the hierarchy is resolved once at startup rather than re-derived at each read site — that is how one site ends up disagreeing.
- **A toggle hides a surface; it never deletes data.** Say so in the config comment as well as here, or someone will switch a feature off to "reset" it and lose an audit trail.

Borrowed from Kubernetes' feature-gate policy — Alpha off by default, Beta on, GA **removed**. What makes that mature is not the flag parsing; it is that every gate has a declared lifecycle and something eventually deletes it.

**Deliberately NOT adopted:** a `--feature-gates=A=true,B=false` style map. Kubernetes needs one because it has dozens across several binaries; at this count, named variables are clearer, and borrowing the syntax of scale without the scale is cargo-culting.

**Also not a mechanism for editions or paid features.** A toggle is by definition something the operator can flip, so it cannot gate anything commercial. GitLab keeps EE in a separate tree behind licence checks for exactly this reason; HashiCorp checks a licence at runtime. If orbital ever wants editions, that is a licensing decision, not a config one.

### Current toggles

| Variable | Kind | Default | What turning it off does |
|---|---|---|---|
| `ORBITAL_CHANGE_CONTROL_ENABLED` | **Ops** | `true` | Removes the change-control feature entirely: the Change Requests queue, the Approval Policies page, their REST endpoints (**404**, not 403 — the routes are never registered) and the nav section. No mutation is gated. With it off the approval gate never runs either, whatever policies remain in the database. **Deletes nothing**: change requests, approvals and policies stay in PostgreSQL and reappear if it is switched back on. Earns a toggle because the feature is *actively harmful* to an adopter running their own change management — two systems answering "was this approved", with orbital's flow invisible to their org's audit. |
| `ORBITAL_INLINE_SELECTOR_REJECT` | **Ops** | `true` | Stops rejecting single-entity `update{Kind}` mutations that inline their `orbId`/`set` instead of passing variables. Those writes then proceed **unstamped** — no `version` bump, no `updatedAt`/`updatedBy`. See [`ERROR-RESPONSES.md`](./ERROR-RESPONSES.md). |
| `ORBITAL_DIVERGENCE_INGEST_ENABLED` | **Ops** | `true` | Stops the S3 poller ingesting divergence reports. Existing entries stay; nothing new arrives. |
| `ORBITAL_API_AUTH_ENABLED` | **Ops** | *(unset)* | Decides whether bearer verification is installed on `/api/v1` and `/graphql`. **Unset it follows `!ORBITAL_DEV`** — the historical coupling, so nothing changes for an existing deployment. Set explicitly, it wins in every auth mode, which is the point: `ORBITAL_DEV=true` + this `=true` gives API auth **and** template hot-reload, a combination `ORBITAL_DEV` alone cannot express. Explicit `false` switches auth off in every auth mode; inherited false follows `ORBITAL_DEV` alone. If auth resolves to enabled but no verifier can be built, orbital **refuses to start**. |

There are currently **no maturity toggles**. Adding one requires naming its removal trigger in this table.

**There is deliberately no global "enforcement on/off" setting.** Enforcement is a property of each policy — disable the one that is misbehaving and the rest keep working. That is what every comparable engine does ([Kyverno](https://kyverno.io/docs/policy-types/cluster-policy/validate/) `validationFailureAction`, [Gatekeeper](https://open-policy-agent.github.io/gatekeeper/website/docs/howto/) `enforcementAction`, [HCP Sentinel](https://developer.hashicorp.com/terraform/cloud-docs/workspaces/policy-enforcement/manage-policy-sets) enforcement level, GitHub rulesets' enforcement status), and a global switch is the blunt version of it. The per-policy control is always reachable: policy administration writes PostgreSQL, never DGraph, so it is never itself gated.

## All settings

Generated from `internal/config/config.go` — the struct tags are the source of truth. A blank default means the value is required, or is derived at runtime.

| Variable | Default |
|---|---|
| `ORBITAL_ADMIN_EMAILS` | `admin@armada.ai` |
| `ORBITAL_API_AUTH_ENABLED` | — |
| `ORBITAL_AUTH_PROVIDERS` | — |
| `ORBITAL_BACKUP_RETENTION_DAYS` | `14` |
| `ORBITAL_BACKUP_RETENTION_MIN_COUNT` | `3` |
| `ORBITAL_BACKUP_SCHEDULE` | — |
| `ORBITAL_BACKUP_TIMEOUT` | `30m` |
| `ORBITAL_BASE_PATH` | — |
| `ORBITAL_BUNDLER_MAX_ATTEMPTS` | `3` |
| `ORBITAL_BUNDLER_MAX_RESPONSE_BYTES` | `10485760` |
| `ORBITAL_BUNDLER_TIMEOUT` | `30s` |
| `ORBITAL_BUNDLER_URLS` | `configbundle-bundler=http://localhost:8020/bundle` |
| `ORBITAL_CHANGE_CONTROL_ENABLED` | `true` |
| `ORBITAL_COOKIE_SECURE` | `false` |
| `ORBITAL_DEV` | `true` |
| `ORBITAL_DGRAPH_ALPHA_GRPC` | `localhost:9080` |
| `ORBITAL_DGRAPH_ZERO_GRPC` | `localhost:5080` |
| `ORBITAL_DIVERGENCE_INGEST_ENABLED` | `true` |
| `ORBITAL_DIVERGENCE_POLL_INTERVAL` | `10s` |
| `ORBITAL_EXPORT_DIR` | `./subgraph-exports` |
| `ORBITAL_EXPORT_TIMEOUT` | `30m` |
| `ORBITAL_INLINE_SELECTOR_REJECT` | `true` |
| `ORBITAL_ISSUE_TRACKER_URL` | `https://dev.azure.com/armadasystems/Commander/_workitems/create/Bug?[System.AreaPath]=Commander\Edge\Edge Platform` |
| `ORBITAL_JOB_HEARTBEAT_INTERVAL` | `10s` |
| `ORBITAL_JOB_ORPHAN_GRACE` | `1h` |
| `ORBITAL_JOB_STALE_AFTER` | `60s` |
| `ORBITAL_LOGIN_RATE_LIMIT_RPS` | `5` |
| `ORBITAL_LOG_LEVEL` | `info` |
| `ORBITAL_MAX_REQUEST_BODY` | `10M` |
| `ORBITAL_MIGRATION_LOCK_TIMEOUT` | `5m` |
| `ORBITAL_OCI_ALLOW_HTTP` | `true` |
| `ORBITAL_OCI_PASSWORD` | — |
| `ORBITAL_OCI_PUBLISH_TIMEOUT` | `10m` |
| `ORBITAL_OCI_REGISTRY` | `localhost:5001` |
| `ORBITAL_OCI_REPO` | `orbital` |
| `ORBITAL_OCI_SIGNING_KEY_PATH` | `deploy/local/cosign.key` |
| `ORBITAL_OCI_USERNAME` | — |
| `ORBITAL_OIDC_DISPLAY_NAME` | `SSO` |
| `ORBITAL_OIDC_ICON_URL` | — |
| `ORBITAL_OIDC_CLIENT_ID` | — |
| `ORBITAL_OIDC_CLIENT_SECRET` | — |
| `ORBITAL_OIDC_ISSUER_URL` | — |
| `ORBITAL_OIDC_REDIRECT_URL` | `http://localhost:8001/auth/callback` |
| `ORBITAL_PORT` | `8001` |
| `ORBITAL_RATE_LIMIT_ENABLED` | `false` |
| `ORBITAL_RATE_LIMIT_RPS` | `40` |
| `ORBITAL_RESTORE_TIMEOUT` | `10m` |
| `ORBITAL_SCHEMA_PATH` | `schema/schema.graphql` |
| `ORBITAL_SESSION_ENCRYPTION_KEY` | `local-dev-enc-key-32-bytes-pad!!` |
| `ORBITAL_SESSION_HMAC_KEY` | `local-dev-hmac-key-change-in-prod` |
| `ORBITAL_SHUTDOWN_TIMEOUT` | `10s` |

---

## `ORBITAL_AUTH_PROVIDERS`

A JSON array of identity providers orbital accepts bearer tokens from. It is the
only source of bearer verification — unset, there is none, and a deployment with
API auth enabled refuses to start rather than serve unauthenticated.
`ORBITAL_OIDC_*` configures the browser login flow, which is a different job.

To run orbital against your own provider locally, copy
`deploy/local/orbital.env.example` to `deploy/local/orbital.env` — `make
run-orbital` sources it when present. It carries a commented example of both
provider shapes, and sets `ORBITAL_API_AUTH_ENABLED=true`, without which
`ORBITAL_DEV=true` bypasses bearer verification entirely.

```json
[
  { "issuer": { "url": "https://keycloak.example.com/realms/aep", "audiences": ["armada-orbital"] },
    "clientID": "aep-fleet-commander",
    "claimMappings": { "username": { "claim": "email" } },
    "delegatedAuthorization": { "role": "admin" } },
  { "issuer": { "url": "https://keycloak.example.com/realms/aep", "audiences": ["armada-orbital", "account"] },
    "clientID": "armada-orbital",
    "claimMappings": { "username": { "claim": "email" }, "groups": { "claim": "orbital_roles" } },
    "roleMapping": [ { "group": "orbital-admin", "role": "admin" } ],
    "defaultRole": "readonly" }
]
```

In a manifest this is a block scalar, which reviews fine in a diff:

```yaml
- name: ORBITAL_AUTH_PROVIDERS
  value: |
    [ … ]
```

### Fields

| Key | Type | Required | Effect |
|---|---|---|---|
| `issuer.url` | string | yes | The `iss` this entry matches. **https only**, and `(url, clientID)` must be unique across entries so a token selects exactly one provider. |
| `issuer.audiences` | []string | yes | Accepted `aud` values; the token must carry **any one**. Handles `aud` as a string or an array. |
| `issuer.certificateAuthority` | string (PEM path) | no | Trust roots for reaching an IdP behind a private CA. Empty uses the system store. |
| `clientID` | string | no | The `azp` this entry matches. Empty = issuer-wide default; a specific entry always wins. A token whose `azp` matches no entry is refused, which makes this the app-token allowlist. |
| `claimMappings.username.claim` | string | yes | Which claim carries identity. Written to the audit actor, and to `users.email` where a row is provisioned. |
| `claimMappings.groups.claim` | string | with `roleMapping` | Which claim carries membership. |
| `defaultRole` | `readonly`/`dev`/`admin` | one of the three role settings | See the table below. |
| `roleMapping[].group`, `.role` | string, role | one of the three | Maps one group value to one orbital role. Most privileged match wins. |
| `delegatedAuthorization.role` | `readonly`/`dev`/`admin` | one of the three | See the table below. |

**Unrecognised fields fail startup**, rather than being silently ignored — a
dropped field in an auth config is a constraint the operator wrote that never
applies. This is why `trustedService` (renamed to `delegatedAuthorization`) and
`claimValidationRules` (removed — `clientID` pins `azp`, `issuer.audiences` pins
`aud`) refuse to boot instead of doing nothing.

### Where the role comes from

Exactly one of these three must be set, and only the first two combine:

| | Who owns the role | User row | Combines with |
|---|---|---|---|
| `defaultRole` | orbital's users table — this is what a NEW user is created with, and an admin edit sticks | created | `roleMapping` |
| `roleMapping` | the provider's groups, re-derived at every login; no match = 401 | created | `defaultRole` |
| `delegatedAuthorization` | an upstream service that authorized its own users; every valid token gets this role | **never created** | nothing |

Set together, `roleMapping` decides and `defaultRole` is the **floor** for a token
matching no group — without it an unmatched login is refused. A provider setting
none of the three is a startup error.

Applying a change needs a restart. See [AUTH.md](AUTH.md) § Multiple identity
providers for why, and for the full model.

## Multi-replica notes

Orbital runs safely at any replica count (see [`deploy/README.md`](../../deploy/README.md) § High availability). Two settings behave **per pod**, not per cluster:

- **`ORBITAL_RATE_LIMIT_RPS`** — token buckets are in-memory, so the effective ceiling is `RPS x replicas`, including the tighter login bucket. Divide the configured value by the expected replica count. Making it exact would require shared state, promoting Valkey from optimisation to hard dependency — against a settled decision. An approximate limit that degrades gracefully is the better trade.
- **Connection pool** (`DefaultMaxConns`, 10, not env-configurable) — total load on PostgreSQL is `10 x replicas`; keep it under the server's `max_connections`.

The three `ORBITAL_JOB_*` durations govern job liveness. **`STALE_AFTER` does not need to exceed job duration** — a running job refreshes its heartbeat, so it is provably alive throughout. `ORPHAN_GRACE` applies only to jobs with no heartbeat at all and must stay far longer, because a rolling update can leave one still executing in the outgoing pod. Rationale and precedents: [`OCI.md`](OCI.md) § Job leases.
