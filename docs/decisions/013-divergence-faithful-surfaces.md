# 013 — Divergence model: faithful surfaces, sticky decisions

**Status:** Draft
**Date:** 2026-06-23
**Supersedes (once finalized):** [012 — Divergence reports supersede prior resolutions](012-divergence-report-supersedes.md)

## Why this exists

ADR 012 picked a specific mechanism — supersede on identical content after Submit — to solve the inferrability bug from the June incident. That mechanism is consistent on its own terms, but it violates a deeper principle the team had not yet articulated: **orbital is a faithful medium between cloud and edge, not an autonomous arbiter of operator decisions.** Silent supersede, even when justified by a content-change rule, invisibly mutates state the operator considered settled. That is incompatible with the medium role this system is built to play.

This ADR retracts ADR 012's supersede-on-recurrence rule and replaces it with the underlying principle, the per-field signals that flow from it, and an explicit exception register.

## Base mental model

Orbital and orb are faithful surfaces. Their job is to make state observable across two human domains (cloud admin / edge admin), not to make decisions on either domain's behalf.

Four base rules govern behavior:

1. **Power lives with the actor; so does responsibility.** Cloud admin and edge admin are the decision-makers within their domains. The system stages information; humans choose actions. Authorized actors may exercise their authority fully, including in ways that consume shared resources (e.g. publishing many reports). The system records and surfaces actor behavior — including unusual patterns — but does not police it. Enforcement of responsibility is out-of-band: human-to-human, or revocation of authorization.
2. **Operations are visible.** The system never silently mutates user-visible state. Every state change is the result of an explicit admin action, OR a system action with a corresponding audit event the admin can observe.
3. **Data is presented faithfully.** Reports, resolutions, and intent comparisons are surfaced as observed. The system does not editorialize ("we think this is now resolved" / "we believe your prior decision is invalid").
4. **Cross-domain assumptions are forbidden.** Orbital cannot assume what the edge has done — loop closure is unknowable in the air-gapped model. Orb cannot assume what the cloud has decided. Each side communicates state; the other side renders it.

These are not new commitments. They are the divergence-domain articulation of orbital's existing architectural invariants ("intent-only CMDB," "cloud never executes against the edge," "orbital never in the reconciliation path," "divergence is data, not an error condition" — see CLAUDE.md). This ADR makes the implication explicit and draws out the per-field consequences.

## Per-field signals

For each `(orbId, field)` tuple, three orthogonal signals are surfaced in the UI. The admin reads all three and decides per field.

| Signal | Source | What it means |
|---|---|---|
| **Current report state** | Latest ingested report | Is the field in the current report? `intended_value`, `override_value`, `who`, `when`. |
| **Resolution history** | `divergence_resolutions` | Has any admin decided on this field? When? What action? Resolutions are sticky and persist until explicitly closed by an admin action. |
| **Stale-by-intent** | Computed at render: report.intended_value vs DGraph current intent | Has cloud intent moved since the report was published? If yes, render a hint that the report's view of cloud state is out of date. |

The system does not act on these signals. It surfaces them and lets the admin choose.

**Important caveat for the stale-by-intent signal.** "Intent has moved" is a derived claim. It tells the most recent story; forensic context (e.g., "you Accepted, then re-Rejected") lives in the audit log, not in the badge. The UI should treat staleness as a *hint*, not a verdict.

## What changes from current code

- **Resolutions become sticky.** No code path drops resolutions automatically. The Option B branch in `internal/divergenceingest/store.go::applyReport` (supersede when content matches and resolutions exist) is removed.
- **The content-differs supersede also relaxes** — see Exception 2.
- **Stale-by-intent surfaced in the UI.** Computed at render time. No new storage required if computed against the live DGraph intent on page load.
- **All admin actions are explicit and audited.** Re-decide, dismiss, and a new "close" action surface explicitly; each emits an audit event.

## Exceptions

The base principles describe ideal behavior. In practice some operational realities force the system to bend a principle. Exceptions are allowed when each is documented here with:
- The principle being bent
- The concrete operational reason the principle would produce an unusable system if strictly followed
- The forensic / observability counterweight that ensures the principle isn't fully lost

Convenience is not a sufficient reason. Exceptions accumulate; principles erode.

### Exception 1 — Tuples absent from the latest report do not remain in the active view

**Principle bent:** "Cross-domain assumptions are forbidden." Strictly read, the absence of a tuple from the latest report has ambiguous cause: (a) the edge loop closed and the divergence really is gone; (b) cb-agent had a transient observation gap; (c) edge admin manually reverted; (d) producer code path skipped the field. Orbital cannot distinguish these from the wire alone.

**What we do instead:** When a tuple is in `divergence_entries` but absent from the latest ingested report, it is removed from the active divergence view. The corresponding `divergence_resolutions` row, if any, persists.

**Operational reason:** Without this, the page accumulates rows for every divergence ever observed on a DC. The active view becomes unactionable.

**Counterweight:**
- The audit log records the absence event explicitly (one audit event per ingest, listing dropped tuples and the publishedAt that triggered it).
- The S3 archive holds every report ever published until retention pruning, so a forensic reconstruction is possible.
- The sticky resolution row, if any, remains queryable — "did the admin ever decide on (orbId, field)?" is answerable even after the tuple drops from the active view.

### Note on what is NOT an exception

**"Show the latest report as primary; surface the queue, don't bury it."** Initially this looked like an exception ("skip to the latest report when a queue exists") but on reflection it is a refinement of the principle, not a bending of it. The base rule is "data is presented faithfully" — not "all data must be presented as primary." Faithful presentation under a queue of N reports has two requirements:

1. **The latest report is the primary surface.** It IS the current truth; older reports in the queue describe stale edge state by definition. Forcing the admin to step through N reports to reach current state is operationally untenable.
2. **The queue itself is visible.** When N > 1, the UI tells the admin "orb has published M reports since your last view" with an affordance to view and act on the older ones if they choose. Hiding the queue would erode admin power; surfacing it preserves the principle.

So the system focuses the admin on current truth but does not hide the history. The older S3 files remain in storage and are accessible from the UI (not just queryable by an auditor). The admin still has the option to drill into any prior report and act on it.

Distinguishing "refinement of the principle" from "exception to the principle" matters because mislabeling trains the team to invoke "exception" loosely.

## Acceptance test

> A cloud admin opens `/divergence-reports` and sees a row. They must be able to:
> (a) Read the current report state, the prior decision history, and any stale-by-intent indicator — all from on-screen content.
> (b) Choose an action explicitly (Accept, Reject, Ignore, Close, or Dismiss).
> (c) Know that no future report ingest will silently undo (b) without their explicit re-decision.

If the implementation produces a screen where a row's decision state changes without an admin action and without a visible audit-derived explanation, this ADR has failed.

## Open questions

1. **Resolution closure UX.** How does the admin explicitly close a sticky resolution? A "Close" button on each resolved row? Bulk close per DC? Implicit closure on tuple-absent (Exception 1)?
2. **Long-tail retention.** Months/years of sticky resolutions accumulate per DC. Per-resolution TTL? Admin-driven archive only? Open until decided.
3. **Re-decide flow.** If the admin changes their mind, do they Close the prior resolution and create a new one (two audit events), or edit-in-place (one event)? Audit trail differs.
4. **Stale-badge attribution.** The badge says "intent has moved since publish" but not *why* (Accept dispatch? manual cloud admin edit? Reject does not move intent). Worth surfacing context, or keep the badge simple?
5. **Current intent source.** For the stale check, do we read DGraph blue (live) or the export view used by the bundler pipeline? Probably blue; worth being explicit so the implementation does not drift.

## Migration

Deliberately empty until the model is ratified. Implementation plan will follow once the principle, exceptions, and acceptance test are agreed.

## References

- Supersedes ADR 012 once finalized.
- Aligns with the "intent-only CMDB" goal and the "orbital is never in the reconciliation path" deployment invariant (CLAUDE.md).
- Builds on the cursor-persistence fix (2026-06-23) that addressed pod-restart re-ingest, which was a separate bug compounding the design hole this ADR closes.
