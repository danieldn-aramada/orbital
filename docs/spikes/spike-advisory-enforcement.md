# Advisory enforcement for approval policies

**Status:** Not started — open question under investigation.

*Moved out of `docs/planning/backlog.md` 2026-09-15: the entry had grown past what a
tracker line can carry. The backlog keeps the question; this doc keeps the reasoning.
Fold decisions into the relevant `docs/reference/<DOMAIN>.md` when this closes, then
delete this file.*

---

**The question:** Should an approval policy have a middle state — evaluate and record, but do not block — so a new policy can be rolled out without risking a lockout?

**Every comparable engine has one and orbital does not:** Kyverno `Audit`, Gatekeeper `dryrun`, HCP Sentinel `advisory`, GitHub rulesets `Evaluate`. Orbital's policies are **Active | Disabled** only (deliberately, 2026-08-30 — two states first, simplest thing that works). **The gap it leaves:** there is no safe way to introduce a policy on a busy namespace and see what it *would* have refused before turning it on. **The work is not the third state, it is the recording** — advisory is indistinguishable from disabled unless a would-have-been-blocked write is written somewhere an operator can read it. Kyverno writes a PolicyReport; orbital would write an audit event, which fits the existing `details` jsonb and the `privileged` precedent (`details.wouldHaveBlocked` + the policy's namespace, which identifies it — one policy per namespace). **Then** the UI's segmented control grows a third segment and `CHANGE-CONTROL.md` loses its "no middle state" caveat. **Do not** reintroduce a global enforcement flag to approximate this — enforcement belongs on the policy, which is what the category does and what `TestApprovalPolicy_DisablingStopsGatingAndKeepsTheRow` pins.

**The all-namespaces policy raised the stakes here** *(carried over 2026-09-14 from that entry, which shipped and was removed).* Enabling a global protects every namespace the instant it is saved — including data centers onboarded next month — and there is no dry run to show what it would have refused first. A global is exactly the policy an operator would most want to trial in advisory mode, and it is the one where guessing wrong locks out the most people.
