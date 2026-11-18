# DGraph schema migrations on deploy

**Status:** Proposed — not started.
**Date:** 2026-11-18
**Scope:** applying `schema/schema.graphql` and its data migrations to orbital's DGraph on every rollout. Pairs with [Atlas migrations](atlas-migrations.md), which does the same for PostgreSQL and defines the shared `orbital --migrate` step (§4.2 there).
**Question:** How does a new orbital image bring DGraph to the schema it expects, safely, without a manual runbook?

---

## 1. The problem

- **Nothing applies the schema on deploy.** Orbital applies it only on `make seed`, restore, export to scratch and orb import, never at startup or rollout (`DGRAPH.md`). An image ahead of its DGraph queries fields that do not exist, and pages render empty or error. It has happened twice: v0.0.25 (a new cluster field) and v0.0.63, the first v20 image.
- **Renames need a data copy, run by hand.** A rename is a new DQL predicate, so v20 needed `scripts/migrate-schema-v20.sh` on every DGraph; until then the renamed fields read `null`. The old predicates were never dropped.
- **Two apply paths read different files.** Restore applies the schema baked into the running image; `make seed-aks-dgraph` applies the one in the local checkout.
- **Drift is detected, not fixed.** The boot log and the `/schema` page report it (`internal/dgraphschema/drift.go`); someone still has to act.

## 2. Proposal

### 2.1 One migrate step per rollout, for both stores

- `orbital --migrate`, the step the Atlas proposal defines, runs once per rollout from a Helm pre-upgrade hook Job and a kustomize Job, before the Deployment rolls.
- **PostgreSQL first, then DGraph**, so DGraph data steps can record their writes in history ([history and undo](history-and-undo.md)).
- **A failure fails the Job,** and the previous pods keep serving. Nothing crashloops.
- A PostgreSQL advisory lock stops two runs from overlapping, the same pattern as orbital's job leases.

### 2.2 Versioned DGraph steps, compiled into the binary

- One step per `schema/VERSION` (`v20`, `v21`, …). A step applies that version's schema and, if needed, runs a data migration written in Go.
- Every step is idempotent: a rerun is a no-op, and a run that failed part-way resumes.
- The schema a step applies is the file shipped in the image, which removes the two-file problem.
- `scripts/migrate-schema-v20.sh` becomes step `v20`.

### 2.3 The record of applied steps lives in DGraph

- A DQL-only predicate on a dedicated node, kept out of the GraphQL schema so consumers of the Topology API never see it.
- **Why DGraph rather than PostgreSQL:** the record travels with DGraph's backups and restores. Restoring an older backup brings back its older record, so the next migrate knows which steps to run again.
- `DGRAPH.md` mentions a PostgreSQL `schema_versions` table; none exists. The only version recorded today is on backup rows, which restore compares against the image.

### 2.4 Every schema change gets a safety class, checked in CI

| Class | Examples | Behaviour |
|---|---|---|
| Additive | New type, new nullable field | Applied automatically |
| Reindexing | `@search` or `@index` added to an existing field | Applied automatically, logged as a reindex; rebuilds the index across the whole graph, so the Job timeout must allow for it |
| Breaking | Rename, removal, type change, non-null field on an existing type | Only through an explicit step that migrates the data |

CI diffs `schema/schema.graphql` against the previous release and fails when the schema changed without a `schema/VERSION` bump, or a breaking change has no step. Orbital has no GraphQL schema parser today; the [GraphQL AST](graphql-ast-parse.md) proposal would supply one.

### 2.5 Expand, then contract

- `DGRAPH.md` already requires backward-compatible schema changes, because orbs may lag orbital by versions. This makes the rule mechanical.
- A rename or removal ships in two halves:
  - **expand** (release N): add the new field and copy the data;
  - **contract** (a later release): `drop_attr` the old predicate, once every consumer has moved.
- Migrations only go forward. A Helm rollback works because the previous image still runs on the expanded schema.
- v20 shipped its expand half only. Dropping `serial` and `kubernetesNodeIpv4` becomes its first contract step.

### 2.6 Where else migrate runs

| Place | Behaviour |
|---|---|
| Restore | After loading a backup, run the steps the backup's record is missing. This replaces the schema-mismatch confirmation |
| Orb | Nothing to do: its importer runs `drop_all`, applies the bundle's schema, then loads data exported from an already-migrated orbital |
| Local development | `make run-orbital` runs `--migrate` first; a no-op when current, so an empty local DGraph needs no manual schema apply |
| Integration tests | `make test-integration` can use it for the test cluster instead of its own `curl` |
| Startup | Keeps detecting and reporting drift. It never refuses to start, per CLAUDE.md's probe rule |

### 2.7 History

A data step writes to DGraph outside the normal write path. Once per-node history ships, the step records each node it changes, attributed to the migration, so the history reconciler does not flag them as unrecorded changes.

## 3. Conventions followed

| Practice | Precedent |
|---|---|
| Ordered migrations with a record of what ran | [Rails](https://guides.rubyonrails.org/active_record_migrations.html) (`schema_migrations`), [Django](https://docs.djangoproject.com/en/stable/topics/migrations/) (`django_migrations`), Atlas |
| Migrations in a step of their own before the new version serves | [GitLab's Helm chart](https://docs.gitlab.com/charts/charts/gitlab/migrations/) runs them as a Job; [Helm hooks](https://helm.sh/docs/topics/charts_hooks/) order work around an upgrade; [NetBox's upgrade](https://netboxlabs.com/docs/netbox/en/stable/installation/upgrading/) runs `migrate` |
| Expand, then contract | [Parallel change](https://martinfowler.com/bliki/ParallelChange.html) |

## 4. Draft acceptance list

To be approved before implementation, per CLAUDE.md.

1. A rollout against a DGraph on an older schema version brings it to the image's version before the new pods serve.
2. A failed migration fails the Job and leaves the previous pods serving.
3. A rerun is a no-op when current, and resumes after a partial failure.
4. Restoring an older backup ends with DGraph at the image's version, its data migrated.
5. CI fails a schema change without a `schema/VERSION` bump, and a breaking change without a step.
6. `make up` then `make run-orbital` against an empty DGraph applies the schema with no manual step.
7. The applied-steps record is not visible through `/graphql`.
8. Two concurrent migrate runs cannot both apply.

## 5. Open questions

- Does a GraphQL schema update leave a DQL-only predicate intact, and do DGraph's export and restore carry it? Verify on DGraph v25.3.1 before relying on §2.3.
- Export already applies a schema to scratch (`internal/handler/export.go`). Does migrate need to touch scratch at all?
- How long does a reindex take on a large graph, and what Job timeout follows from it?
- When can a contract step run, given orbs may lag? It needs a signal for the oldest orb version still in service.
- Fix the stale `schema_versions` line in `DGRAPH.md` when this lands.
