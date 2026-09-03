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

There are currently **no maturity toggles**. Adding one requires naming its removal trigger in this table.

**There is deliberately no global "enforcement on/off" setting.** Enforcement is a property of each policy — disable the one that is misbehaving and the rest keep working. That is what every comparable engine does ([Kyverno](https://kyverno.io/docs/policy-types/cluster-policy/validate/) `validationFailureAction`, [Gatekeeper](https://open-policy-agent.github.io/gatekeeper/website/docs/howto/) `enforcementAction`, [HCP Sentinel](https://developer.hashicorp.com/terraform/cloud-docs/workspaces/policy-enforcement/manage-policy-sets) enforcement level, GitHub rulesets' enforcement status), and a global switch is the blunt version of it. The per-policy control is always reachable: policy administration writes PostgreSQL, never DGraph, so it is never itself gated.

## All settings

Generated from `internal/config/config.go` — the struct tags are the source of truth. A blank default means the value is required, or is derived at runtime.

| Variable | Default |
|---|---|
| `ORBITAL_ADMIN_EMAILS` | `admin@armada.ai` |
| `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` | `5fc832f6-843e-4207-93dd-b3c3a77c06f2` |
| `ORBITAL_AUTH_MODE` | — |
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
| `ORBITAL_JWT_AUDIENCE` | — |
| `ORBITAL_JWT_CLIENT_ID` | — |
| `ORBITAL_JWT_DEFAULT_ROLE` | `readonly` |
| `ORBITAL_JWT_ISSUER` | — |
| `ORBITAL_LOGIN_RATE_LIMIT_RPS` | `5` |
| `ORBITAL_LOG_LEVEL` | `info` |
| `ORBITAL_MAX_REQUEST_BODY` | `10M` |
| `ORBITAL_OCI_ALLOW_HTTP` | `true` |
| `ORBITAL_OCI_PASSWORD` | — |
| `ORBITAL_OCI_PUBLISH_TIMEOUT` | `10m` |
| `ORBITAL_OCI_REGISTRY` | `localhost:5001` |
| `ORBITAL_OCI_REPO` | `orbital` |
| `ORBITAL_OCI_SIGNING_KEY_PATH` | `deploy/local/cosign.key` |
| `ORBITAL_OCI_USERNAME` | — |
| `ORBITAL_OIDC_CLIENT_ID` | `5fc832f6-843e-4207-93dd-b3c3a77c06f2` |
| `ORBITAL_OIDC_CLIENT_SECRET` | — |
| `ORBITAL_OIDC_ISSUER_URL` | `https://login.microsoftonline.com/8f231c2a-9551-4b40-be17-5b24afe5e890/v2.0` |
| `ORBITAL_OIDC_REDIRECT_URL` | `http://localhost:8001/auth/callback` |
| `ORBITAL_PORT` | `8001` |
| `ORBITAL_RATE_LIMIT_ENABLED` | `false` |
| `ORBITAL_RATE_LIMIT_RPS` | `40` |
| `ORBITAL_RESTORE_TIMEOUT` | `10m` |
| `ORBITAL_SCHEMA_PATH` | `schema/schema.graphql` |
| `ORBITAL_SESSION_ENCRYPTION_KEY` | `local-dev-enc-key-32-bytes-pad!!` |
| `ORBITAL_SESSION_HMAC_KEY` | `local-dev-hmac-key-change-in-prod` |
| `ORBITAL_SHUTDOWN_TIMEOUT` | `10s` |
