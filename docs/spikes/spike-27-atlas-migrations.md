# Spike 27 — Adopt Atlas for Postgres schema migration

**Status:** Design proposal — open for review. No code written.
**Date:** 2026-08-26
**Scope:** orbital's PostgreSQL schema evolution (`ent/migrate`, `cmd/orbital/main.go`), the deploy path, and CI. Orb's SQLite is a secondary target (§8).
**Question:** How does orbital evolve its Postgres schema against long-lived data — adding constraints to populated tables — without crashlooping deploys?
**Relationship to Spike 34:** independent. That spike's `events` → `audit_events` rename runs **first**, under auto-migration with an explicit drop of the old tables — so Atlas baselines an already-correct schema rather than inheriting a rename to unwind.

**Evidence policy:** external claims carry sources in §10. ent behaviour below was verified against the **ent v0.14.6 source** in the module cache, not only the docs.

---

## 1. Trigger

The **v0.0.23 AKS deploy crashlooped** adding an FK on `registry_artifacts.export_job_id`. The long-lived AKS database held orphan rows, the constraint could not be created, and the pod died on startup and restarted forever.

The important part is *why it was not caught*: **fresh test databases cannot express this failure class.** Every local run, integration test, and CI job starts from an empty schema where the constraint trivially applies. The bug exists only against accumulated data, which lives in exactly one place nobody tests against.

That is the permanent shape of schema-diff-based auto-migration, not a one-off.

---

## 2. Current state (verified 2026-08-26)

| Fact | Location |
|---|---|
| Migration runs **in-process, at startup** | `cmd/orbital/main.go:67` |
| `db.Schema.Create(ctx, migrate.WithDropColumn(true))` | same |
| Failure is **fatal** → crashloop | `log.Fatalf("migrate: %v", err)`, next line |
| **12 tables** under management | `ent/migrate/schema.go` |
| **No versioned migrations directory** | `ent/migrate/` holds only `migrate.go` + `schema.go` |
| **No `atlas.hcl`** | repo root and `ent/` |
| **Atlas CLI not installed** on the dev machine | `which atlas` → not found |
| `ariga.io/atlas v0.36.2` already an **indirect** dep | `go.mod:40` |
| ent `v0.14.6` | `go.mod:6` |
| Tests auto-migrate too | `export_integration_test.go:59`, `ingester_integration_test.go:49` |
| Orb does the same on SQLite (no drop-column) | `internal/orb/store/db.go:27` |
| **`WithGlobalUniqueID` is never called** — no `ent_types` table | `ent/migrate/migrate.go` (re-exported symbol only) |

That last row matters: ent's baselining docs warn specifically about matching the contents and ordering of the `ent_types` system table. **We don't have one**, so that hazard does not apply here.

### Four problems

1. **No versioned history.** The schema is derived by diffing live DB state against generated Go. No artifact to review, no ordering, no way to answer "what changed between v0.0.22 and v0.0.23."
2. **No data migrations.** ent's diff engine emits DDL only. v0.0.23 needed *DML first* — clean up or backfill the orphans, then add the constraint. There is nowhere to express that.
3. **`WithDropColumn(true)` is armed in production.** Remove a field from an ent schema and the next deploy silently drops the column and its data. Combined with (1) — no review artifact — this is the riskiest line in the deploy path.
4. **Every replica races.** Migration runs inside `main()` before the server starts, so at >1 replica every pod attempts the same DDL on boot. Postgres DDL is transactional so it is survivable, but it is unreviewed concurrency on the critical path.

### The silent one: ent cannot rename

Verified in ent v0.14.6 source (`dialect/sql/schema/atlas.go`, `planInspect`): ent inspects **only the tables its own schema declares** — the `InspectOptions.Tables` filter is built from the ent-generated table list. So a renamed-away table is never inspected, never enters the diff, and **no `DROP TABLE` change is structurally possible**.

Rename ent type `Event` → `AuditEvent` against a populated database and you get a **new empty `audit_events` table**, with `events` left intact, fully populated, and **invisible to ent**. No error. No warning.

`WithDropColumn(true)` is irrelevant — it clears a bit from a *column-level* skip mask (`atlas.go:555-563`). There is **no `WithDropTable` option** and no rename support at any level. ent's documented rename escapes (`entsql.Annotation` / `StorageKey`, diff hooks) are column-level and cannot help, because the old table never reaches the diff for a hook to intercept.

`Create`'s own doc comment states the contract: *"It works in an **append-only** mode, which means, it only creates tables, appends columns to tables or modifies column types."*

---

## 3. Why Atlas

Atlas is built by **Ariga**, the team that maintains ent, and `entgo.io/docs/versioned-migrations/` is ent's own documentation prescribing it as the path off auto-migration. **ent v0.14.6's migration engine already *is* Atlas internally** — `ariga.io/atlas` is in `go.mod` as an indirect dependency. Adopting versioned migrations makes an existing dependency direct rather than introducing a new vendor.

It supplies the three missing things: versioned SQL generated from the ent schema diff, hand-written DML interleaved in the same ordered stream, and static analysis in CI.

### Rejected alternatives

| Option | Why not |
|---|---|
| **Liquibase / Flyway** | JVM in the build and deploy path for a pure-Go project. Violates the pure-Go/no-CGO posture; a toolchain nobody here maintains. |
| **Custom migration tooling** | Same reasoning that rejected a custom DGraph schema-migration tool (CLAUDE.md § Settled Decisions). |
| **Keep auto-migration, add guardrails** | Cannot express data migrations and cannot produce a reviewable artifact. Treats the symptom. |
| **Do nothing** | `WithDropColumn(true)` stays armed in production, migrations keep crashlooping every replica on startup, and the next constraint-on-populated-table change reproduces v0.0.23 exactly. |

---

## 4. Design

### 4.1 Generate versioned migrations

**Recommended — Atlas CLI with the official `ent://` loader.** No build tags, no generated Go program:

```bash
atlas migrate diff <name> \
  --dir "file://ent/migrate/migrations" \
  --to  "ent://ent/schema" \
  --dev-url "docker://postgres/16/dev?search_path=public"
```

`--dev-url` is a **throwaway dev database** Atlas uses to replay the existing migration directory and diff it against the desired ent schema. It is not our real database and is wiped each run.

*(The alternative — a `//go:build ignore` program at `ent/migrate/main.go` calling `migrate.NamedDiff`, behind the `sql/versioned-migration` feature flag — also works but is strictly more machinery. ent's docs now lead with the CLI approach.)*

Workflow becomes:

```
edit ent/schema/*.go → go generate ./ent → atlas migrate diff <name> → review the .sql → commit
```

Files land in `ent/migrate/migrations/` as `20260826114629_<name>.sql`, alongside **`atlas.sum`** — an integrity checksum file that **deliberately causes a git merge conflict** when two developers add migrations concurrently. That is the intended safety mechanism, not a nuisance.

### 4.2 Move migrations off startup

Replace `db.Schema.Create(...)` in `cmd/orbital/main.go`.

| Option | Pros | Cons |
|---|---|---|
| **(a) K8s init container** | Once per rollout, not per replica; blocks the pod until schema is ready; standard | New container spec; needs the migrate tooling in the image |
| **(b) `orbital --migrate` flag** run by a Job/init container | One binary, no extra image; also usable from a runbook | Still needs orchestration for once-per-rollout |

**Recommend (b) invoked from (a).** Reuses the single-Dockerfile/two-target setup with no new image and keeps a manual escape hatch.

**The placement change is what ends the crashloop class, independent of Atlas.** A failed migration should fail a *Job*, loudly, with the old pods still serving — instead of taking down every replica.

### 4.3 Disarm `WithDropColumn(true)`

Drop it. Under versioned migrations a column drop becomes an explicit line in a reviewed `.sql` that lint flags as destructive. That is where the decision belongs.

### 4.4 CI — and an honest limit

1. **`atlas migrate lint`** on every PR touching `ent/`.
2. **`atlas migrate validate`** — ensures the directory's `atlas.sum` integrity hash matches, catching hand-edited or reordered files.
3. **Apply pending migrations against a redacted snapshot of the AKS database.**

**What lint actually catches:**

| Family | Codes | Detects |
|---|---|---|
| **Destructive** | DS101/**DS102**/**DS103** | schema dropped / **table dropped** / **non-virtual column dropped** |
| **Data-dependent** | MF101/MF102/**MF103**/**MF104** | unique index on existing column / non-unique→unique / **non-nullable column added to existing table** / **nullable→non-nullable** |
| **Backward-incompatible** | BC101–BC104 | table renamed / column renamed / table dropped / column dropped |

**⚠️ What lint does NOT catch — and this is the correction that matters most:**

> **Adding a foreign key when orphan rows exist is not covered by any lint analyzer.** There is no MF code for it; the FK-adjacent analyzers are CD101 (constraint deletion) and PG306 (Postgres locking), neither of which addresses orphan data.
>
> **`atlas migrate lint` would NOT have caught v0.0.23.**

Two further limits worth stating precisely:

- **Lint is static analysis and never connects to the target database.** MF103 fires because the change is *categorically* unsafe on a non-empty table, not because Atlas observed rows. Correct behaviour for CI — but it tells you "this class of change is data-dependent," not "this will fail on prod."
- Adding a NOT NULL column **with a DEFAULT** is not flagged, because the default satisfies existing rows.

Atlas's own answer to this is **Pro pre-migration checks** (`checks.sql` assertions evaluated against the live target before the DDL runs; the docs name *"confirming orphan rows don't exist before adding foreign key constraints"* verbatim as the use case).

> **DECIDED: we stay on free Atlas. Pro is out of scope.**

### 4.4.1 The free-tier equivalent — verified working

Pro's `checks.sql` is a convenience wrapper. The same guarantee is expressible in a plain migration file, because we control the SQL. **Two mechanisms, both free, both verified against local Postgres on 2026-08-26.**

**(a) A hand-written precondition that fails loudly.** Put it at the top of the migration that adds the constraint:

```sql
DO $$
DECLARE n bigint;
BEGIN
  SELECT count(*) INTO n FROM registry_artifacts c
    WHERE c.export_job_id IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM export_jobs p WHERE p.id = c.export_job_id);
  IF n > 0 THEN
    RAISE EXCEPTION 'migration precondition failed: % orphan row(s) in registry_artifacts.export_job_id; backfill or delete them before adding the FK', n;
  END IF;
END $$;
```

Verified output against a seeded orphan:

```
ERROR:  migration precondition failed: 1 orphan row(s) in ... ; backfill or delete them before adding the FK
```

That is strictly better than what v0.0.23 produced — a named precondition with a row count and a remediation instruction, instead of a raw constraint violation inside a crashlooping pod.

**(b) Fix it deterministically with a data migration.** DML before DDL in the same ordered stream (§4.6) — this is the capability auto-migration never had:

```sql
UPDATE registry_artifacts SET export_job_id = NULL
  WHERE export_job_id IS NOT NULL
    AND NOT EXISTS (SELECT 1 FROM export_jobs p WHERE p.id = registry_artifacts.export_job_id);
-- ... assertion from (a) ...
ALTER TABLE registry_artifacts ADD CONSTRAINT ... FOREIGN KEY ...;
```

Verified end-to-end: `UPDATE 1` → assertion passes → `ALTER TABLE` succeeds. With `-- atlas:txmode` the whole file is one transaction, so a failed assertion rolls back cleanly and leaves nothing half-applied.

**Convention to adopt:** any migration adding a foreign key or a NOT NULL constraint to an existing table **must** carry an explicit precondition block, and lint's MF103/MF104 warnings are the trigger to write one. Lint tells you the change is data-dependent; the precondition tells you whether *this* database actually has the problem.

### 4.4.2 What this leaves

With Pro out, the defence is three layers, none of them paid:

| Layer | Catches | When |
|---|---|---|
| `atlas migrate lint` (MF103/MF104/DS102/BC101) | the *class* of risky change | PR, static |
| Redacted-snapshot apply in CI | the *actual* orphan rows | PR, against real data |
| Hand-written precondition (§4.4.1) | the actual rows, at apply time | deploy, with a clear message |
| Migration as a Job, not on startup (§4.2) | — | turns any failure into a failed Job, not a crashloop |

**Item 3 above (redacted snapshot) is therefore load-bearing, not garnish** — it is the only layer that catches the problem *before* a human is waiting on a deploy. But note the precondition block means that even if the snapshot pipeline is skipped, the failure is diagnosable in seconds rather than mysterious.

### 4.5 Baselining the existing database

**There is no `atlas migrate baseline` subcommand.** Baselining is the **`--baseline` flag on `atlas migrate apply`**.

1. Ensure the ent schema exactly matches what is deployed.
2. Generate an initial migration capturing current state — diff against an **empty** dev database, producing one file that creates everything.
3. Verify the generated file matches production.
4. Apply:
   ```bash
   atlas migrate apply --dir "file://ent/migrate/migrations" \
     --url "postgres://..." --baseline "<version>"
   ```
   Atlas records that version as applied **without executing it**, then proceeds with anything newer.

**Failure modes:**

- **Atlas refuses by default.** *"If your database contains resources but no revision information yet, Atlas will refuse to execute migration files."* You will hit this until you baseline. Guardrail, not a bug.
- **`--allow-dirty` is the wrong tool.** It bypasses the check entirely and is meant for tables Atlas should not manage at all. Using it to silence the error leaves **no baseline record**, so every later `migrate diff` replays against a state that does not match reality → silent drift.
- **A baseline that does not exactly match production poisons everything downstream**, since all future diffs replay the directory.
- Use **`--dry-run`** to print pending files and SQL without executing.

**This step touches the real database and is the sharp edge of the whole spike. Rehearse against a restored snapshot before running it anywhere that matters.**

### 4.6 Data migrations

`atlas migrate new <name>` creates an empty file for hand-written SQL — documented for resources Atlas does not capture in DDL and for seeding data, explicitly including `INSERT`. Same `<version>_<name>.sql` naming, applied in the same lexicographic order, so DDL and DML interleave in one stream.

**Gotcha:** after hand-writing or editing any file you **must** run `atlas migrate hash` to recompute `atlas.sum`, or `atlas migrate validate` fails. Per-file transaction control via `-- atlas:txmode`.

---

## 5. Acceptance criteria

- [ ] `ent/migrate/migrations/` exists and is committed, with a baseline covering the current 12 tables
- [ ] `atlas migrate status` clean against local **and** AKS
- [ ] `cmd/orbital/main.go` no longer calls `db.Schema.Create`; the server does not migrate on boot
- [ ] `WithDropColumn(true)` gone from the production path
- [ ] CI runs `atlas migrate lint` + `validate` on PRs touching `ent/`
- [ ] CI applies pending migrations against a redacted AKS snapshot (§4.4.2 — load-bearing now that Pro is out)
- [ ] Every migration adding an FK or NOT NULL to an existing table carries a precondition block (§4.4.1)
- [ ] A deliberate reproduction of v0.0.23 — FK added to a table seeded with orphan rows — **fails in CI**, not at deploy
- [ ] A data migration (DML then DDL) demonstrated end-to-end at least once
- [ ] The dev invariant holds: `make up` → `make run-orbital` → `make run-orb`, no extra setup (§7)
- [ ] `docs/reference/MIGRATIONS.md` documents "I changed an ent schema, now what?"

---

## 6. Open questions

1. ~~Atlas Pro, or the redacted-snapshot pipeline?~~ **DECIDED 2026-08-26: free Atlas only.** Pro pre-migration checks are out; §4.4.1 gives the verified free equivalent (hand-written precondition blocks + DML-before-DDL data migrations). Remaining sub-question is Q3 — the snapshot pipeline still needs an owner.
2. **Does `make test-integration` migrate via Atlas or keep `Schema.Create`?** Tests start empty, so auto-migration is correct and faster there — but then tests never exercise the path production uses. Leaning: tests apply real migrations, making drift impossible by construction. Affects `export_integration_test.go:59` and `ingester_integration_test.go:49`.
3. **Where does the redacted AKS snapshot live, who refreshes it, and what is redacted?** The audit table holds actor emails and full mutation payloads. This has a privacy dimension and needs an owner, not just a pipeline.
4. **Is the Atlas CLI a hard developer prerequisite, or generation-only?** Contributors who never touch `ent/` should not need it. Leaning: generation-only, documented in `CONTRIBUTING.md`, with CI enforcing that generated migrations were committed.
5. **Init container vs Helm pre-upgrade hook Job.** §4.2 recommends `--migrate` from an init container; a Helm hook is the more conventional K8s answer and interacts differently with rollbacks. Check against the chart work in `armada-orbital`.
6. **Rollback policy.** Atlas supports down-migrations but they are not universally safe (a dropped column's data is gone). Decide whether the policy is roll-forward-only — most teams land there — and write it down.
7. **Does orb follow?** See §8.

---

## 7. Risks & prerequisites

| Risk | Mitigation |
|---|---|
| **Baselining is the sharp edge** (§4.5) | Rehearse against a restored snapshot before touching AKS |
| **Lint does not cover the original failure** (§4.4) | Resolve Q1 before claiming the gap is closed |
| **Atlas CLI installed nowhere today** | Resolve Q4; add to `CONTRIBUTING.md` + CI image regardless |
| **`make up` / `make run-orbital` must keep working with zero setup** | Non-negotiable dev invariant. If migrations move off startup, the Makefile target must run them — a developer must never hand-run a migrate step |
| **Failure signature changes from crashloop to failed Job** | This is the goal, but update whatever watches deploys so a failed migration Job is not silently ignored |
| **`atlas.sum` merge conflicts** | Expected and intentional; document the `atlas migrate rebase` / regenerate workflow so it is not treated as breakage |

---

## 8. Orb (SQLite)

`internal/orb/store/db.go:27` calls `client.Schema.Create(ctx)` with no drop-column. Orb's risk profile differs genuinely: SQLite, single process, and `orb.db` is a **cache of imported state** rather than a system of record — a corrupted orb DB is recoverable by re-importing.

Atlas supports SQLite, so one toolchain covers both with two migration directories and no second vendor. **Recommend deferring** until orbital's Postgres migration is proven, then porting deliberately. Do not do both at once.

---

## 9. Deliverable

Fold decisions into a new `docs/reference/MIGRATIONS.md`, then delete this spike per the spike-lifecycle rule in `CLAUDE.md`. (`ROADMAP.md`'s "ADR-014" deliverable was corrected 2026-08-26 — `docs/decisions/` was removed 2026-07-07.)

---

## 10. References

- ent — [Automatic Migration](https://entgo.io/docs/migrate/) • [Versioned Migrations](https://entgo.io/docs/versioned-migrations/)
- Atlas — [Migration Analyzers](https://atlasgo.io/lint/analyzers) • [Verifying Migration Safety](https://atlasgo.io/versioned/lint) • [Applying Migrations](https://atlasgo.io/versioned/apply) • [Pre-migration Checks](https://atlasgo.io/versioned/checks) • [Manual Migrations](https://atlasgo.io/versioned/new) • [CLI Reference](https://atlasgo.io/cli-reference) • [Supported databases](https://atlasgo.io/databases)
- ent v0.14.6 source verified locally at `$GOMODCACHE/entgo.io/ent@v0.14.6/dialect/sql/schema/{atlas.go,migrate.go}`
