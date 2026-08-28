# Roadmap

## Status

- **Phase:** MVP capabilities substantially complete → hardening toward GA.
- **Timeline:** Prototyping (Apr–May 2026, done) · MVP (Jun–Jul 2026) · GA target ~Aug 2026. *All future dates subject to change.*
- **Remaining MVP items:** none blocking — orb deployment packaging is done (Spike 14); orb registry is post-MVP.
- Dated capability log lives in **Recent accomplishments** (below); the full completed list is in **Shipped** (bottom).

---

## Recent accomplishments

- **2026-08-27** — Artifact compare UI: Publish History tab bar with route-backed Compare (linkable `?from=&to=`, selection persists), data-only stat header; `?dc=`/`status`/`limit` on `/api/v1/oci/artifacts` (fixes the capped-list defect, answers configbundle's version-lag ask); UI copy audit applied.
- **2026-08-26** — Audit log direction settled (Spike 34 closed): CloudTrail-primary convention, `events`→`audit_events` rename across tables/ent/handler, operator-configured retention (non-prescriptive, default keep-forever), `BeforeFields` drift guard.
- **2026-08-25** — Spike 30 shipped (`/api/v1/export/preview` content-diff + pre-publish review modal); Spike 33 shipped (one ConfigItem ownership declaration; audit-rollup drift fixed); Spike 31 guarded Apply (`expectedContentHash` → 409).
- **2026-08-21** — Designed the changeset-diff spike family: Spike 30 (pre-export preview = current vs last-published-artifact content diff, not audit-log; in-memory `canonicalNode`, OCI-digest baseline, `terraform plan` pattern), Spike 31 (AEP guarded-Apply + selective revert), and reconciled Spike 25 onto the shared content-diff core.
- **2026-08-14** — ServerMaintenance ConfigItem (schema v6): `enabled` switch + optional DateTime window, colo servers seeded present-and-off, bundler-downstream; JSON-editor field-clearing via DGraph `remove` (set:null no-op); architecture cheatsheet.

---

## Now / In flight

**Spike 21 — Observability / Monitoring integration** · *In progress (partial)*
How does orbital produce traces, logs, and metrics into the AKS cluster's existing OTel + Mimir + Loki stack without coupling code to backends?
> **Done:** HTTP access logs aligned to OTel HTTP server semantic conventions in orbital + orb (2026-06-09); orbital metrics reach devcc AMW via istio-merge + Cortex via a self-owned otel-collector (2026-08-03). **Remaining:** OTel SDK init, trace middleware, slog→OTel log bridge, export-pipeline spans, ServiceMonitor manifest; pick the canonical dashboards home (a fresh session owns this — see `project_devcc_observability` memory). Design in `docs/reference/OBSERVABILITY.md` and `docs/reference/monitoring-stack.md`.

---

## Next — open questions

Each spike is a question to answer; results refine MVP/post-MVP scope.

**Spike 9b — Valkey cache-aside** · *Not started*
What is the right caching strategy for read-heavy graph queries, and does orbital degrade correctly without it? (Post-MVP — baseline performance first.)

**Spike 19 — `orb scan` Infrastructure Scanner** · *Post-MVP*
How does an operator seed orbital with real iDRAC/storage config from a Galleon without manual transcription?
> CLI-only, stateless, operator-invoked. Scans Redfish endpoints on management LAN → GraphQL upsert mutations → human review → pipe to orbital GraphQL API. Output identical in structure to seed files. Never auto-imports. `internal/discovery/redfish/`, `internal/cli/scan/`. `internal/orb/` (server) must never import from these packages.

**Spike 23 — Audit existing tests for value** · *Not started*
Which tests guard real regressions vs which are theater that should be deleted?
> Walk every `*_test.go` and Playwright spec. For each, name the regression class it catches in one sentence. Classify **KEEP** (specific regression guard, security-critical, edge cases on pure functions, persistence round-trip) vs **DROP** (tautological; duplicates visual validation; integration tests that don't currently run due to unrelated build breaks; assertions that mostly test stdlib behavior). Output: delete list + one-line justification each. Calibration baseline = the test rules in CLAUDE.md. Triggered by 2026-06-13 session.

**Spike 26 — Provider-portable identity: id_token as Bearer** · *Not started*
How do we move orbctl + orbital from the AAD-specific "access_token carries identity" pattern to the standards-aligned OIDC "id_token as Bearer" pattern that works across any OIDC provider?
> **Why it matters:** today's pattern only works because AAD v2 embeds identity claims (`name`, `oid`, `preferred_username`, `upn`) into JWT access tokens — a provider-specific convenience, not the OIDC standard. Auth0/Okta include only `sub`; Google issues opaque access tokens. Pointing orbital at any of those breaks identity extraction. **Current state:** `internal/orbauth/auth.go:24` requests `api://.../user_impersonation offline_access` (no `openid`); `internal/auth/bearer.go:84-86` reads `claims.PreferredUsername`→`claims.UPN` (AAD-only); orbctl never sees an id_token. **Target:** orbctl requests `openid email profile offline_access`; persists + sends `id_token` as Bearer; orbital verifies id_token signature + audience + standard claims. Matches the existing UI `/auth/callback` flow (`oidc.go:62`, already uses `gooidc.IDTokenVerifier`) and the kubectl convention. **Scope:** `internal/orbauth/`, `internal/auth/bearer.go`, `internal/orbctl/`; migration path for existing users; decide if access_token is still useful. **Open Q:** does any orbital path actually need the access_token, or is it purely identity-as-claims today? Triggered by 2026-06-24 conversation.

**Spike 27 — Adopt Atlas for Postgres schema migration** · *Design complete, not started*
How does orbital handle schema evolution against legacy data — adding constraints to populated tables — without crashlooping deploys?
> **Design: `docs/spikes/spike-27-atlas-migrations.md`.** **Trigger:** v0.0.23 AKS deploy crashlooped adding an FK on `registry_artifacts.export_job_id`; long-lived DB had orphans that ent's schema-diff can't see. Fresh test DBs miss the class entirely. **Adopt Atlas** (ariga.io, ent-native — already an indirect dep; ent's migration engine *is* Atlas): versioned SQL under `ent/migrate/migrations/`, data migrations interleaved, `atlas migrate lint` in CI. **Decided: free tier only** — `atlas migrate lint` does **not** catch orphan-rows-blocking-an-FK (no analyzer covers it; lint never connects to a DB), so it would not have caught v0.0.23. The verified free equivalent is a hand-written `DO $$ … RAISE EXCEPTION $$` precondition block plus DML-before-DDL in the same migration; convention is that every FK/NOT-NULL-on-populated-table migration carries one. **Also found:** ent auto-migration **cannot rename** — `planInspect` only inspects ent-declared tables, so a renamed-away table silently orphans with all its data (verified in ent v0.14.6 source); and `WithDropColumn(true)` is armed in production at `main.go:67`. **Scope:** replace `db.Schema.Create(...)` in `cmd/orbital`; move migrations off startup (init container / `--migrate` flag) so a failure fails a Job instead of crashlooping every replica; CI applies pending migrations against a redacted snapshot of AKS Postgres; drop `WithDropColumn(true)`; orb SQLite follows later, not concurrently. **Independent of Spike 34** — that rename runs first under auto-migration, so Atlas baselines the already-correct schema. **Deliverable:** a new `docs/reference/MIGRATIONS.md` (decisions inline, per CLAUDE.md's Reference Index — the `docs/decisions/` ADR directory was removed 2026-07-07). Rejected: Liquibase/Flyway (Java tax), custom tooling (same reasoning as the "do not build a custom migration tool" decision for DGraph in CLAUDE.md § Settled Decisions).

**Spike 28 — In-pod bundler → orbital service-auth model** · *Post-MVP*
Should the co-located cb-bundler authenticate to orbital's `/graphql` via full AAD client-credentials (current) or via a loopback/localhost trust — and reconcile the contradictory docs?
> **Trigger:** external-jwt demo work (2026-07-23) broke the bundler's publish callback — external-jwt mode replaced the AAD verifier, so the bundler's AAD client-credentials token got signature-rejected. **Tactical fix shipped:** dual-issuer fallback (external-jwt routes Keycloak bearers to the Keycloak verifier, all others to the AAD `BearerVerifier`), restoring the ADR-010 path. **Real decision:** docs describe two conflicting models — **Model A (AAD client-credentials)** in `aad-bundler-role-assignment.md` + ADR 010 + `bearer.go` (what's built) vs **Model B (localhost bypass)** in `configbundle-integration.md:457/463/464` (only literally true in local dev `ORBITAL_DEV=true`; prod runs Model A). **Decide:** keep AAD (IdP-consistent, heavier) OR adopt real loopback-trust (deletes AAD from the hot path + App Role runbook; commits to the sidecar invariant; supersedes ADR 010). **Regardless: fix the doc contradiction** — 457/463/464 vs the AAD runbook cannot both stand. **Deliverable:** decision + doc reconciliation; if Model B, an ADR superseding 010.

**Spike 29 — Publish-action metrics (success / latency / failure-stage)** · *Not started*
What operational metrics should orbital emit around publish/export, and how do we collect them?
> **Trigger:** 2026-07-27 `/metrics` review. **Current state:** orbital exposes `/metrics` (Prometheus `client_golang`) with 3 HTTP-level metrics — but publish is an **async job**, so HTTP metrics capture only the fast `202` trigger, not outcome/duration/failure-stage. Publish truth lives only in `ExportJob`/`RegistryArtifact` DB rows. **Scope:** (1) scrape orbital (ServiceMonitor or pod annotation — **overlaps Spike 21**, reconcile don't duplicate); (2) publish domain metrics at job finalization in `publisher.go`/`export.go`: `orbital_publish_total{datacenter,result}`, `orbital_publish_duration_seconds{datacenter}`, `orbital_publish_failures_total{stage}`. **Decide:** metric namespace (`orbital_*`) and — reconcile with Spike 21 — `client_golang` at `/metrics` vs OTel `MeterProvider`→Mimir. **Complementary to** the publish audit event (Spike 25): audit = who/when (compliance), metrics = rate/latency/failure (ops).

**Spike 30 — Pre-export change preview ("diff preview")** · *Shipped 2026-08-24*
As a data center owner, how do I review how the export I'm about to publish differs from the last one I published, instead of publishing blind?
> **Shipped.** Synchronous, read-only `POST /api/v1/export/preview` returning a flat, per-`orbId` **desired-state content diff** — current blue subgraph vs the last published artifact pulled by OCI digest — computed in memory (no scratch DGraph). Surfaced in orbital's UI as the confirm gate on Publish. Explicitly a desired-state delta, **not** an apply-forecast. Rejected the audit log as the diff source (a `dropAll` restore is invisible to it). Shares its normalize+compare core with **Spike 25** (published-vs-published), which should reuse it rather than build an audit engine.
> **Reference (spike doc folded in and deleted per the spike-lifecycle rule in CLAUDE.md):** endpoint semantics, baseline states, why-not-the-audit-log, and precedent → `docs/reference/OCI.md` § "Export preview"; snapshot normalization + DGraph wire-shape gotchas → `docs/reference/DGRAPH.md` § "Comparing two graph snapshots"; the `expectedContentHash` guard → `OCI.md` § "Guarded Apply". Implementation: `internal/graphdiff/`, `internal/handler/export_preview.go`.

**Spike 31 — AEP change-management: guarded Apply + selective revert + attribution** · *Shipped 2026-08-25*
As a data center owner reviewing changes from multiple writers, how do I publish exactly what I reviewed — and discard what I don't want?
> **Shipped — orbital's side is complete; the remaining pieces need no orbital code.** (1) **Guarded Apply** built: optional `expectedContentHash` on `POST /api/v1/export` → `409 MVCC_CONFLICT` if intent moved between preview and Apply, with the verified snapshot pinned into the export so *shipped == reviewed*. (2) **Selective revert** is a client pattern, not an endpoint — the preview returns each field's prior value, so revert = writing it back via the existing `update{Type}` mutation. A batch `POST /api/v1/export/revert` was designed and **rejected**: its only gain was atomicity, and partial revert is harmless (nothing shipped; re-preview shows what remains) while the hash guard already ensures you publish what you reviewed. (3) **Attribution** is a client-side join of the diff against `GET /api/v1/audit-log?orbId=…` — diff answers *what*, audit answers *who/when*. **Orbital does not model PIM** — it enforces `RequireRole` on whatever the token carries; elevation is IdP-side. Reference: `OCI.md` §§ "Export preview" / "Guarded Apply", `AUDIT.md` § Event model, `AUTH.md` (PIM boundary).

**Spike 35 — Audit before/after capture, undo, and an intent change feed** · *Not started*
Should orbital persist a full before/after snapshot per changed node — and does that unlock undo-a-change and a CDC-style intent feed?
> **Split out of Spike 34 (closed 2026-08-26) as the part that is still an open design question, not settled work.** Today `details.before` is persisted but `after` is the mutation *input*, and `BeforeFields` is a hand-declared per-type subset. **Verified constraints:** DGraph v25 is Apache-2.0 with **no community/enterprise split** — CDC *is* available to us (`ee_features: ['backup_restore','cdc']`) but is **new-values-only**, so it cannot supply a before-image; there are **no pre-mutation triggers** (lambda pre-hooks were proposed and rejected upstream on latency grounds) and `@lambdaOnMutate` fires post-commit with the client's input, not prior state. The **only** race-free primitive is a **DQL upsert block** (query+mutation in one transaction) — which routes capture through `/mutate`, bypassing the GraphQL proxy that is currently the mutation chokepoint. That tension is the core design question. **Scope:** where to capture; `AuditEventResource` before/after columns (additive, not blocked on Spike 27); changed-fields vs full snapshots (NetBox stores full object snapshots, we lean changed-fields); undo-a-change with an optimistic "changed since" check — **name it distinctly from Spike 31's baseline-anchored publish revert**; blind-spot reduction by snapshot-diffing orbital's own restore/import via `graphdiff`. **Do NOT build the change feed without a named consumer.** If built: REST cursor feed, not Kafka — air-gap-friendly and `GET /api/v1/audit-log` already has `since` + pagination.

**Spike 34 — Audit log: convention, direction, and backlog** · *Closed 2026-08-26*
> **Answered and folded into `docs/reference/AUDIT.md`; spike doc deleted per the spike-lifecycle rule.** The audit log is **provenance for control-plane actions** — every attempt to act on orbital's API, successful or not. It answers *who did what, in one act, and when* (per-event, attributed); it never answers *what is the state* or *what is the net difference* — those are the Topology API and `graphdiff`. Same split AWS makes between Config and CloudTrail. **Convention: CloudTrail primary** for structure/taxonomy, NetBox for per-object presentation, K8s for the two-stream separation. **Shipped:** `events` → **`audit_events`** rename (tables, ent types, handler, file) so both layers share the root noun — the argument is collision, not consistency: K8s minted `audit.k8s.io` precisely because bare `Event` was already taken by a 1-hour-TTL operational stream. Also shipped: a `BeforeFields` drift guard, the phantom `/api/v1/events` route removed from the docs, and the publish-history panel relabelled "Audit Log" with explainers so it stops colliding with the content diff's "Changes". **Retention decided: orbital is not prescriptive** — `ORBITAL_AUDIT_RETENTION_DAYS` default `0` = indefinite, adopter sets it; the unratified "≥ 12 months" claim was removed. **Remaining work → Known debt § "Audit log — open backlog"; the still-open design question → Spike 35.**

**Spike 33 — Unify ConfigItem ownership into a single declaration** · *Shipped 2026-08-24*
As orbital adds ConfigItem types, how do we declare owned-child containment once — instead of hand-maintaining drift-prone parallel lists?
> **Shipped.** Mechanism (b) chosen — one shared Go declaration in `internal/configitems/registry.go` — over (a) a `schema.graphql` annotation, because DGraph rejects unknown directives (`@custom` is a resolver, not metadata) and orbital deliberately has no schema parser. `Type.OwnerEdges` models multi-parent, precedence-ordered ownership (`NetworkInterface`, `IPAddress`, `StorageVolume`); `OwnedChildren()`/`OwnedOrbIDSelection()` drive one generic registry-driven audit collector that replaced both hand-walked `collect*RelatedOrbIDs` functions. **Fixed real drift:** storage devices/volumes and switch-side interfaces now roll up in audit tabs; NetworkDevice and DataCenter aggregate children at all. **R3 consistency test** cross-checks the registry against `schema.graphql` at `make test-unit`. Reference: `docs/reference/DGRAPH.md` § "ConfigItem ownership" — the load-bearing findings (ServiceNow as the cautionary case against runtime-configurable ownership; ordered-slice precedence; the unenforced cross-namespace question) are folded in there. The 7-system comparative survey lived in a spike doc, deleted 2026-08-27 per the spike-lifecycle rule; recoverable via `git log docs/spikes/`. **Non-goal:** runtime-configurable ownership.

---

**Network topology rollout — remaining DCs** · *Data migration, not a spike*

> Colo is fully modeled + reconciled (Spike 32, shipped below). The same NetBox × Redfish reconcile is repeated per remaining data center to seed its fabric into orbital — bounded data-migration work, no open design question. **Endpoint:** once a DC is migrated, orbital is its network source of truth and NetBox is retired for it. Join keys + slow-API gotchas in `docs/reference/NETBOX.md`; discovery algorithm + colo examples in `docs/network-model.md §8`. Cabling = edge (no `Cable` node); VLAN/IP/MTU stay OUT.

## Production readiness (pre-GA)

- **Deployment:** ingress architecture, dedicated hostnames, TLS, internal vs external LB; production namespace layout, resource limits; Ratel via dedicated DNS + Istio VirtualService. Depends on infra-team input + auth/ingress architecture — not resolvable in prototype spikes alone. (orbital `//go:embed` pending if/when needed.)
- **CI/CD:** GitHub Actions workflow — `.github/` exists but has no workflow files yet (build/tag/push on merge to main; deploy to AKS dev on tag).
- **AKS smoke suite:** `make smoke-aks` exists but is shallow — expand to run the read-only Playwright projects against the AKS deployment.
- **Security hardening:** fix critical/high items before staging/prod exposure — see **Known debt → Audit backlog**. Includes the edge orb NetworkPolicy (default-deny + allow cb-controller/cb-agent/kube-system) deferred from Spike 14 — `deploy/edge/base` currently ships no NetworkPolicy, so the edge relies on cluster-level isolation until this lands.
- **Perf & cost:** benchmark DGraph query latency under realistic load; AKS node SKU cost estimate; Grafana dashboard + ≥1 alert (error rate / memory). Valkey cache-aside (Spike 9b) is post-MVP — baseline first.

---

## Known debt

**Validation gate — before closing any item:** `go build ./...` + `make test-unit` for Go changes; `make test-integration` for PostgreSQL/DGraph; `make test-e2e` or manual browser check for UI/JS; negative test (wrong config is actually rejected) for security-critical items.

### Track A — fix now (no test harness required)

| Item | Notes |
|---|---|
| Replace `title=""` tooltips with Tippy.js | 9+ usages in `divergence-reports.gohtml`, `users.gohtml`, `backup-jobs-tbody.gohtml`, `cluster-tab.gohtml`. Native tooltips have ~1s delay, no theming/positioning. Migrate to Tippy.js (~10KB). |
| Refactor bundler URL config DSL | `ORBITAL_BUNDLER_URLS=configbundle-bundler=http://...` is a custom micro-DSL in one env var; already caused one bug (preflight probed the raw `name=url` as a URL). Better: one env var per bundler, or ConfigMap-mounted YAML. |
| Collapse duplicate cluster backup structs | `backupKindResponse` (DGraph decode) and `backupKindTab` (template view struct) are hand-synced — adding a field means updating both or the cluster tab truncates mid-render (the 2026-07-27 `retentionDays` bug). Now *caught* by `TestClusterTab_BackupWithRetentionRendersFullFragment` + buffered `renderHTML`, but drift is still possible. Render the template off the query struct directly so a new field lands in one place. `internal/handler/cluster.go`. |
| Unify `/graphql` proxy-guard error envelope | **Deferred — build only when a client needs it.** Proxy guards (403/409/400) return the REST `errorResponse` body; DGraph's native `errors[]` passes through untouched, so a caller gets one shape or the other. Unify = wrap proxy-guard rejections in a synthetic `errors:[{message, extensions:{code,httpStatus,hint}}]`. Cheap (codes already match Apollo) but touches 403/409 behavior + orbctl parsing + e2e — don't do speculatively. Context in `docs/reference/ERROR-RESPONSES.md`. |
| Replace hand-rolled GraphQL request parsing with a real AST parse | **Address soon.** The `/graphql` proxy understands mutations via regex + string matching (`knownMutationRe`, `extractOperations`, comment-stripping, the Spike 31 inline-selector guard) — parser-level fragility without parser-level correctness. A real AST parse (`gqlparser`, needs dep sign-off) strengthens authz + stamping together and removes the inline-selector constraint (Spike 31 Option B). Not MVP-urgent (every real caller uses the variable form) but this is the principled fix. Touches the load-bearing `Handle` path — do it deliberately with the Spike-31 tests as the net. |

### Audit backlog — May 2026 audit, triaged 2026-07-23

> Verified against current code; source finding files deleted. The security audit's two **Critical** findings (unauthenticated `/graphql`; missing readiness probe) and "RBAC defined but not enforced" were already **FIXED** (Spike 11 + single authenticated `/graphql` group + `/healthz`). **No item below is an OSS release-blocker.**

| Item | Notes |
|---|---|
| OIDC config ships real Armada identifiers as defaults | **Pre-OSS deletion pass, not a code condition.** Blank the real tenant/app-ID/allowlist/`admin@armada.ai` defaults to `""` — `config.go:60-72`. The *fail-closed* half is already handled generically by S.16 (`server.go` refuses to boot in prod when `apiAuth` is empty); do NOT add an Armada-specific `== "<tenant-guid>"` boot check (rejected 2026-08-13 — vendor cruft that doesn't serve OSS). Once defaults are empty, a prod deploy that forgets to configure OIDC fails the boot via S.16. (A.1) |
| ~~Rate limiting absent~~ **DONE 2026-08-13** | Per-IP token buckets (in-memory, single-replica), opt-in via `ORBITAL_RATE_LIMIT_ENABLED` (off in dev/AKS-dev, prod enables); tighter bucket on `/user/login`; denials render the `RATE_LIMITED`/429 envelope + `Retry-After`. `server.go`. (S.12) |
| ~~No request body size limit~~ **DONE 2026-08-13** | Global `echomw.BodyLimit(ORBITAL_MAX_REQUEST_BODY, default 10M)` bounds the unbounded `io.ReadAll` on `/graphql`; over-limit renders the `CONTENT_TOO_LARGE`/413 envelope. No file-upload endpoints, so global is safe. `server.go`. (S.7) |
| ~~OIDC-unreachable degrades to no-auth~~ **DONE 2026-08-13** | `server.New` now returns an error and refuses to boot when `!Dev && len(apiAuth)==0` — covers unreachable OIDC discovery, verifier-init failure, and unset issuer in one generic guard. Dev unaffected (`!Dev`-gated). `server.go`. (S.16) |
| ~~No audit on destructive deletes~~ **DONE 2026-08-13** | `BackupHandler.Delete` → `deleteBackup` and `OCI.DeleteArtifact` → `deleteArtifact` management events (actor via `actorFromContext`, `writeAuditEvent`). Test gap: integration guard (`//go:build integration`, needs `make up`) asserting each delete writes its event — add via `newBackupHandler` + `testDB.Event.Query()` when the stack is up. (S.10) |
| ~~MVCC silent-pass on bad version type~~ **DONE 2026-08-13** | `toFloat64` now returns `(float64, ok)`; a malformed `ifVersion` is rejected `400 BAD_USER_INPUT` (a garbage concurrency token is a client error, not a "reload and retry" 409 — deviates from the finding's suggested 409, deliberately). Unit-pinned by `TestToFloat64` !ok cases. `graphql.go`. (A.3) |
| OIDC nonce + constant-time state | Add `nonce` binding; `subtle.ConstantTimeCompare` for state — `oidc.go:95`. (S.14, S.15) |
| Export/restore job-creation TOCTOU | Serialize (mutex or unique partial index); concurrent triggers corrupt scratch-DGraph — `export.go:216-230`. (S.8) |
| Async jobs orphaned on shutdown | Track goroutines (WaitGroup + cancellable ctx); SIGTERM mid-restore can leave DGraph wiped — `server.go`, `restore.go:286`. (S.9) |
| Orb registration not unique | Unique indexes on `datacenter_id` + `public_key` — `ent/schema/orb.go`. (S.11) |
| Job-status columns unindexed | `index.Fields("status")` on ExportJob/Backup/RestoreJob; conflict queries are full table scans. (A.7) |
| Backup zip in `/tmp`, defer-only cleanup | Write to a controlled dir + orphan reaper — `backup.go:627-631`. (A.2) |
| Export scratch-dir leak | `defer os.RemoveAll(jobScratchDir)` after MkdirAll — `export.go:900`. (A.4) |
| `orb scan` fabricates success | Return "not implemented" instead of hardcoded "Found 3 BMC interfaces" until Spike 2 real discovery — `scan.go:16`. (A.6) |
| GET `/export/jobs` writes to DB | Move `StatusStale` marking out of the List handler (HTTP-safety violation; swallows the write error) — `export.go:255-262`. (S.18) |
| OCI push/sign dual credential stacks | Unify ORAS + go-containerregistry creds, or document the coupling — `publisher.go:316-318,371-373`. (A.5) |
| DGraph query string interpolation | Parameterize interpolated queries (`%q`-quoted, low practical risk) — `export.go:963,1049,1223`. (S.17) |
| Orbital templates not embedded | orbital handlers still `ParseFiles` from disk; orb already embeds. Deployment note, non-security — `web/embed.go`. (D.1) |

### Audit log — open backlog (Spike 34 closed 2026-08-26; decisions in `docs/reference/AUDIT.md`)

> Direction, convention, naming, and retention policy are **settled and folded into `docs/reference/AUDIT.md`** (§§ What the audit log is for / Naming / CloudTrail field parity / Retention). What remains is work, not open questions.

| Item | Sev | Notes |
|---|---|---|
| **Rejected mutations write no audit row** | **High** | `graphql.go` gates the audit write on `!hasGQLErrors(respBytes)`, so a mutation that passes authz and is then rejected by DGraph (validation, constraint, schema) leaves **zero trace**. `authorizationDenied` *is* covered separately by `RequireRole` middleware — the hole is post-authz failures. CloudTrail records these; see `AUDIT.md` § CloudTrail field parity. Fix pairs with the `error_code`/`error_message` columns below. |
| **Add the 4 missing CloudTrail fields** | Medium | `event_source` (`graphql`/`rest`/`internal` — currently unknowable), `source_ip_address`, `user_agent` (the standing OWASP gap), `error_code`/`error_message`. All **additive nullable columns**, so safe under ent's append-only auto-migration — not blocked on Spike 27. Make the error fields mandatory top-level, not buried in a response body. |
| **Retention: env var + in-process pruner** | Medium | Decided: `ORBITAL_AUDIT_RETENTION_DAYS`, default `0` = indefinite, operator sets it (orbital is not prescriptive). **Nothing prunes today.** Ship the pruner in-process from day one — NetBox's 90-day default sat inert behind an optional cron and silently deleted history on upgrade when 4.4 activated it. Also surface row count / oldest-record age, since the default is unbounded. |
| `changes[]` absent for creates, bulk adds, multi-op events | Medium | `before` is fetched only for single-entity mutations carrying `id`/`orbId`, so `addServer(input: [...])` produces no diff. NetBox changelogs all three. Cheaper alternative: emit `changes[]` with `before: null` so clients have one code path. |
| Non-orbId-filtered mutations record empty `resource_ids` | Medium | Mutations filtered by another field that select only `{numUids}` are invisible to every per-orbId panel. Documented in `AUDIT.md` § extractResourceIDs. |
| `after` is the mutation input, never a post-mutation re-read | Low | If DGraph coerces or partially applies, the audit asserts something that did not happen. Decide: pay a round-trip, or document it. Leaning document. |
| Out-of-band writes are invisible to API consumers | Low | `dropAll` restore writes zero rows; direct-DQL and seeding bypass the proxy. Documented internally, but nothing tells a **client** the trail has holes — consider a `disclaimer` field like the diff endpoints carry. |
| Subtree audit scope is N calls | Low | Clients traverse Topology for descendant orbIds then pass ≤32 repeatable `orbId` params. A schema-level `descendantOrbIds` field would make it one call. Build when a client asks. |


### Track B — needs a 15–20 min Opus design session before Sonnet implements

| Item | Notes |
|---|---|
| DGraph client abstraction | 22+ raw `http.Post` calls across 7 handler files, no timeouts, no pooling. Extract `internal/dgraph/client.go`. **Interface shape is a design decision** (transport vs semantic level) — do NOT implement on Sonnet without a settled design. Primary unlock for Go integration testing. |
| `internal/handler/` god package | ~3,500 lines mixing routing, business logic, DGraph calls, file I/O. Decompose post-MVP in 3 steps: extract `internal/storage/`, then `internal/export/`, then `internal/backup/`. Do NOT start before the MVP feature cut. |
| Registry-driven orbId derivation (kill the `leafSuffix` escape hatch) | **Trigger:** ServerMaintenance feature, 2026-08-13. **Problem:** `configitems.BuildEditTargets` derives every owned-child edit-target orbId as `fmt.Sprintf("%s:%s-%s", namespace, name, leafSuffix(kind))` (`registry.go:469`), where `leafSuffix` (`registry.go` ~518) is a hardcoded `switch`. That only matches the **legacy** `<ns>:<parentName>-<suffix>` shape. New prefix-convention types (`server-maintenance-<serial>`, `network-adapter-<serial>-<FQDD>`) don't fit, so every editor-creatable type needs a hand-written `OverrideEditTargetOrbID` in its page handler — e.g. `server.go` overrides `IdracSettings` (with the *fetched* id) **and** `ServerMaintenance` (with a *constructed* `<ns>:server-maintenance-<serviceTag>`). The correct orbId shape lives in code, so the registry is no longer the single source of truth for orbId. **Why it bites:** for an *optional* child (first-time-create is the common case) the wrongly-derived id is what actually gets written — a silent correctness footgun, not cosmetic. **Fix:** let the `Type` struct declare its orbId pattern so `BuildEditTargets` derives the right id with no override. **Design decision (why Track B):** the pattern needs natural-key inputs the registry doesn't hold (owner serial/serviceTag, FQDD, …). Options: **(a)** `OrbIDKey func(parent map[string]any) string` per-type closure — handlers pass the fetched parent object; handles multi-part keys (network types); **(b)** declarative template + `KeyField` naming the parent field that supplies the key (e.g. `server-maintenance-{key}`, KeyField `serviceTag`) — simpler but weak for multi-part keys; **(c)** just move each construction into a named registry helper (declared once, not re-derived per handler). Recommend (a). **Scope:** `registry.go` (`Type` + `BuildEditTargets`, retire `leafSuffix`); remove the now-redundant `OverrideEditTargetOrbID` calls in `server.go` (+ audit `cluster.go` / any network editor) where the pattern covers them, keeping override only for genuine legacy/fetched-id cases; add a registry test asserting each editor-creatable type derives its documented `DGRAPH.md` orbId. **Acceptance:** adding a new prefix-convention ConfigItem needs ZERO handler orbId override — schema + registry entry is enough (the add-configitem playbook's promise). Pin with a test that derives `ServerMaintenance` → `<ns>:server-maintenance-<serial>` with no handler code. |

### Architecture — requires design discussion before any code

| Item | Notes |
|---|---|
| Orbital HA — pervasive single-replica assumptions | Deployed `replicas: 1`, `strategy: Recreate`. Divergence ingester (`lastIngestedByDC` in-memory), backup scheduler (cron double-fires), publish-history ingester all assume single-leader. Going HA needs leader election / advisory locks / dedicated ingest deployment. **Do NOT scale past `replicas: 1`** — double-ingestion corrupts divergence state. |
| Auto-apply DGraph GraphQL schema on startup | Today orbital does NOT apply `schema.graphql` on boot, so a schema-bumping image deploy silently drifts until someone POSTs to `/admin/schema` — the 2026-07-27 AKS burn (v0.0.25 queried `retentionDays`, DGraph still v3 → every cluster 404'd). **Proposed:** apply the schema additively on startup, mirroring ent auto-migrate for PostgreSQL. **Design points:** (1) re-scopes the "schema migration out of MVP" decision; (2) blue-green apply to active/blue on boot; (3) additive-only guard; (4) confirm multi-replica racing is harmless (DGraph serializes); (5) fix the `schema.graphql:6` vs `DGRAPH.md` doc contradiction. Alternatives: kustomize initContainer, GitOps PreSync Job. Triggered by the 2026-07-27 burn. |

---

## Post-MVP enhancements

| Item | Notes |
|---|---|
| SDL syntax highlighting on Schema page | Replace plain `<pre>` with Prism.js `language-graphql` on both orbital + orb schema pages. Self-host Prism JS/CSS (no CDN). ~30 min once asset serving is scoped. |
| Divergence observation audit (forensics) | Ingester hard-deletes `DivergenceEntry` rows on loop closure, so "what did we see and when" is unanswerable. Action audit (who decided) is covered by `DivergenceResolution` + `Event`; observation audit (what value, claimant, first-seen→closed-at) is not. Order: (1) check if the resolution Event payload snapshots `current_value`/`intent_value`/`claimant`; (2) if not, soft-delete via a `closed_at` column + index; (3) defer TTL until size is a real problem. Avoid a parallel history table. |
| Migrate password hashing to Argon2id | Current bcrypt is sound, not broken. Argon2id is OWASP's first recommendation (memory-hardness). `golang.org/x/crypto/argon2` already indirect. Needs a migration path (detect by hash prefix on login, re-hash on verify). |
| Migrate enum-like String fields to GraphQL enums | `String # value1, value2` across the schema (`IPAddress.type/role`, `KubernetesCluster.cni/environment`, etc.). DGraph supports `enum` — schema-enforced values, typed filters, self-documenting introspection. Do as a single coordinated cut (half-and-half reads worse). |
| Swagger response-model sweep (Tier 2) | Request bodies are typed DTOs (done 2026-07-23). Responses still use `{object} map[string]string` on ~65 annotations across 12 files — renders as the useless `additionalProp` bag. Replace with named response types (shared `ErrorResponse`, `ConflictResponse{error,id}`, endpoint-specific). One pass + `make docs`. Prioritize orbital (AEP consumes it) over orb. |
| Refactor bundler URL config away from `name=url` env DSL | See Track A. Options by effort: (1) one env var per bundler; (2) ConfigMap-mounted YAML (`ORBITAL_BUNDLERS_FILE`) — the right MVP+1 step, keeps docker-compose/bare-metal portability; (3) K8s Service discovery by label selector. Touches `config.go`, `bundler/client.go`, `server.go` (preflight), `oci.go`, `deploy/base/deploy.yaml`. |

---

## Shipped

One line per completed spike/capability. Implementation detail is in git history + the domain docs (`docs/reference/*.md`); dated capability summaries are in **Recent accomplishments**.

- **Handler integration suite green again** (2026-08-25) — fixed 10 failures + a 1-in-4 flake. Tests invoked handlers directly, so a returned `*echo.HTTPError` never reached the central `ErrorHandler` and never became the JSON envelope they asserted on; added a `renderErr` helper mirroring production. Also: one stale `{"id":...}` expectation, and a `clearEvents` race against background audit writers (FK on `event_resource_types`).
- **Artifact-to-artifact diff** (API 2026-08-25, UI 2026-08-27) — `GET /api/v1/export/compare?from=&to=` returns the desired-state delta between any two published artifacts of the same DC, pulled by immutable digest and diffed in memory on the shared `graphdiff` core (no scratch DGraph, no DGraph call at all). **UI:** Publish History gained a tab bar whose tabs are **real routes**, not client-side panels, so `/publish-history/compare?from=&to=` reproduces a given diff for sharing; the Compare tab picks data center + two versions, and the selection survives tabbing away via `sessionStorage`. Cold start opens on the most recently published DC — defaulting to the alphabetically-first one landed users on an empty state. Reuses the export-preview renderer verbatim, since both come from the same core. **Header carries data only** (stat row: Changed/Added/Removed/Unchanged, colour not bold, same colours as the sections below; same-day publishes collapse to times) — the GitHub/terraform convention that a diff header states counts and never explains what the view isn't. The audit-backed panel stays, relabelled **"Audit Log"** to match the noun used everywhere else in the product; the one sentence explaining why the two can disagree (ordered event stream vs net difference) lives **only** on the zero-differences empty state, where that confusion actually bites. Enabled by `?dc=`/`status`/`limit` filters on `/api/v1/oci/artifacts` — which also answered configbundle's version-lag ask without a new endpoint. **Compare on `digest`, not `tag`:** the tag sequence is per OCI repository and the repo name derives from the DC's editable name, so a rename restarts it at v1 for the same orbId.
- **Divergence reporting end-to-end** (2026-08-25) — edge overrides surface to cloud admins: orb intake → orbital ingestion + `/divergence-reports` UI + accept/reject/ignore dispatching `update{Type}`; cb-controller side (event-driven Divergence Reporter + `spec.takeover[]`/`spec.ignored[]` + reclaim) shipped in configbundle. Cross-repo E2E tracked as configbundle Spike 8.

| # / Area | Done | Summary |
|---|---|---|
| 1 · AKS deployment validation | Apr 20 | Orbital + DGraph on AKS, NetworkPolicy, pod recovery validated |
| 2 · Orb CLI structure | Apr 22 | Single binary; `orb start` long-running edge service |
| 3 · PostgreSQL / ent data model | May 5 | ent tables: users, orbs, namespaces, jobs, audit log, OCI artifacts |
| 4 · Web UI | May 6 | Data Centers (HTMX inline edit + audit diff), Servers cross-DC DataTable + drill-down, Export/Backup/Restore/Audit/Schema/Divergence pages, Playwright E2E |
| 5 · Authentication | May 8 | OIDC + local auth, CLI keychain, bearer token validation end-to-end |
| 6 · DGraph backup to S3 | May 9 | Async backup to Azure Blob/S3, count-based retention, presigned download; `ORBITAL_BACKUP_SCHEDULE` cron single source of truth |
| 7 · DGraph restore | May 14 | Full restore via dgraph-live pod, validated on AKS |
| 8 · AKS dev environment | May 18 | Deploy manifests, Helm charts, seed scripts, deploy guide |
| 9 · Hardware data modeling | May 15 | iDRAC schema fields; 9 data centers from real Netbox hostnames + rack topology |
| 10 · Air-gap sync round-trip | — | Orb loads `json.gz` into local DGraph and serves offline |
| 11 · Authorization | Jun 5 | `readonly<dev<admin`, `RequireRole`/`RequireAdmin`, `/users` admin UI + last-admin guard, `ORBITAL_ADMIN_EMAILS` bootstrap, readonly UI gating, single authenticated `/graphql`. See `AUTH.md`. |
| 12 · Orb import API | — | OCI puller (oras-go v2), cosign verify, `dgraph live` subprocess, polling loop; consumer dispatch on both import paths; import history UI |
| 13 · Divergence intake (orb) | May 24 | `POST /api/v1/divergence` replaces pending set; `/divergence/publish` writes snapshot to S3 |
| 14 · Orb deployment model | Aug 10 | Kustomize-native edge deploy (`deploy/edge/base` + `colo-galleon` overlay): orb + edge DGraph + Zot ACR mirror; unified Dockerfile (two targets, dgraph baked in); `//go:embed` templates/static; `SubprocessBackend`; `/healthz` readiness; `make release-check`. Demo-proven on colo-galleon. Helm superseded by kustomize (canonical); default-deny NetworkPolicy deferred to security hardening. |
| 15 · Orb API surface & authN/Z | Jun 7 | MVP: no in-process auth; NetworkPolicy is the gate. Post-MVP: SA + TokenReview. See `ORB.md §Auth`. |
| 16 · Orb UI | May 24 | Orbital + orb share template infra, different nav/capability surfaces |
| 17 · ES module split of app.js | Jun 6 | `app.js` → `shared.js` + `orbital.js` + `orb.js`; conditional load in `head.gohtml`; `window.*` bridge; web dir cleanup |
| 18 · ConfigBundle bundler integration | May 26 | Per-request bundler URLs, all-or-nothing before push, `enriched`/`bundler_error` on RegistryArtifact, retryable HTTP + size cap. `internal/bundler/`. |
| 20 · DGraph schema versioning + backup manifest | Jun 9 | `schema/VERSION`; versioned backup filenames + `manifest.json`; restore 409 on schema mismatch + `confirmSchemaMismatch` override |
| 24 · Divergence MVCC + storage split | Jun 14 | Divergence/resolution stay in PostgreSQL (graph move rejected); `ConfigItem.version` auto-increment, `ifVersion` opt-in race detection |
| 31 · Reject inline single-entity mutations | Jul 28 | Option D — `rejectInlineSelectors` guard 400s `update{Kind}` lacking a variable selector; `VARIABLE_FORM_REQUIRED` with copy-pasteable hint |
| 32 · Network fabric (device↔device) | Aug 12 | `NetworkDevice`/`NetworkAdapter`/`NetworkInterface` (interface reused for server NICs, BMC, and switch/firewall ports); colo fabric (OOB↔SRX↔ToR) + server↔switch cabling seeded from NetBox × Redfish; Network Devices UI + Connections tab; `server-<serial>` orbId migration; colo 100% reconciled incl. Supermicro A100. Other-DC rollout = data migration (see Next). |
| orbctl | May 11 → Jun 9 | `get datacenter[s]`, bearer auth, macOS keychain, kubectl-style output; Homebrew tap; per-component `cli/v*` versioning (ADR-009); pure-Go build; silent token refresh |
| Config Export + OCI pipeline | May 9 – 18 | Scratch-based scoped export (dedicated scratch DGraph per job), oras-go v2 + cosign signing, air-gap-safe publish |
| Audit Log System | May 5 – 13 | GraphQL mutation interceptor, before/after field diff, three-source orbId extraction, per-entity audit tabs. See `AUDIT.md`. |
| Assorted debt fixes | Jun 25 – 29 | Stuck-job reaper, session store singleton, OCI push rollback on sign failure, `RegistryArtifact→ExportJob` edge, named orbctl operations, resource-tab audit cap (200), reload-button error guards, DC/Server tab handler collapse (ADR 011), async goroutine timeouts, prod HMAC check, restore checksum verify, backup-retention orphan fix |

---

## External integration dependencies

| System | Role | Status |
|---|---|---|
| **Atlas UI** | Digital twin — queries orbital via GraphQL to visualize topology | Integration approach defined |
| **PLM** | Bill of materials for hardware — orbital may query to enrich config items | Vendor evaluation in progress |
| **ITSM** | Links support tickets to config changes | Vendor evaluation in progress |
