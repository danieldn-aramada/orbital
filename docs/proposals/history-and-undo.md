# Full history, undo and change feed

**Status:** Proposed — not started. The starting position below comes from discussions on 2026-11-05 and 2026-11-15 and is not a decision.

*Moved out of `docs/planning/backlog.md` 2026-11-08: the entry had grown past what a tracker line can carry. Fold decisions into `docs/reference/AUDIT.md` when this closes, then delete this file.*

---

**The question:** Should orbital keep a complete history of every ConfigItem? If so, as git-style versioning of the whole graph, or as a complete audit log? The motivating use case is node-level rollback: restore a node at version 10 to what it was at version 1.

## Target design (proposed, not ratified)

Three stores, one job each, and one rule: every change goes through orbital.

```
          every change: UI · API · orbctl · rollback · restore · divergence accept
                                       │
                             orbital write path
                  (version check · change control · audit identity)
                                       │
               ┌───────────────────────┴───────────────────────┐
               ▼                                               ▼
   DGraph — CURRENT STATE                      PostgreSQL — HISTORY (working copy)
   what each node is now                       one record per node per change:
                                               before · after · who · authority · when
                                               append-only, hash-chained, indexed by node + version
                                                               │ closed segments (e.g. hourly)
                                                               ▼
                                   Write-once object storage — HISTORY (authoritative)
                                   S3 Object Lock or Azure locked retention;
                                   optionally also streamed to the adopter's SIEM

   Verifier, running continuously:
     • the chain in PostgreSQL is intact and not behind the archive   → else alert, refill from the archive
     • each node's DGraph version equals its latest history record    → else alert, record an unrecorded change
```

| Scenario | What happens |
|---|---|
| Edit a node | Write to DGraph, read it back, append a history record with before and after images |
| Roll a node back to v1 | A new write restoring v1's image, becoming v11, through change control. Nothing is rewritten |
| Restore DGraph from a backup | Recorded as one change per affected node, under the restore's audit event |
| PostgreSQL is rolled back | The verifier sees the chain behind the archive, alerts, and refills the missing segments. At most one segment is lost |
| A direct write to DGraph | The verifier sees the version mismatch, alerts, and records an unrecorded change |
| Retention | PostgreSQL drops whole archived segments; the archive keeps them for the adopter's write-once period |
| A node as of date X | The latest record at or before X, from PostgreSQL or the archive |
| The whole graph as of date X | The nearest published export, a signed full snapshot |

**What orbital can then claim — "immutable history":** every change to every node is recorded in full, with who and under what authority; orbital never overwrites or deletes a record; any alteration or deletion of the record is detectable, and recoverable up to the last segment; writes that bypass orbital are detected, not silent.

**Deliberately left out:** time-versioning the graph inside DGraph (unsupported), branching (a separate concern), git as the store (its history can be rewritten), and Merkle proofs (needed only if outside parties verify single records).

In established terms: CloudTrail's shape (events delivered to write-once storage, with hashes to verify them), holding NetBox-style per-object before and after images, with rollback as a forward write like `git revert`.

## Answers (proposed, not ratified)

| Question | Answer |
|---|---|
| Keep a complete history of every ConfigItem? | Yes. One event per node per write, with full before and after images; the after-image read back from DGraph |
| Whole-graph versioning or a complete audit log? | A complete audit log, with the per-entity images on the audit event's existing per-entity rows (`audit_event_resources`) — NetBox `ObjectChange` granularity, not a second store. Not whole-graph versioning: DGraph keeps no queryable history, `orbId` uniqueness rules out side-by-side copies, and published exports already provide whole-graph snapshots |
| Roll a node at v10 back to v1 | Read v1's image by `orbId` and version, write it back as v11 through the normal write path (version precondition, audit, change control). History is never rewritten. No branching |
| Retention | Keep everything by default, add a growth metric, archive to write-once object storage, and let the operator prune what is already archived |
| Can orbital claim "immutable history"? | Once complete history and the audit hash chain with external anchors ship, on top of the existing append-only guard. Until then, "append-only" (`audit-model.md` §5) |

## Already decided

- Branching. A branch is a stored changeset, not a copy of the graph (`change-control-research.md`, addendum 2026-08-26). Real branches only if long-lived, isolated workspaces are needed. Rollback does not need branching: a branch stages future changes, a rollback restores a past state.
- One history store. The audit log serves both provenance and lineage (`AUDIT.md`, "ONE table serves both provenance and lineage").
- Discarding an unpublished change is the client's job: the export preview carries each field's before-value, and orbital exposes no revert endpoint for it (`OCI.md`). Node rollback is outside that decision.
- Whole-graph snapshots exist. Every published export is a signed, versioned, full snapshot, and export compare diffs two of them.

## The gap

`AUDIT.md`: orbital is "NetBox-shaped on row admission and NOT on payload". An event stores what the caller sent as the after-state and a flat before-image. So:

1. A create has no before, so it has no `changes`.
2. An event covering several items cannot be attributed per item. This overlaps with [One audit event per entity](audit-event-per-entity.md).
3. Nothing answers "what did this item look like on date X" without replaying partial inputs.

What is stored today (`auditMutation` in `internal/handler/graphql.go`):

- Every update stores the node's own scalar fields as they were before the write (`details.before`, from `configitems.BeforeSelection`), plus the query text and the caller's variables. Children and relationships are not included. `changes` is computed when the row is read.
- So the node at version N is the `before` of the write that moved it to N+1. That holds only if every write went through orbital's API (a direct DGraph write leaves no event), the write was not a multi-entity save, and the row can be found; nothing indexes `orbId` and `version`.
- Storage per node grows with writes × fields. ConfigItem types have 13 scalar fields at the median and 19 at most, so a row is roughly 0.5–2 KB, plus any large JSON-string field, which is copied on every write.

## How comparable tools handle history

| Tool | History | Git-like versioning |
|---|---|---|
| NetBox | Changelog with a full before and after copy per change; kept 90 days by default (`CHANGELOG_RETENTION`), configurable to forever. Core records changes but has no revert | Branch, diff, merge and revert through the netbox-branching plugin (source-available license) |
| Nautobot | Changelog like NetBox's | A git-semantics app on Dolt; no commits since September 2024, docs moved to Nautobot's archive site |
| InfraHub | Built by Infrahub on Neo4j, not by the database. Every edge carries `branch`, `from`, `to` and `status`: an update closes the current edge and adds a new one, a delete adds a tombstone, and a query `at=<time>` keeps the edges valid at that time | Copy-on-write branches, tagged on the same edges |
| Terraform, Argo CD | Git is the store | By construction; the model where git holds config, which orbital is not |

Full before/after changelogs are standard. Whole-database versioning is rare: InfraHub designed its storage around time from the start, and the one retrofit (Nautobot on Dolt) went dormant.

InfraHub's history is queryable by time but not tamper-evident. Its own code rewrites history on rebase, branch delete and failed-merge rollback; its activity log, which records who changed what, is deleted after 7 days by default ([#6906](https://github.com/opsmill/infrahub/issues/6906)); and it has no API to list a field's history ([#11481](https://github.com/opsmill/infrahub/issues/11481)). No node-revert mutation was found in its source.

## What the databases provide

| Database | History or time travel |
|---|---|
| Neo4j, Memgraph | None. Neo4j's modelling guide says to add `validFrom`/`validTo` yourself; its change data capture is a change feed, and point-in-time restore restores backups. Memgraph garbage-collects old versions |
| PostgreSQL | No system-versioned tables. The PostgreSQL 19 docs say system time "could be emulated using triggers"; the `temporal_tables` extension does that |
| DGraph | MVCC for snapshot isolation only. Old versions are discarded after Raft snapshots (by default every 30 minutes and 10,000 entries), timestamps are logical counters, and its maintainers say historical reads are not supported. Reading at an earlier timestamp has open correctness bugs ([dgraph#9856](https://github.com/dgraph-io/dgraph/issues/9856)). Its change data capture emits new values only, per predicate, keyed by internal `uid` |
| Built in | Datomic, XTDB, Dolt, TerminusDB, and SQL:2011 system-versioned tables in MariaDB, SQL Server and Db2 |

Most CMDB-shaped products build a per-entity version table in the application: NetBox `ObjectChange`, Hibernate Envers, PaperTrail, django-reversion, and for ent, `flume/enthistory`.

## Retention and the "immutable history" claim

`audit-model.md` §5 says to avoid "immutable" until enforcement backs it. InfraHub uses the word for history that is append-only by convention, not tamper-evident, with its activity log deleted after 7 days. Orbital can claim it once all four hold:

| Property | Status |
|---|---|
| Nothing is overwritten | ✅ A PostgreSQL trigger refuses UPDATE and DELETE on the audit tables, verified at startup |
| Every change is recorded in full | ❌ This spike |
| Alteration is detectable | 📋 Designed, not built: a hash chain over audit rows with heads published to write-once storage (`audit-model.md` §5) |
| Old history is kept, not lost | ❌ The retention decision below |

**Where tamper-evident logs are stored — the evidence.**

| Store | Evidence |
|---|---|
| PostgreSQL and other SQL databases | Trillian, the log server behind Certificate Transparency, added PostgreSQL storage in v1.7.0, contributed by Sectigo for its production logs. SQL Server 2022 and Azure SQL ship ledger tables; Oracle ships blockchain tables, a hash chain per row. AWS retired QLDB and points users to Aurora PostgreSQL |
| Git | Content is hash-linked, but refs are mutable: a force-push rewrites history, detectable only by someone who kept the old commit id. Author fields are client-supplied strings (GitHub, 2026-10-12). No evidence of git as a production audit-event store; GitOps uses it for desired state, not event logs |

In every case the tamper evidence comes from hashing plus publishing the latest hash outside the operator's control, never from the database. PostgreSQL has none built in; the one extension found, `pg_credereum`, is an unmaintained prototype.

**What popular open-source projects do** (about 30 checked against source or docs, 2026-11-15). Of 24 that keep an audit or change log, 15 store it in their own SQL database, 10 of those PostgreSQL-only or by default; six write to files, syslog or log streams. Two git forges, GitLab and Gitea, keep their audit logs in SQL, not git. None of the 24 is tamper-evident: append-only is enforced only by the application. Tamper evidence appears only in transparency logs, which add a Merkle tree and signed checkpoints over SQL or object storage. Git serves as history of content or desired state, and Gerrit's docs say its metadata refs "may be rewritten".

| Project | What it logs | Store | Tamper-evident? | Source |
|---|---|---|---|---|
| NetBox | Create, update, delete, with before/after JSON snapshots | PostgreSQL table; 90-day default retention | No | [docs](https://github.com/netbox-community/netbox/blob/main/docs/features/change-logging.md) |
| Nautobot | Same changelog model as NetBox | PostgreSQL or MySQL; 90-day default retention | No | [settings](https://github.com/nautobot/nautobot/blob/develop/nautobot/core/settings.py) |
| InfraHub | Activity log of user actions | PostgreSQL (Prefect events table); 7-day default | No | [issue](https://github.com/opsmill/infrahub/issues/6906) |
| GitLab | Instance, group, project and user audit events | PostgreSQL tables; optional stream to HTTP, S3, GCP | No | [docs](https://docs.gitlab.com/user/compliance/audit_events/) |
| Gitea | Security-relevant actions | Its SQL database; off by default, 30-day retention | No | [PR](https://github.com/go-gitea/gitea/pull/38189) |
| Gerrit | Code-review metadata; group-membership audit log | Git refs (NoteDb), replacing SQL since 3.0 | Commit hashes; docs: refs "may be rewritten" | [docs](https://gerrit-review.googlesource.com/Documentation/note-db.html) |
| Keycloak | Login and admin events | SQL tables; off by default | No | [docs](https://www.keycloak.org/docs/latest/server_admin/#auditing-user-events) |
| Zitadel | Every change, as event-sourced events | PostgreSQL event table | No | [docs](https://zitadel.com/docs/concepts/eventstore/overview) |
| Teleport | Audit events; session recordings | PostgreSQL, DynamoDB, Firestore, Athena or files; recordings in object storage | No | [docs](https://goteleport.com/docs/reference/deployment/backends/) |
| Vault / OpenBao | Every API request and response | File, syslog or socket; secret values HMAC'd | No; the HMAC protects confidentiality | [docs](https://developer.hashicorp.com/vault/docs/audit) |
| Kubernetes | Every API-server request | Log file or webhook | No | [docs](https://kubernetes.io/docs/tasks/debug/debug-cluster/audit/) |
| Argo CD | Desired-state changes; sync activity | Git history plus Kubernetes Events | Git hashes only | [docs](https://argo-cd.readthedocs.io/en/stable/operator-manual/security/#auditing) |
| Temporal | Workflow event history | Cassandra, MySQL, PostgreSQL or SQLite | No | [docs](https://docs.temporal.io/temporal-service/persistence) |
| Trillian (CT logs, Rekor v1) | Certificates and signed artifacts | MySQL/MariaDB, PostgreSQL, Spanner or CockroachDB | Yes: Merkle tree, signed tree heads | [repo](https://github.com/google/trillian) |
| Sunlight (Let's Encrypt CT) | Certificates | Object storage or filesystem, plus a small lock database | Yes: Merkle tiles, signed checkpoints | [blog](https://letsencrypt.org/2024/03/14/introducing-sunlight/) |

**Hash chain or Merkle tree.** A per-row hash chain with heads published to write-once storage is the conventional core (Schneier–Kelsey 1998; Oracle blockchain tables). Merkle trees add compact proofs that one record is in the log and that a later state extends an earlier one. They are needed when outside parties verify individual records, as in Certificate Transparency, not for a single operator who can scan the whole log (Crosby & Wallach 2009). Proposed: chain each row's content hash rather than the row, so a full image can later be redacted without breaking the chain, and a Merkle tree can still be added.

**How others handle growth.** InfraHub keeps value history in Neo4j indefinitely and prunes its activity log after 7 days. NetBox prunes its changelog after 90 days by default, configurable to forever, with no built-in archive. Audit-heavy products keep a recent window in the database and archive the rest: AWS CloudTrail shows 90 days and delivers everything to S3 with signed digest files for integrity checks; GitHub Enterprise streams its audit log to S3, Azure Blob, Splunk and others. Long-term retention usually belongs to the adopter's archive or SIEM, not the application database.

**Proposed retention:**

- Keep everything by default; the adopter sets the period (`audit-model.md` §6).
- Add a growth metric (`audit-model.md`, gap 4).
- Archive closed segments to the object storage orbital already supports (S3-compatible stores and Azure Blob), under write-once retention: S3 Object Lock in compliance mode, or an Azure locked time-based retention policy. Target the S3 Object Lock API rather than a product, so any compatible store can serve an air-gapped install.
- Archive and prune in whole hash-chain segments, never single rows, so pruning keeps the chain verifiable (`audit-model.md` §6).
- Pruning archived segments is an operator choice. A rollback beyond the database window first restores the segment from the archive.

## Starting position

1. Do not build git-style versioning of the whole graph. DGraph has no versioned storage, so it would be a storage rewrite, and `orbId` uniqueness rules out side-by-side copies. Whole-graph snapshots already exist as published exports. Do not read DGraph at an old timestamp either: it is unsupported, unretained and has open bugs.
2. Store a full before and after per entity on every write, one event per entity — NetBox's `ObjectChange` model. Read the after-image back from DGraph rather than taking the caller's input; NetBox serialises the saved object for the same reason. Then:
   - per-item history is complete, creates included;
   - undo is a new write that restores an old image, through the normal write path (version precondition, audit, change control). History is never rewritten, as with `git revert`, PaperTrail `reify` and django-reversion;
   - "this item as of date X" is the latest event at or before X.
3. Retention: unbounded today with no growth metric (`audit-model.md`, gap 4), and full images grow the log faster. Proposal in "Retention and the 'immutable history' claim" above.
4. Revisit whole-graph versioning only on a specific need: browsing the whole graph as of a past time, or long-lived isolated workspaces.

## Design recommendations (proposed, not ratified)

**a. Where history lives — on the existing per-entity rows.** Add `version`, `action` (create, update, delete), `before`, `after` and the schema version to `audit_event_resources`, with a unique index on `orb_id` + `version`. The audit event stays the one record and each node it touched gets its own row, so this is not a second store (`AUDIT.md`, "ONE table serves both provenance and lineage"). It also closes [One audit event per entity](audit-event-per-entity.md): a multi-node save becomes one row per node. The append-only trigger already covers the table.

**b. Capturing the after-image — read back and check the version.** After the write, read the node back by `orbId` and check its `version` equals the one orbital stamped. A mismatch means a concurrent write intervened; then store `before` plus the fields this write set, marked as derived. This works however DGraph handles the mutation's payload selection; testing that is only an optimisation, to save the extra read.

**c. What an image covers — the node's own fields plus its single-reference edges as `orbId`s** (`Server.rack` as the rack's `orbId`), so a re-pointed relationship can be rolled back. Children such as `idracSettings` get their own rows; restoring a whole page save restores every row of that audit event. Today's before-image excludes relationships, so this widens it. A restore that would point an edge at a node that no longer exists is refused.

**d. A write that reaches DGraph but not PostgreSQL — record after the write, and reconcile.** Keep today's behaviour: audit writes never block or fail a request. Add a reconciler: for each node, DGraph's `version` must equal the highest version in history, and a mismatch is a gap that is logged, counted and alerted on. The same check catches writes that bypassed orbital, such as migration scripts, which a pending row written first would not. The growth metric ships with it.

**e. Rollback API — a server endpoint.** History lives in PostgreSQL, which `/graphql` cannot serve; restoring a page save must be atomic; and orbital's API-first rule says clients should not each re-implement it.

1. Read endpoints for a node's versions and for a single version.
2. Restore with a preview (the diff) first, then apply with the expected current version.
3. The write goes through the normal path: version stamp and check, an audit event marked as a restore of vN, and change control, so a gated namespace turns it into a change request.

`OCI.md`'s "revert is the client's job" covers only discarding an unpublished preview.

**f. Build order.** The target design at the top is the end state; this is only the order to build it in.

| Phase | Ships |
|---|---|
| 1. Complete history | Images on the per-entity rows, read-back with the version check, the `orb_id` + `version` index, the reconciler, the growth metric, restore recorded as a change (g) |
| 2. Rollback | The history read API, a version timeline in the UI, restore with preview |
| 3. Immutable | Hash chain, published anchors, archive to write-once storage, retention, detection of a PostgreSQL rollback. Only from here can orbital claim "immutable history" |

**g. Backup and restore — a restore is a recorded change.** Backup needs no change: it exports DGraph's current state. Restore does: it runs `drop_all`, loads the backup with `dgraph live`, and writes one `restoreBackup` audit event. Against per-entity history that breaks three things:

1. Versions go backwards. A node at v10 restored from a v5 backup gets v6 on its next write, which collides with the existing v6 history row.
2. The reconciler flags every restored node as a gap.
3. Nodes created or deleted since the backup vanish or reappear with no history.

Proposed: inside the restore job, before it releases its lease, compare each restored node with its latest history row. Where they differ, set the version to the highest recorded plus one and write a `restore` row: before is the last recorded image, after is the restored one. Record deletes for nodes the backup lacks and restore-creates for nodes it brings back, all under the `restoreBackup` event. The version bump does not trigger a publish, because the export content hash excludes `version` (`OCI.md`). This ships with Phase 1: the first restore after history is keyed by version would otherwise break the index.

Full restore stays the disaster-recovery tool; node rollback is for mistakes. A node can be rolled back from an over-broad restore like any other change.

## Open questions

- Whether an ordered per-entity event stream is enough as a change feed for downstream consumers.
- The planned audit hash chain (`audit-model.md`, gap 9) becomes more valuable once events carry full images. Confirm the content-hash proposal above before the chain is built.
- Schema drift: store `schema/VERSION` with each image, and decide whether a restore across schema versions is refused or mapped.
- Large JSON-string fields: copy them on every write, or store each value once by content hash.
- Archive: format, cadence, and whether the hash chain must ship first so archived segments are verifiable.
- Writes that bypass orbital's API: the reconciler detects them. Decide whether to also record them after the fact.
- PostgreSQL point-in-time recovery: the target design detects it (chain behind the archive) and refills from the archive, losing at most one segment. Still open: the default segment size, what alerts before the hash chain is built, and how unrecorded changes are shown to operators.
- DGraph and PostgreSQL backups run on separate schedules, so restoring both "from the same night" will not line up. Should each DGraph backup record the history position at backup time (latest audit event or chain head), so a combined restore can be checked?
- Endpoint names for the history and restore API, settled against real adopters' APIs.

## Sources

- [Infrahub: query historical data](https://docs.infrahub.app/immutable-history/query-historical-data)
- [Infrahub source: edge properties](https://github.com/opsmill/infrahub/blob/4cca2b05f327046ee40a30cc7827f49f6b95e76b/backend/infrahub/core/graph/schema.py#L216-L234) and [attribute update and delete](https://github.com/opsmill/infrahub/blob/4cca2b05f327046ee40a30cc7827f49f6b95e76b/backend/infrahub/core/query/attribute.py#L48-L102)
- [Nautobot Version Control app](https://github.com/nautobot/nautobot-app-version-control)
- [NetBox configuration: CHANGELOG_RETENTION](https://netboxlabs.com/docs/netbox/en/stable/configuration/miscellaneous)
- [NetBox `to_objectchange()`](https://github.com/netbox-community/netbox/blob/main/netbox/netbox/models/features.py)
- [Neo4j: versioning](https://neo4j.com/docs/getting-started/data-modeling/versioning/)
- [PostgreSQL 19: temporal tables](https://www.postgresql.org/docs/19/ddl-temporal-tables.html)
- DGraph maintainers on historical reads: [2018](https://discuss.dgraph.io/t/query-the-state-of-the-database-at-any-time-in-the-past/3757), [2020](https://discuss.dgraph.io/t/querying-for-historical-values-and-creation-and-modification-timestamps-as-datetime/9097)
- [DGraph change data capture](https://docs.dgraph.io/v24.1/admin/enterprise-features/change-data-capture)
- [flume/enthistory](https://github.com/flume/enthistory)
- AWS CloudTrail: [event history](https://docs.aws.amazon.com/awscloudtrail/latest/userguide/view-cloudtrail-events.html), [log file integrity validation](https://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-log-file-validation-intro.html)
- [GitHub Enterprise: streaming the audit log](https://docs.github.com/en/enterprise-cloud@latest/admin/monitoring-activity-in-your-enterprise/reviewing-audit-logs-for-your-enterprise/streaming-the-audit-log-for-your-enterprise)
- Write-once storage: [S3 Object Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html), [Azure immutable storage](https://learn.microsoft.com/en-us/azure/storage/blobs/immutable-storage-overview)
- Tamper-evident logs: [Schneier & Kelsey 1998](https://www.schneier.com/wp-content/uploads/2016/02/paper-auditlogs.pdf), [Crosby & Wallach 2009](https://static.usenix.org/event/sec09/tech/full_papers/crosby.pdf), [Trillian PostgreSQL storage](https://github.com/google/trillian/blob/master/storage/postgresql/README.md), [SQL Server ledger](https://learn.microsoft.com/en-us/sql/relational-databases/security/ledger/ledger-database-ledger), [Oracle blockchain tables](https://docs.oracle.com/en/database/oracle/oracle-database/23/arpls/dbms_blockchain_table.html), [pg_credereum](https://github.com/postgrespro/pg_credereum)
- Git's limits: [GitHub push attribution, 2026-10-12](https://github.blog/changelog/2026-10-12-push-attribution-public-preview), [gittuf design (reference state log, force pushes)](https://github.com/gittuf/gittuf/blob/main/docs/design-document.md)
