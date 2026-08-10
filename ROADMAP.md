# Roadmap

## Status

- **Phase:** MVP capabilities substantially complete → hardening toward GA.
- **Timeline:** Prototyping (Apr–May 2026, done) · MVP (Jun–Jul 2026) · GA target ~Aug 2026. *All future dates subject to change.*
- **Remaining MVP items:** orb deployment packaging (Spike 14); orb registry is post-MVP.
- Dated capability log lives in **Recent accomplishments** (below); the full completed list is in **Shipped** (bottom).

---

## Recent accomplishments

- **2026-08-04** — Orb edge observability: `/metrics` (dedicated registry) with round-trip propagation + RED, scraped via ServiceMonitor → colo-dev-main Grafana dashboard; fixed sig-sync regression (edge Zot tag-regex now matches cosign `.sig`).
- **2026-08-03** — Cut colo-dev-main edge propagation ~9min→~8s (Zot mirror scoped to v70+ tag filter + CPU/worker bump + SyncLegacyCosignTags for sigs); redesigned Publish History columns — failure error moved out of the Status badge into its own column.
- **2026-07-29** — Audit-log API readied for AEP (orbital's first client): `operation_name` JSONB filter, pre-computed `changes` diff field (present iff single-entity), DGraph UIDs stripped from audit before-state, typed Swagger response + cheatsheet "Audit log" recipes.
- **2026-07-28** — Standard error envelope (`error`/`code`/`httpStatus`/`hint`) via a central Echo `ErrorHandler` + code registry, retiring Echo's `{"message"}`; Spike 31 done — inline single-entity mutations rejected `400 VARIABLE_FORM_REQUIRED` (default on, kill switch).
- **2026-07-27** — external-jwt bearer auth validated on AKS dev; fixed cluster-edit truncation (retentionDays render-struct drift); hardened all template rendering to buffer-then-write (drift fails loud, not silent); auth-mode + FIPS startup logs.

---

## Now / In flight

**Spike 14 — Orb deployment model** · *In progress*
What does orb look like deployed at the edge — topology, runtime deps, air-gap constraints?
> **Done:** local edge sim (orb DGraph + Zot ACR mirror); orb Dockerfile (unified single Dockerfile, two targets, dgraph binary baked in); orb `//go:embed` templates+static; `SubprocessBackend` replaces idle-pod `K8sBackend`; orbital `/healthz` readiness probe; `make release-check` orchestrator; kustomize overlays canonical. **Remaining:** Helm chart for orb; NetworkPolicy manifest (default-deny + allow cb-controller/cb-agent/kube-system).

**Spike 21 — Observability / Monitoring integration** · *In progress (partial)*
How does orbital produce traces, logs, and metrics into the AKS cluster's existing OTel + Mimir + Loki stack without coupling code to backends?
> **Done:** HTTP access logs aligned to OTel HTTP server semantic conventions in orbital + orb (2026-06-09); orbital metrics reach devcc AMW via istio-merge + Cortex via a self-owned otel-collector (2026-08-03). **Remaining:** OTel SDK init, trace middleware, slog→OTel log bridge, export-pipeline spans, ServiceMonitor manifest; pick the canonical dashboards home (a fresh session owns this — see `project_devcc_observability` memory). Design in `docs/reference/OBSERVABILITY.md` and `docs/reference/monitoring-stack.md`.

**Spike 22 — Divergence reporting end-to-end** · *Phases 0+A+A.1 done; Phase B blocking*
How do field-level edge overrides surface to cloud admins, with cb-controller producing reports and orbital ingesting/resolving them?
> **Done:** Phase 0 (orb mapping + intake); Phase A (orbital ingestion, REST, server-rendered UI with grouped/expandable rows, MinIO integration test); Phase A.1 (Accept dispatches `update{Type}` mutation via GraphQL proxy, resolution recorded only on success). **Remaining:** Phase B in configbundle repo (cb-bundler emits `mapping.json` layer, Divergence Reporter ctrl.Runnable, `spec.takeover[]` consumption); Phase C cross-repo E2E test; Ed25519 signing deferred post-MVP. Design context: `docs/reference/SDD-CONTEXT.md §12`.

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

**Spike 25 — Publish provenance & changeset diff** · *Not started*
How do operators tell what changed between two published OCI artifacts, given orbital is intentionally not gitops?
> **Today:** OCI digest pins the bundle, audit log records every intent mutation — both exist, not stitched on the publish history page. "What changed between v8 and v9" is answerable today only by manually time-windowing the audit log against publish timestamps. **Minimum:** enrich the publish audit event with `{oci_digest, version, audit_cursor_at_publish, entry_count}`; add a "Changes since previous publish" panel on the publish history detail page that runs the cursor-window query (cheap, indexable, monotonic). **Explicit non-goal:** do NOT log all N resource IDs per publish — the OCI artifact already IS the authoritative manifest; duplicating ~780 IDs per publish would inflate the audit log by ~285K rows/year for data that's already digest-pinned. **Reference systems:** NetBox per-object change log, Argo CD sync history, Helm `helm history` + `helm diff`, Terraform Cloud plan output — all share the shape: versioned anchor + changeset-since-previous-anchor. **Gitops critique to answer:** (a) rollback = DGraph backup/restore tied to publish digest (we have this); (b) "diff v8..v9" = cursor-window audit query (this spike); (c) why not git? — intent lives in DGraph because traversal queries are the primitive, git is bad at that shape; (d) **legit gap:** no "review changeset before Publish" approval workflow — split out if/when scoped. Triggered by 2026-06-23 conversation.

**Spike 26 — Provider-portable identity: id_token as Bearer** · *Not started*
How do we move orbctl + orbital from the AAD-specific "access_token carries identity" pattern to the standards-aligned OIDC "id_token as Bearer" pattern that works across any OIDC provider?
> **Why it matters:** today's pattern only works because AAD v2 embeds identity claims (`name`, `oid`, `preferred_username`, `upn`) into JWT access tokens — a provider-specific convenience, not the OIDC standard. Auth0/Okta include only `sub`; Google issues opaque access tokens. Pointing orbital at any of those breaks identity extraction. **Current state:** `internal/orbauth/auth.go:24` requests `api://.../user_impersonation offline_access` (no `openid`); `internal/auth/bearer.go:84-86` reads `claims.PreferredUsername`→`claims.UPN` (AAD-only); orbctl never sees an id_token. **Target:** orbctl requests `openid email profile offline_access`; persists + sends `id_token` as Bearer; orbital verifies id_token signature + audience + standard claims. Matches the existing UI `/auth/callback` flow (`oidc.go:62`, already uses `gooidc.IDTokenVerifier`) and the kubectl convention. **Scope:** `internal/orbauth/`, `internal/auth/bearer.go`, `internal/orbctl/`; migration path for existing users; decide if access_token is still useful. **Open Q:** does any orbital path actually need the access_token, or is it purely identity-as-claims today? Triggered by 2026-06-24 conversation.

**Spike 27 — Adopt Atlas for Postgres schema migration** · *Not started*
How does orbital handle schema evolution against legacy data — adding constraints to populated tables — without crashlooping deploys?
> **Trigger:** v0.0.23 AKS deploy crashlooped adding an FK on `registry_artifacts.export_job_id`; long-lived DB had orphans that ent's schema-diff can't see. Fresh test DBs miss the class entirely. **Adopt Atlas** (ariga.io, ent-native): versioned SQL under `ent/migrate/migrations/`, data migrations interleaved, `atlas migrate lint` in CI. **Scope:** replace `db.Schema.Create(...)` in `cmd/orbital`; move migrations off startup (init container / `--migrate` flag); CI applies pending migrations against a redacted snapshot of AKS Postgres; drop `WithDropColumn(true)`; same for orb SQLite. **Deliverable:** ADR-014. Rejected: Liquibase/Flyway (Java tax), custom tooling (mirrors ADR-007 for DGraph).

**Spike 28 — In-pod bundler → orbital service-auth model** · *Post-MVP*
Should the co-located cb-bundler authenticate to orbital's `/graphql` via full AAD client-credentials (current) or via a loopback/localhost trust — and reconcile the contradictory docs?
> **Trigger:** external-jwt demo work (2026-07-23) broke the bundler's publish callback — external-jwt mode replaced the AAD verifier, so the bundler's AAD client-credentials token got signature-rejected. **Tactical fix shipped:** dual-issuer fallback (external-jwt routes Keycloak bearers to the Keycloak verifier, all others to the AAD `BearerVerifier`), restoring the ADR-010 path. **Real decision:** docs describe two conflicting models — **Model A (AAD client-credentials)** in `aad-bundler-role-assignment.md` + ADR 010 + `bearer.go` (what's built) vs **Model B (localhost bypass)** in `configbundle-integration.md:457/463/464` (only literally true in local dev `ORBITAL_DEV=true`; prod runs Model A). **Decide:** keep AAD (IdP-consistent, heavier) OR adopt real loopback-trust (deletes AAD from the hot path + App Role runbook; commits to the sidecar invariant; supersedes ADR 010). **Regardless: fix the doc contradiction** — 457/463/464 vs the AAD runbook cannot both stand. **Deliverable:** decision + doc reconciliation; if Model B, an ADR superseding 010.

**Spike 29 — Publish-action metrics (success / latency / failure-stage)** · *Not started*
What operational metrics should orbital emit around publish/export, and how do we collect them?
> **Trigger:** 2026-07-27 `/metrics` review. **Current state:** orbital exposes `/metrics` (Prometheus `client_golang`) with 3 HTTP-level metrics — but publish is an **async job**, so HTTP metrics capture only the fast `202` trigger, not outcome/duration/failure-stage. Publish truth lives only in `ExportJob`/`RegistryArtifact` DB rows. **Scope:** (1) scrape orbital (ServiceMonitor or pod annotation — **overlaps Spike 21**, reconcile don't duplicate); (2) publish domain metrics at job finalization in `publisher.go`/`export.go`: `orbital_publish_total{datacenter,result}`, `orbital_publish_duration_seconds{datacenter}`, `orbital_publish_failures_total{stage}`. **Decide:** metric namespace (`orbital_*`) and — reconcile with Spike 21 — `client_golang` at `/metrics` vs OTel `MeterProvider`→Mimir. **Complementary to** the publish audit event (Spike 25): audit = who/when (compliance), metrics = rate/latency/failure (ops).

**Spike 30 — Pre-export change preview ("dry run")** · *Not started*
Let clients see what would change in the next bundle BEFORE clicking export/publish.
> **Prospective twin of Spike 25** (retrospective diff between two *published* artifacts): same audit-cursor-window mechanism, anchored "last publish → now" — **unify, don't build parallel machinery.** **Three levels, increasing cost:** (1) **Pending changeset (audit-based)** — intent mutations since last publish; cheap, reuses the audit pipeline; recommended MVP. (2) **Config payload diff** — field-level diff of the export payload vs last published snapshot; needs export-to-scratch + subgraph diff. (3) **Full bundle preview** — dry-run the whole export→bundle pipeline; heaviest, cross-repo. **Lean:** Level 1, orbital-owned. **Open Qs (parked 2026-07-28):** granularity; surface (API for AEP / UI panel / both); baseline anchor when never published; orbital-only vs bundler dry-run. Existing `download:true` export already produces `json.gz` without publishing — a preview could build on that. Warrants an Opus design session before code.

---

## Production readiness (pre-GA)

- **Deployment:** ingress architecture, dedicated hostnames, TLS, internal vs external LB; production namespace layout, resource limits; Ratel via dedicated DNS + Istio VirtualService. Depends on infra-team input + auth/ingress architecture — not resolvable in prototype spikes alone. (orbital `//go:embed` pending if/when needed.)
- **CI/CD:** GitHub Actions workflow — `.github/` exists but has no workflow files yet (build/tag/push on merge to main; deploy to AKS dev on tag).
- **AKS smoke suite:** `make smoke-aks` exists but is shallow — expand to run the read-only Playwright projects against the AKS deployment.
- **Security hardening:** fix critical/high items before staging/prod exposure — see **Known debt → Audit backlog**.
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
| OIDC config leaks real prod identifiers | Remove real Azure AD tenant + app-ID defaults; fail-on-default-when-`!Dev` (mirror the HMAC check) — `config.go:55-66,121`. (A.1) |
| Rate limiting absent | Per-IP on `/graphql`, tighter on `/user/login` — `server.go`. (S.12) |
| No request body size limit | `middleware.BodyLimit`; unbounded `io.ReadAll` at `graphql.go:89` = DoS. (S.7) |
| OIDC-unreachable degrades to no-auth | Fail startup instead of Warn + nil `apiAuth` — an air-gap deploy can boot unauthenticated — `server.go:144-158`. (S.16) |
| No audit on destructive deletes | Add audit events to `BackupHandler.Delete` (`backup.go:509`) and `OCI.DeleteArtifact` (`oci.go:188`). (S.10) |
| MVCC silent-pass on bad version type | `toFloat64` → `(float64, ok)`; unparseable version = 409, not silent pass — `graphql.go:173,623-634`. (A.3) |
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

### Track B — needs a 15–20 min Opus design session before Sonnet implements

| Item | Notes |
|---|---|
| DGraph client abstraction | 22+ raw `http.Post` calls across 7 handler files, no timeouts, no pooling. Extract `internal/dgraph/client.go`. **Interface shape is a design decision** (transport vs semantic level) — do NOT implement on Sonnet without a settled design. Primary unlock for Go integration testing. |
| `internal/handler/` god package | ~3,500 lines mixing routing, business logic, DGraph calls, file I/O. Decompose post-MVP in 3 steps: extract `internal/storage/`, then `internal/export/`, then `internal/backup/`. Do NOT start before the MVP feature cut. |

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
| 15 · Orb API surface & authN/Z | Jun 7 | MVP: no in-process auth; NetworkPolicy is the gate. Post-MVP: SA + TokenReview. See `ORB.md §Auth`. |
| 16 · Orb UI | May 24 | Orbital + orb share template infra, different nav/capability surfaces |
| 17 · ES module split of app.js | Jun 6 | `app.js` → `shared.js` + `orbital.js` + `orb.js`; conditional load in `head.gohtml`; `window.*` bridge; web dir cleanup |
| 18 · ConfigBundle bundler integration | May 26 | Per-request bundler URLs, all-or-nothing before push, `enriched`/`bundler_error` on RegistryArtifact, retryable HTTP + size cap. `internal/bundler/`. |
| 20 · DGraph schema versioning + backup manifest | Jun 9 | `schema/VERSION`; versioned backup filenames + `manifest.json`; restore 409 on schema mismatch + `confirmSchemaMismatch` override |
| 24 · Divergence MVCC + storage split | Jun 14 | Divergence/resolution stay in PostgreSQL (graph move rejected); `ConfigItem.version` auto-increment, `ifVersion` opt-in race detection |
| 31 · Reject inline single-entity mutations | Jul 28 | Option D — `rejectInlineSelectors` guard 400s `update{Kind}` lacking a variable selector; `VARIABLE_FORM_REQUIRED` with copy-pasteable hint |
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
