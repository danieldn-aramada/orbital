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
| `orbId` cross-type uniqueness is unenforced | Hi | DGraph's `@id` is unique *per implementing type*, so a Rack and a Server may share an orbId. Convention holds today (2064 ids, 0 collisions). → [DGRAPH.md](../reference/DGRAPH.md) |
| Restore fails on macOS — `TMPDIR` vs the `/tmp` mount | Med | `restore.go:402` uses `os.MkdirTemp("")`; the dgraph host wrapper mounts only `/tmp`. Worse: `drop_all` runs *before* the load is proven possible. |
| `orb scan` fabricates success | Med | Returns a hardcoded "Found 3 BMC interfaces" instead of "not implemented". `scan.go:16` |
| DGraph query string interpolation | Med | Parameterize — `export.go:1012`, `:1105`, `:1279` (bare `%s`). |
| Export/restore job-creation TOCTOU | Med | Concurrent triggers corrupt scratch DGraph; serialize with a mutex or unique partial index. `export.go` |
| Async jobs orphaned on shutdown | Med | SIGTERM mid-restore can leave DGraph wiped. The reaper now marks the row failed, but the work itself is still not drained — needs WaitGroup + cancellable ctx. `restore.go` |
| Orbital deploys with downtime — `Recreate`, no PDB | Med | Replica safety shipped 2026-09-16; this did not. `strategy: Recreate` with no PodDisruptionBudget means orbital is down during every deploy and node drain, at any replica count. → [deploy/README.md](../../deploy/README.md) |
| OIDC nonce + constant-time state | Med | Add `nonce` binding; `subtle.ConstantTimeCompare` for state. `oidc.go:95` |
| OIDC config ships real Armada identifiers as defaults | Med | Blank tenant/app-ID/authority before OSS. → [AUTH.md](../reference/AUTH.md) |
| `docs/auth.md` documents two auth flows; orbital has three | Med | external-jwt is missing and the default changed under it. `config.go:122` |
| No test asserts a destructive delete writes its audit event | Med | `delete.go` DC/Server/Cluster paths plus `backup.go:527`, `oci.go:206`. |
| No e2e covers two concurrent edits | Med | The MVCC regression that motivated the spec has no browser-level test. |
| Consolidate the 4 per-type edit-modal templates | Med | ~95% identical; a new ConfigItem family costs a 5-file copy across templates, handlers and `orbital.js`. → [UI.md](../reference/UI.md) |
| Replace hand-rolled GraphQL parsing with an AST parse | Med | Now **Spike 37** — the six selector lookups funnel through `resolveWriteSelector`/`resolveSetMap`, so the swap is two function bodies. → [backlog.md](backlog.md) |
| Audit retention: env var + in-process pruner | Med | `ORBITAL_AUDIT_RETENTION_DAYS`, default `0` = indefinite. → [AUDIT.md](../reference/AUDIT.md) |
| `changes[]` absent for creates, bulk adds, multi-op events | Med | The cheap half of the per-entity audit spike. → [backlog.md](backlog.md) |
| Non-orbId-filtered mutations record empty `resource_ids` | Med | A mutation filtered on another field selecting only `{numUids}` names nothing. |
| Replace `title=""` tooltips with Tippy.js | Low | 34 usages across 10 templates. |
| Refactor the bundler URL config DSL | Low | `ORBITAL_BUNDLER_URLS=name=url` is a micro-DSL in one env var; already caused one bug. → [CONFIG.md](../reference/CONFIG.md) |
| Collapse duplicate cluster backup structs | Low | `backupKindResponse` and `backupKindTab` are hand-synced. `internal/handler/cluster.go` |
| Unify `/graphql` proxy-guard error envelope | Low | **Deferred** — guards return the REST envelope, DGraph errors pass through. Build when a client needs it. → [ERROR-RESPONSES.md](../reference/ERROR-RESPONSES.md) |
| UUIDv4 primary keys should be v7 | Low | 8 tables still `Default(uuid.New)`; helper at `ent/schema/uuidv7.go`. Do **not** rewrite existing ids — v4 and v7 coexist in one column. |
| Edit-modal e2e covers 1 of 5 ConfigItem families | Low | Server only. |
| `NewGraphQL` test constructions don't state their configuration | Low | ~23 remaining sites pass a flag they never exercise; prefer a named constructor. |
| Orb registration not unique | Low | Unique indexes on `datacenter_id` + `public_key`. `ent/schema/orb.go` |
| Job-status columns unindexed | Low | Conflict queries full-scan ExportJob/Backup/RestoreJob. |
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
| Auto-apply the DGraph GraphQL schema on startup | Med | Orbital does not apply `schema.graphql` on boot, so a schema-bumping deploy needs a manual step. → [DGRAPH.md](../reference/DGRAPH.md) |

---

**Before closing any item:** `go build ./...` + `make test-unit`; `make test-integration` for PostgreSQL/DGraph; `make test-e2e` or a browser check for UI; a negative test (wrong config is actually rejected) for anything security-critical.
