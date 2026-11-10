# Spike 35 — Full history, undo and change feed

**Status:** Not started — open question under investigation. The starting position below comes from a discussion on 2026-11-05 and is not a decision.

*Moved out of `docs/planning/backlog.md` 2026-11-08: the entry had grown past what a tracker line can carry. Fold decisions into `docs/reference/AUDIT.md` when this closes, then delete this file.*

---

**The question:** Should orbital keep a complete history of every ConfigItem? If so, as git-style versioning of the whole graph, or as a complete audit log?

## Already decided

- Branching. A branch is a stored changeset, not a copy of the graph (`change-control-research.md`, addendum 2026-08-26). Real branches only if long-lived, isolated workspaces are needed.
- One history store. The audit log serves both provenance and lineage (`AUDIT.md`, "ONE table serves both provenance and lineage").
- Revert is the client's job. Orbital exposes no revert endpoint (`OCI.md`).
- Whole-graph snapshots exist. Every published export is a signed, versioned, full snapshot, and export compare diffs two of them.

## The gap

`AUDIT.md`: orbital is "NetBox-shaped on row admission and NOT on payload". An event stores what the caller sent as the after-state and a flat before-image. So:

1. A create has no before, so it has no `changes`.
2. An event covering several items cannot be attributed per item. This overlaps with [One audit event per entity](spike-audit-event-per-entity.md).
3. Nothing answers "what did this item look like on date X" without replaying partial inputs.

## How comparable tools handle history

| Tool | History | Git-like versioning |
|---|---|---|
| NetBox | Changelog with a full before and after copy per change; kept 90 days by default (`CHANGELOG_RETENTION`), configurable to forever | Branch, diff, merge and revert through the netbox-branching plugin (source-available license) |
| Nautobot | Changelog like NetBox's | A git-semantics app on Dolt; no commits since September 2024, docs moved to Nautobot's archive site |
| InfraHub | Every value is timestamped; any query takes `at=<time>` and returns the graph, schema included, as it was | Copy-on-write branches; time travel is built into its storage model |
| Terraform, Argo CD | Git is the store | By construction; the model where git holds config, which orbital is not |

Full before/after changelogs are standard. Whole-database versioning is rare: InfraHub designed its storage around time from the start, and the one retrofit (Nautobot on Dolt) went dormant.

## Starting position

1. Do not build git-style versioning of the whole graph. DGraph has no versioned storage, so it would be a storage rewrite, and `orbId` uniqueness rules out side-by-side copies. Whole-graph snapshots already exist as published exports.
2. Store a full before and after per entity on every write, one event per entity — NetBox's `ObjectChange` model. Then:
   - per-item history is complete, creates included;
   - undo is writing the before back;
   - "this item as of date X" is the latest event at or before X.
3. Decide retention here. It is unbounded today with no growth metric (`audit-model.md`, gap 4), and full images grow the log faster.
4. Revisit whole-graph versioning only on a specific need: browsing the whole graph as of a past time, or long-lived isolated workspaces.

## Open questions

- Where the full images live: in `audit_events.details`, or a separate table keyed by entity and `version`.
- Whether `orbId@version` becomes the key for as-of reads.
- Whether an ordered per-entity event stream is enough as a change feed for consumers such as AEP.
- Undo against a later edit: is it a new change request, and how does it conflict-check?
- The planned audit hash chain (`audit-model.md`, gap 9) becomes more valuable once events carry full images.

## Sources

- [Infrahub: query historical data](https://docs.infrahub.app/immutable-history/query-historical-data)
- [Nautobot Version Control app](https://github.com/nautobot/nautobot-app-version-control)
- [NetBox configuration: CHANGELOG_RETENTION](https://netboxlabs.com/docs/netbox/en/stable/configuration/miscellaneous)
