# Should a data-center source-of-truth have a built-in approval mechanism?

> **Evidence companion to [`CHANGE-CONTROL.md`](./CHANGE-CONTROL.md)** — start there for what orbital actually does. This is the landscape research behind the *decision to build it at all*: public vendor docs, adoption signals, and how comparable systems (InfraHub, NetBox, ServiceNow, Terraform Cloud, Vault, GitHub) solve the same problem.
>
> **Kept because "why did we build this rather than buy or skip it" is a question that comes back**, usually from someone new, and reproducing the survey is expensive. Point-in-time as of 2026-08-26 — treat adoption numbers and vendor feature claims as dated. The **decisions** it informed are settled and live in `CHANGE-CONTROL.md`; nothing here is normative.

**Purpose:** Decision-support research for orbital (graph-native, API-first CMDB / source-of-truth for modular data centers). Question: *given orbital's product category, SHOULD it build change-approval (maker-checker: user A proposes a config change, it must be approved by user B before it becomes authoritative intent) into the product itself, or delegate it?*

**Provenance:** Compiled 2026-08-26 from four parallel background research agents, each covering one comparable category against the same six-question rubric (built-in vs delegated? enforced at the store's write-path or advisory/upstream? staged pending-state or commit-then-audit? fixed vs pluggable workflow? two-person/M-of-N? primary-doc citation). Kept OUTSIDE the orbital repo intentionally.

**Status:** Research/decision-input. NOT a ratified decision. Contradicts one point of the recent Spike 31 decision (see Provenance note at end).

---

## Bottom line up front

**Yes — the capability belongs in orbital's category and fits its architecture — but the evidence sharpens *what kind* and *where*.**

- Orbital's two closest analogs (NetBox, InfraHub) are converging on built-in, *enforced* proposed-change review, and they share orbital's defining trait: a **single API write-path** (a real chokepoint).
- The enterprise CMDBs that do NOT gate their store (ServiceNow, Device42) don't because they became **discovery-*populated*** (many write paths: Discovery, IRE, imports) — a *deviation* from the ITIL definition, in which a CMDB **is** the authorized/intended config and discovery is a *separate* drift-check run against it. Orbital kept the intent model, so it **can** gate. (Corrected + widened — see the 2026-08-29 addendum below.)
- The single most consistent finding across all four categories: **an approval gate is only real if it sits at the layer every write passes through.** For orbital that is its GraphQL mutation API — not a client (AEP), because orbctl and third-party clients also write.
- Caveat: everyone who ships enforced review **monetizes it as a premium tier** → it is a significant investment, not a bolt-on. So the real decision is *timing/positioning*, not feasibility.

---

## Evidence summary table

| Product | Category | Built-in? | Gate on the *store's* write path? | Staged (proposed state)? | Proposer≠approver? |
|---|---|---|---|---|---|
| **InfraHub** (OpsMill) | graph-native SoT — direct analog | Core mechanism | Yes (Enterprise); conflict-detection always on; Community bypassable | Yes — git-like copy-on-write branches | Yes — self-approval blocked; audited break-glass |
| **NetBox** | DCIM SoT | Core=audit only; paid Enterprise "Change Management" | Yes (Enterprise) — "cannot merge without approved CR, regardless of protect_main" | Yes — via netbox-branching | Yes — reviewer groups + min approvals |
| **Nautobot** | DCIM SoT | Core "Approval Workflows" (3.0+) | No — only Job execution, not direct Device/Site writes | No — approves the trigger, not the data | Configurable min_approvers |
| **iTop** | ITIL CMDB | Core (Simple + ITIL change) | No — ticket workflow around the CMDB | No | Single-approver core; M-of-N is paid |
| **ServiceNow** | ITSM CMDB | First-party module | No — approval on separate change_request; CMDB commits via IRE; "Unauthorized Change" = post-hoc detection | No | Yes (on the change-request object) |
| **BMC Helix CMDB** | ITSM CMDB | Module + Sandbox dataset | Partial — Sandbox stages CI edits, but documented self-service promote, not confirmed two-person | Yes (Sandbox) | Yes (change side only) |
| **Device42** | pure CMDB | None | No — commit-then-audit only | No | No |
| **Terraform Cloud / Spacelift / env0** | IaC run-plane | Yes — apply gate + policy-as-code | Yes — at their own apply chokepoint | Yes (plan) | Only Spacelift documents it |
| **Atlantis / Flux / Argo CD** | GitOps | No — delegate to Git PR | Enforced only if the tool is the sole path (Argo CD RBAC bypassed by direct kubectl to CRDs) | Varies | Via VCS |
| **Vault Control Groups** | enforcement primitive | Yes — M-of-N | Yes — Vault core API, client-agnostic | Request held (wrapping token) | Yes, incl. cross-group |
| **K8s admission / Gatekeeper** | enforcement primitive | Gate primitive | Yes — apiserver, all clients | No (allow/reject; can check for approval evidence) | Policy-defined |
| **GitHub/GitLab branch protection** | enforcement primitive | Yes — required reviews | Yes — server-side merge API | Yes (PR) | Yes (enforced; bypassable for direct-push-privileged) |
| **AWS SSM Change Manager** | enforcement | Yes — M-of-N levels | Weak — gates one invocation path, not the resource store; closed to new customers Nov 2025 | — | Yes |

---

## Category 1 — DCIM / network sources-of-truth (orbital's closest analogs)

### NetBox — three layers, do not conflate
- **Core (OSS):** Change Logging + Journaling = audit, NOT approval. Commit-then-audit. A peer-review feature request (github.com/netbox-community/netbox issue #11942) was **closed "not planned"** — core maintainers deliberately declined it.
- **netbox-branching plugin:** official (NetBox Labs), but **"NetBox Limited Use License 1.0"** (source-available, NOT OSI open source). Git-like branches/diff/merge/revert as first-class REST ops. **No review gate by itself.** **Verified 2026-08-28:** correct for the released branch — `main`'s `LICENSE.md` is titled "NetBox Limited Use License 1.0". Note the licence is **in flux**: `develop` currently carries **PolyForm Shield License 1.0.0** instead. Both are source-available with a noncompete and neither is OSI-approved, so the characterisation holds either way. **The noncompete is directly relevant to orbital**, not just a licensing footnote — `main`'s text withholds the right to use the software "to provide a managed service or software products that includes, integrates with, or extends NetBox in a way that competes with any product or service of NetBox Labs." Orbital is a data-center source of truth in NetBox's own category, so **adopting netbox-branching as a dependency is very likely foreclosed regardless of engineering fit** — which independently reinforces the build-it-ourselves verdict.
- **NetBox "Change Management":** official, **paid, Enterprise/Cloud only**. Enforced: *"A branch cannot be merged unless it has an approved change request. This check is enforced regardless of the protect_main setting."* Configurable reviewer groups + min approvals; roles (Change Managers vs Change Reviewers); staleness detection. **Verified 2026-08-28 against primary sources:** NetBox Labs docs state Change Management "is exclusively available in NetBox Enterprise and NetBox Cloud," and that "merging a branch always requires an associated change request in Approved status… enforced regardless of the `protect_main` setting." Stronger than "currently paid": the community request to bring it to the OSS core ([netbox-community/netbox#19694](https://github.com/netbox-community/netbox/issues/19694), filed 2025-06-11) was **closed as "not planned"** — NetBox Labs has explicitly declined to move the gate into the free tier.
- **netbox-branch-review:** independent community plugin (MIT) that reverse-builds the review gate on top of netbox-branching for OSS users.
- Cites: netboxlabs.com/docs/developer/plugins-extensions/changes/ ; netboxlabs.com/docs/changes/models/review/ ; pypi.org/project/netbox-branch-review/

### Nautobot
- Core "Approval Workflows" (3.0+), generic `ApprovableModelMixin` — model-agnostic in principle, **but only `ScheduledJob` uses it in core** today. So it gates **Job execution** (incl. the bulk-edit/bulk-delete jobs the UI invokes), NOT a direct PATCH to a Device via API/GraphQL. Configurable multi-stage (`ApprovalWorkflowDefinition` → stages with `approver_group` + `min_approvers`). Self-approval blocking not addressed in docs.
- Golden Config app: config-plan diffs for device config can route through approval before execution — downstream consumer of the same mechanism, not review on the data model.
- Cites: docs.nautobot.com/projects/core/en/stable/user-guide/platform-functionality/approval-workflow/ ; source `nautobot/extras/models/approvals.py`

### OpsMill InfraHub — the direct analog
- **Storage: Neo4j** (Bolt/Cypher; optional Memgraph) — *not* orbital's DGraph, but **both are graph-native** (the shared axis; both sit apart from the relational CMDBs). Its branching is **application-layer copy-on-write on Neo4j**, *not* a database feature — neither Neo4j nor DGraph has native branching. **Why orbital's mechanism differs:** DGraph's globally-unique `@id` + lack of cheap branch-scoping make *in-graph* branches costly, so orbital uses a **Postgres changeset** for the same concept (see the branching addendum). Same model, different engine, engine-adapted mechanism.
- Core, central to product identity. Proposed Changes ≈ GitHub/GitLab PRs. Branches = copy-on-write graph snapshots (delta-only vs branched_from).
- **Enforcement split by edition:** Community has the *mechanism* (branches, diff, comments, approve/reject) but **no built-in enforcement** — a direct `infrahubctl branch merge` bypasses review unless you strip `global:edit_default_branch` + branch-merge perms via RBAC. Enterprise adds hard gates: `INFRAHUB_POLICY_REQUIRED_PROPOSED_CHANGE_APPROVALS=<n>` and `INFRAHUB_POLICY_REVOKE_PROPOSED_CHANGE_APPROVALS`, enforced at the GraphQL/API layer (client + automation), not UI-only.
- **Conflict detection is ALWAYS enforced** (both editions) at the merge/data layer.
- **Self-approval explicitly blocked:** *"you cannot approve your own changes."* Audited **break-glass**: Super Admins can self-approve / bypass / edit main directly.
- Lifecycle: Draft → Open → Approved → Merged (or Closed). Default main branch enforces stricter integrity than feature branches; full validation at merge.
- Cites: docs.infrahub.app/topics/proposed-change ; opsmill/infrahub repo docs/docs/proposed-changes/*.mdx , branches/*.mdx , change-approval/change-approval-workflow.mdx

### Ralph — none (asset lifecycle "Transitions" ≠ change approval).
### iTop — core OSS "Simple Change Management" + "ITIL Change Management" (CAB/ECAB), but it's a **ticket workflow layered around the CMDB**, no data staging; the CMDB write isn't gated at the data layer. Core = single-approver; real M-of-N is the paid "Change approval light" extension.

**Category verdict:** Built-in proposed-change review is **becoming the differentiator, not yet the norm**. The two graph-native, API-first tools (NetBox, InfraHub) — orbital's closest shape — are converging on **git-style branch-and-merge with peer review baked into the store, enforced at the write layer**. But it's immature and **monetized as premium** by every vendor shipping it (NetBox Enterprise-only; InfraHub enforcement Enterprise-only; Nautobot only wired to jobs).

---

## Category 2 — Enterprise ITSM CMDBs

### ServiceNow
- Change Management is first-party — but approval lives on a **separate `change_request` object**, NOT on the CMDB. The CMDB write path (Discovery, IRE, Import Sets, manual edit) **commits immediately** based on data-source trust/recency; the IRE has no approval gate.
- The optional **"Unauthorized Change"** add-on is **detective, not preventive**: a business rule fires *after* a `cmdb_ci` insert/update, checks for an approved Change Request, and raises a post-hoc event/record if missing. The write already happened.
- Configurable Change Approval Policies; two-person/CAB on the change-request object (not on the CI record).
- Cites: servicenow docs change-approval-policy ; unauth-change-properties ; ServiceNow IRE docs.

### BMC Helix CMDB
- Change Management module (Approval Server, configurable phases) = same "approval on a workflow object" pattern.
- **Sandbox dataset:** CI edits land in a per-user sandbox first, then "Promote" merges to production via the Reconciliation Engine. Genuine staging — but docs describe it as **self-service** (concurrency/safety), NOT confirmed as a two-person control. Vendor marketing paraphrase ("restrict updates to approved changes") not backed by primary docs found.
- Cites: docs.bmc.com sandbox-datasets-and-the-promotion-process ; change-management-approval-process.

### Device42 — pure-play CMDB
- **No approval concept.** All writes (UI, API, autodiscovery, import) are unconditional; everything is recorded in Audit Logs (commit-then-audit). Change management explicitly delegated to external ITSM (ServiceNow/Jira/Zendesk).

**Category verdict:** Built-in approval is the norm in ITSM suites — **but never as a gate on the CMDB data store.** Approval lives on a separate change-request object; the store commits immediately; violations caught post-hoc. This is a **consequence of CMDBs having many uncontrolled write paths**, not a design ideal. Precedent for a graph CMDB: fast ungated store + strong audit/diff/reconciliation, approval delegated — *unless you can actually gate the write path.*

---

## Category 3 — IaC / GitOps control planes

- **Terraform Cloud / Enterprise:** apply approval is core ("plans require confirmation before apply"), enforced at TFC's own apply chokepoint (UI/CLI/API all hit the same run-state gate; auto-apply is an explicit server-side opt-out). Plan artifact = staged. Policy-as-code (Sentinel/OPA) is a second hard-block gate. **No native proposer≠approver.**
- **Spacelift:** "Approval Policies" (Rego) gate run state transitions (UNCONFIRMED → won't schedule until approved), server-side. **Documents "Preventing self-approval" as a first-class pattern.**
- **env0:** native approval policies (OPA), enforced at deploy/destroy chokepoint (not on tasks/PR-plans). Self-approval = DIY policy.
- **Atlantis:** no native approval — delegates to VCS PR review ("approved by at least one person other than the author"). Enforced at Atlantis's apply command, but the fact lives in Git; bypassable by applying outside Atlantis.
- **Argo CD:** no dedicated approval — RBAC + Sync Windows. **Critical seam:** Application/AppProject are K8s CRDs; anyone with kubectl to those objects **bypasses Argo CD's RBAC entirely** (governed only by K8s RBAC).
- **Flux / Crossplane:** pure reconcilers; no approval; gating must be upstream Git PR + K8s admission.

**Category verdict:** Two families. Git-native tools delegate approval to Git PR and build no native primitive — but that guarantee **"is only as strong as Git being the sole write path"** (Argo CD CRD bypass is the cautionary case). Commercial run-planes built approval into the apply state machine. Because orbital's write path is its **own GraphQL mutations (not Git)**, the Terraform-Cloud/Spacelift family (enforce at your own chokepoint) is the apt precedent — not delegate-to-Git.

---

## Category 4 — Enforcement primitives / M-of-N patterns

- **HashiCorp Vault Control Groups (Enterprise):** M-of-N human approval before a request completes. Gated request returns a response-wrapping token; completes only after N authorizers approve via API. Enforced at Vault core / HTTP API → client-agnostic (CLI/Terraform/SDKs are all API clients). Declarative config: ACL `control_group` block naming factor groups + `approvals = N`; multiple factors stack (cross-team two-person). **Closest architectural sibling to a store-level gate.** (Open Q: interaction with root tokens/sudo not confirmed.)
- **K8s admission (ValidatingAdmissionWebhook / ValidatingAdmissionPolicy CEL):** enforced at kube-apiserver after authn/authz, before etcd persist; applies regardless of client; writes only (reads bypass). VAP CEL *can* enforce "write must carry approval annotation/token" (a gate), but cannot *conduct* the human approval — perfect fit for "thin gate + external workflow." (K8s's one built-in approval primitive: `certificatesigningrequests/approval` subresource — single-approver, RBAC-gated.)
- **OPA / Gatekeeper:** admission webhook; **ConstraintTemplate (Rego logic) / Constraint (parameterized binding) split = the cleanest model for fixed-gate + pluggable-policy.** Audit vs enforce modes decoupled. Not an approval-collection system itself.
- **AWS SSM Change Manager:** built-in approval workflow (templates, up to 5 levels × 5 approvers, M-of-N, calendar/freeze). BUT enforced only at `StartChangeRequestExecution` (one invocation path), NOT the underlying resource store — a principal with direct IAM perms bypasses it. **Weakest chokepoint precedent; closed to new customers Nov 7 2025.**
- **GitHub / GitLab branch protection:** required reviews enforced **server-side at the merge API**, immune to git client (GitLab: protected branches "cannot be deleted with local Git commands or third-party clients"). Proposer≠approver enforced (GitHub blocks author self-approval). Who-approves configured via CODEOWNERS / approval rules; external systems can be required approvers or required status checks (the closest to pluggable). Bypassable for principals granted direct-push on the protected ref.

**Category verdict:** **"Thin, fixed enforcement gate at the system's actual write chokepoint + configurable/external approval workflow" is the established best-practice pattern.** For orbital (DGraph as SoT, GraphQL mutations as the single intended write path): **Vault Control Groups is the closest architectural sibling** (store-level, client-agnostic, response-deferring gate with declarative M-of-N via policy), and **Gatekeeper's ConstraintTemplate/Constraint split is the best model for keeping the approval-policy layer swappable** without entangling it with enforcement code.

---

## Four cross-cutting patterns

1. **Three places approval can live** — (a) beside the store, commit-then-detect (ServiceNow/Device42/iTop — a workaround for many-write-path stores); (b) inside the store as a branch/proposed-change (InfraHub/NetBox — chosen by single-API graph-native tools); (c) a chokepoint gate with external workflow (Vault/K8s/GitHub/TFC/Spacelift).
2. **The chokepoint truth** — every category independently re-discovered that a gate not at the universal write path is advisory (Argo CD kubectl→CRD bypass; ServiceNow forced into post-hoc detection; InfraHub Community `branch merge` bypass). The ones that hold enforce at the single layer all clients traverse (Vault core, apiserver, GitHub merge API, NetBox "regardless of protect_main").
3. **In orbital's exact category, enforced review is the emerging premium tier** — real, converging, monetized (NetBox built a paid Enterprise product; InfraHub gates enforcement behind Enterprise). Valued enough to sell; substantial enough nobody gives it away.
4. **Separate the gate from the workflow, universally** — Gatekeeper's template/constraint split, Vault's configurable factors, K8s pluggable webhooks. Nobody hard-codes an approval *workflow engine* into the store.

---

## What this means for orbital specifically

- **Orbital is in the NetBox/InfraHub category** (graph-native, API-first DC SoT) where the trend is toward built-in review — NOT the ITSM-CMDB category whose "don't gate the store" verdict is a consequence of write-path sprawl orbital doesn't share.
- **Orbital's write path is a genuine chokepoint** — GraphQL mutations are the single intended path to intent. Unlike ServiceNow, orbital *can* enforce at the store. Unlike AEP-only enforcement, only the API layer covers orbctl + third-party clients. (This is the precise gap in the current "let AEP layer it" decision: orbital has other writers, so a client-side gate is the Argo-CD-CRD-bypass shape.)
- **Orbital already owns the hard parts of the InfraHub/NetBox model:** `graphdiff` = the branch diff; export-preview + guarded-apply (`expectedContentHash`/MVCC) = merge-time conflict detection (InfraHub's "conflict detection always enforced" analog); blue-green/scratch DGraph = adjacent staging infra; `readonly<dev<admin` + `RequireRole` = authz base. The genuinely NEW capability = a persisted proposed-change (branch-like) staged before it becomes authoritative intent, plus an approval-required gate on its merge.

---

## Recommended shape (if the decision is "build it")

1. **Enforce at orbital's mutation/apply chokepoint, not in AEP.** The most consistent finding across all four categories. Only place the gate holds for every client.
2. **Model it as staged proposed-change (branch) + approval-required-to-merge**, reusing graphdiff + guarded-apply + blue-green — the InfraHub/NetBox shape orbital is unusually close to. NOT the ServiceNow separate-ticket + commit-then-detect model (that's for stores that can't gate).
3. **Build the primitive, not a workflow engine.** Proposed-change state + `proposer ≠ approver` + configurable N-approvals threshold. Leave routing/notifications/ITSM to the client — matching Gatekeeper's fixed-gate/pluggable-policy split, honoring "ITSM out of scope" + general-platform goals.
4. **First-class `proposer ≠ approver`, with an audited break-glass** (InfraHub's Super Admin bypass beats a silent gap).
5. **Opt-in per protected class** (per-type / per-namespace), so adopters who don't want four-eyes pay nothing.

---

## The honest caveat + the decision that's actually the architect's

Everyone who ships this **monetizes it as premium** → significant investment, not a bolt-on. So the decision is **timing/positioning**, not feasibility:
- Compete in the NetBox-Enterprise / InfraHub premium space → enforced proposed-change review is becoming table-stakes for that tier; build it, at the chokepoint, as above.
- Otherwise → the ServiceNow model (fast ungated store + strong audit/diff/reconciliation, approval delegated) is legitimate and well-precedented — **but only if you accept client-side approval is bypassable** by orbctl/third parties (or constrain all writes through the approving path). That's the exact trade Spike 31 made.

## Provenance note — relationship to Spike 31 (shipped 2026-08-25)

Spike 31 decided approval is client-layered: *"a client's approval workflow is satisfied by orbital's existing gates (role on write + expectedContentHash on publish)"* (docs/reference/AUTH.md). This research contradicts that on ONE specific, evidence-backed point: a **client-layered gate cannot be enforced** given orbital's multiple write clients (orbctl, third parties) — the recurring "chokepoint truth." Also note orbital has **zero two-person controls** today: guarded-apply is optimistic-concurrency + self-review, not maker-checker. If enforced four-eyes is a real requirement, Spike 31's posture needs a conscious revisit. If it isn't, the current posture is defensible.

---

## Addendum (2026-08-26) — Branching mechanism: NetBox proves it on Postgres; the DGraph path is branch-as-changeset

**Correction to an earlier framing.** An intermediate take in the discussion argued "DGraph isn't branch-native, so branching is disproportionately expensive." That is **wrong as a blanket claim** — branching is an **application-layer** construct, not a database-engine feature. NetBox is Django on **PostgreSQL** (not a graph store) and ships first-class branching. The store doesn't make branching possible or impossible; it makes it cheap or not.

### How netbox-branching actually works (on Postgres)
It leans on two things:
1. **PostgreSQL schema-per-branch for isolation.** Creating a branch provisions a *dedicated Postgres schema* populated with a copy of Main at the branch point; a `DynamicSchemaDict` replaces Django's `DATABASES` and routes queries to the active branch schema, so branch edits never leak into Main or sibling branches.
2. **Changelog replay for sync/merge.** It reuses NetBox's existing `ObjectChange` changelog, replaying entries in order to sync Main→branch and to merge branch→Main (with conflict detection).

So NetBox got branching relatively cheaply because Postgres handed it **near-free isolation** (schemas) *and* it already had a **replayable changelog**.

### Why the cheap NetBox mechanism does NOT port cleanly to orbital/DGraph
- **The isolation half doesn't port cheaply.** DGraph (community) has no Postgres-schema equivalent to provision + route per branch. The alternatives both have real costs:
  - *DGraph instance per branch* — heavy; orbital runs only blue + one scratch today.
  - *Branch-tagged data in one graph* — collides head-on with orbital's own design: `orbId` is `@id` (globally unique on the `ConfigItem` interface), so a branch's modified copy of e.g. `server-SN123` cannot coexist with Main's `server-SN123` in the same instance. The uniqueness constraint actively fights same-instance materialized branches.
- **The replay/changeset half ports fine — and orbital is already there.** `graphdiff` + export-preview already compute "Main + a changeset" in memory; guarded-apply (`expectedContentHash`/MVCC) is merge-with-conflict-detection. That is the second half of what netbox-branching does.

### The reconciling design: model a branch as a named changeset, not a materialized copy
This gives the **branching UX** the premium tier sells (create branch → diff vs Main → review → approve → merge) **without** per-branch storage:
- **Branch state is rendered on demand** as Main + the stored changeset (orbital already does this for preview).
- **Merge** = apply the changeset to Main, guarded by MVCC; **approval gates the merge.**
- **Unlimited concurrent branches, cheaply** (they are stored diffs, not DB copies) — actually better concurrency than schema-per-branch, and it sidesteps the `@id` collision.
- **Tradeoff vs materialized branches:** you cannot point the GraphQL proxy at a branch and browse it "as if live" through arbitrary ad-hoc queries (there's no isolated schema to route to). For orbital's targeted-change patterns this is a small loss; sweeping refactors want diff-and-review anyway, not live browsing.

### Bottom line on branching
Branching is achievable in orbital and *is* a legitimate competitive feature — but the DGraph-honest implementation is **branch-as-replayable-changeset**, not materialized per-branch schemas. **This is a later/optional investment, not the first thing to build.** The first thing is the **proposal + approval** layer (which is itself the "changeset + review + guarded merge" core that branching would later build on). Full materialized branching should only be revisited if a concrete need for isolated, live-browsable, long-lived workspaces emerges.

**Sources:** https://github.com/netboxlabs/netbox-branching/blob/develop/docs/index.md ; https://deepwiki.com/netboxlabs/netbox-branching ; https://netboxlabs.com/docs/extensions/branching

---

## Design decisions (2026-08-26) — how the proposal + approval layer would actually work

*These are the concrete design decisions worked out after the research above. Decision-input, not yet ratified/spiked. The consensus direction is: build the **proposal + approval** layer FIRST (branching is a later/optional build on top).*

### D1 — The proposal/changeset is stored in PostgreSQL, NOT DGraph
**Principle:** DGraph holds *authoritative intent only*. A proposal is by definition *not-yet-intent* (a pending change with workflow state), so it must not live in the intent graph. It's operational/workflow data — Postgres's established role (jobs, users, audit logs, schema versions, backups; audit log is the closest analog).

Five reasons:
1. It's operational/workflow data (status machine + author + approvers + payload) — same shape as jobs/audit, already Postgres/ent.
2. It must not pollute the graph — a pending, unapproved change in DGraph could leak into the Topology API, exports, or seeds. Postgres storage enforces "nothing reaches DGraph until merge" structurally.
3. Sidesteps the `@id` collision: a changeset containing a modified `server-SN123` can't coexist with the authoritative `server-SN123` in the same DGraph instance (global `@id` uniqueness). Stored as a serialized `jsonb` payload, it's a *description* of intended changes, not live nodes.
4. Relational workflow modeling (proposal→author FK, proposal→approvals join, `proposer≠approver`, N-of-M, "open proposals assigned to me") is textbook relational and already the ent pattern.
5. Matches the REST convention — operational workflow objects are REST + Postgres-backed (like jobs), not part of the GraphQL config graph.

**Shape:** `proposal` row (id, status, author, scope/namespace, `expectedContentHash` captured at open) + `jsonb` changeset payload (serialized mutations or a canonical target end-state) + `approvals` table (proposal_id, approver, decision, timestamp) — all via ent. **DGraph only sees the change at merge time**, as one MVCC-guarded mutation.

### D2 — Enforcement model: reject-and-redirect, NOT silent-transform
A gated `/graphql` mutation is **rejected** with `APPROVAL_REQUIRED` (+ a hint pointing to `POST /api/v1/proposals`) — it does NOT silently become a proposal.

Why: a GraphQL mutation that returns "pending" instead of the mutated object breaks the GraphQL contract *and* breaks the invariant "GraphQL mutations update authoritative intent only." Keeping the mutation as "writes intent, or is rejected — never something else" preserves the invariant and keeps proposals where they belong (REST workflow resource in Postgres). Slightly more work for clients (two shapes: mutation for direct writes, REST proposal for gated), but honest and enforced at the chokepoint — which is the point (orbctl and third-party clients hit the same gate; a client-side-only gate is bypassable, per the research's "chokepoint truth").

### D3 — Bypass is an explicit, audited break-glass capability, NOT a silent admin exemption
A bypass must exist (every comparable system has one: InfraHub Super Admin break-glass, NetBox `bypass_policy`, GitLab direct-push-to-protected). Two refinements:
- Make it a distinct **`bypass_approval` capability** (grant to admin by default if desired) rather than hardcoding "role == admin is exempt." On a general platform, *who* can bypass should be per-protected-class policy.
- **Audit every bypass as a bypass** ("written without approval by X"). Governance nuance: the usual reason for four-eyes is compliance/change-control, and an admin making unreviewed changes is exactly the risk that control targets. Silent admin exemption undercuts half the value; a *logged* break-glass keeps the fast path AND the audit story auditors want. (InfraHub's model: Super Admins can self-approve/bypass, but it's an audited escape hatch.)

### D4 — Worked example: changing a server's iDRAC setting
iDRAC is an owned child of Server, so "change an iDRAC setting" is an `updateServer` mutation (edited through the server's nested JSON tree).

**Today (no approval):** client POSTs `/graphql` `updateServer` → `graphql.go` (auth `RequireRole≥dev` → rate limit → MVCC `ifVersion` → forward to blue DGraph → audit) → authoritative intent immediately.

**Changeset world, caller=`dev`, class protected:**
1. Client POSTs `/graphql` `updateServer`.
2. Gate: class protected (e.g. "namespace `prod`, type `Server` requires approval") + caller lacks bypass → **reject** `APPROVAL_REQUIRED`. Nothing written to DGraph.
3. Client submits `POST /api/v1/proposals` with the changeset (scoped to the server's owned subgraph) → Postgres row, `status=open`, `author=dev`, captures subgraph `expectedContentHash`.
4. Second user (approver ≠ author) reviews the `graphdiff` (current vs proposed) → approves → `status=approved`.
5. Merge: read changeset from Postgres, apply `updateServer` to blue DGraph, MVCC-guarded by captured hash (aborts → re-review if subgraph moved). Audit: author + approver + merger. `status=merged`.
6. Now authoritative intent; next publish ships it.

**Changeset world, caller=`admin` (has bypass):** client POSTs `/graphql` → gate sees `bypass_approval` → writes straight through to DGraph like today, but the audit event is flagged as a **bypass**. Immediate intent.

**UI:** for a gated class, the JSON-editor "Save" creates a proposal (thin renderer calling the proposal API) and shows "pending approval," instead of committing. Same API, consistent with API-first.

### D5 — The gate only fires for a "protected class" (opt-in, per-type / per-namespace)
Whether a given change is gated depends on config (e.g. "namespace `prod` + type `Server` → require approval"). Non-protected classes commit directly as today, even for a `dev`. Adopters who don't want four-eyes pay nothing. Matches how every surveyed tool scopes the gate.

### Two-gate clarification (authoring vs publish)
Orbital already gates the **publish** boundary (intent → edge: export-preview + guarded-apply). This proposal layer gates the **authoring** boundary (proposal → intent). Complementary, like "PR review before merge to main" vs "release approval before deploy." The four-eyes-on-a-config-value scenario belongs at **authoring**. NetBox/InfraHub gate authoring (merge-to-main = commit-to-SoT), so adding an authoring gate is what pulls orbital level with the premium tier.

### Open decisions (need the architect's call before this is a real spec)
- **(a) Proposal scope** — single config item / owned subgraph (server + children) / whole namespace? Drives merge-conflict-detection complexity; ties into the Spike 33 ownership/subgraph model. *(Most important — decide first.)*
- **(b) Which boundary** — authoring gate vs publish gate vs both? (For the stated scenario: authoring.)
- **(c) orbital-vs-AEP split** — recommended line: orbital owns the enforced gate + proposal resource + `proposer≠approver` + audited break-glass; AEP owns routing/notifications/who-approves/ITSM. Confirm.

### Suggested sequencing
- **v1:** proposal-as-changeset + review diff + `proposer≠approver` approval + guarded merge, opt-in per protected class, enforced at the GraphQL mutation layer. Reuses `graphdiff` + MVCC.
- **v2:** richer policy (N-of-M, per-namespace approver groups), break-glass polish, audit.
- **later / optional:** full branching (branch-as-changeset per the Addendum), only if a concrete need for isolated, live-browsable, long-lived workspaces emerges.

---

## Product landscape: who has approval, and how popular (2026-08-26)

*Star counts via `gh api` (point-in-time, 2026-08-26). Market-share/adoption figures are from vendor-comparison aggregators — treat as rough, directional only.*

### Who has approval — and, crucially, who gates the STORE's write path (the model orbital would follow)

| Product | Category | Has approval? | Gates the *store's* write path? |
|---|---|---|---|
| **InfraHub** | graph-native SoT (direct analog) | Yes | **Yes** (Enterprise for hard enforcement) |
| **NetBox** | DCIM SoT | Yes (Enterprise-paid) | **Yes** ("no merge without approved CR") |
| **Nautobot** | DCIM SoT | Partial — only gates *Job execution*, not direct data writes | No (not yet) |
| **iTop** | ITIL CMDB | Yes (core OSS) | No — ticket workflow *around* the CMDB; M-of-N is a paid extension |
| **ServiceNow** | ITSM CMDB | Yes | **No** — separate change_request + post-hoc detection |
| **BMC Helix CMDB** | ITSM CMDB | Yes + Sandbox staging | Partial (self-service promote, not confirmed two-person) |
| **Device42** | pure CMDB | **No** | — |
| **Ralph** | DCIM | **No** | — |
| **Terraform Cloud / Spacelift / env0** | IaC run-plane | Yes | Yes — at *their own* apply chokepoint |
| **Atlantis / Argo CD / Flux / Crossplane** | GitOps | No native (delegate to Git PR / RBAC) | Only if the tool is the sole write path |
| **Vault Control Groups** | enforcement primitive | Yes (M-of-N, Enterprise) | Yes (Vault core API) |
| **AWS SSM Change Manager** | enforcement | Yes (M-of-N) | Weak (one invocation path; closed to new customers) |
| **GitHub/GitLab branch protection** | enforcement | Yes | Yes (server-side merge API) |

**Takeaway:** among products *in orbital's actual category* (graph-native / DCIM sources-of-truth), only **InfraHub** and **NetBox** gate the store itself — and both **charge for it** (approval is the premium tier). ServiceNow/Device42/BMC are ITSM/legacy CMDBs that either don't gate the store or lack approval entirely → weaker precedents. The case for "orbital should build this" rests on **NetBox (proven demand) + InfraHub (architectural frontier / direct analog)**, not ServiceNow/Device42.

### OSS mindshare anchor (GitHub stars)

| Project | Stars | Age | Role |
|---|---|---|---|
| **NetBox** | ~21,400 | since 2016 | dominant OSS source-of-truth |
| **Nautobot** | ~1,600 | since 2021 | NetBox fork / enterprise-y alt |
| **InfraHub** | ~510 | founded 2023 | newest challenger, git-style SoT |

### What the two key products are

- **BMC Helix CMDB** — the CMDB inside BMC Helix ITSM (cloud rebrand of BMC Remedy; CMDB descends from BMC Atrium CMDB). A decades-old, **commercial, closed-source** enterprise ITSM suite. Popularity: significant *legacy* enterprise footprint but a distant second to ServiceNow (ServiceNow >40% ITSM share / ~80% of Fortune 500; BMC in the low hundreds of companies on adoption trackers) and generally declining. Not developer-facing, not OSS. **Its approval model (separate change-request object, not a store gate) is not the model orbital would follow.**
- **InfraHub (OpsMill)** — young open-source **graph-based "infrastructure source of truth"** with built-in version control, branches, peer-reviewed proposed changes, CI, API. OpsMill founded 2023 (France). **Closest architectural analog to orbital.** Popularity: early-stage (~510 stars) but credible momentum — **$14M Series A (May 2026)**, marquee references (TikTok; Eurofiber cut deployment 5 days → 15 min), integrates with Ansible/Nornir/Terraform. The "where the category is heading" signal.

**Sources:** SiliconANGLE (OpsMill $14M Series A, 2026-05) ; Packet Pushers (InfraHub launch) ; Budibase (BMC Helix vs ServiceNow) ; 6sense (BMC Helix ITSM market share) ; GitHub star counts via `gh api` 2026-08-26.

---

## Wider scan + the intent-vs-discovery correction (2026-08-29)

A broader second pass (beyond the original eight primary-doc products) plus two clarifications that sharpen — not change — the conclusion. *This section is **scan-level** (search snippets), not per-product primary-doc verification like Categories 1–4; deepen any single product if it becomes decision-relevant.*

### Wider scan: no new store-gater — everything lands in the existing buckets
Searched **i-doit, CMDBuild, GLPI, Ralph** (OSS CMDBs); **Jerikan, IP Fabric, Slurpit** (network-as-code / SoT); **Jira Service Management / Assets (Insight), Freshservice, Ivanti** (enterprise ITSM). No product gates its own store beyond InfraHub / NetBox. The additions fall into two buckets already established above:

- **Change management as a *separate workflow object* — does NOT gate the CI store** (the ServiceNow pattern): **Jira Service Management / Assets** (CAB approvals + CI/CD deployment gating, but on the change-request / release, not the Assets store), **Freshservice**, **Ivanti** (approval workflows by change type), and OSS **GLPI / i-doit** (ITIL change tickets). **CMDBuild** ships a generic workflow engine you *could* build a gate in, but nothing gates the store by default.
- **Review enforced via Git — the data lives in Git, the gate is a Git PR**: **Jerikan** (YAML-in-Git + merge-request review) and the **GitOps** tools (Atlantis / Flux / Argo CD). **IP Fabric** enforces review by *creating a NetBox branch* — i.e., it borrows NetBox's gate rather than having its own.

### Is the leaders' change management "Git-based"? For the DATA — no.
"Git-based" splits three ways, and the two closest analogs do **not** store their data in Git:
- **NetBox branching** — Postgres **schema-per-branch** + changelog replay. No Git.
- **InfraHub** — data branches / proposed-changes / merges live **inside its Neo4j graph database** (version control built into the DB). Git is used **only for the code side** — transformations, queries, generators (Jinja2 / Python) in external repos — **not the infrastructure data**. (Its git-heavy framing = git *vocabulary* for the data model + *real* Git for the code/artifact layer.)
- **Literally Git** (data in Git, gate = Git PR): Jerikan, GitOps.

→ Orbital's model — a **changeset in Postgres merged into the DGraph SoT** — is git-*concepts-on-its-own-store*, matching NetBox (Postgres) and InfraHub (Neo4j), **not** data-in-Git. Aligned with the closest analogs on exactly this axis; InfraHub's Git-for-code is a separate concern (orbital's equivalent boundary is the export API + deployment/bundler layer).

### The intent-vs-discovery correction (why "few gate the store" is unsurprising, not doubtful)
An earlier framing ("CMDBs are discovery-fed, so they can't gate") conflated **practice with definition**. Corrected:
- **By definition (ITIL), a CMDB is the *authorized / intended* configuration** — "what should be in place per governance." **Discovery is a *separate* process** that reports observed state and is compared against the CMDB to flag *unauthorized* changes. The CMDB is meant to be an intent record updated through change/approval; discovery detects drift *against* it.
- **In practice**, enterprise CMDBs (ServiceNow, Device42, BMC) became discovery-***populated*** — using discovery to *fill* the record, not just check it — because maintaining authored intent by hand didn't scale. This is a well-known failure mode ("the enduring myth of CMDB").
- **So "few gate the store" really means "few kept the intent model."** The discovery-*populated* CMDBs can't gate (you cannot human-approve a discovery firehose); the intent / authored SoTs — **orbital, InfraHub, NetBox** — can, and do. **Gating the store is the *orthodox* CMDB behaviour**, not an exotic one.
- **Orbital is not the outlier — it is the faithful CMDB.** It holds intent, keeps discovery out of scope (observed state = divergence reported from the edge, then reconciled), and is therefore *more* aligned with the ITIL definition than the discovery-populated majority. CLAUDE.md's "intent-only CMDB" label is precisely this.

**Sources (scan-level):** ITSM "enduring myth of CMDB" whitepaper; ITIL CMDB overviews (Device42, Atlassian); InfraHub architecture + Git-repository docs (OpsMill); netbox-branching architecture (DeepWiki); Jerikan / Git-as-network-SoT (Vincent Bernat); IP Fabric ↔ NetBox branching; JSM / Ivanti change-management comparisons.
