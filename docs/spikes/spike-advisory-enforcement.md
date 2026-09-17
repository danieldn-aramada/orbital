# Advisory enforcement for approval policies

**Status:** Not started — open question under investigation. **The "verdict: yes" was
withdrawn 2026-09-17**: its two stated premises were tested against the code and both
failed. The question is still open, but as ergonomics, not as a safety gap.

*Moved out of `docs/planning/backlog.md` 2026-09-15: the entry had grown past what a
tracker line can carry. The backlog keeps the question; this doc keeps the reasoning.
Fold decisions into the relevant `docs/reference/<DOMAIN>.md` when this closes, then
delete this file.*

---

**The question:** Should an approval policy have a middle state — evaluate and record, but do not block — so a team can see what *would* be gated before turning it on?

**Every comparable engine has one and orbital does not:** Kyverno `Audit`, Gatekeeper `dryrun`, HCP Sentinel `advisory`, GitHub rulesets `Evaluate`. Orbital's policies are **Active | Disabled** only (deliberately, 2026-08-30 — two states first, simplest thing that works). **Do not** reintroduce a global enforcement flag to approximate a middle state — enforcement belongs on the policy, which is what the category does and what `TestApprovalPolicy_DisablingStopsGatingAndKeepsTheRow` pins.

## What the premises claimed, and what the code says — tested 2026-09-17

**"…without risking a lockout." A lockout is not possible.** Policy administration is `adminAPI` → PostgreSQL (`server.go:600-602`) and never passes through the graph gate; `CHANGE-CONTROL.md` § "Enforcement is per POLICY — there is no global on/off" states it ("the lever is always reachable") and `TestApprovalPolicy_DisablingStopsGatingAndKeepsTheRow` pins it. **Disable is always one click.** So the cost of enabling a policy and finding it wrong is not an outage — it is some writes taking a `403` that names the policy and says approval is required, to a caller who is present, holds the error, and can propose a change request instead. That is the feature working.

**This is the load-bearing difference from the precedents above.** Kyverno and Gatekeeper sit in a cluster's admission webhook, where a bad policy wedges deployments for everyone and the recovery path may itself be gated — which is why a dry-run mode is table stakes *there*. Orbital's off-switch sits outside the gate by construction, and the blast radius is bounded to config mutations that fail loudly and reversibly. **Do not cite the category precedent without carrying this distinction with it.**

**"The work is not the third state, it is the recording." The recording already exists.** `approval_gate.go:152` logs every refusal with `policy`, `actor`, `role`, `types` and `orb_ids`, written at the chokepoint so it covers `/graphql`, `DispatchMutation` and cascade-delete from one place, and pinned by `TestGate_RefusalIsLoggedOnceAndNamesTheEntity`, `…FromTheInternalDispatchPathToo` and the negative `TestGate_AllowedAndBypassedWritesLogNoRefusal`. "Turn it on, watch for ten minutes, turn it off" is already an instrumented experiment.

**"No safe way to see what it would have refused." The retrospective version needs no new feature.** `GET /api/v1/audit-log?namespace=<ns>&resource_type=<T>&since=…&until=…`. Every gated write is a mutation, every committed mutation is audited, and `resourceTypes` comes from `extractOperations` — the same type vocabulary a policy's `types` uses. *"What would this policy have caught last month"* is a filter over data that already exists, available immediately rather than after a soak period.

## What advisory mode would still add

Stated fairly, because the question is not closed:

- It is a button rather than a hand-built audit-log filter.
- It applies the policy's `bypass_roles` for you — the audit-log method leaves the operator to reason about who would have been exempt.
- For an **all-namespaces** policy it is one report rather than N per-namespace queries.

**So it is a convenience over an existing capability, not a missing safety mechanism** — which is the reclassification, and the reason this should not be ranked ahead of work that closes something genuinely absent. If it is built, the recording is an audit event (`details.wouldHaveBlocked` + the policy's namespace) under the exception already carved in `AUDIT.md` § Row admission: a would-have-been-blocked write has no caller reading the result, which is the asymmetry that earns the row.

## What the earlier framing got wrong, so it is not rebuilt

The claim that the all-namespaces policy "raises the stakes" rested entirely on the lockout premise — a global covering data centers onboarded next month is real, but the recovery is the same one click, and the refusal log names every entity it blocked. **The stakes are that more people see a `403` for longer, not that anyone is locked out.** Keep the distinction: it is the difference between a feature that must exist before the global is enabled, and one that makes enabling it more comfortable.
