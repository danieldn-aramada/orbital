# Change Control Reference

> **Audience:** anyone touching change requests, approval policies, the write gate, or the config editor's Save path.

Read this before: `internal/approval/`, `internal/handler/changerequest*.go`, `internal/handler/approval_gate.go`, the `approval_*` ent tables, `/api/v1/change-requests`, `/api/v1/approval-policies`, or `configitem-editor.js`.

Orbital's maker-checker layer: a **change request** proposes a set of ConfigItem changes, a peer approves it, and merging applies it. The engine underneath is action-type-agnostic — v1 implements one action type (`config.mutation`) and a second adds rows with a different `payload` shape plus its own adapter, with **no schema change** to the engine tables.

**Opt-in.** With no enabled `approval_policy`, every write behaves exactly as it did before this existed. Installing the feature changes nothing until an admin declares a protected class.

## Settled Decisions

### The gate lives in `writeToDGraph`, and has exactly ONE exemption

`GraphQL.writeToDGraph` is the single function every DGraph write passes through. The policy check is there — **not** in the `/graphql` handler, which is a chokepoint for CLIENTS but not for WRITES: divergence-Accept dispatches `update{Type}` internally and would have walked straight past a check placed in `Handle`.

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

Changing which types a policy covers is a `PATCH` to that policy — **Edit** on the row, prefilled from what is stored. A second `POST` for the same namespace is a **409** saying so. The edit path is not optional polish: without it the only route to "also protect Rack" is delete-and-rebuild from memory, and the 409's own hint would be advice the UI cannot follow. The namespace is disabled while editing, because moving it is deleting one policy and creating another.

*Not supported, and the thing that would justify revisiting it:* **exclusions** — "every type except X". Picking 17 of 19 is miserable and is also the wrong policy, since it does not cover future types. It needs a schema change plus a precedence rule of its own, so it waits for evidence someone wants it.

### Change requests are identified as `<namespace>-<number>`

`colo-42`. Per-namespace numbering, so every data center counts from 1 and an id pasted into chat says which site it is about. The surrogate key is a plain auto-increment `bigint` and is **never exposed** — the API accepts and returns the human id everywhere, including `POST` responses, `/change-requests/colo-42`, `details.changeRequestId` on audit events, and `changeRequestId` in `/proposed-changes`.

**Why not a UUID.** GitHub (`#123`), GitLab (`iid`) and Jira (`PROJ-42`) all pair a machine id with a short human one, and three of them use integers for the machine id. UUID bought nothing here: orbital has one PostgreSQL, no distributed writes, and its own backup/restore covers **DGraph**, not this database — so the sequence-reuse-after-restore hazard that justifies UUIDs elsewhere does not arise. What was actually missing was a human id, which is a different problem from the key type.

**Why hyphen and not `:`.** `orbId` namespace-qualifies with `:`, and this deliberately does not follow it. A colon is invalid in a Kubernetes **label value** (which is why `orbId` had to move to an annotation), cannot start a relative-URL path segment without being read as a scheme (RFC 3986 §4.2), and is illegal in Windows filenames and git refs. There is no reason to carry a known-problematic separator into a new identifier.

**Parsing is unambiguous despite hyphenated namespaces.** Split on the LAST hyphen: the number is always the final segment and always digits, so `alaska-dot-cruiser-42` and even `dc-2-42` resolve correctly. `splitCRID` is the one implementation; `crHumanID` is the one formatter, kept beside it so they cannot drift.

**Allocation is `max(number)+1` per namespace, made safe by a UNIQUE index on `(namespace, number)`** — two concurrent creates can read the same max, but only one insert survives and the other retries against the now-higher max (`createNumbered`, 3 attempts). A counter table with `INSERT … ON CONFLICT … RETURNING` would avoid the retry, but it is a second source of truth for a number the rows already carry, and a row deleted by hand would leave it pointing past reality. **Numbers are never reused**: `max()+1` skips gaps, because an id that once meant one change must never come to mean another.

**Namespaces are now formally immutable.** Renaming one strands every change-request reference, exactly as renaming a Jira project key does. This was already true in practice — `orbId` embeds the namespace — but it is now load-bearing in a second place.

`approvals` and `merge_attempts` keep UUID primary keys: nobody says those out loud. They were switched to **UUIDv7** (time-ordered, so inserts append rather than scatter); the older tables still default to v4 and migrate under the sweep in `docs/planning/debt.md`.

### The queue identifies a request by id, and titles it by what it changes

Columns are `ID · Title · Namespace · Author · Status · Approvals · Age`. **The id is first and always shown** — it is the only column guaranteed to differ between two rows, and it is what someone quotes when asking a colleague to look at one.

**Titles are derived from the changeset, not the entity** (`changesetTitle` in `configitem-editor.js`): `server-CWJHDX3 · ServerMaintenance.enabled → true`, or `server-CWJHDX3 · 2 fields` when several change. The old form was `'Update ' + orbId`, which named what was touched rather than what changed — so *every* edit to a given server produced the same title and the queue could not tell two different proposals apart, let alone two identical ones. Owned children are qualified by type, because `enabled` alone does not say enabled on what. A value is inlined only when it is a short scalar; a title is a label, not a payload dump.

**Age is relative** (`just now`, `6m ago`), with the absolute date on hover. Two requests made minutes apart both read `Aug 30, 2026` under date granularity, which is exactly when you most need to tell them apart.

Catching an accidental duplicate at CREATION is separate and still open — see `docs/planning/backlog.md`.

### Seeing what is already proposed

Two surfaces, one purpose: stop someone retyping an edit a colleague already proposed.

- **Banner** on the entity's detail page — how many requests are open for it *or anything it owns*, with links. `GET /api/v1/change-requests?orbId=…` (repeatable).
- **Field marks** on the rendered fields — which field, and what is proposed for it. `GET /api/v1/proposed-changes?orbId=…`, keyed by orbId.

**A mark is an index INTO the proposal, never a second value.** One proposal names its value, author and age; two or more show a count; disagreement is called out. Rendering `enabled false (pending 'true')` reads as a fact when it is one of two competing claims, and a proposal touching five fields would be scattered across five rows with no way to see it as the unit a reviewer approves. Every comparable product does the same — [Infrahub](https://docs.infrahub.app/topics/proposed-change) puts field detail in the proposed change's own diff view, [NetBox branching](https://netboxlabs.com/docs/extensions/branching/) switches you into a branch context, MediaWiki's pending-changes uses a page banner.

**Why `/proposed-changes` is separate from `/change-requests`.** The second answers *"list the requests"*; the first answers *"what is proposed about these entities"*. Deriving one from the other means inverting it — every request, every change item, every key in `set` — then grouping by (orbId, field) and comparing values to spot conflicts. That walk is exactly what the export-preview burn taught us not to leave to clients. It is also **PostgreSQL-only**: the change-request list renders each row, and rendering derives staleness at a DGraph round-trip apiece, which is unaffordable on every page load.

**Overlaying is a map lookup, not a join.** `orbId` is `@id` on the ConfigItem interface — globally unique across every type — so a client reads entities from `/graphql`, reads proposals from `/proposed-changes` **in parallel**, and does `proposals[node.orbId].fields[name]`. Orbital's own UI does exactly this and has no privileged path.

**The client suppresses no-ops; the API cannot.** A proposal whose value already equals the current value changes nothing — someone applied the same edit directly, or the two reads straddled a merge. `/proposed-changes` reads PostgreSQL only, so it has no idea what the current value is; the client holds both halves and drops the mark. The request still appears in the banner: it still needs closing or merging. `conflicting` is likewise recomputed client-side over the *surviving* proposals — the API's flag counts them all, and two proposals of which one is already true leave one live claim and no conflict.

**On the read skew.** The two calls hit two stores at two instants, so a merge landing between them can show a stale value or a proposal for a value already applied. Neither is a decision point — the gate re-resolves the policy inside `writeToDGraph` and merge re-derives staleness — and the no-op rule erases the visible case. Note that a single merged endpoint would **not** fix this: there is no transaction spanning DGraph and PostgreSQL, so it would narrow the window, not close it. The race is inherent to two stores, not to two calls.

### Policy administration is audited

Create, update and delete each write a `management` audit event: actor, the namespace as resource id, `before`/`after` of every policy field, and `enforcementStopped` when `enabled` went true→false. Readable at `GET /api/v1/audit-log?operation_name=createApprovalPolicy` (or `?resource_id=<namespace>`) and on the audit-log page.

**Why it is not optional:** policy administration decides what needs review *at all*, so it is the most consequential act in the feature — and it was the only part leaving no trace. A bypassed write was audited; removing the policy that would have gated it was not. A delete carries the whole policy because afterwards the event is the only record it existed. A **refused** write records nothing: a trail entry for a change that never took effect is worse than none.

### Bypass belongs to the POLICY, not the user

`approval_policy.bypass_roles` (default `["admin"]`). Orbital's role model stays `readonly < dev < admin` with no per-user capability flags — "who may bypass" is a question the admin answers per protected class. A caller in `bypass_roles` also skips `proposer ≠ approver`: demanding a second pair of eyes from someone who could have written directly is friction with no control value.

**A privileged write is recorded in the AUDIT LOG, not just logged.** `details.privileged` + `details.bypassedPolicy` land on the mutation's own audit event. A `logger.Warn` alone was the first implementation and it was wrong: "who bypassed review last quarter" is asked from `/api/v1/audit-log` by someone with no prior suspicion. See `AUDIT.md`.

### Derive, don't maintain — there is no `stale` column and no stored `approved`

Staleness is recomputed on **every** read: hash the current scope, compare to `base_hash`. Not event-driven, because orbital has no event bus and marking hooks would be needed in four places — one of which (`dropAll` restore) cannot be hooked per-entity at all. Every missed hook is a request that silently claims to be fresh. The derived version has **zero** hook sites.

**The anchor is the scope's VERSION VECTOR, not its content.** `base_hash` hashes sorted `orbId@version` pairs (`scopeVersions` + `versionHash`), read with one DQL query over two predicates. It replaced a full subtree fetch + normalize + content hash, which `State` paid **per rendered request** — and the nav badge renders the open queue on every page load, so that cost multiplied by two factors at once.

Sound because `version` is orbital's OCC counter and every write through orbital bumps it: `graphql.go` stamps `set.version` on update and `version: 1` on create, and merge's `applyItem` does the same. A vector that moves is intent that moved.

**The limit, accepted deliberately:** a write reaching DGraph *without* passing through orbital never bumps the counter and is therefore invisible to staleness. Orbital owns intent, so a writer going around it has already left the model this check describes — and the same counter already backs the `ifVersion` conflict check on the mutation path, so trusting it here is consistent rather than novel. `TestCR_OutOfBandWriteThatSkipsTheVersionCounterIsNotSeen` pins both halves (out-of-band invisible, orbital-mediated seen) so the trade is a decision on the record. If direct-to-DGraph writes ever become real, that test is what has to change.

Content is still fetched, but only by the two callers that need node values: the diff view and merge, via **`StateWithSnapshot`**. `State` itself never populates `Snapshot`. Adding a third caller that reads `st.Snapshot` from `State` gets an empty map, not a slow path — check which one you are calling.

`approved` is **not in the status enum** (`open | rejected | merged | closed`). An approval counts only while its `approved_at_hash` matches the current hash, so "approved" is `valid_approvals >= required` evaluated at read time — and reverts to `open` on its own when the base moves. Leaving it out of the enum makes the wrong state unrepresentable rather than merely discouraged.

Approvals are **hash-stamped, not dismissed**: the row survives and renders as *"approved an earlier version"*, which is information. Silently vanishing approvals look like the system lost someone's review.

**An approval re-anchors `base_hash`** to what the reviewer just looked at. Without this a stale request could never leave that state — re-review made its approvals count again while `stale` stayed true forever and merge refused it. Sound because `/diff` is always computed against CURRENT intent, so approving *is* an attestation of the current state.

**`required: 0` (no policy) reads as approved**, so a voluntarily-opened request in an ungoverned namespace still merges — guarded only by staleness, which is exactly guarded-Apply semantics.

**Terminal requests do not derive staleness.** A merge moves the graph, so a just-merged request would otherwise report itself stale with zero approvals.

### Merge applies items ONE AT A TIME, and a partial merge is self-correcting

Not a choice — measured. **Two root fields in one mutation are not atomic** (the first commits when the second fails), and **a nested object under an edge LINKS rather than deep-writes** (`firmwareVersion` stayed `1.0.0` after a mutation that claimed to set it to `9.9.9`, returning success). A multi-entity changeset therefore cannot be one atomic mutation. **Do not try to "fix" merge into a transaction.**

So a partial merge is a first-class outcome, and **there is no `merge_failed` status**:
- the request stays `open`; a `merge_attempt` records what each item did
- what applied stays applied; re-merging is a no-op for it
- **approvals survive iff the base moved by exactly the items this merge applied** — re-read the version vector after applying and compare (`movedOrbIDs`); if every moved orbId is one we applied, rebase `base_hash` and carry the valid approvals forward, otherwise leave it and let it go stale. Must use the same anchor as `State`: a rebase that wrote a CONTENT hash here would store a value `State` can never reproduce, leaving the request permanently stale

A transient failure costs one retry click; a genuine third-party write still forces re-review. *Conservative in one case, deliberately:* writing an edge also updates the inverse edge on its target, so if both ends are in the same changeset the untouched end reads as third-party drift. Rare, and the cost is one extra approval — the safe direction.

Merge guard order is **TARGET_MISSING → stale → not-approved**, most specific first. A deleted target is the only one whose remedy is not "review it again"; merging anyway would rebuild the entity from a partial field-delta — a successful-looking merge that quietly corrupts data.

### Changeset contract

Target **end-state**, not a mutation replay. One entity per item; **an edge value may carry only `orbId`/`id`**. A nested entity is rejected at creation with a `400` — see the link-not-deep-write measurement above. `type` is optional on input (resolved from `orbId`, which is `@id` on `ConfigItem` and therefore globally unique) and always present on output. `op` is explicit (`upsert | update | delete`), never inferred: a reviewer approving "update" must not have it silently become "create".

**Validated at CREATION**, against the **deployed schema via DGraph introspection** — not `schema/schema.graphql`, because the file is what this build *would* deploy while introspection is what is deployed *now*, and those differ after a restore. `<Type>Patch` gives the settable fields; `Add<Type>Input`'s NON_NULLs give create requirements. Validating here means a proposal that could never apply never reaches a reviewer — their attention is the scarce resource this whole feature spends.

**Edge targets are validated too.** A reference to an orbId that does not exist is not a dangling link — DGraph reads it as a nested CREATE and fails the whole mutation complaining about the *target's* required fields. An item may reference an entity an **earlier** item creates; merge applies in order.

### Divergence Accept REACTS to the gate

It dispatches with `gateEnforce` and, on an `APPROVAL_REQUIRED` refusal, opens a change request pre-filled from the entry and returns **`202 Accepted`** with a `changeRequestId`, recording **no resolution** — intent has not moved. One authority; asking the policy separately would be a second copy of the rule to keep in sync. Reject and Ignore are never gated: neither touches intent.

The report's `type_name` is a **hint**, not a requirement — orbital resolves the type from the orbId. The old "update intent manually" `422` is gone; the gate made that advice circular.

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
