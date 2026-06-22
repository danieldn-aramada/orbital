# 012 — Divergence reports supersede prior resolutions

**Status:** Proposed
**Date:** 2026-06-21

## Context

The `/divergence-reports` page promises *"latest snapshot orb published."* That's a snapshot semantic: the page shows one moment in time, and decisions on that moment are either present or absent. The persistence layer doesn't match. Today, resolutions persist across snapshots, gated by a value-based MVCC staleness check that asks DGraph whether the field's current intent still matches the operator's chosen outcome. The check is correct in isolation but invisible to the operator — load-bearing logic the UI never discloses.

Today's incident made this concrete. A DC had two divergence entries on the same iDRAC, decided in one batch (`ipmiEnabled` Accept, `sshEnabled` Reject). A later cloud-side edit reverted `ipmiEnabled` to `false` while leaving `sshEnabled` untouched. orb republished the same divergence content. The page rendered:

- `ipmiEnabled` → pending (Accept silently dropped: value moved)
- `sshEnabled` → still "rejected" (Reject still effective: value unchanged)

Two semantically identical rows. Different decision states. No on-screen reason. **A dev user cannot infer this.** And no amount of explanation copy fixes it without re-introducing the cross-report state that violates the snapshot promise.

## Principle

**The page promises single-report semantics. The persistence layer must match.**

A snapshot is whole. Decisions on a snapshot do not survive a new snapshot. If orb publishes a report whose content differs from the prior, orbital ingest is a supersede event — every prior resolution for that DC is dropped, and every entry in the new report is pending.

## Decision

Adopt **report-supersedes** semantics. Resolutions are scoped to the lifetime of a specific report ingest; they end when orbital ingests a content-differing report.

"Content" is the set of `(entry_orb_id, field, override_value)` tuples on the report. If the set is identical to what's already in `divergence_entries` for that DC, the ingest is a no-op for state (only `last_seen_at` / `last_report_published_at` advance). If the set differs in any way — added, removed, or value-changed tuples — the ingest atomically drops all `divergence_entries` and `divergence_resolutions` for that DC and inserts every tuple from the incoming report as fresh pending.

The MVCC value-based staleness check is removed. `intended_at_version` is removed from both `divergence_entries` and `divergence_resolutions`. Accept's optimistic-concurrency guard (the one legitimate non-UI consumer of the version anchor) is removed; Accept becomes last-writer-wins against orbital DGraph, on the basis that concurrent cloud-admin races on the same field at the same second are rare enough to live with, and any such race is detectable in the audit log post-hoc.

## Precondition

orbital's ingester is the authority for content comparison. **orb is allowed to be dumb** — it can publish identical content repeatedly without harm. orbital reads each incoming report, compares against the current `divergence_entries` set for the DC, and decides supersede-or-no-op locally. No coordination with orb is required; no orb-side change ships with this ADR.

(orb-side dedup remains a future optimization for S3 bandwidth, not a correctness requirement.)

## Acceptance test

> A dev opening `/divergence-reports` and seeing two rows of the same Config Item must be able to explain every difference in the Decision column from on-screen content alone, OR there must be no difference.

If the implementation produces a screen where two identical-looking rows have different decision states without on-screen explanation, this ADR has failed.

## Migration

**The behavioral change lives in the ingester.** `internal/divergenceingest/` becomes responsible for set comparison and transactional supersede:

1. Load incoming report → compute set of `(entry_orb_id, field, override_value)`
2. Load existing entries for the DC → compute same set
3. Set equal → touch `last_seen_at` / `last_report_published_at`, return
4. Set different → in one transaction: delete all DC entries (resolutions cascade), insert all tuples from incoming report, emit audit event `supersedeDivergenceReport` with counts

Atomicity is required: a partial supersede leaves orbital in a worse state than no supersede. Use a single ent transaction with rollback on any error.

Everywhere else, the change is deletion:

- `internal/handler/divergence.go::List` — remove the value-match closure, the cache wrapper, and the stale-check block. Resolutions render as-stored.
- `internal/handler/ui.go::DivergenceReports` — same removals.
- `internal/handler/divergence.go::dispatchAcceptMutation` — remove the `intended_at_version` guard at the top of the function; remove the version-bump from the mutation's `set` clause (orbital's general version-bump policy on mutations handles this).
- `fetchAndCompareCurrentValue` package function — delete.
- `internal/handler/divergence.go::isStale` and `Dismiss`'s isStale check — delete; Dismiss becomes a straight delete of the entry + cascade of the resolution.
- ent schema: remove `intended_at_version` from `divergenceentry` and `divergenceresolution`. Migration drops the columns.

The action-filter path in `List` reduces to a SQL filter on `resolution.action`. No DGraph calls, no caching.

## Trade-offs

**Won:**
- UI fully inferrable from rendered output. Today's bug cannot recur.
- ~150 LOC of MVCC plumbing deleted across List, DivergenceReports, ingest, Accept dispatch.
- Mental model collapses to one sentence and matches the page's stated promise.
- Bundler-feed and operator-feed surfaces become identical SQL queries.

**Lost:**
- Operator may re-decide a field they previously decided if any sibling field's divergence content changes. Friction is bounded because divergence reports per DC are small (typically <10 entries) and stable in steady-state.
- Forensic history of operator decisions across past reports lives only in the audit log, not in `divergence_resolutions`. Audit log is the correct home.
- Accept dispatch becomes last-writer-wins under concurrent cloud-admin edits to the same field. Mitigation: rare; audit trail surfaces post-hoc.

**Rejected alternative — partial supersede.** Keep resolutions for `(orbId, field)` tuples that exist in both old and new reports with the same override value; wipe only the rest. Faster operator workflow, but **reintroduces the inferrability bug** under a different rule ("why did this row survive but not that one?"). The principle either holds or it doesn't. If full-supersede operator friction proves unacceptable in dogfooding, the correct response is tightening orb's publish discipline (so identical-content reports don't ship), not smuggling hidden cross-report state back into orbital.

## References

- Today's incident: `colo:CFRHDX3-idrac` Accept(ipmiEnabled) + Reject(sshEnabled), with cloud-side edit between report cycles.
- Affected source: `internal/handler/divergence.go`, `internal/handler/ui.go::DivergenceReports`, `internal/divergenceingest/*`.
- Existing memory notes on divergence semantics and MVCC architecture are partially superseded; the user maintains memory directly.
