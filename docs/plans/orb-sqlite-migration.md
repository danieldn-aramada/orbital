# Orb SQLite Migration — Persistence Consolidation + Publish History

> **Audience:** Sonnet or Opus in a new session.
> **Goal:** Consolidate orb's four ad-hoc JSON persistence files into one SQLite database (`orb.db` on the DataDir PV), backed by ent. Add the missing publish-history feature.
> **Estimated effort:** ~13–17 hours of focused work. Spread across 3–5 sessions.
> **Read first:** `CLAUDE.md`, `docs/reference/ORB.md`, `internal/orb/importer.go`, `internal/divergence/divergence.go`.
> **Related memories:** `project_pure_go_no_cgo.md`, `project_orb_sqlite_migration.md` (this doc's pointer).

---

## Context

### Why this work exists

Orb currently persists to four JSON files on its DataDir PV:

| File | What it holds | Pattern |
|---|---|---|
| `import-history.json` | Full log of all imports | Rolling array — **history preserved** |
| `divergence/current.json` | Current pending overrides orb tracks | Single-snapshot, replaced on each update |
| `divergence/published.json` | The **most recent** publish record only | Single-snapshot, **overwritten each publish** |
| `overrides.json` | Legacy override cache — **dead code** (see Phase 0 finding) | Was SSA-override-era; removed in Spike 17 refactor per `project_orb_override_system.md` |

**The user-visible gap:** `divergence/published.json` keeps only the LAST publish record. Orb needs a **history** of published divergence reports so operators can see when reports went out, correlate with orbital ingestion, and audit the courier/network flow.

**The architectural gap:** 4 different ad-hoc JSON stores with 4 different patterns (rolling vs snapshot) is drift-prone. Adding a 5th JSON file for publish history would compound the mess. SQLite consolidates all persistence under one schema, one backup story, one migration story.

### Success criteria

- Single `orb.db` file on the DataDir PV replaces the three live JSON files (`import-history.json`, `divergence/current.json`, `divergence/published.json`).
- New tables via ent: `import_records`, `pending_overrides`, `published_reports`.
- Publish history is queryable — new `GET /api/v1/divergence/publish-history` endpoint + orb UI section.
- One-shot migration: on first startup, JSON files are read and imported into SQLite. Idempotent (re-runs are no-ops).
- Rollback safety: JSON files are NOT deleted during the migration. They stay on disk as fallback until Phase 7 cleanup (next release).
- Zero API contract changes on existing endpoints (`/api/v1/import/history`, divergence page fragments).
- Pure Go build preserved (`CGO_ENABLED=0`) — see `project_pure_go_no_cgo.md`.
- Round-trip tests for every persistence path (per CLAUDE.md "Any persistence requires a round-trip test").

---

## Locked-in decisions

These were argued through in the session that produced this plan. Do NOT relitigate unless new evidence emerges.

| Decision | Rationale |
|---|---|
| **SQLite (not CNPG, not rqlite, not JSON-only)** | Embedded, single-file, ACID, air-gap-friendly. CNPG overkill for orb's scale. rqlite is a distributed daemon (wrong category — reintroduces network dependency). |
| **`modernc.org/sqlite` driver (not `mattn/go-sqlite3`)** | Pure Go — respects the CGO_ENABLED=0 project invariant. `mattn` requires CGO and would be the first CGO dependency in the codebase. Verified in Phase 0: v1.53.0 compiles clean with CGO_ENABLED=0, cross-compiles to linux/amd64 as a static ELF. Widely deployed (ent tests, Litestream) — not obscure. |
| **ent for schema + queries** | Orbital already uses ent (v0.14.6) for Postgres. Same tooling, different dialect. Orb's ent package is separate (`internal/orb/store/ent/`), independent schema. |
| **Single-replica orb** | The `orb-data` PVC is `ReadWriteOnce` — K8s will refuse to schedule a 2nd replica anyway. Deploy strategy is `Recreate`, so brief downtime during pod restart is already the accepted model. SQLite fits. |
| **No backups (rely on PV reclaim)** | Orb's data is regenerable — imports come from orbital (OCI/courier), divergence pending state regenerates on next cb-controller sweep. Storage class `rook-ceph-block` on RWO PVC has its own reliability story. Revisit if orb ever holds source-of-truth data. |
| **3 tables, not 4** | `overrides.json` code is dead (LoadOverrides/SaveOverride have zero callers). Delete `overrides.go` + the clear-on-import block as small pre-Phase-1 cleanup. Not part of the SQLite migration itself. |
| **JSON files stay on disk until Phase 7** | Rollback safety during bake-in period. Delete only after production stability confirmed in a release. |
| **No `ORB_DB_PATH` env var** | Derive `DBPath = filepath.Join(cfg.DataDir, "orb.db")` at server construction. One less config knob. |

---

## Phase 0 — verify assumptions ✅ COMPLETE

Completed in the session that produced this plan. Findings:

1. **`overrides.json` is defunct.** `LoadOverrides` + `SaveOverride` in `internal/orb/overrides.go` have zero callers. Only reference: `importer.go` clears the file on import (dead-cleanup logic). Matches memory `project_orb_override_system.md` — SSA overrides removed in Spike 17.
2. **`ORB_DATA_DIR` is a real PVC.** `deploy/edge/base/orb.yaml` — `PersistentVolumeClaim: orb-data`, storage class `rook-ceph-block`, `ReadWriteOnce`, 1Gi, mounted at `/var/lib/orb`. Docker Compose bind mount to same path.
3. **ent v0.14.6, Go 1.25.5.** Current.
4. **`modernc.org/sqlite` v1.53.0 works pure-Go.** Compiled `CGO_ENABLED=0` on darwin/arm64 + cross-compiled to linux/amd64. Static ELF, ~9.1 MB. In-memory INSERT + SELECT round-trip verified.

---

## Phase 0.5 — dead code cleanup (30 min, optional pre-work)

Small pre-migration cleanup. Not strictly required for Phase 1, but keeps the SQLite scope tight.

- [ ] Delete `internal/orb/overrides.go` entirely.
- [ ] Remove the `overridesFile` constant in `internal/orb/importer.go` (line 33).
- [ ] Remove the "clear overrides.json on import" block in `importer.go` (lines ~163–167).
- [ ] `go build ./...` to confirm no callers exist.
- [ ] Run unit tests to confirm nothing depended on the dead code.

Can be a single small commit before Phase 1 starts. If skipped, Phase 4 has to handle it.

---

## Phase 1 — ent scaffolding for orb (2–3 hours)

New package: `internal/orb/store/`. Mirrors orbital's `ent/` structure but scoped to orb's schema tree.

**Files to create:**

- `internal/orb/store/generate.go` — `//go:generate go run entgo.io/ent/cmd/ent generate ./schema`
- `internal/orb/store/schema/import_record.go`
- `internal/orb/store/schema/pending_override.go`
- `internal/orb/store/schema/published_report.go`
- `internal/orb/store/client.go` — `New(dbPath string) (*ent.Client, error)`

**Schema definitions:**

```go
// import_records — replaces import-history.json
type ImportRecord struct { ent.Schema }

func (ImportRecord) Fields() []ent.Field {
    return []ent.Field{
        field.String("tag").Unique(),
        field.String("digest"),
        field.String("dc_orb_id").Optional(),
        field.String("export_job_id").Optional(),
        field.Time("imported_at"),
        field.Enum("status").Values("done", "failed", "partial"),
        field.Enum("verification").Values("verified", "not-applicable", "signature-invalid").Optional(),
        field.Text("layers_json"),  // opaque; same array shape as today's JSON layers[]
        field.Text("error").Optional(),
    }
}
func (ImportRecord) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("dc_orb_id"),
        index.Fields("imported_at"),
    }
}

// pending_overrides — replaces divergence/current.json (snapshot semantics)
type PendingOverride struct { ent.Schema }

func (PendingOverride) Fields() []ent.Field {
    return []ent.Field{
        field.String("entry_id").Unique(),  // matches divergence.OverrideEntry.ID
        field.String("type_name").Optional(),
        field.String("entry_orb_id"),
        field.String("field"),
        field.Text("intended_value").Optional(),
        field.Text("override_value").Optional(),
        field.String("who").Optional(),
        field.Time("first_seen_at"),
        field.Int("intended_at_version").Optional(),
        field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
    }
}
func (PendingOverride) Indexes() []ent.Index {
    return []ent.Index{ index.Fields("entry_orb_id") }
}

// published_reports — new; the reason we're doing this
type PublishedReport struct { ent.Schema }

func (PublishedReport) Fields() []ent.Field {
    return []ent.Field{
        field.String("dc_orb_id"),
        field.Time("published_at"),
        field.String("s3_key"),
        field.String("s3_endpoint").Optional(),
        field.Int("entry_count"),
        field.Enum("status").Values("published", "superseded").Default("published"),
        field.Text("summary_json").Optional(),  // {"typesTouched": [...], "actorCount": N}
    }
}
func (PublishedReport) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("dc_orb_id", "published_at"),  // list history per DC
    }
}
```

**Snapshot semantics for `pending_overrides`:** the current JSON-file model REPLACES the entire set on each save. Preserve this in SQLite via `client.PendingOverride.Delete().Exec(ctx)` + bulk create in a single transaction inside `Store.Save()`.

**Client init:**

```go
import _ "modernc.org/sqlite"

drv, err := sql.Open("sqlite", dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
if err != nil { return nil, err }
client := ent.NewClient(ent.Driver(drv))
if err := client.Schema.Create(ctx); err != nil { return nil, err }
```

**go.mod:**

```
require (
    modernc.org/sqlite v1.53.0
    // ... existing entgo.io/ent v0.14.6 stays
)
```

**Validation:** `go generate ./internal/orb/store/...` produces ent code. Unit test round-trips one row of each entity through `store.New(":memory:")`.

---

## Phase 2 — wire into orb startup (30 min)

- [ ] In `internal/orbconfig/config.go`, derive `cfg.DBPath = filepath.Join(cfg.DataDir, "orb.db")` in the config constructor. No env var.
- [ ] In `internal/orbserver/server.go` (or wherever orb constructs its handlers), call `store.New(cfg.DBPath)` at startup. Fail-fast on error (bad PV, permission denied).
- [ ] Thread `*ent.Client` through to the places that need it (importer, divergence store, handlers).

**Validation:** Fresh orb starts, `orb.db` appears on DataDir, `sqlite3 orb.db .schema` shows the 3 tables in WAL mode.

---

## Phase 3 — one-shot migration from JSON files (2–3 hours)

**File:** `internal/orb/store/migrate.go`

Called from server startup, after `Schema.Create`, before any writes:

```
For each of the 3 legacy files:
  1. If file doesn't exist → skip (fresh install)
  2. If corresponding table is non-empty → skip (already migrated; idempotent)
  3. Read file, parse JSON, insert rows in one transaction
  4. Log: "migrated N rows from <file> into <table>"
  5. DO NOT delete the file (safety fallback for one release)
```

**Idempotency guarantee:** running migration multiple times is a no-op. Enables Phase 7 rollback.

**Validation:**
- Integration test: seed a DataDir with realistic JSON files → start orb → SQLite has expected row counts
- Integration test: run migration twice → second run inserts zero rows
- Integration test: empty DataDir → migration is a clean no-op

---

## Phase 4 — swap writers (3–4 hours)

**Files to modify:**

- `internal/orb/importer.go` — `recordHistory()` writes to `client.ImportRecord.Create()...` instead of appending to the JSON file.
- `internal/divergence/divergence.go` — `Store.Save(entries)` becomes a transactional DELETE + INSERT on `pending_overrides`. `Store.SavePublishRecord(rec)` becomes `client.PublishedReport.Create()` (APPEND, not overwrite — this is the feature).
- `internal/orbserver/divergence_scheduler.go` — no change needed if it goes through `Store.SavePublishRecord`.

**Contracts to preserve:**
- `Store.Save(entries)` — snapshot semantics preserved (DELETE + INSERT in one transaction)
- `Store.LoadPublishRecord()` — returns the LATEST `published_reports` row (compat with existing callers)
- Import history append shape unchanged from a caller's perspective

**Validation:**
- Unit tests for each Store method against in-memory SQLite
- Integration test: publish 3 divergence reports → `published_reports` has 3 rows (history semantics work)
- Existing tests pass (import completes, divergence Save/Load round-trip)

---

## Phase 5 — swap readers (2 hours)

**Files to modify:**

- `internal/orbserver/import_handlers.go` — `importHistory()` reads from SQLite instead of `orb.LoadHistory(dataDir)`.
- `internal/orbserver/divergence_handlers.go` — divergence page data reads from SQLite.

**Contract preserved:** JSON shapes returned by `/api/v1/import/history` and the divergence page fragment are UNCHANGED. Only the read source changes.

**Validation:**
- Existing e2e tests pass (`e2e/orb.spec.ts` — import history API tests, verification field)
- Manual smoke: hit `/api/v1/import/history` → same JSON shape as before, but from SQLite

---

## Phase 6 — publish history feature (3–4 hours)

**The actual reason we did this migration.**

**Backend:**
- `internal/orbserver/divergence_handlers.go` — new handler `GET /api/v1/divergence/publish-history`
  - Query params: `dcOrbId` (optional filter), `limit` (default 50, max 200), `offset`, `sort=published_at`, `dir=desc|asc`
  - Returns: JSON array of `PublishedReport` records
- **HTMX-native pattern**: column headers are `<a hx-get="/publish-history?sort=...&dir=...">`, pagination is `<a hx-get="?page=2">`. URL state IS the state. No client-side table library. (Per the table library discussion — orbital and orb are moving to HTMX-native tables; this is a good place to demonstrate the pattern.)

**Frontend:**
- `web/templates/orb/pages/divergence.gohtml` — new section: "Publish History"
- Server-rendered table with sort/filter via query params + HTMX swap of results block
- No client-side JS table library required

**Validation:**
- Publish 3 reports → history endpoint returns 3 rows in correct order
- Integration test for the endpoint (query params, filter, pagination)
- E2E test: publish → visit divergence page → history section shows the row

---

## Phase 7 — cleanup (1 hour — DEFERRED to next release)

**Do NOT do this in the same release as Phase 4-6.** Wait until orb has run in production with SQLite for at least one release cycle. Rollback safety.

- [ ] Delete `orb.LoadHistory` file-reader.
- [ ] Delete file paths in `divergence.Store` (SQLite is now the only source).
- [ ] On startup: log-and-delete the 3 legacy JSON files if they still exist (they've been migrated already; keeping them is dead weight).
- [ ] Remove the migration code from Phase 3 (nothing left to migrate on new deployments).

---

## Testing story per phase

| Phase | Test type | What it pins |
|---|---|---|
| 1 (schemas) | Unit tests | Each ent model round-trips through an in-memory DB |
| 2 (wiring) | Startup smoke | `orb start` succeeds, `orb.db` created, WAL mode enabled |
| 3 (migration) | Integration | Seed JSON → migrate → SQLite has data; idempotency |
| 4 (writers) | Integration | Each write path lands correct row(s); transaction semantics for snapshot tables |
| 5 (readers) | Existing e2e | `/api/v1/import/history` returns same shape; divergence page renders |
| 6 (feature) | Integration + e2e | Publish → history endpoint → row present; UI renders new section |
| 7 (cleanup) | Startup smoke | Legacy JSON files deleted; orb still works |

**Round-trip discipline:** because this is persistence, every write path gets a "write → close → reopen → read" test. Per CLAUDE.md "Any persistence requires a round-trip test."

---

## Rollback plan

**During Phase 4–6 (JSON files still on disk):**
- Revert the code changes.
- Orb reads JSON files again on next start.
- No data loss (JSON files were never deleted).
- `orb.db` sits unused on the PV; can be deleted manually or ignored.

**After Phase 7 (JSON files deleted):**
- Rollback is harder — no automatic fallback.
- Manual recovery: `sqlite3 orb.db "SELECT ..."` → export to JSON → restart with old code.
- By the time we're at Phase 7, we should be confident enough not to need this.

---

## Non-goals (deferred)

- **Backups / Litestream** — orb data is regenerable; PV reclaim + storage-class defaults are sufficient. Revisit when orb holds source-of-truth data.
- **HA / multi-replica** — RWO PVC forbids it anyway. If HA becomes required, adopt CNPG (not rqlite; CNPG has better K8s ecosystem).
- **Cross-package schema sharing** — orbital's ent (Postgres) and orb's ent (SQLite) stay independent. Same tooling, different schemas.
- **API endpoint renames** — existing endpoints keep JSON shapes unchanged.
- **`overrides.json` migration** — the code is dead; delete it in Phase 0.5, don't migrate content.

---

## Ready state

Phase 0 complete. Green light for Phase 1 in the next session.

**Recommended next-session invocation:**

> "Continue orb SQLite migration. Start Phase 1 (ent scaffolding) — or Phase 0.5 (delete overrides.go dead code) if you want to tackle that first."

Session will:
1. Load `CLAUDE.md` + `MEMORY.md` (automatic)
2. See `project_orb_sqlite_migration.md` memory pointer
3. Read this plan
4. Run `git log --oneline -10` + `git status` to see what's landed
5. Start Phase 1 (or 0.5 first)

Estimated Phase 1 effort: **2–3 hours of focused work.** Fits comfortably in one session.
