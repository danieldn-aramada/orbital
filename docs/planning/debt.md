# Tech Debt

Every entry is **open**. Closed items are **deleted, not struck through** — `CHANGELOG.md` records what shipped and git history holds the reasoning.

**Interim home.** Comparable projects (Kubernetes, Rust, Django, Terraform) keep debt in the issue tracker with labels. Move it there when the workflow is ready.

**Two rules when editing this file.** A row is one line: what is wrong, where, how bad, where to read more — reasoning belongs in the domain doc, implementation plans belong in git. And **before deleting a row, read it for work it still carries**: on 2026-09-14 three rows marked DONE each had an open follow-up buried inside them.

**Sev:** Hi = correctness, security or data loss · Med = real cost, no immediate risk · Low = tidiness.

---

## Ready to pick up

| Item | Sev | Notes |
|---|---|---|
| Config editor Save is not atomic | Med | **Narrowed 2026-09-16** — pre-flight + sequential fail-fast means a detectable failure now writes nothing, and the exposure is milliseconds rather than minutes ([UI.md](../reference/UI.md)). What remains: a DGraph/network outage mid-sequence still partials, reported not silent. True atomicity needs one DQL transaction, which bypasses `@id`, `!` and `@hasInverse` — investigated and deferred, notes in `.local/editor-atomic-save-investigation.md`. |
| Restore fails on macOS — `TMPDIR` vs the `/tmp` mount | Med | `restore.go:402` uses `os.MkdirTemp("")`; the dgraph host wrapper mounts only `/tmp`. Worse: `drop_all` runs *before* the load is proven possible. |
| DGraph query string interpolation | Med | Parameterize — `export.go:1012`, `:1105`, `:1279` (bare `%s`). |
| Export/restore job-creation TOCTOU | Med | Concurrent triggers corrupt scratch DGraph; serialize with a mutex or unique partial index. `export.go` |
| Async jobs orphaned on shutdown | Med | SIGTERM mid-restore can leave DGraph wiped. The reaper now marks the row failed, but the work itself is still not drained — needs WaitGroup + cancellable ctx. `restore.go` |
| `deploy/base` still deploys with downtime — `Recreate`, `replicas: 1`, no PDB | Med | Fixed in the `dev-netbox` overlay 2026-09-16, not in base — so an adopter copying `deploy/base` gets downtime on every deploy and drain while [deploy/README.md](../../deploy/README.md) § High availability tells them multi-replica is safe. |
| OIDC nonce + constant-time state | Med | Add `nonce` binding; `subtle.ConstantTimeCompare` for state. `oidc.go:95` |
| `docs/auth.md` predates the provider list | Med | Written for the single-issuer path; says nothing about `ORBITAL_AUTH_PROVIDERS`, which is what an integrator now authenticates against. |
| No test asserts a destructive delete writes its audit event | Med | `delete.go` DC/Server/Cluster paths plus `backup.go:527`, `oci.go:206`. |
| No e2e covers two concurrent edits | Med | The MVCC regression that motivated the spec has no browser-level test. |
| Consolidate the 4 per-type edit-modal templates | Med | ~95% identical; a new ConfigItem family costs a 5-file copy across templates, handlers and `orbital.js`. → [UI.md](../reference/UI.md) |
| Replace hand-rolled GraphQL parsing with an AST parse | Med | Now **Spike 37** — the six selector lookups funnel through `resolveWriteSelector`/`resolveSetMap`, so the swap is two function bodies. → [backlog.md](backlog.md) |
| Audit retention: env var + in-process pruner | Med | `ORBITAL_AUDIT_RETENTION_DAYS`, default `0` = indefinite. → [AUDIT.md](../reference/AUDIT.md) |
| `changes[]` absent for creates, bulk adds, multi-op events | Med | The cheap half of the per-entity audit spike. → [backlog.md](backlog.md) |
| Non-orbId-filtered mutations record empty `resource_ids` | Med | A mutation filtered on another field selecting only `{numUids}` names nothing. |
| Replace `title=""` tooltips with Tippy.js | Low | 34 usages across 10 templates. |
| A re-decided approval erases the prior decision and its comment | Low | `decide` is delete-then-create (`changerequest.go:696`), so a rejection vanishes when that approver later approves. The obvious fix (`superseded_at` + a partial unique index) must be taught to 3 load sites and 5 iteration sites of `st.Approvals` — miss one and the approval count that gates merges inflates. → [CHANGE-CONTROL.md](../reference/CHANGE-CONTROL.md) |
| Refactor the bundler URL config DSL | Low | `ORBITAL_BUNDLER_URLS=name=url` is a micro-DSL in one env var; already caused one bug. → [CONFIG.md](../reference/CONFIG.md) |
| Collapse duplicate cluster backup structs | Low | `backupKindResponse` and `backupKindTab` are hand-synced. `internal/handler/cluster.go` |
| Unify `/graphql` proxy-guard error envelope | Low | **Deferred** — guards return the REST envelope, DGraph errors pass through. Build when a client needs it. → [ERROR-RESPONSES.md](../reference/ERROR-RESPONSES.md) |
| UUIDv4 primary keys should be v7 | Low | 8 tables still `Default(uuid.New)`; helper at `ent/schema/uuidv7.go`. Do **not** rewrite existing ids — v4 and v7 coexist in one column. |
| Edit-modal e2e covers 1 of 5 ConfigItem families | Low | Server only. |
| `NewGraphQL` test constructions don't state their configuration | Low | ~23 remaining sites pass a flag they never exercise; prefer a named constructor. |
| The `Orb` ent type is a table with no writer | Low | **Blocked, not ready** — no code anywhere creates, queries or updates an `Orb` (the `orbs` table is empty), and no orb→orbital registration flow is designed in [ORB.md](../reference/ORB.md). The unique indexes this row used to ask for (`datacenter_id`, `public_key`) would encode a guess about registration semantics — one orb per DC? an HA pair? — with no consumer to check it against. Add them when registration is built, in the same change. |
| Backup zip in `/tmp`, defer-only cleanup | Low | Write to a controlled dir + orphan reaper. `backup.go:627` |
| Export scratch-dir leak | Low | Per-job dirs never removed. `export.go:948` → [OCI.md](../reference/OCI.md) for why the obvious fix is wrong |
| GET `/export/jobs` writes to the DB | Low | `StatusStale` marking inside a List handler. `export.go:285` |
| OCI push/sign dual credential stacks | Low | Unify ORAS + go-containerregistry creds, or document the coupling. `publisher.go:316` |
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
| graphdiff surfaces build HTML in JavaScript | Med | Against the house pattern; convert whole surfaces only. → [UI.md](../reference/UI.md) |
| Storage and Network panels cannot surface a proposed change | Med | The field-mark renderer assumes a field table. → [UI.md](../reference/UI.md) |
| Auto-apply the DGraph GraphQL schema on startup | Med | **Drift is now detected and reported** (boot log + `/schema` page, 2026-09-17); applying it is still manual. Before automating, settle which SDL diffs are safe unattended — `@search` on an existing predicate reads as additive and reindexes the whole graph. → [DGRAPH.md](../reference/DGRAPH.md) |

---

**Before closing any item:** `go build ./...` + `make test-unit`; `make test-integration` for PostgreSQL/DGraph; `make test-e2e` or a browser check for UI; a negative test (wrong config is actually rejected) for anything security-critical.
