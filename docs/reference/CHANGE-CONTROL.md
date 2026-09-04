# Change Control Reference

> **Audience:** anyone touching change requests, approval policies, the write gate, or the config editor's Save path.

Read this before: `internal/approval/`, `internal/handler/changerequest*.go`, `internal/handler/approval_gate.go`, the `approval_*` ent tables, `/api/v1/change-requests`, `/api/v1/approval-policies`, or `configitem-editor.js`.

Orbital's maker-checker layer: a **change request** proposes a set of ConfigItem changes, a peer approves it, and merging applies it. The engine underneath is action-type-agnostic — v1 implements one action type (`config.mutation`) and a second adds rows with a different `payload` shape plus its own adapter, with **no schema change** to the engine tables.

**Opt-in.** With no enabled `approval_policy`, every write behaves exactly as it did before this existed. Installing the feature changes nothing until an admin declares a protected class.

## Settled Decisions

- **Staleness is TWO signals with two owners: `stale` (the author's) and `subtreeChanged` (the reviewer's).** *(Added 2026-09-04. Supersedes the single-signal model, and REVERSES the "name them from `base_versions`, not the client's `version`" decision below.)* `stale` is true when **at least one change object's `version` no longer matches its node's current version** — a statement about what the author proposed, computed from the changeset. `subtreeChanged` is true when the reviewed scope's hash moved without any change object going out of date — typically an edit to an owned child, a statement about what the reviewer looked at. **Both block merge**; neither is derived from the other.

  **Why split what was one flag.** The conflated version let a reviewer clear, in one click, a proposal written against a value that had since moved: approving re-anchored `base_hash`, `stale` went false, and the merge wrote the author's now-outdated `set` over someone else's edit. The two facts have different remedies, so one flag could only ever offer the wrong one to somebody. Now `stale` is computed from the changeset and **re-approving cannot clear it** — only the author rebasing can (`PATCH` with the current `version`, or drop the object). The re-anchor stays exactly as it was, because clearing `subtreeChanged` is the one thing it is *right* for.

  **Consequence, deliberate:** an approved request can be `stale`, and it stays `approved` while refusing to merge. `availableActions` drops `merge` and offers `edit` to the author, and a rebase bumps `changeset_revision`, which dismisses approvals **even when the graph has not moved** — the reviewer approved a different proposal. This is GitHub's "dismiss stale approvals on push", with the version vector standing in for the commit sha. A change object carrying **no** `version` can never be item-stale and relies on `subtreeChanged` alone, which is the scope anchor doing the job it always did.

- **A competing request is surfaced at REQUEST level on the review page, never as a per-row mark.** `GET /api/v1/proposed-changes?orbId=…` already returns the field-level projection of every active request touching a set of entities; the review page groups it by the *other* request and renders one notice above the Changes box. Two reasons it is not a column in the Changes table: that table is `renderExportPreviewTable`, **shared with the export preview and artifact compare** where a sibling-request mark is meaningless — and a reviewer needs *"colo-45 also proposes `enabled=false`"* as one fact, not the same fact scattered across however many fields overlap (the same argument the field marks on entity pages make for the opposite direction). **Filter the current request out of the response** — the endpoint reports every active proposal including this one, and a missing filter renders a notice that is always present and always wrong, with no other symptom. Conflicting overlap (different value) reads stronger than agreeing overlap (same value), because conflicting decides an outcome: whichever merges first wins the field and the other is refused until re-reviewed. Pinned by `e2e/change-request-overlap.spec.ts`.
- **A changeset item is an end-state payload with an OPTIONAL entity-level precondition — not a delta.** `set` carries the values you want; `version` carries the entity's `version` as you read it. Supplied ⇒ conditional: orbital refuses with **409 `MVCC_CONFLICT`** at creation if the entity has moved. Omitted ⇒ unconditional, guarded by the scope anchor alone. This is `If-Match` (RFC 9110) and `resourceVersion` inside a Kubernetes patch body — conventional in both places, invented in neither. **Do NOT redefine `set` as a delta**: a create has nothing to compare against, and orbital is a declarative intent store. **Field-level protection still exists, but at MERGE and from the SERVER's ancestor** (`base_values`), not from anything a client asserts — see the three-way merge below.
- **The client-supplied `before` was REMOVED (2026-09-03).** It carried the values the caller read, keyed like `set`, and refused at creation if one had moved. Two reasons it went. It was a **second concurrency vocabulary** for a question `/graphql` already answered with `version`, which is the cost an integrator pays for our convenience; and its premise — that server-side version stamping was unreliable, so a field-level assertion was needed — **stopped being true** when the write pre-flight moved into the single write path and divergence-Accept started stamping like every other writer. **What this costs, stated plainly:** a creation-time refusal is now entity-grained, so a third party editing a DIFFERENT field of the same entity while you compose now refuses your proposal where `before` would have accepted it. Judged worth it — re-reading and re-proposing is cheap, the merge-time three-way is unchanged, and it also retires the whole-value composite-field false-conflict filed in `debt.md`. **`base_values` is NOT `before` and was NOT removed** — they were routinely confused; see below.
- **`version` details.** A mismatch uses the **409 `MVCC_CONFLICT`** envelope, and an entity-level problem carries **no `Field`** — that absence is how a client tells it from the field-level conflict merge produces. It is honoured on `op: delete` — where a stale precondition costs most, since a delete destroys work with no diff to recover it from. Rules: **one per item, never per field** (an entity has one version); **omission is legal** and leaves the item guarded by the scope anchor alone; and supplying it against an entity that does not exist is **refused at validation (400)**, not ignored — a caller that asked for a check and silently did not get one is worse off than one that never asked.
- **A stale merge NAMES the entities that moved.** *(Added 2026-09-03. **Partly superseded 2026-09-04** — `staleEntities` is now computed from the change objects' own `version`, which is what `stale` means. `base_versions` remains the anchor behind `subtreeChanged`, and the "all four sites must set both" rule below still binds. The paragraph's argument against the client's `version` — that re-approval could never satisfy it — no longer applies, because clearing item staleness is deliberately no longer re-approval's job.)* `base_hash` is a fingerprint of the scope's `orbId@version` vector, so it has always answered *"did anything move"* and never *"which one"*. `base_versions` stores the vector it fingerprints, so the refusal carries `problems[]` naming each moved entity and both versions — for **every** request, including ones whose author never sent a precondition. It wraps `errCRStale` rather than replacing it, so the status, the code and every `errors.Is` call site are unchanged. **Why not the author's `version`:** that is their read at PROPOSAL time. Once anything moves that entity the token is permanently unsatisfiable, and re-approval — which is how staleness is meant to be cleared, in one click — could never satisfy it; only an amend could. That is the same trap that made storing raw mutations unacceptable (§ Rejected: storing the mutation). `base_versions` is re-captured wherever `base_hash` is (create, amend, the approve-rebase, the post-merge rebase), so it moves with the review instead of outliving it. **All four sites must set both** — a vector that drifts from its own fingerprint degrades the refusal to the unnamed one with nothing observable saying so; pinned by a test asserting `versionHash(base_versions) == base_hash` after each.
- **`base_values` is the ancestor, and it exists because orbital renders branches rather than materializing them.** InfraHub and NetBox get an ancestor free from a branch point; orbital cannot copy a branch because `orbId` is `@id` and a copy could not coexist with main's (`change-control-research.md`, addendum 2026-08-26). So the ancestor is recorded: `orbId → predicate → value`, scoped to the fields the changeset touches, in **graphdiff-normalized form** so a merge-time comparison runs between two values from one normalizer. Recomputed wherever `base_hash` is — Create, Amend, **and Approve**. A re-review that moved only the hash would clear `stale` while every moved field stayed a conflict forever.
- **Merge is three-way per field, pre-checked atomically, and the write is narrowed to match.** Each field resolves to *applies* / *already-satisfied* / *conflicts*. Satisfied fields are **dropped from the write** — the stored changeset keeps them, because that is the author's declared intent and orbital never edits it, but re-writing a value that is already there only bumps `version` and emits an audit row for nothing (and invalidates every other open request on that entity). **Narrowing is what makes the field-level guard SAFE, not an optimisation:** `applyItem` writes the whole `set`, so guarding narrowly while writing widely pushes stale status-quo values over other people's edits. Guard narrowly and write narrowly, or guard widely and write widely — the mixture is the one combination that loses data.
- **A conflict refuses the WHOLE merge, before anything is applied.** Kubernetes SSA does the same (`SDD-CONTEXT.md` §6.3: *"the whole apply fails 409. K8s does NOT silently apply the non-conflicting field"*), and the reasoning carries: a reviewer approved a set, so applying an arbitrary subset of it is not what anyone signed off. The partial-merge machinery stays for **runtime** failures — an entity that vanishes mid-apply — which is a different thing from a conflict known in advance.
- **The field-level guard catches what `base_hash` structurally cannot.** `base_hash` is a version-vector fingerprint, so a write that changes a value without bumping `version` — a direct DQL write, a restore, `make seed` — leaves it matching. Measured 2026-09-01: with `stale: false` and `status: approved`, a merge that would have destroyed a third party's value was refused naming the field. `base_hash` still does its job (cheap, catches the common case first); this is the second line.
- **Approve recomputes the ancestor UNCONDITIONALLY, not only when `base_hash` moved.** `base_hash` is a version-vector fingerprint, so a write that changes a value without bumping `version` — divergence-Accept, a restore, a direct DQL write — leaves it matching while `base_values` goes stale. Gating the recompute on the hash made such a conflict **unclearable by any action**: approving returned `200`, changed nothing, and merge kept refusing; the only escape was closing the request and proposing again. Measured and fixed 2026-09-01. Approving *is* the act of attesting to current state, so current state is what the ancestor must become. Cost: one subgraph read per approve, which is not a hot path.
- **`GET /{id}/diff` returns `satisfied[]` beside `changes[]`.** `changes` means "what would change" and must keep meaning exactly that; `satisfied` is the part of the proposal that would do nothing. Without it the table silently shrinks when someone else applies part of a request, with no signal that it did or why. **Derived FROM the computed diff, never by re-comparing values** — a second comparison would need its own notion of equality and would disagree with the first on exactly the DGraph-round-trips-as-string cases it exists to surface. Absence from the diff *is* the definition of satisfied.

- **The title is written by the PROPOSER; the fallback is the entity name, nothing more.** The propose footer carries a `Change request title` input prefilled with the entity name (namespace stripped) and selected on first focus, so replacing it is one gesture and accepting it is none. `fallbackTitle()` in `configitem-editor.js` is the single definition — the input prefills from it and the submit path falls back to it, so **accepting the prefill and clearing the box store the same string**. There is deliberately no third value that appears only when you leave the field alone.
- **Do NOT append a field count to the generated title.** It used to read `server-GD206G4 · 1 field`, on the reasoning that a count makes a generated value read as a label rather than as a human title. The queue then gained a dedicated `Change` column carrying exactly that count, so the tail became the same fact twice on one row. It also used to say `Update <entity>` when it could not count any fields — i.e. for a create-only changeset, the least update-like case there is. One form for every branch. The review page renders the stored title **verbatim** and strips nothing, so what the queue shows and what the heading shows can never disagree.
- **Legacy rows keep the titles they were stored with.** Requests created before the title field carry `<entity> · N fields`, which now duplicates the `Change` column on their row. Left alone deliberately: a title is a record of what someone called a request at the time, and neither rewriting stored history nor regex-matching the generated shape at render time is worth the redundancy on a set of rows that ages out.
- **Title is capped at 255 characters, enforced in the handler.** `field.String("title")` declares no `Size`, so ent generates `varchar(255)` (`description` declares one and gets `text`). Before the check existed a longer title reached Postgres and failed at INSERT, surfacing a user error as a 500. Enforced by `validateTitle` on both create and amend, documented as `maxLength:"255"` in the Swagger DTOs, and mirrored as `maxlength` on both inputs. Long context belongs in `description`, which is unbounded.
- **Rename is a title-only `PATCH /api/v1/change-requests/:id` and does NOT invalidate approvals.** `Amend` re-anchors `base_hash`/`base_present`/`base_effect` only inside `if cs != nil`, so a title-only patch touches `title`, `updated_at` and `updated_by` and nothing else. Verified against the running stack: after renaming an approved request, `status`, `approvals`, `stale`, `effect`, `changes`, `reviews` and `availableActions` were all byte-identical. A rename is not a re-proposal — do not "helpfully" re-capture the base on it.
- **The review page's Rename button is rendered from `availableActions` containing `edit`, never from client-side role logic.** `edit` is the API's name for amend, of which the UI implements only the rename half; the button is labelled for what it does. **Known asymmetry:** `Amend` gates on the *stored* status (`open`), while `availableActions` gates on the *derived* status — so the API accepts a rename on a request that reads `approved`, but the UI never offers one there. Renaming after approval is provably harmless (see above); if that gap matters, the fix is adding `edit` to the `StatusApproved` branch of `availableActions`, not a client-side override.
- **The queue lists requests by TITLE, with a narrow derived size column beside it.** Columns: ID, Title, Change, Namespace, Author, Status, Approvals, Age. This reverses an earlier "Change, not Title" decision whose stated reason — *the stored title is a label written once at creation* — expired the moment proposers began writing titles. Every comparable queue lists the human summary (GitHub, GitLab, Jira, ServiceNow, Terraform Cloud) and none renders a diff into the list; all of them pair it with a magnitude, which is what the narrow `Change` column is for. **Keep both.** Title alone loses "how much am I approving", which no title can promise to say and which stays derived on read, so it follows an amended changeset even when the title does not. The full before/after belongs on the detail page — the old wide column rendered a whole `before → after` for a one-field request and collapsed to a bare count for a twelve-field one, telling you least about the request that mattered most.
- **The queue's Title cell truncates and is NOT link-blue.** It is free text at up to 255 characters, so `#cr-table td.cr-title` sets `text-overflow: ellipsis` with the full value on the cell's `title` attribute — the global `.th, .td` rule in `main.scss` matches those class names, not real cells, so this has to be stated. Its link renders in `--bulma-text-strong`, not the link colour: it is the widest column, and colouring every row turns a third of the table blue next to an already-blue ID column. GitHub does the same and puts the affordance on hover.
- **The review page heading is `<title>` then the id in muted grey** — GitHub's `Title #123`. The id stays in the heading rather than moving to a Details row: it is what people quote, it is the only thing distinguishing two requests whose authors chose the same title, and the browser tab (`<id> · Change Request`) is the only other place it appears.
### The gate lives in `writeToDGraph`, and has exactly ONE exemption

`GraphQL.writeToDGraph` is where the policy check lives — **not** in the `/graphql` handler, which is a chokepoint for CLIENTS but not for WRITES: divergence-Accept dispatches `update{Type}` internally and would have walked straight past a check placed in `Handle`.

The same function now also carries the **write pre-flight** — the before-fetch, the `version`/`updatedAt`/`updatedBy` stamping and the `version` check — moved down out of `Handle` on 2026-09-03 for the identical reason, after divergence-Accept was found writing intent unstamped and therefore invisible to `base_hash` staleness. Order inside the function is load-bearing: fetch → `version` → stamp → strip → **policy check** → POST. The gate sees the body exactly as DGraph will, and an MVCC conflict is refused *before* the gate, so a caller holding stale state is told to reload rather than told to open a change request they would then have to redo.

> **⚠️ The GATE covers every write; the write PATH is still not single — 2026-09-03.** `DELETE /api/v1/config-items/:type/:id` plans a cascade and POSTs DQL directly, so it never reaches `writeToDGraph`. It used to escape the policy check entirely: measured under a policy with `bypassRoles: []`, `updateDataCenter` was refused `403 APPROVAL_REQUIRED` while `DELETE` of the same entity returned `200 {"deleted":1}` seconds later. **That is closed** — the decision was extracted into `checkPolicyFor(ctx, orbIDs, types, caller)`, which the delete calls directly, with the same status, code and hint, over every type the cascade removes. So the guarantee in this section now holds for deletes too. **What is still not true is the structural claim**: two functions reach DGraph, not one, so a third write path added later would have to remember the gate rather than inherit it. Prefer routing new writes through `writeToDGraph`; if you cannot, call `checkPolicyFor` and say why here.

`gateMode` is an **explicit argument**, never a context value, so every exemption is visible at its call site and findable with one `grep gateExempt`.

**The one legitimate exemption is `applyItem` in the merge path.** Enforcing there means an approved change request cannot apply itself — the merge is refused for lacking the approval it already has, and the request becomes permanently unmergeable by anyone outside `bypass_roles`. Circular by construction. `TestGate_MergeOfApprovedRequestStillWorksWhileGated` merges as a plain `dev` and fails if the exemption is removed. **Do not add a second exemption.**

`readFromDGraph` is deliberately separate so reads never need a skip-the-check flag.

### Who can see policies, and who can change them

**readonly+ can READ the policy list; only admin can change it.** The UI gate matches the API in both directions — `apiReadonly` serves `GET /approval-policies`, `adminAPI` owns the writes, and the page mirrors exactly that.

The read has to be open, because *"why was my change gated?"* is a **dev's** question and this page is the answer. A page visible only to admins hides the explanation from everyone who needs it, and they end up asking a human instead.

For non-admins the write controls are **omitted, not disabled**: a greyed-out Delete still says "you could do this", and the API would refuse anyway. The state renders as plain coloured text instead of a segmented control.

*(This was wrong on first delivery — the page demanded admin while the API served readonly+. A UI gate stricter than the API's is its own bug, in the same family as one that is looser.)*

### Enforcement is per POLICY — there is no global on/off

Each policy is **Active** or **Disabled**, and that is the only lever. There is deliberately no global "enforcement enabled" setting: when one policy misbehaves an operator needs to stop *that* one, not everything, and a global switch is the blunt version of the same thing. Every comparable engine puts it on the policy — Kyverno's `validationFailureAction`, Gatekeeper's `enforcementAction`, Sentinel's enforcement level, GitHub rulesets' enforcement status.

Disabling must not DELETE the row: an operator who turns a policy off to unblock an incident has to turn it back on with its settings intact, and a toggle that destroys configuration is one nobody dares use.

The lever is always reachable: policy administration writes PostgreSQL, never DGraph, so it never passes through the gate. A misconfigured policy therefore cannot lock out the means of disabling it. `TestApprovalPolicy_DisablingStopsGatingAndKeepsTheRow` pins all three.

**No middle "advisory" state yet** — every one of those engines has one (Audit / dryrun / advisory / Evaluate) for rolling a policy out safely, and orbital does not. Tracked in `docs/planning/backlog.md`; it needs somewhere to record what *would* have been blocked, or it is indistinguishable from Disabled.

`ORBITAL_CHANGE_CONTROL_ENABLED=false` removes the feature entirely — a different lever for a different purpose, see `CONFIG.md`.

### ONE policy per namespace, holding its own list of types

`approval_policy` is unique on `(action_type, namespace)`. Scope lives inside the row as an either/or:

| `all_types` | `types` | |
|---|---|---|
| `true` | `[]` | every type in the namespace, **including ones added to the schema later** |
| `false` | `["Server","Rack"]` | exactly those types |
| `true` | `["Server"]` | **refused** — the row would say two different things |
| `false` | `[]` | **refused** — protects nothing while reporting itself Active |

**Both invalid shapes are refused rather than one silently winning.** An "ignore the unused field" rule stores a row whose meaning depends on knowing which half the code honours, and the operator believes whichever half they typed. Refusing makes the pair a proper either/or with no third state.

**The rule is a database CHECK constraint (`approval_policy_scope_exclusive`), not only an API check.** The API validates first so the error says *which* rule was broken — a constraint violation cannot. The constraint is what makes it true of the DATA: a policy written by a migration, a `psql` session, or a future handler that forgets goes straight to the table. Note `jsonb` null is a **scalar**, so `jsonb_array_length()` raises on it rather than returning 0 — the constraint tests `jsonb_typeof(types) = 'array'` first, and the handler stores `[]` rather than null.

**All-types is a boolean, not a list of every type.** Ticking all nineteen would look identical today and let the twentieth arrive ungoverned. Same reason K8s RBAC spells `resources: ["*"]` and IAM spells `"Resource": "*"` rather than enumerating.

**Why one row per namespace and not one per type.** Multiple policies in a namespace can name the same type, so a gated write has several candidate answers to *"which policy did this?"* and needs a written precedence rule to pick one. Composition — `max(required_approvals)`, `intersection(bypass_roles)` — resolved that on paper but produced a governing policy that exists in no row: nothing to point a developer at, nothing to disable. One row per namespace makes the namespace the policy's identity, which is why `details.bypassedPolicy` and the `APPROVAL_REQUIRED` message can both name it and be unambiguous.

The cost is real and accepted: `required_approvals` and `bypass_roles` are per **namespace**, so *"Server needs two approvals, IdracSettings needs one"* is not expressible. Revisit only with a concrete request for it, and revisit by adding per-type overrides inside the row — **not** by allowing a second row, which reintroduces precedence.

**All-namespaces policies — SHIPPED 2026-09-02.** Asked for from demo feedback: an operator with twenty data centers should not need twenty policies, and — the stronger half — data center twenty-one must not land **unprotected** because nobody remembered. That is `all_types`' own argument one level up: *"AllTypes is not every type that exists today"*, and neither is this.

**Shape mirrors `all_types` exactly:** an `all_namespaces` bool, `namespace` relaxed to optional, and a CHECK making the other two combinations unrepresentable — a row setting both says two contradictory things, and whichever the code honoured, the row would no longer describe what is protected.

**Resolution is FALLBACK, not overlay: a global policy governs a namespace only when that namespace has no policy row of its own.** This is what lets the feature coexist with the one-row-per-namespace rule above instead of reversing it — exactly one row still governs any write, so *"which policy did this?"* keeps a single answer with a name, a row to point at, and something to disable. **Rejected: union / strictest-wins** (`max(required_approvals)`, `intersection(bypass_roles)`), which is what GitHub rulesets, Kyverno, Gatekeeper and AWS SCPs all do — because it recreates *"a governing policy that exists in no row"*, the specific failure the decision above was made to avoid. Worth knowing the category convention was considered and declined: policy-enforcement systems layer, configuration systems override, and orbital picks the configuration semantics deliberately because the single-name property is load-bearing here.

**A `disabled` namespace policy SHADOWS the global; it does not fall through.** `enabled=false` means *"this namespace is deliberately exempt"*, not *"this row is not in force"* — it is the per-namespace off switch, and the alternative leaves no way to exempt one namespace short of a weak-but-enabled policy. **Consequence, accepted:** a global policy is escapable by design. That is not a privilege hole — only admins manage policies — but it does mean "global" means **default**, never **floor**. Do not describe it as a floor in the UI.

**Two implementation notes for whoever builds it.** A **partial unique index** (`UNIQUE(action_type) WHERE all_namespaces`, via `entsql.IndexWhere`) is required: Postgres treats NULLs as distinct in a unique index, so relaxing `namespace` off NOT NULL silently permits **two** global policies — the multiple-candidates problem this whole design exists to prevent, shipping green. And `auditPolicy` attaches the namespace as the audit **resource id**, which a global row does not have; use the literal `*` **for the audit event only**, leaving the stored `namespace` NULL, so the sentinel never enters the data model or a `WHERE namespace = $1`.

**UI:** a checkbox mirroring *"All types in this namespace"*, not an entry inside the namespace `<select>` — a sentinel among real namespaces invites picking it by accident. The type checkbox relabels to plain *"All types"* in global mode, and the namespace picker is **hidden rather than disabled**: a dead select still naming a namespace invites reading the policy as covering only it. A namespace row that is currently overriding a global says **"overrides all-namespaces"** on the row — fallback is correct and still reads as a bug the first time someone meets it, most sharply when the namespace row is the weaker one.

**Editing cannot move a policy between the two scopes**, exactly as it cannot move a namespace: `PATCH` refuses a differing `allNamespaces` with a `400` rather than ignoring it, because a silently-dropped scope change leaves the caller believing the gate widened. The UI disables the checkbox while editing for the same reason — a live control the API will reject is a trap.

**`governingPolicy` is the ONE resolution rule; do not write a second.** Both callers go through it — the change-request engine via `policyRow`, and the write gate via `matchingPolicy`. They previously each ran their own query, and this feature initially shipped working in the engine and **invisible to the gate**: merges demanded approval while a direct `/graphql` mutation on the same namespace wrote straight through. Two implementations of one authorization rule diverge toward *open*, which is the direction that leaves no symptom. Caught by `TestGlobalPolicy_GatesANamespaceCreatedAfterIt`; the weaker-namespace test had been passing for the wrong reason (nothing was gated at all), which is why "the suite is green" was not the check that found it.

Changing which types a policy covers is a `PATCH` to that policy — **Edit** on the row, prefilled from what is stored. A second `POST` for the same namespace is a **409** saying so. The edit path is not optional polish: without it the only route to "also protect Rack" is delete-and-rebuild from memory, and the 409's own hint would be advice the UI cannot follow. The namespace is disabled while editing, because moving it is deleting one policy and creating another.

*Not supported, and the thing that would justify revisiting it:* **exclusions** — "every type except X". Picking 17 of 19 is miserable and is also the wrong policy, since it does not cover future types. It needs a schema change plus a precedence rule of its own, so it waits for evidence someone wants it.

### "Change control" is the mechanism; "change management" is the adopter's practice

Orbital provides **change control** — a gate on the write path that refuses an unreviewed change. It does **not** provide **change management**, the surrounding practice: CAB, scheduling, risk assessment, reviewer routing, notifications, post-implementation review. That belongs to whatever ITSM the adopter already runs, and calling orbital's feature "change management" would over-claim it — the exact failure the *compose, don't replace* posture exists to prevent. Say: *"orbital enforces change control at its write path; it slots under your existing change-management process."*

"Control" is also the precise word from **configuration management**, orbital's actual discipline: configuration control is the CM function governing how changes to a **baseline** are proposed, evaluated and approved, and orbital's baseline is design intent.

*Known wrinkle:* ITIL v3 said "change management"; ITIL 4 (2019) renamed it "change control"; ITIL 4 (2020) renamed it again to "change enablement", on the grounds that *control* sounded too restrictive. Someone ITIL-fluent may flag the term as dated. Orbital uses it in the older configuration-management sense, which is the accurate one for a gate over a baseline — the ITIL 4 rename was a philosophical repositioning, not a semantic correction.

### The term is "change request", from NetBox and ServiceNow

Both call this object a **change request**, and they are the two most widely deployed systems in orbital's category, so the vocabulary is already familiar to the operators and integrators most likely to arrive with an opinion. Rejected: **"pull request"** (GitHub's term — VCS-shaped, and orbital has no fork/branch to pull from), **"proposed change"** (InfraHub's — accurate, but the less-recognised name of the three), and **"proposal"** (the working name during design; generic, and it never says *change to what*).

**The name is borrowed from ServiceNow; the enforcement model is NOT.** ServiceNow's change request sits *beside* the CMDB — the store commits immediately and unauthorised writes are caught after the fact by detection. Orbital gates the write path itself, which is NetBox's model ("a branch cannot be merged unless it has an approved change request"). Sharing ServiceNow's vocabulary must not be read as sharing its architecture; see `change-control-research.md` for the survey behind that split.

**`merge` is the terminal verb, not `apply`.** NetBox's word, and deliberately not orbital's own **guarded Apply** (`expectedContentHash` on export, `OCI.md`), which governs the *publish* boundary. Two different gates — authoring vs release — and reusing one verb for both would conflate them.

### Change requests are identified as `<namespace>-<number>`

`colo-42`. Per-namespace numbering, so every data center counts from 1 and an id pasted into chat says which site it is about. The surrogate key is a plain auto-increment `bigint` and is **never exposed** — the API accepts and returns the human id everywhere, including `POST` responses, `/change-requests/colo-42`, `details.changeRequestId` on audit events, and `changeRequestId` in `/proposed-changes`.

**Why not a UUID.** GitHub (`#123`), GitLab (`iid`) and Jira (`PROJ-42`) all pair a machine id with a short human one, and three of them use integers for the machine id. UUID bought nothing here: orbital has one PostgreSQL, no distributed writes, and its own backup/restore covers **DGraph**, not this database — so the sequence-reuse-after-restore hazard that justifies UUIDs elsewhere does not arise. What was actually missing was a human id, which is a different problem from the key type.

**Why hyphen and not `:`.** `orbId` namespace-qualifies with `:`, and this deliberately does not follow it. A colon is invalid in a Kubernetes **label value** (which is why `orbId` had to move to an annotation), cannot start a relative-URL path segment without being read as a scheme (RFC 3986 §4.2), and is illegal in Windows filenames and git refs. There is no reason to carry a known-problematic separator into a new identifier.

**Parsing is unambiguous despite hyphenated namespaces.** Split on the LAST hyphen: the number is always the final segment and always digits, so `alaska-dot-cruiser-42` and even `dc-2-42` resolve correctly. `splitCRID` is the one implementation; `crHumanID` is the one formatter, kept beside it so they cannot drift.

**Allocation is `max(number)+1` per namespace, made safe by a UNIQUE index on `(namespace, number)`** — two concurrent creates can read the same max, but only one insert survives and the other retries against the now-higher max (`createNumbered`, 3 attempts). A counter table with `INSERT … ON CONFLICT … RETURNING` would avoid the retry, but it is a second source of truth for a number the rows already carry, and a row deleted by hand would leave it pointing past reality. **Numbers are never reused**: `max()+1` skips gaps, because an id that once meant one change must never come to mean another.

**Namespaces are now formally immutable.** Renaming one strands every change-request reference, exactly as renaming a Jira project key does. This was already true in practice — `orbId` embeds the namespace — but it is now load-bearing in a second place.

`approvals` and `merge_attempts` keep UUID primary keys: nobody says those out loud. They were switched to **UUIDv7** (time-ordered, so inserts append rather than scatter); the older tables still default to v4 and migrate under the sweep in `docs/planning/debt.md`.

### The queue identifies a request by id, and states its CHANGE

Columns are `ID · Change · Namespace · Author · Status · Approvals · Age`. **The id is first and always shown** — it is the only column guaranteed to differ between two rows, and it is what someone quotes when asking a colleague to look at one.

**The Change column renders `effect` from the API — it does not compute one.** Every response carries `effect: {entities, fields, orbId, type, field, before, value, cleared}`. `orbId`/`type` are present whenever exactly one entity is touched; `field`/`before`/`value` only when exactly one field is.

Named `effect`, not `summary`, for two reasons: `/diff` already returns a differently-shaped field called `summary` (`{added, removed, modified, unchanged}`), and two shapes under one name on adjacent endpoints is a trap; and `effect` names the distinction the field exists for — what the request would DO, as against `changes`, which is what it says. It matches the column that stores it (`base_effect`). Nothing upstream has a convention to copy here: git and GitHub call counts a *diffstat* and Terraform a *plan summary*, but all of them are counts only, while this states the change outright when there is exactly one. So a row is two branches — `server-CWJHDX3 · enabled → true` or `server-1W8Y2Z3 · 6 fields` — not a walk over `changes`. That walk is the point: **any client building a queue would otherwise re-implement it**, which is the bespoke-client-logic smell the API-first rule names by hand. It also means the row follows an amended changeset, where a stored string cannot.

**The field is NOT qualified by type in the cell.** The orbId sits beside it and already names the kind, because orbIds are conventional `<kind>-<natural-key>` — `server-maintenance-CWJHDX3 · enabled` says enabled on what. `type` stays in the response for callers that want it.

**`title` is a count, and belongs on the detail page** (`changesetTitle`): `server-1W8Y2Z3 · 6 fields`. It used to name the field and inline the value, which was a second copy of what the row now states — written once at creation, never recomputed. A count cannot drift into a lie the way a value can. The title should eventually be written by the PROPOSER (backlog); deriving it is a placeholder, not a design.

**Age is relative** (`just now`, `6m ago`), with the absolute date on hover. Two requests made minutes apart both read `Aug 30, 2026` under date granularity, which is exactly when you most need to tell them apart.

**Three tabs: `Needs my review` · `Open` · `Closed`.** State is the axis that partitions — `Open` is non-terminal, `Closed` is merged OR rejected OR withdrawn, and the Status column names which. GitHub's repo PR list collapses the same way: how a request ended is a property of the row, not of the list. `Needs my review` is a SHORTCUT to the actionable subset, not a state, and it exists because it is what the nav badge counts — clicking a badge must land somewhere visibly filtered. It renders only for `CanMutate` (`RoleAtLeast(dev)`, the same minimum the API enforces): readonly can never approve, so `?awaiting_review=true` returns zero rows for them by construction and the tab would be permanently empty; readonly lands on `Open`.

Do NOT re-add `Mine` or `All`. Both overlapped the state tabs while looking like peers of them — the defect that made five tabs unreadable — and `Author` is already a column, which answers "what did I propose" by eye at this volume. **Orbital has no reviewer assignment**, so "needs my review" resolves to *open, minus the ones I wrote*; it is an actionable filter, not an inbox someone routed to you.

Catching an accidental duplicate at CREATION is separate and still open — see `docs/planning/backlog.md`.

### A changeset records what the author CHANGED — not the whole entity

`buildChangeset` narrows every `update` item to fields whose value actually moved (`changedOnly`). The mutation path still sends an entity's whole scalar payload, which is harmless for DGraph — writing a field its current value is a no-op — but a changeset is not a mutation: a reviewer reads it, and **merging writes every field it names**. A six-field payload for a one-field edit claimed authority over five fields nobody touched, and every count derived from it read six. MVCC was the only thing preventing it from reverting a colleague's concurrent write.

**Narrowed against the SNAPSHOT taken when the modal opened, never against current intent.** Only the editor knows which fields this person touched. The server can only ask "does this differ from intent *now*", which is a different question with a worse answer: if a colleague edited a field while the modal was open, comparing against current intent would KEEP the stale value and silently revert their write on merge.

`create`/`upsert` items are not narrowed — a new entity legitimately needs every field.

**The API still accepts a full end-state on purpose.** A reconcile-style client (orbctl, AEP) asserting a complete desired state is legitimate, so orbital does not reject or rewrite a wide changeset — no declarative system does (Terraform, Kubernetes and Argo all accept complete state and compute the delta separately).

### `effect` is captured with the base — `base_effect`

A queue row must say how much a reviewer is approving, and payload width is a bad proxy for it: a reconcile client's changeset can name 22 fields and change one. So the delta is computed ONCE at creation, against the same snapshot `base_hash` is captured from, and stored in `base_effect` (`storedEffect` in `changerequest_base.go`).

That pairing IS a saved Terraform plan — a point-in-time delta plus the state anchor it was computed against, refused on apply when state moved. Orbital's derived `stale` is the refusal half, already built. GitHub stores diff stats the same way; `kubectl diff` and Argo recompute instead, which is the expensive shape.

- **Stored, not derived, and that is consistent with derive-don't-maintain** — that rule governs state that changes ON ITS OWN (staleness, approval validity). A delta is a fact about a moment, exactly like `base_hash`.
- **Recomputed on `PATCH`**, which re-anchors the base anyway. Carrying the old delta forward would describe changes the request no longer proposes.
- **Never fails a create.** `storedEffect` returns its error for logging only; a nil effect falls back to deriving one from the changeset (`resolveEffect`). A display convenience must not cost someone a validated proposal — and that same fallback is what every row written before this field always used.
- **`before` only exists on the effect path.** A payload states what a field becomes, never what it was, so a row rendered from the fallback shows `→ after` and is still correct.
- **Field names are unqualified.** `graphdiff` yields `Server.hostname` because a diff spans entities; the summary carries `type` separately, so the prefix is stripped and both paths produce one shape.
- **Do NOT** call `/diff` per row (N+1 of the expensive path: a subtree query AND a round trip each), recompute on every read (pays continuously for a number that changes only on a write), or reject/normalise wide changesets on write (breaks the client the full-state contract exists for).

Pinned by `TestCREffect_*` — read back through `ListChangeRequests`, including the negative (a payload that changes nothing reports 0) and the fallback. Producers are named for their source (`effectFromDiff`, `effectFromChangeset`, chosen by `resolveEffect`) so the real thing and the approximation can never be mistaken for each other.

**Watch the parameter chain.** The narrowing shipped broken once: `buildChangeset` did it correctly, the editor passed `rootBefore`, and `proposeChange` — which sits between them and destructures its parameters — forwarded a fixed list that dropped it. The pure-function test passed the whole time. A test calling a helper directly cannot see a caller that never reaches it; the e2e that drives the real editor is what caught it.

### Seeing what is already proposed

Three surfaces, one purpose: stop someone retyping an edit a colleague already proposed.

- **Banner** on the entity's detail page — how many requests are open for it *or anything it owns*, linked **by id** (`colo-58`), never by title. A title is a stored string that can describe a changeset since amended; an id cannot go stale, and it is what people quote to each other. The banner says *something is in flight and here is the way through* — WHICH field and what value are the field marks' job, on the row where they are actionable. `GET /api/v1/change-requests?orbId=…` (repeatable).
- **Field marks** on the rendered fields — which field, and what is proposed for it. **Two facts only: the value, and a Review link.** Author and age were on the row and are not: four facts crowded the cell in every layout — a dedicated column would have been *narrower* than the inline space, so it relocates the wrapping rather than fixing it — and neither is why anyone scans the table. Knowing a change is proposed is what stops you retyping it; who proposed it is the first thing on the review page one click away. `terraform plan` makes the same cut (`~ field = old -> new`). The verb carries the status, so `approved →` says "merges next" without a second clause. Spacing is ONE flex gap on `.js-field-mark-text`, never per-part `ml-*`: the parts are conditional, so per-part margins put the same fact at a different distance depending on what else rendered. `GET /api/v1/proposed-changes?orbId=…`, keyed by orbId. Wired on the **Server Summary** table (the server's own six editable scalars) and the **maintenance panel** (the owned child). A table opts in with `data-field-orbid` + `data-field-values`; a row opts in with `data-field="<name>"` and a `.js-field-mark` slot. Field names must match the type's `FormFields` in `configitems/registry.go` — those are the keys a changeset uses. Edge references (Data Center, OOB IP, Rack) get NO slot: the editor cannot write them, so a mark there could never fire. Marks shipped 2026-08-30 wired to the maintenance panel alone, so a proposal on `Server.manufacturer` raised the banner and annotated nothing.
- **Tab dots** on a detail tab whose panel holds a proposed field — because a field mark is invisible until you open the tab holding it, so a proposal on a server's maintenance window was findable only by clicking through every panel. **Presence, not a count:** at tab level the question is binary — one field or three, you open it either way — and counts across tabs invite a comparison that encodes nothing. Danger-coloured when anything inside conflicts, which is the same condition the marks escalate on, so the two surfaces cannot tell different stories. Carries `title`/`aria-label`, since at this size colour is the signal and cannot also be the only one.

**A mark is an index INTO the proposal, never a second value.** One proposal names its value, author and age; two or more show a count; disagreement is called out. Rendering `enabled false (pending 'true')` reads as a fact when it is one of two competing claims, and a proposal touching five fields would be scattered across five rows with no way to see it as the unit a reviewer approves. Every comparable product does the same — [Infrahub](https://docs.infrahub.app/topics/proposed-change) puts field detail in the proposed change's own diff view, [NetBox branching](https://netboxlabs.com/docs/extensions/branching/) switches you into a branch context, MediaWiki's pending-changes uses a page banner.

**A tab declares the orbIds it covers; it never scrapes rendered marks.** `data-panel-orbids` on the `<li>`, read in the same pass that fills the field marks and from the same `/api/v1/proposed-changes` response — one request for the page, not one per panel. Scraping `.js-field-mark` children would pass every test today because every server panel renders eagerly, and would break silently the first time one goes lazy. **Suppression must come from the same source as the rows** (`data-field-values`): two sources let a tab claim more than the panel beneath it shows, and a dot that fires for a no-op is always on, which trains people to ignore it. **Only panels that are field/value tables get dots** — Storage and Network render *lists of child entities*, carry no `data-field-orbid`, and are marked in `docs/planning/debt.md` as unable to surface a proposal at all. Audit is excluded deliberately: nothing is ever proposed in a history view.

**Why `/proposed-changes` is separate from `/change-requests`.** The second answers *"list the requests"*; the first answers *"what is proposed about these entities"*. Deriving one from the other means inverting it — every request, every change item, every key in `set` — then grouping by (orbId, field) and comparing values to spot conflicts. That walk is exactly what the export-preview burn taught us not to leave to clients. It is also **PostgreSQL-only**: the change-request list renders each row, and rendering derives staleness at a DGraph round-trip apiece, which is unaffordable on every page load.

**Overlaying is a map lookup, not a join.** `orbId` is `@id` on the ConfigItem interface — globally unique across every type — so a client reads entities from `/graphql`, reads proposals from `/proposed-changes` **in parallel**, and does `proposals[node.orbId].fields[name]`. Orbital's own UI does exactly this and has no privileged path.

**The client suppresses no-ops; the API cannot.** A proposal whose value already equals the current value changes nothing — someone applied the same edit directly, or the two reads straddled a merge. `/proposed-changes` reads PostgreSQL only, so it has no idea what the current value is; the client holds both halves and drops the mark. The request still appears in the banner: it still needs closing or merging. `conflicting` is likewise recomputed client-side over the *surviving* proposals — the API's flag counts them all, and two proposals of which one is already true leave one live claim and no conflict.

**On the read skew.** The two calls hit two stores at two instants, so a merge landing between them can show a stale value or a proposal for a value already applied. Neither is a decision point — the gate re-resolves the policy inside `writeToDGraph` and merge re-derives staleness — and the no-op rule erases the visible case. Note that a single merged endpoint would **not** fix this: there is no transaction spanning DGraph and PostgreSQL, so it would narrow the window, not close it. The race is inherent to two stores, not to two calls.

### Policy administration is audited

Create, update and delete each write a `management` audit event: actor, the namespace as resource id, `before`/`after` of every policy field, and `enforcementStopped` when `enabled` went true→false. Readable at `GET /api/v1/audit-log?operation_name=createApprovalPolicy` (or `?resource_id=<namespace>`) and on the audit-log page.

**Why it is not optional:** policy administration decides what needs review *at all*, so it is the most consequential act in the feature — and it was the only part leaving no trace. A bypassed write was audited; removing the policy that would have gated it was not. A delete carries the whole policy because afterwards the event is the only record it existed. A **refused** write records nothing: a trail entry for a change that never took effect is worse than none.

### Bypass belongs to the POLICY, not the user

`approval_policy.bypass_roles` (default `["admin"]`). Orbital's role model stays `readonly < dev < admin` with no per-user capability flags — "who may bypass" is a question the admin answers per protected class. A caller in `bypass_roles` also skips `proposer ≠ approver`: demanding a second pair of eyes from someone who could have written directly is friction with no control value.

**A REFUSED write is NOT audited — only logged.** `403 APPROVAL_REQUIRED` writes no `audit_events` row, by design: `AUDIT.md` § Row admission admits a row only when state changed or a security event occurred, and a policy refusal is neither (the caller *has* the role; the workflow says "not yet"). The caller is present and holds the error, and the durable record is the write that eventually lands — NetBox and GitHub both work this way. So *"how many writes were blocked pending review"* is an app-log question, not an audit-log one. **The one exception, when it ships, is advisory enforcement** — a would-have-been-blocked write has no caller reading the result, so it must be recorded.

**A privileged write is recorded in the AUDIT LOG, not just logged.** `details.privileged` + `details.bypassedPolicy` land on the mutation's own audit event. A `logger.Warn` alone was the first implementation and it was wrong: "who bypassed review last quarter" is asked from `/api/v1/audit-log` by someone with no prior suspicion. See `AUDIT.md`.

**A bypass-capable caller gets BOTH destinations in the editor, and review is the primary one.** The footer shows `Propose change` (green, first) *and* `Save directly` (solid amber — caution, and distinct from both the green primary and the plain Cancel). Holding the bypass role previously meant losing review entirely — one button that wrote through — so an admin who wanted a second pair of eyes on a production edit had to give up the role to get one. Being *allowed* to skip review is not the same as wanting to. A caller who may NOT bypass still gets one button: a second control that always 403s trains people to click through errors. `applyGateState` owns this for all four edit modals — do NOT add a propose button to a page's modal wiring. Pinned by `e2e/change-requests.spec.ts` "gets both actions, with Propose change as the primary".

**The two labels ARE the notice — privileged mode renders no prose.** There used to be a paragraph explaining that this role may write directly; it said what the buttons already say, since the word *directly* set against *Propose change* is itself the statement that one path skips review. Prose was a third thing to read before a choice the labels had already made obvious. The one fact a label cannot carry — that the bypass is recorded — is the `title` on `Save directly`. This does NOT weaken "stated, never silent" (above): that rule was written when Save was the only button and read identically to an ungated one. The gated notice ("Needs approval") stays, because there the button alone cannot say that nothing changes until someone approves.

**Both buttons share one submit handler**, differing by a `forcePropose` flag. Recomputing the changeset in a second handler would be the one place the two paths could disagree about what the user edited — and if the flag were ever dropped, Propose would fall through to the ordinary dispatch, write straight to the graph, and produce the audit row of a legitimate bypass with no trace that review was asked for. It would look like it worked. Pinned by "an admin's Propose change opens a request and writes nothing".

### Derive, don't maintain — there is no `stale` column and no stored `approved`

Both staleness signals are recomputed on **every** read: `subtreeChanged` hashes the current scope and compares to `base_hash`; `stale` compares each change object's `version` to its node's current version. Not event-driven, because orbital has no event bus and marking hooks would be needed in four places — one of which (`dropAll` restore) cannot be hooked per-entity at all. Every missed hook is a request that silently claims to be fresh. The derived version has **zero** hook sites.

**The anchor is the scope's VERSION VECTOR, not its content.** `base_hash` hashes sorted `orbId@version` pairs (`scopeVersions` + `versionHash`), read with one DQL query over two predicates. It replaced a full subtree fetch + normalize + content hash, which `State` paid **per rendered request** — and the nav badge renders the open queue on every page load, so that cost multiplied by two factors at once.

Sound because `version` is orbital's OCC counter and every write through orbital bumps it: `graphql.go` stamps `set.version` on update and `version: 1` on create, and merge's `applyItem` does the same. A vector that moves is intent that moved.

**The limit, accepted deliberately:** a write reaching DGraph *without* passing through orbital never bumps the counter and is therefore invisible to staleness. Orbital owns intent, so a writer going around it has already left the model this check describes — and the same counter already backs the `version` conflict check on the mutation path, so trusting it here is consistent rather than novel. `TestCR_OutOfBandWriteThatSkipsTheVersionCounterIsNotSeen` pins both halves (out-of-band invisible, orbital-mediated seen) so the trade is a decision on the record. If direct-to-DGraph writes ever become real, that test is what has to change.

Content is still fetched, but only by the two callers that need node values: the diff view and merge, via **`StateWithSnapshot`**. `State` itself never populates `Snapshot`. Adding a third caller that reads `st.Snapshot` from `State` gets an empty map, not a slow path — check which one you are calling.

`approved` is **not in the status enum** (`open | rejected | merged | closed`). An approval counts only while its `approved_at_hash` matches the current hash, so "approved" is `valid_approvals >= required` evaluated at read time — and reverts to `open` on its own when the base moves. Leaving it out of the enum makes the wrong state unrepresentable rather than merely discouraged.

Approvals are **hash-stamped, not dismissed**: the row survives and renders as *"approved an earlier version"*, which is information. Silently vanishing approvals look like the system lost someone's review.

**The scope expands DOWNWARD only.** `baseScope` takes the declared orbIds and adds each one's *owned* subtree (`collectRelatedOrbIDsBatch`) — never its parent. So a request declaring `colo:server-X` is anchored on the server **and** its iDRAC and maintenance children, while a request declaring only `colo:X-idrac` is anchored on the child alone. Editing the child makes a server-targeting request stale; editing the server does **not** make an iDRAC-targeting request stale. That follows the ownership direction and is intended — a child's meaning does not depend on its parent's scalars — but it surprises people who assume "same server" means "same scope."

**An approval re-anchors `base_hash`** to what the reviewer just looked at, which is what clears `subtreeChanged`. Without this such a request could never leave that state — re-review made its approvals count again while the flag stayed true forever and merge refused it. Since 2026-09-04 this clears **only** `subtreeChanged`: `stale` is computed from the changeset, so no re-approval can move it. Sound because `/diff` is always computed against CURRENT intent, so approving *is* an attestation of the current state.

**`required: 0` (no policy) reads as approved**, so a voluntarily-opened request in an ungoverned namespace still merges — guarded only by staleness, which is exactly guarded-Apply semantics.

**Terminal requests do not derive staleness.** A merge moves the graph, so a just-merged request would otherwise report itself stale with zero approvals.

### Merge applies items ONE AT A TIME, and a partial merge is self-correcting

Not a choice — measured. **Two root fields in one mutation are not atomic** (the first commits when the second fails), and **a nested object under an edge LINKS rather than deep-writes** (`firmwareVersion` stayed `1.0.0` after a mutation that claimed to set it to `9.9.9`, returning success). A multi-entity changeset therefore cannot be one atomic mutation. **Do not try to "fix" merge into a transaction.**

So a partial merge is a first-class outcome, and **there is no `merge_failed` status**:
- the request stays `open`; a `merge_attempt` records what each item did
- what applied stays applied; re-merging is a no-op for it
- **approvals survive iff the base moved by exactly the items this merge applied** — re-read the version vector after applying and compare (`movedOrbIDs`); if every moved orbId is one we applied, rebase `base_hash` and carry the valid approvals forward, otherwise leave it and let it go stale. Must use the same anchor as `State`: a rebase that wrote a CONTENT hash here would store a value `State` can never reproduce, leaving the request permanently stale

A transient failure costs one retry click; a genuine third-party write still forces re-review. *Conservative in one case, deliberately:* writing an edge also updates the inverse edge on its target, so if both ends are in the same changeset the untouched end reads as third-party drift. Rare, and the cost is one extra approval — the safe direction.

**Whose limitation this is, precisely.** DGraph's *GraphQL layer* scopes a transaction to ONE root field — every GraphQL mutation starts its own transaction, so two root fields commit independently. **Not a database limitation:** DGraph is ACID, and Hasura wraps a whole request in one Postgres transaction, so this is a choice of DGraph's generated API rather than a property of graph stores. Writing via **DQL** would put orbital in control of the transaction, but only by reimplementing type→predicate mapping, `@id` upsert semantics, schema validation and `version`/`updatedAt` stamping — coupling orbital to DGraph's internal model, against both "do not replace DGraph" and keeping DGraph invisible to clients. **And it would still not be atomic end to end**, because a merge writes DGraph *and* PostgreSQL and no transaction spans the two. Atomicity is unreachable here regardless of how the DGraph half is written, which is why the design converges instead.

**The answer to give an adopter who asks why writes are not transactional:**

> Orbital doesn't offer transactional multi-entity writes, and neither does `kubectl apply`, `terraform apply`, or Argo. It's a declarative store: changes are expressed as desired end-state, applied per item, **recorded per item**, and **safe to re-apply**. What you get instead of rollback is that you can always see exactly what landed, and retrying is a no-op for what already did.

Non-atomic multi-object application is the **norm** for declarative infrastructure, not an orbital quirk — `kubectl apply` of five objects leaves the first two applied when the third fails; `terraform apply` records partial state and expects a re-run; Argo, Flux and Crossplane converge continuously and none roll back. Rollback belongs to imperative transactional systems. Convergence is also not a local excuse for this one path: the changeset is target end-state, staleness and `approved` are derived on every read rather than stored, and divergence is reconciled the same way — the whole product recomputes from current state.

*Known window, accepted:* between a partial merge and its retry, intent holds part of a reviewed change, and nothing blocks a publish in that gap — the export preview would faithfully show the partial state and allow it. Small (re-merge closes it) and not worth pursuing atomicity for, but if it ever matters the cheap fix is surfacing "merge incomplete" in the preview, not a transaction.

Merge guard order is **TARGET_MISSING → stale → not-approved**, most specific first. A deleted target is the only one whose remedy is not "review it again"; merging anyway would rebuild the entity from a partial field-delta — a successful-looking merge that quietly corrupts data.

### Changeset contract

Target **end-state**, not a mutation replay — with an **optional entity-level precondition**. `set` names the values you want, never a delta; `version` names the entity's `version` as you read it. Supplying it makes the item conditional; omitting it makes it unconditional. See "Preconditions" below. One entity per item; **an edge value may carry only `orbId`/`id`**. A nested entity is rejected at creation with a `400` — see the link-not-deep-write measurement above. `type` is optional on input (resolved from `orbId`, which is `@id` on `ConfigItem` and therefore globally unique) and always present on output. `op` is explicit (`upsert | update | delete`), never inferred: a reviewer approving "update" must not have it silently become "create".

**Validated at CREATION**, against the **deployed schema via DGraph introspection** — not `schema/schema.graphql`, because the file is what this build *would* deploy while introspection is what is deployed *now*, and those differ after a restore. `<Type>Patch` gives the settable fields; `Add<Type>Input`'s NON_NULLs give create requirements. Validating here means a proposal that could never apply never reaches a reviewer — their attention is the scarce resource this whole feature spends.

**Edge targets are validated too.** A reference to an orbId that does not exist is not a dangling link — DGraph reads it as a nested CREATE and fails the whole mutation complaining about the *target's* required fields. An item may reference an entity an **earlier** item creates; merge applies in order.

### How we arrived at this shape

Asked 2026-09-02 — *"is there a name for this convention?"* Recorded because the shape looks borrowed from Kubernetes and is not, and the difference matters when someone extends it.

**Three DGraph constraints forced it, in this order:**

1. **DGraph LINKS on an edge; it does not write through it.** Sending `idracSettings: {firmwareVersion: "9.9.9"}` returns **success and writes nothing**. So edges are identity-only and the changeset is **flat — one item per entity**. This is the reason, not a style preference.
2. **`orbId` is `@id` and globally unique**, so `type` is resolvable from it and an item needs no path. The id *is* the address — which is also why there is no JSON-Pointer-style `path` field.
3. **`set: null` is a no-op in DGraph**; clearing requires a `remove`. Hence `clear[]` as a sibling of `set`, rather than RFC 7396's null-means-delete.

`op` is explicit for a different reason — a reviewer approving "update" must not have it silently become "create" because someone deleted the entity mid-review.

**Names: three conventions, not one.** Nothing here is invented, and nothing here is one named standard:

| Part | Convention |
|---|---|
| `set` | **JSON Merge Patch (RFC 7396)** — a partial document of target values. One deviation: `clear[]` instead of `null`, per constraint 3 |
| the `changes[]` envelope | **id-in-body batch patch** — `[{id, …fields}, …]`. **NetBox's bulk `PATCH` is exactly this**, and NetBox is the domain peer, so it outranks the Kubernetes comparison |
| `version` | **`If-Match` (RFC 9110)** / Kubernetes `metadata.resourceVersion` in a patch body — object-grain OCC, and the same token orbital's own `/graphql` already accepts |

**A worked example** — bumping a server's iDRAC firmware. `type` is absent because `orbId` is `@id` and orbital resolves `IdracSettings` from it; supply `type` only when creating an entity that does not exist yet. `version` is optional, and it is the difference between a refusal that names this entity and one that only says something in scope moved:

```json
POST /api/v1/change-requests
{
  "title": "Bump 5HSC3D4 iDRAC firmware to v3",
  "namespace": "2f-uae",
  "changes": [
    { "orbId": "2f-uae:5HSC3D4-idrac",
      "op": "update",
      "set": { "firmwareVersion": "v3" },
      "version": 7 }
  ]
}
```

**Against NetBox specifically**, since the envelope is borrowed from it and the resemblance is easy to over-read:

| | Aligned |
|---|---|
| Envelope | An array of per-object patches keyed by id **in the body** — NetBox's bulk `PATCH` on a list endpoint (`[{"id": 1, "name": "x"}, …]`) is structurally identical |
| Semantics | A partial document of **target values**, not a delta or an op-list. RFC 7396 in both |
| Granularity | One object per item; related objects are referenced, never written through. NetBox patches a device and its interfaces separately, which is our edge-carries-only-`orbId` rule |

| | Divergent | Why |
|---|---|---|
| **The proposal itself** | NetBox has **none** — a PATCH applies immediately | The proposal mechanism in that ecosystem is the **branching plugin**, and it is not a payload shape: it is a real Postgres schema per branch that you write to with ordinary API calls, whose "changeset" is derived by replaying the branch changelog at merge. Orbital stores the changeset **as** the artifact because it cannot materialize a branch — `orbId` is `@id`, so a copy cannot coexist with main's |
| **Key** | NetBox `id` is a database PK; orbital's `orbId` is a derivable natural key | A client must look up a NetBox id before it can patch; an orbId is constructed with no round trip, which is what makes `upsert` idempotent |
| **`op`** | NetBox infers it from the HTTP verb | All three kinds arrive in ONE batch inside one POST here, so the verb cannot carry it — and a reviewer approving "update" must not have it become "create" |
| **Clearing** | NetBox uses `null`; orbital uses `clear[]` | DGraph treats `set: null` as a no-op |
| **Type** | NetBox's URL names it (`/api/dcim/devices/`); orbital resolves it from `orbId` | `orbId` is globally unique across every type |
| **`version`** | **No NetBox equivalent** | NetBox core is last-writer-wins on writes; `prechange_data` is a record, not a precondition. Orbital's is a precondition, and the same one `/graphql` accepts |

**On the Kubernetes comparison — correct the premise before reusing it.** *"A k8s patch does not require previous values or a version"* is Kubernetes' **default**, not its only mode. It has three opt-in preconditions: `metadata.resourceVersion` **inside the patch body** (enforced by the apiserver), JSON Patch's `test` op (per-path, fails the whole patch), and SSA's `force: false` → 409 against another field manager. So `version` is the FIRST of those three, at the same object grain, spelled with orbital's own field name — not a foreign concept bolted on. (Until 2026-09-03 orbital also had a field-grained `before`, matching the second; it was removed once `version` covered the entity-level question. Field-grained protection survives at merge, from the server-recorded ancestor.)

**Last-writer-wins is fine for the PAYLOAD and not for the MERGE DECISION — do not fuse them.** These are separate properties and conflating them is what makes the design look contradictory:

- **Payload:** declarative target end-state. No base is needed to *interpret* it, which is why a stale changeset is never rewritten — `set` still says what the author wants.
- **Merge:** guarded. `base_versions` names the entity that moved; `base_values` + three-way-per-field names the field; both refuse before anything is applied.

**And the reason the second is necessary is the one thing Kubernetes genuinely lacks.** It is *not* "approval and review" — Kubernetes has validating/mutating admission webhooks, ValidatingAdmissionPolicy, OPA and Kyverno, all real policy enforcement on writes. What it lacks is **asynchronous, human-latency review**: admission control decides inside the write, in milliseconds, while orbital holds a proposal for hours or days during which the graph keeps moving. LWW is safe exactly when read → decide → write is one instant. Stretch it across a human's attention span and it silently destroys whichever write lost the race — with a reviewer's signature on the losing one.

> **Kubernetes' concurrency model assumes the decision is instantaneous. Orbital's is not, so orbital must store an ancestor Kubernetes can do without.**

One known limit of the shape is filed in `docs/planning/debt.md` § Architecture: a changeset cannot distinguish asserted intent from incidental fields.

### Divergence Accept REACTS to the gate

It dispatches with `gateEnforce` and, on an `APPROVAL_REQUIRED` refusal, opens a change request pre-filled from the entry and returns **`202 Accepted`** with a `changeRequestId`, recording **no resolution** — intent has not moved. One authority; asking the policy separately would be a second copy of the rule to keep in sync. Reject and Ignore are never gated: neither touches intent.

The report's `type_name` is a **hint**, not a requirement — orbital resolves the type from the orbId. The old "update intent manually" `422` is gone; the gate made that advice circular.

### `?status=` is repeatable and OR-ed, and an unknown value is refused

`status=merged&status=rejected&status=closed` is every terminal state. There is **no aggregate keyword** for it — the three stored values already say it, and a coined term would have to be learned (`active` is the one exception, and only because `approved` is *derived* from the approval count and cannot be named as a stored value).

An unrecognised value returns `400`. The switch this replaced had no default, so `status=Merged` matched nothing, applied no predicate, and returned the ENTIRE queue — the same silent-wrong-answer shape as a truncated filter and harder to spot, because the response looks right. Pinned by `TestValidStatusFilter` / `TestStoredStatePredicates` / `TestStatusWanted`.

### `?orbId=` filters in SQL, before rendering

Rendering a row derives staleness, so filtering afterwards paid DGraph round-trips for rows that were never going to be returned. Fine for a queue page; ruinous for the pending-change lookup that runs on **every** detail view and usually matches nothing. Now jsonb containment against a GIN index, before any render. `TestListFilter_NonMatchingOrbIDQueryMakesZeroDGraphCalls` pins it on a counting stub — move the filter back after `render()` and every result stays correct while the badge becomes the most expensive call in the product.

**`awaiting_review` is narrowed the same way**, and it is the one filter with no namespace or orbId to narrow by — the nav badge fires it on every page load. Two of the three reasons a row is discarded are knowable in SQL: a `readonly` caller can never approve (short-circuits to an empty response before any query), and you cannot approve your own request. The second exclusion is applied **only when no enabled policy grants the caller's role bypass** (`roleBypassesAnyPolicy`, one query for all rows) — bypass makes `approve` available on your own request, so excluding unconditionally would silently under-count. `TestAwaitingReview_BypassRoleStillSeesTheirOwnRequests` is the exactness guard.

Measured on the local stack, one badge call with six open requests: **0** DGraph queries for their author and for a readonly caller, 18 for a reviewer who must see all six.

Use `sql.P` + `b.Arg()`, never `sql.ExprP` with `?` — ent does not substitute the placeholder and ships a literal `?` to Postgres.

**Measured:** the GIN index is insurance, not a live optimisation. At 1 row and at 5,000 Postgres correctly prefers a seq scan; at 50,000 it chooses `Bitmap Index Scan` on its own. Today's win is entirely the ordering.

**`orbId` is repeatable and the values are OR-ed — and the LIST is the caller's to build.** `?orbId=a&orbId=b` matches every request touching any of them, the same semantics as `/api/v1/audit-log`. Reading it with Echo's `QueryParam`, which returns only the FIRST value, was a live bug: the editor's pending-change notice asked about a server and got an answer about a server, while the changeset that existed named `<ns>:server-maintenance-<serial>` — **an edit to an owned child records the CHILD's orbId and never the parent's**, so the notice stayed silent on exactly the entity someone was about to edit again. The parent→child knowledge stays in the page composer that already pulled the subtree (`collectRelatedOrbIDs` → `data-related-orb-ids`, shared with the audit tab so the two surfaces cannot disagree about what "this server" covers) — **do not add subtree traversal to this handler**, for the reason in `AUDIT.md` § "REST audit-log API is node-specific".

**Over 128 orbIds is REFUSED (`400 BAD_USER_INPUT`), never truncated** — on this endpoint and on `/api/v1/audit-log`, which share one `maxOrbIDFilter`. A truncated filter answers a question nobody asked and is indistinguishable from a correct answer: the same silent-wrong-answer failure the repeatable form exists to remove.

**The cap is sized from measurement.** The largest real owned subtree in the seeded `colo` namespace is 35 orbIds — a populated server, dominated by storage devices and network interfaces — so 128 is ~3.5x headroom. The original 32 sat *below* that, which meant ordinary data hit it: `/audit-log` truncated silently (the Server audit tab was dropping its overflow children with no signal) and the change-request lookup needed client-side chunking. Both are gone; `activeChangeRequestsFor` sends one request.

**If a caller ever legitimately needs more, the exit is a POST-with-body read** — the shape [Prometheus `/api/v1/query`](https://prometheus.io/docs/prometheus/latest/querying/api/) and Elasticsearch `_search` use for queries too long for a URL. **Not** client-side chunking, which pushes an overlap-aware union into every consumer (chunks overlap in their results: a request naming both a server and its iDRAC comes back in two of them, so counts can never be summed). **Not** server-side subtree expansion, which `AUDIT.md` rules out — which orbIds a view rolls up is the caller's decision, not orbital's.

**`status=active`** means not-terminal (open + approved). It exists because `approved` is derived, so `status=open` excludes an approved-but-unmerged request — and "does this entity have a change in flight" has no other single-call answer. Making a client OR two queries together would put orbital's lifecycle logic in the client.

**No per-node endpoint.** Comparable systems all filter the collection (GitHub `?head=`, K8s field selectors, NetBox `?changed_object_id=`, Terraform `?filter[status]=`). GitLab's sub-resources hang off resources that already exist in its API, and orbital has no REST `/config-items/` parent to hang one off.

### UI

**The Change Control pages are client-rendered**, unlike Users and Divergence Reports which query the DB in their handler. Deliberate: orbital's UI is a *consumer* of these public endpoints, never a privileged path. The payoff is that the review view renders buttons straight from `availableActions`, so eligibility lives in exactly one place.

**The nav badge is asynchronous** (`layout.MenuItem.BadgeSrc`). "Awaiting MY review" needs each candidate rendered, and the menu is on every page — computing it inline would make every page load pay for a change-request scan. The count is `total` from the same endpoint the item links to, so badge and page cannot disagree.

**The propose path FLATTENS what the mutation path folds.** The editor folds a brand-new child subtree into its parent's `set` as a nested object — legal for a DGraph mutation, forbidden in a changeset. `buildChangeset` re-derives the same edit flat: wrapper, then children, then the root referencing the wrapper by `{orbId}`. It is **not** a translation of the `calls` array.

`clear` carries the field NAME; the mutation path's `remove` carries its prior VALUE. Lossless — merge re-reads the current value, and if it moved the request is stale and will not merge.

**A refusal is a redirect, never a dead end.** `403 APPROVAL_REQUIRED` from a Save offers to open a change request with the edit already in hand. Declining leaves the modal open and the edit intact.

## Validating locally

```bash
make up            # deps
make run-orbital   # :8001
make seed          # after orbital is up
```

`make seed` creates the cast this feature needs:

| Account | Password | Why it exists |
|---|---|---|
| `admin@armada.ai` | `admin` | In `bypass_roles` by default — writes through, audited as privileged |
| `dev@armada.ai` | `dev` | Gated by policies. The proposer. |
| `dev2@armada.ai` | `dev` | The second reviewer. **Peer review needs two peers** — without this you can only ever test `required_approvals: 1`. |
| `user@armada.ai` | `user` | readonly |

**Never change a role with `psql`.** `PUT /api/v1/users/:id/role` (the *Users* page) is the
shipped path, it has a last-admin guard, and writing to the table directly means you are
validating a route no operator takes.

**1.** As **admin**: *Change Control ▸ Approval Policies ▸ Add policy* — namespace `2f-uae`,
Scope left at **All types in this namespace**, 1 approval. The row shows *All types* and its
control must show **Active**. Try *Add policy* for `2f-uae` a second time: **409**, one policy
per namespace. Untick All types and pick none: refused in the modal, which stays open.
Then **Edit** the row — namespace locked, everything else prefilled — narrow it to `Server`
and the table shows the list rather than *All types*.

**2.** As **dev** (private window): open a server in `2f-uae` and edit `hostname`. Save
reads **Propose change**. Saving closes the modal and leaves you on the server, where the
pending-change banner now names the request and links to it — proposing is a save, not a
navigation. The
graph is untouched:

```bash
curl -s localhost:8080/graphql -H 'Content-Type: application/json' \
  -d '{"query":"{ getServer(orbId:\"2f-uae:server-5HSC3D4\"){ hostname } }"}'
```

**3.** As **admin**: open it from *Change Requests*. The diff is against **current** intent,
and **Approve** is offered to you but was not to the author. Approve, then **merge as the
dev** — a dev merging what they could not write directly is the exemption that stops merge
deadlocking.

**4. Staleness.** With another request open and approved, edit the same server straight in
DGraph, bypassing orbital:

```bash
curl -s localhost:8080/graphql -H 'Content-Type: application/json' \
  -d '{"query":"mutation{ updateServer(input:{filter:{orbId:{eq:\"2f-uae:server-5HSC3D4\"}}, set:{model:\"CHANGED\"}}){ numUids } }"}'
```

Reload: **stale**, the approval still listed but marked *approved an earlier version*, and
merge returns `409 MVCC_CONFLICT`. Nothing was notified — the next read recomputed.
Approving again recovers it.

**5. N-of-M.** Set the policy to 2 approvals and `bypassRoles` to a role nobody holds.
`dev` proposes, `dev2` approves (1 of 2, merge → 409), `admin` approves (2 of 2), merge
succeeds. This is what the second seeded dev exists for.

**6. The bypass is recorded.** As admin, save a field directly. It writes through, and:

```bash
curl -s -b cookies.txt 'localhost:8001/api/v1/audit-log?limit=1' \
  | jq '.events[0].details | {privileged, bypassedPolicy}'
# {"privileged": true, "bypassedPolicy": "2f-uae"}
```

**7. The scope rule holds below the API.** The either/or is a CHECK constraint, so it
survives a caller that never touches the handler:

```bash
psql "postgres://orbital:orbital-local-dev-secret@localhost:5432/orbital?sslmode=disable" \
  -c "INSERT INTO approval_policies (id, action_type, namespace, all_types, types, required_approvals, bypass_roles, enabled, created_at, created_by)
      VALUES (gen_random_uuid(),'config.mutation','probe', true, '[\"Server\"]'::jsonb, 1, '[\"admin\"]'::jsonb, true, now(), 'psql');"
# ERROR: violates check constraint "approval_policy_scope_exclusive"
```

**Clean up:** delete the policy in the UI. Nothing else to undo — the seeded accounts are
permanent fixtures.

## Gotchas

- **A comment asserting a property is not evidence of it.** The privileged-write bypass shipped with `// recorded as such` directly above a `logger.Warn`, and every re-read confirmed the claim instead of checking it.
- **Running the Go integration suite wipes DGraph** (`ResetDGraphE` in the handler package's `TestMain`), destroying e2e fixtures. Re-seed between them.
- **Approval policies and in-flight change requests are GLOBAL test state.** One policy row changes how every editor in the app behaves. Tests must establish the state they need, not assume it.
- **`upsert: true` with an edge ref to a non-existent orbId** attempts a nested create and fails on the target's required fields. Create the target first.

## Deferred, do not re-litigate

- **Branching / isolated workspaces.** NetBox proves branching on Postgres; the DGraph path would be branch-as-changeset, which is what a change request already is. Revisit only if live-browsable isolated workspaces become a real need.
- **Amend from the UI.** The API supports `PATCH`; the review view omits Edit. Amending invalidates every approval anyway, so close-and-reopen gives the identical outcome for one extra click — while amend would cost a whole new editing surface.
- **The unfiltered queue page** renders every row. Tracked in `docs/planning/debt.md` with the cheapest next fix named. The default tab narrows in SQL first, so only "All" is expensive.

---

**Why orbital builds this at all** — the landscape survey across InfraHub, NetBox, ServiceNow, Terraform Cloud, GitHub and Vault — is in [`change-control-research.md`](./change-control-research.md). Point-in-time, non-normative.

*The full deliberation — 33 numbered decisions with the measurements that produced them, including two that were reversed — is in git history: `git log --diff-filter=D -- docs/spikes/spike-36-*`.*
