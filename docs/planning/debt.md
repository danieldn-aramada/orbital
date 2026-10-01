# Tech Debt

Every entry is **open**. Closed items are **deleted, not struck through** — `CHANGELOG.md` records what shipped and git history holds the reasoning.

**Interim home.** Comparable projects (Kubernetes, Rust, Django, Terraform) keep debt in the issue tracker with labels. Move it there when the workflow is ready.

**Two rules when editing this file.** A row is one line: what is wrong, where, how bad, where to read more — reasoning belongs in the domain doc, implementation plans belong in git. And **before deleting a row, read it for work it still carries**: on 2026-09-14 three rows marked DONE each had an open follow-up buried inside them.

**Sev:** Hi = correctness, security or data loss · Med = real cost, no immediate risk · Low = tidiness.

---

## Ready to pick up

| Item | Sev | Notes |
|---|---|---|
| List pages silently truncate at 500 rows | Med | `genericListQuery(..., 500)` caps every list page and nothing in the UI says so — past 500 the table reads as the whole set. Sharper since list filters landed 2026-09-29: the facet's options are built from the RENDERED rows and it filters that same truncated window, so a filtered view looks authoritative while being a slice. 190 servers today, so it does not bite yet. Cheapest honest fix is to state the cap when it is hit; paging or a server-side filter is the scale-correct one and turns a toolbar widget into a query-param contract. |
| Computed columns have no home — three projections lost with the bespoke pages | Med | A network device's connected servers no longer show their Kubernetes cluster, node role or GPU flag (a three-hop projection), and the rack list lost its server count. Both are the same shape — **a column computed across a path** (`column: server.kubernetesNode.cluster.name`, `column: servers.count`) — and would reuse the existing column machinery rather than needing a panel registry. Deferred deliberately 2026-09-26 to avoid adding a rendering case; see `docs/spikes/spike-38-configurable-views.md`. |
| Three integration packages never apply a schema — they free-ride on another package's `TestMain` | Med | `approval`, `configitems` and `dgraphschema` query test alpha (:8083) but only `handler` and `divergenceingest` call `testutil.ResetDGraph`, which is what applies the schema. On a fresh stack they fail with "There's no GraphQL schema in Dgraph"; otherwise they pass only because another package ran first, which `go test -p 1` hides and parallel packages would expose. Worse, `TestOrphans_CleanGraphReportsNothing` **passes against a completely empty graph** — verified 2026-09-29 by dropping :8083 — so the gap reads as green rather than red. `make seed` applies the schema to blue and scratch, never to :8083, and the `make test-integration` preflight checks :8083 is *alive*, not that it is *usable*. Fix: an idempotent `testutil.EnsureSchema` in each reading package's `TestMain` (reset is wrong for them — they only read), a preflight that probes `getGQLSchema`, and an orphan test that fails on an absent schema. |
| `orbId` is no longer immutable — `XPatch` accepts it, and orbital's proxy applies it | Hi | Schema v8 moved `orbId` to `@id(interface: true)`; DGraph excludes a *type-level* `@id` from the generated Patch but not an interface-level one, so `updateServer(set:{orbId:…})` now succeeds — through the proxy too, which stamps `updatedBy` and bumps `version`. Re-keying orphans every child (`network-adapter-<serviceTag>-…`), splits the audit trail across two ids, and breaks cb-controller's SSA list-map identity. DGRAPH.md still documents the opposite as load-bearing. Proven on a throwaway node 2026-09-22 — evidence and options in `.local/orbid-immutability-broken.md`. Likely fix: reject `orbId` in `set` at the proxy write gate, keeping `@id(interface: true)` for uniqueness. |
| Config editor Save is not atomic | Med | **Narrowed 2026-09-16** — pre-flight + sequential fail-fast means a detectable failure now writes nothing, and the exposure is milliseconds rather than minutes ([UI.md](../reference/UI.md)). What remains: a DGraph/network outage mid-sequence still partials, reported not silent. True atomicity needs one DQL transaction, which bypasses `@id`, `!` and `@hasInverse` — investigated and deferred, notes in `.local/editor-atomic-save-investigation.md`. |
| Restore fails on macOS — `TMPDIR` vs the `/tmp` mount | Med | `restore.go:402` uses `os.MkdirTemp("")`; the dgraph host wrapper mounts only `/tmp`. Worse: `drop_all` runs *before* the load is proven possible. |
| DGraph query string interpolation | Med | Parameterize — `export.go:1012`, `:1105`, `:1279` (bare `%s`). |
| Export/restore job-creation TOCTOU | Med | Concurrent triggers corrupt scratch DGraph; serialize with a mutex or unique partial index. `export.go` |
| Async jobs orphaned on shutdown | Med | SIGTERM mid-restore can leave DGraph wiped. The reaper now marks the row failed, but the work itself is still not drained — needs WaitGroup + cancellable ctx. `restore.go` |
| `deploy/base` still deploys with downtime — `Recreate`, `replicas: 1`, no PDB | Med | Fixed in the `dev-netbox` overlay 2026-09-16, not in base — so an adopter copying `deploy/base` gets downtime on every deploy and drain while [deploy/README.md](../../deploy/README.md) § High availability tells them multi-replica is safe. |
| No test asserts a destructive delete writes its audit event | Med | `delete.go` DC/Server/Cluster paths plus `backup.go:527`, `oci.go:206`. |
| No e2e covers two concurrent edits | Med | The MVCC regression that motivated the spec has no browser-level test. |
| Replace hand-rolled GraphQL parsing with an AST parse | Med | Now **Spike 37** — the six selector lookups funnel through `resolveWriteSelector`/`resolveSetMap`, so the swap is two function bodies. → [backlog.md](backlog.md) |
| Audit retention: env var + in-process pruner | Med | `ORBITAL_AUDIT_RETENTION_DAYS`, default `0` = indefinite. → [AUDIT.md](../reference/AUDIT.md) |
| `changes[]` absent for creates, bulk adds, multi-op events | Med | The cheap half of the per-entity audit spike. → [backlog.md](backlog.md) |
| Non-orbId-filtered mutations record empty `resource_ids` | Med | A mutation filtered on another field selecting only `{numUids}` names nothing. |
| A re-decided approval erases the prior decision and its comment | Low | `decide` is delete-then-create (`changerequest.go:696`), so a rejection vanishes when that approver later approves. The obvious fix (`superseded_at` + a partial unique index) must be taught to 3 load sites and 5 iteration sites of `st.Approvals` — miss one and the approval count that gates merges inflates. → [CHANGE-CONTROL.md](../reference/CHANGE-CONTROL.md) |
| Refactor the bundler URL config DSL | Low | `ORBITAL_BUNDLER_URLS=name=url` is a micro-DSL in one env var; already caused one bug. → [CONFIG.md](../reference/CONFIG.md) |
| Unify `/graphql` proxy-guard error envelope | Low | **Deferred** — guards return the REST envelope, DGraph errors pass through. Build when a client needs it. → [ERROR-RESPONSES.md](../reference/ERROR-RESPONSES.md) |
| UUIDv4 primary keys should be v7 | Low | 8 tables still `Default(uuid.New)`; helper at `ent/schema/uuidv7.go`. Do **not** rewrite existing ids — v4 and v7 coexist in one column. |
| Edit-modal e2e covers 1 of 5 ConfigItem families | Low | Server only. |
| `NewGraphQL` test constructions don't state their configuration | Low | ~23 remaining sites pass a flag they never exercise; prefer a named constructor. |
| The `Orb` ent type is a table with no writer | Low | **Blocked, not ready** — no code anywhere creates, queries or updates an `Orb` (the `orbs` table is empty), and no orb→orbital registration flow is designed in [ORB.md](../reference/ORB.md). The unique indexes this row used to ask for (`datacenter_id`, `public_key`) would encode a guess about registration semantics — one orb per DC? an HA pair? — with no consumer to check it against. Add them when registration is built, in the same change. |
| Backup zip in `/tmp`, defer-only cleanup | Low | Write to a controlled dir + orphan reaper. `backup.go:627` |
| Export scratch-dir leak | Low | Per-job dirs never removed. `export.go:948` → [OCI.md](../reference/OCI.md) for why the obvious fix is wrong |
| GET `/export/jobs` writes to the DB | Low | `StatusStale` marking inside a List handler. `export.go:285` |
| OCI push/sign dual credential stacks | Low | Unify ORAS + go-containerregistry creds, or document the coupling. `publisher.go:316` |
| Every list page uses a one-tab `tabs is-boxed` bar instead of a page heading | Med | `shared/pages/generic-list.gohtml` and `orb/datacenter.gohtml` render a tab strip containing a single "Summary" tab and no `<p class="is-size-4">` heading, so list pages carry no `data-testid="page-heading"` and e2e selects on `#generic-table` internals instead. [UI.md](../reference/UI.md) says single-tab pages use a heading. **Deferred 2026-09-23 because it meant editing six core list pages; four of those templates were deleted in the schema-derived migration, so it is now ONE template that fixes every list page at once.** Still a visible redesign — it wants a look, not a regex. |
| The login gate is inlined in 13 orbital pages | Low | `shared/components/login-gate.gohtml` exists and is used by 6 templates; the rest still paste the same four-line notification. Convert when touched. |
| orb's detail-tab e2e skip unless orb has imported a bundle | Low | `orb.spec.ts` datacenter/cluster/server tab tests `test.skip` when orb's graph is empty, which `make up` + `make seed` leaves it. Four of the suite's 21 skips, and it means a regression in orb's tab rendering passes locally — the 2026-09-23 shared-JS fault was caught by orbital's suite only. Needs a seeded orb fixture or an export→publish→import in the e2e setup. |
| `internal/page/loader.go` is dead | Low | `page.LoadTemplates` walks a whole FS and is called nowhere; both apps build explicit parse sets instead. Found 2026-09-23 while adding `TestAllTemplatesReachable`, which covers `.gohtml` files but not unused Go. |
| `ORB_CONSUMERS` defaults to a hardcoded `localhost:8095` | Med | `orbconfig/config.go:88` defaults to `[{"name":"cb-controller","url":"http://localhost:8095/dispatch"}]`. In any container `localhost` is that container, so every dispatch fails connection-refused and every import finishes `partial` — which trained the release-check gate to accept `partial` as normal for two months. Same class as cb-bundler's hardcoded Entra defaults (fixed 2026-09-23): a default that silently points at something that is not there. The release-check compose now sets `ORB_CONSUMERS="[]"` explicitly, but the default itself is unchanged and will do this again in the next environment. Consider defaulting to empty and making consumers opt-in. |
| Orbital templates not embedded | Low | orbital `ParseFiles` from disk; orb already embeds. `web/embed.go` |
| `after` is the mutation input, never a post-mutation re-read | Low | A field the server rewrote reads as the caller sent it. |
| Out-of-band writes are invisible to API consumers | Low | restore `dropAll`, direct DQL and seeding bypass the proxy. |
| Subtree audit scope is N calls | Low | Clients traverse Topology for descendant orbIds, then pass ≤128 repeatable params. |

---

## Needs design first

A 15–20 min design session before implementation — these have a choice in them, not just work.

| Item | Sev | Notes |
|---|---|---|
| DGraph client abstraction | Med | 30 raw `http.Post`/`NewRequest` calls across 12 handler files. |
| `internal/handler/` god package | Med | 16,604 lines across 33 files. |
| Registry-driven orbId derivation | Med | Kill the `leafSuffix` escape hatch. `registry.go:633` → [DGRAPH.md](../reference/DGRAPH.md) |
| A changeset cannot distinguish ASSERTED from INCIDENTAL fields | Med | `set` is target end-state, so an untouched field is indistinguishable from one deliberately set. → [CHANGE-CONTROL.md](../reference/CHANGE-CONTROL.md) |
| graphdiff surfaces build HTML in JavaScript | Med | Against the house pattern; convert whole surfaces only. Unchanged by the 2026-09-23 UI pass, which normalised the markup those renderers emit (house table class, `.table-container`, no per-cell `is-size-7`) but left them in JS. → [UI.md](../reference/UI.md) |
| A relationship table cannot surface a proposed change | Med | The field-mark renderer assumes a field table: on `generic-detail.gohtml` only the field rows, the metadata box and owned-child boxes carry a `js-field-mark` slot. A child rendered as a relationship TABLE — a cluster's nodes, a server's storage devices — has none, so a pending change to one is invisible on the parent page. Was filed as "Storage and Network panels" against the bespoke pages; those are gone and the gap moved to the generic template unchanged. → [UI.md](../reference/UI.md) |
| Auto-apply the DGraph GraphQL schema on startup | Med | **Drift is now detected and reported** (boot log + `/schema` page, 2026-09-17); applying it is still manual. Before automating, settle which SDL diffs are safe unattended — `@search` on an existing predicate reads as additive and reindexes the whole graph. → [DGRAPH.md](../reference/DGRAPH.md) |

---

**Before closing any item:** `go build ./...` + `make test-unit`; `make test-integration` for PostgreSQL/DGraph; `make test-e2e` or a browser check for UI; a negative test (wrong config is actually rejected) for anything security-critical.
