# 007 — DGraph Schema Migration

**Date:** 2026-06-07  
**Status:** Research complete — no tooling adopted for MVP  
**Context:** Removing Spike 12 (DGraph operations runbook) from the roadmap prompted a research pass on what schema migration tooling exists for DGraph and what production teams actually do.

---

## Does DGraph have built-in schema migration tooling?

**No.** DGraph ships with the admin endpoint and nothing else.

- **Apply:** `POST /admin` with the `updateGQLSchema` mutation (or `dgraph live --schema` for bulk loads, or DQL `alter` on the gRPC client). Updates propagate via Raft; the schema is in memory immediately. Index/data rebuilds happen asynchronously in the background.
- **Fetch:** `getGQLSchema` query against `/admin`.
- There is no versioning table, no checksum/history, no migration runner, no rollback affordance. The "version" of your schema is whatever you last POSTed.

A 2022 community feature request titled "Schema/data migration tool" was raised and never delivered. The official response on related threads: "Dgraph does not provide a straight forward solution" — teams are expected to write their own or use the admin endpoint in CI.

---

## Liquibase/Flyway equivalents for graph databases

| DB | State |
|---|---|
| Neo4j | Mature. `neo4j-migrations` (Neo4j Labs) is the canonical tool — Flyway-style versioned Cypher scripts. Liquibase has an official Neo4j extension (formerly Liquigraph). |
| JanusGraph / DSE Graph | One unmaintained tool (`cormaxed/graph-migrate`, Java, DSE-specific) — not active. |
| **DGraph** | **Nothing.** No Atlas, Bytebase, Sqitch, Liquibase, or Flyway driver. No actively maintained third-party migration tool. The `dgraph-io/migration` tool that exists is a MySQL→RDF importer, not a schema versioning tool. |

Atlas (Ariga) has a plugin model; if someone ever writes a DGraph driver it would likely land there. As of June 2026, no such driver exists.

---

## What production teams actually do

The consistent pattern from `discuss.dgraph.io`:

- **Schema in Git** as source of truth — a single `schema.graphql`, hand-maintained, reviewed like code.
- **CI step POSTs the schema** to `/admin/schema` after the cluster is up. Idempotent — POSTing an unchanged schema is a no-op for data (but see sharp edges below about index rebuilds).
- **Data migrations are hand-written one-offs** — DQL upsert blocks or ad-hoc application endpoints. Multiple community members describe theirs as "really hacky"; this is representative, not exceptional.
- **Decouple code and schema deploys** — additive schema change first, app deploy second. For breaking changes, run two app versions concurrently against a dual-named schema using the `@dgraph(pred:)` directive to bridge old + new field names.

---

## Sharp edges when changing a live DGraph schema

These are confirmed across community threads and the Hypermode docs:

### 1. Full-schema POSTs rebuild all re-declared indexes

Posting the full schema with an unchanged `@search(...)` directive alongside a new field causes DGraph to drop and rebuild the existing index. **Index rebuilds run in background, but queries against those predicates fail — not just slow — until the rebuild finishes.** Workaround: only send modified predicates via the DQL `alter` gRPC client. For GraphQL SDL this is awkward because `updateGQLSchema` takes the full schema. Plan a maintenance window for any change that adds or modifies a search index on a large predicate.

### 2. Scalar→UID is forbidden with existing data

If the predicate already has scalar data, DGraph will not convert it to a UID-type. Requires deleting the predicate first (losing data), then re-adding.

### 3. Adding `@id` to an existing field

The schema POST itself succeeds. Duplicate values in existing data cause query failures discovered at query time, not at migration time. Always validate for duplicates before applying `@id` to an existing predicate.

### 4. Renames do not exist at the predicate level

DGraph does not rename a predicate. Two options:
- **(a) Data migration:** write a DQL upsert that copies old→new and drops old. Requires a data migration plan + downtime window.
- **(b) `@dgraph(pred:)` directive:** rename only the GraphQL surface while the underlying predicate name stays stable. Zero-downtime on the DB side; clients migrate their query text separately. This is the preferred approach for GraphQL-layer renames.

### 5. No transactional schema change

Multi-predicate changes are not atomic. A partial failure mid-apply leaves a mixed state. Mitigation: design every change to be additive and independently reversible. Never batch unrelated schema changes into one apply.

### 6. Type renames follow the same rule

Renaming a DGraph type (`type Server { ... }` → `type ComputeNode { ... }`) requires a data migration to rewrite all `dgraph.type` predicates on existing nodes. The `@dgraph(type:)` directive can bridge the GraphQL name, but the underlying type label is what DGraph stores on each node.

---

## Minimal production-safe approach for this stack

Given the absence of tooling, the right approach for a v1 production rollout without building a custom tool:

### 1. Schema in Git (already done)

`schema/` directory is the single source of truth. Schema PRs are reviewed like code. Every change includes an assessment of whether it falls into the additive-safe or breaking category.

### 2. Init container or Job on rollout

A small Kubernetes Job (or init container on the orbital Deployment) that POSTs `schema.graphql` to `/admin/schema` on every rollout. Idempotent — if nothing changed, it's a no-op for data. This is the "Flyway lite" for DGraph: owned by the same deploy that ships the new binary.

The orbital binary itself could own this, gated by an `--apply-schema` flag on startup.

### 3. Additive-only rule, enforced by review

Every schema PR diff is inspected for:
- Field removals (breaking — clients still referencing the field will error)
- Type narrowings (may require deleting the predicate)
- Index changes on large predicates (requires maintenance window — query failures during rebuild)
- `@id` additions on existing fields (requires duplicate-check before apply)
- Type or field renames without `@dgraph(pred:)`/`@dgraph(type:)` bridge

These are "needs migration plan + maintenance window", not "merge and deploy." Tooling doesn't save you from this discipline — Neo4j teams with Liquibase must enforce the same rules manually.

### 4. `@dgraph(pred:)` for any rename

Use the directive to keep the underlying predicate stable while renaming the GraphQL surface. The schema apply is a no-op for data; no data migration needed. Clients migrate their query text at their own pace.

### 5. Data migrations as versioned Go functions

When a data migration is unavoidable (bulk copy, predicate rename with actual data movement, duplicate cleanup before `@id` apply):

- Author it as a versioned Go file: `internal/dgraph/migrations/2026_06_xx_description.go`
- Register in an ordered slice with an idempotency check
- Use a `schema_migrations` table in PostgreSQL (already present for ops data) to track which migrations have been applied — `version`, `name`, `checksum`, `applied_at`, `applied_by`
- Run on orbital startup before serving traffic

This is the homegrown Flyway pattern several DGraph community members describe. PostgreSQL as the bookkeeping store removes the "where do we record what ran" problem. Cost: ~200 lines of Go, no external tool, no new dependency.

### 6. Restore-test schema applies

The backup→restore infrastructure already exists. A CI job that restores last night's backup into a scratch DGraph and applies the current schema catches compatibility regressions before they reach production.

---

## Schema versioning and backup artifact naming

**Status:** Design settled 2026-06-07. Implementation deferred to next session.

### The operator scenario that drove this design

A developer ships a schema change. The new schema (`v2`) goes live; data populates for several days. The team decides to roll back to `v1`. An operator needs to go to S3 and find the most recent `v1` backup — without opening each zip, without orbital running, possibly under pressure.

A content hash (`sch-a3f7c81d`) is useless here. The operator doesn't know which hash corresponds to `v1`. A human-readable version in the filename is what they need.

### Why not a content hash

A hash of the raw SDL bytes changes whenever a developer adds a comment or reformats the file — changes with zero impact on DGraph behavior. A hash of normalized predicates (parsed, canonical, comment-stripped) avoids this but requires a DGraph SDL parser and a stable canonical serialization format. Both hash approaches also share the same deeper problem: they answer "did the file change?" not "do I care about this change?" That's a judgment call only a human can make.

**Decision: use a human-maintained version label, not a hash. The hash is dropped entirely.**

### Backup filename format

```
orbital-schema-v1-binary-v0.4.2-20260509T135041Z.zip
```

- `schema-v1` leads — it's the restore-compatibility signal. Enables trivial S3 filtering: `orbital-schema-v1-*.zip`
- `binary-v0.4.2` follows — forensic context (which orbital produced this backup; useful if a bug is discovered in backup logic)
- Timestamp last — ISO 8601 basic format, lexically sortable
- Both labels are explicit (`schema-` and `binary-` prefixes) to remove ambiguity about which version gates restore

### `schema/VERSION` file

A single-line file at `schema/VERSION` containing the current schema version label (e.g. `v1`). This is the authoritative source read by orbital at backup time.

- **Not** a comment in the SDL — comments are formatter-strippable and fragile to parse
- **Not** a Go constant — would couple schema version to binary release cycle
- **Not** a DGraph custom directive — adds a place for the version to lie

Bumped manually by the developer making a DGraph-relevant schema change. Comments, whitespace, and formatting changes to `schema.graphql` do **not** bump the version.

### CI enforcement

CI parses `schema.graphql` predicates on the PR's base and head. If the parsed predicates differ and `schema/VERSION` is unchanged → fail. The developer decides whether to bump; CI ensures they can't forget. CI does not attempt to classify the change as breaking or non-breaking — that's a human judgment made in PR review.

### `manifest.json` inside the zip

Every backup zip includes a `manifest.json` alongside `data.json.gz` and `schema.gz`:

```json
{
  "manifestVersion": 1,
  "createdAt": "2026-05-09T13:50:41Z",
  "orbitalVersion": "v0.4.2",
  "schemaVersion": "v1",
  "dgraphVersion": "v25.3.1"
}
```

No hash field. The schema version is the contract.

The manifest is authoritative for the UI and restore logic. The filename is for humans browsing S3 directly. Both carry `schema_version`; if they disagree (object was manually renamed), the UI flags it.

### Restore compatibility check

At restore time, orbital reads `manifest.json` from the selected zip and compares `schemaVersion` against the current `schema/VERSION`:

- **Match** → restore proceeds normally
- **Mismatch** → surface a clear warning: *"This backup was taken against schema v1. The current schema is v2. Restore will replace the DGraph schema."* Require explicit confirmation before proceeding.

Old zips without a manifest (created before this feature) restore without the check; the UI marks them "legacy — no manifest."

### Schema ent table columns

Add `schema_version` (string, from `schema/VERSION` at backup time) and `binary_version` (string, from `internal/version.Version`) to the `backup` ent table. These back the UI badge display without requiring the zip to be downloaded.

---

## Drop + live load as a planned migration tool

The `SubprocessRestoreBackend` (drop + `dgraph live`) is currently framed as disaster recovery. It is equally the correct mechanism for **planned breaking schema migrations**.

For breaking schema changes (predicate type change, `@id` addition on a populated field, type rename without `@dgraph(type:)` bridge), the supported migration path is:

1. Take a backup (`POST /api/v1/backup`) — produces a zip with the current data shaped against the old schema
2. Deploy the new orbital binary with the updated `schema.graphql` and bumped `schema/VERSION`
3. Restore from that backup via the Restore page — orbital drops DGraph and live-loads the backup, then applies the new schema
4. Verify
5. Resume traffic

This is identical to the disaster-recovery flow. The "Disaster Recovery" page title is misleading — both flows use the same mechanism. The page will be retitled **"DGraph Reload"** to reflect this.

Additive schema changes (new field, new type, new index on empty predicate) do not require a reload — apply via `POST /admin/schema` directly.

---

## Decision

The settled decision in `CLAUDE.md` — "schema migration automation is out of MVP scope, a runbook is sufficient" — is consistent with the broader community state. The conventional wisdom for production DGraph is: **schema in Git + CI apply + additive discipline + hand-written data migrations.** No Flyway-shaped tool exists to adopt.

**Do not build a custom migration CLI for MVP.** The smallest defensible formalization, when the need is felt post-MVP:
1. Init container in `deploy/base/` applying schema on rollout
2. `schema_migrations` ent table + versioned Go migration functions

Build this after ≥3 painful migrations reveal the actual shape of what you need — not before.

---

## Sources

- [DGraph Schema Migration (Hypermode docs)](https://docs.hypermode.com/dgraph/graphql/schema/migration)
- [DGraph admin endpoints — updateGQLSchema](https://docs.dgraph.io/v24.1/admin/admin-endpoints/)
- [Discuss: Feature request — Schema/data migration tool](https://discuss.dgraph.io/t/feature-request-schema-data-migration-tool/14989)
- [Discuss: Ways to migrate schema/data](https://discuss.dgraph.io/t/ways-to-migrate-schema-data/9023)
- [Discuss: Is it reliable to periodically update schema on production?](https://discuss.dgraph.io/t/is-it-reliable-to-periodically-update-schema-on-production/13145)
- [Discuss: How to set up automated migration](https://discuss.dgraph.io/t/how-to-set-up-automated-migration/10758)
- [Discuss: Migrating (renaming predicates, etc)](https://discuss.dgraph.io/t/migrating-renaming-predicates-etc/5239)
- [neo4j-migrations (Neo4j Labs)](https://neo4j.com/labs/neo4j-migrations/)
- [Liquibase + Neo4j](https://www.liquibase.com/blog/understand-use-graph-databases-automate-schemaless-deployments-with-neo4j-liquibase)
- [Atlas plugin model](https://atlasgo.io/atlas-vs-others)
- [DGraph migration tool (MySQL importer, not schema versioner)](https://dgraph.io/docs/migration/migrate-tool/)
