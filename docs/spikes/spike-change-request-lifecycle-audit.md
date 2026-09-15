# Change-request lifecycle events in the audit log

**Status:** Not started — open question under investigation.

*Moved out of `docs/planning/backlog.md` 2026-09-15: the entry had grown past what a
tracker line can carry. The backlog keeps the question; this doc keeps the reasoning.
Fold decisions into the relevant `docs/reference/<DOMAIN>.md` when this closes, then
delete this file.*

---

**The question:** Should the change-request lifecycle (create / amend / approve / reject / close / merge) emit `audit_events`, so a namespace's review history is answerable without opening every request one at a time?

**What is audited today:** the writes a merge produces (every item goes through `DispatchMutation`, so the *effect* lands in the audit log like any mutation — `changerequest_merge.go:246`), approval-policy create/update/delete with `before`/`after`, bypass writes (`details.privileged` + `details.bypassedPolicy`), and authn/authz failures. **What is not: the lifecycle itself.** It lives in the approval tables — `approval_requests.status` + `updated_by`, one `approvals` row per reviewer (with `approved_at_hash`), one `merge_attempts` row per execution — and surfaces only through `GET /api/v1/change-requests/:id`. So *"what happened to colo-3"* is answerable; *"show me every review action in colo last month"* is not. **Per-request is the right primary view and matches the category** — NetBox and GitHub both stop there, GitHub's timeline being per-PR rather than in the org audit log — so this is not self-evidently a bug. **The question that decides it:** must a compliance reviewer reconstruct a namespace's review history without opening each request? If yes, the lifecycle emits `management` events alongside the table rows; the `details` jsonb already fits and the resource id is the namespace, exactly as policy admin does.

**Settled 2026-09-02 and NOT part of this entry:** a policy refusal (`403 APPROVAL_REQUIRED`) writes no audit row — business-logic refusals and technical failures are app-log only, per `AUDIT.md` § Row admission. An earlier version of this entry called that an inconsistency with `authorizationDenied` and said to fix it regardless; that was wrong. The two criteria are *state changed* and *security event*, and a policy refusal is neither — the caller has the role, the workflow says "not yet".

**Two gaps that remain even if the lifecycle stays out of the audit log:** (1) **amend leaves no trace of what it replaced** — `SetPayload` overwrites and `updated_by`/`updated_at` are single-slot, so a request amended three times shows only the last actor and the current changeset. Partly mitigated: amend recomputes `base_hash`, which stamps existing approvals stale, so no approval silently survives a payload swap — but *"was this materially changed after Alice reviewed it"* is still unanswerable. (2) **reject and close carry the actor only in `updated_by`** — durable in practice since nothing follows a terminal state, but a slot rather than an event. Both are cheap to close inside the approval tables (an append-only `approval_request_events` child table) without touching `audit_events` at all, which is likely the right first move if either is wanted.

**If the audit-log half is built, do NOT duplicate state:** the approval tables stay authoritative for status and approval counts; events record transitions only. And share the shape with the advisory-enforcement entry above — both need "a policy decision worth recording", and two sessions inventing two shapes for it is the obvious failure mode.
