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

### Bypass belongs to the POLICY, not the user

`approval_policy.bypass_roles` (default `["admin"]`). Orbital's role model stays `readonly < dev < admin` with no per-user capability flags — "who may bypass" is a question the admin answers per protected class. A caller in `bypass_roles` also skips `proposer ≠ approver`: demanding a second pair of eyes from someone who could have written directly is friction with no control value.

**A privileged write is recorded in the AUDIT LOG, not just logged.** `details.privileged` + `details.bypassedPolicy` land on the mutation's own audit event. A `logger.Warn` alone was the first implementation and it was wrong: "who bypassed review last quarter" is asked from `/api/v1/audit-log` by someone with no prior suspicion. See `AUDIT.md`.

### Derive, don't maintain — there is no `stale` column and no stored `approved`

Staleness is recomputed on **every** read: hash the current scope, compare to `base_hash`. Not event-driven, because orbital has no event bus and marking hooks would be needed in four places — one of which (`dropAll` restore) cannot be hooked per-entity at all. Every missed hook is a request that silently claims to be fresh. The derived version has **zero** hook sites.

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
- **approvals survive iff the base moved by exactly the items this merge applied** — re-read after applying and compare; if every difference is on an item we applied, rebase `base_hash` and carry the valid approvals forward, otherwise leave it and let it go stale

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

Use `sql.P` + `b.Arg()`, never `sql.ExprP` with `?` — ent does not substitute the placeholder and ships a literal `?` to Postgres.

**Measured:** the GIN index is insurance, not a live optimisation. At 1 row and at 5,000 Postgres correctly prefers a seq scan; at 50,000 it chooses `Bitmap Index Scan` on its own. Today's win is entirely the ordering.

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
blank type, 1 approval. The row must read **enforced**; *not enforced* means the gate flag
is off.

**2.** As **dev** (private window): open a server in `2f-uae` and edit `hostname`. Save
reads **Propose change**. Saving lands you on the new request's review page — and the
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
