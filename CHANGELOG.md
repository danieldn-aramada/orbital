# Changelog

Notable changes to **orbital** (cloud), **orb** (edge), and **orbctl** (CLI).

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are
[semantic](https://semver.org/), tagged per component:

| Component | Tag prefix |
|---|---|
| orbital (server) | `v*` |
| orb (edge) | `orb/v*` |
| orbctl (CLI) | `cli/v*` |

**This file is the source of truth**, not the GitHub release page. Orbital targets air-gapped
deployments — an operator holding the tarball and no network connection must still be able to read
what changed. GitHub Release bodies are generated from this file, never the other way round.

> Releases before **v0.0.31** (2026-08-12) predate this changelog. For that history see
> `git log` and `git tag --sort=-creatordate`; the delivery timeline is in `ROADMAP.md`.

---

## [Unreleased]

### Added
- **Staleness is now two signals, and only the author can clear the one that matters.**
  `stale` means at least one change object's `version` no longer matches its node — a fact about
  what the *author* proposed. `subtreeChanged` means the reviewed scope moved without any change
  object going out of date, typically an edit to an owned child — a fact about what the *reviewer*
  looked at. Both block merge. Previously these were one flag, which let a reviewer clear, in one
  click, a proposal written against a value that had since moved: approving re-anchored the request,
  `stale` went false, and the merge wrote an outdated value over someone else's edit. The two facts
  have different remedies, so a single flag could only ever offer the wrong one to somebody.
  **Re-approving no longer clears `stale`** — the author rebases, by `PATCH`ing the changeset with
  the current `version` or dropping the object. A request can now be `approved` *and* `stale`, in
  which case `availableActions` drops `merge` and offers `edit` to the author. A rebase dismisses
  approvals **even when the graph has not moved**, because the reviewer approved a different
  proposal — the same rule as GitHub's "dismiss stale approvals on push", with the version vector
  standing in for the commit sha. An item sent without a `version` can never be item-stale and is
  guarded by the scope anchor alone, exactly as before.
- **The concurrency precondition is `version`, not `ifVersion`** *(breaking; renamed before any
  external client adopted it).* A precondition named after the node's own field is what Kubernetes
  (`resourceVersion`), Google Cloud (`etag`), Firestore, DynamoDB, Hibernate and plain SQL all do;
  `if`-prefixing is an HTTP **header** idiom (`If-Match`) that reads wrong on a body field. It could
  not collide with the `version` orbital stamps, because a changeset rejects `version` inside `set`
  outright and on `/graphql` the two sit at different nesting levels — the same separation
  Kubernetes relies on between `metadata` and `spec`. Applies everywhere the token appears:
  `/graphql` variables, change-request items, and `DELETE /api/v1/config-items/{type}/{id}?version=`.
  **A client still sending `ifVersion` is refused `400` naming the new field** rather than having its
  guarantee silently dropped. A mutation that declares its own `$version` is also refused — orbital
  adds the predicate itself, and a hand-written one gets no conflict detection.
- **Change-request preconditions are now one concept, not two — `before` was removed.**
  An item's optional precondition is `version`, the entity's `version` as you read it: the same
  token `/graphql` mutations accept, meaning the same thing. The field-level `before` a client used
  to send is gone. It existed because server-side version stamping was unreliable on one write path,
  and that is fixed — every writer now stamps — so it had become a second concurrency vocabulary for
  a question already answered. **Field-level protection is unchanged at merge**, where it comes from
  the ancestor orbital records itself (`base_values`) rather than from anything a client asserts:
  a merge still refuses per field, naming the field, and still drops already-satisfied fields from
  the write. **What this costs:** a creation-time refusal is now entity-grained, so a third party
  editing a *different* field of the same entity while you compose refuses your proposal where
  `before` would have accepted it — reload and propose again. It also retires the whole-value
  false-conflict on composite JSON fields that `before` could not express. Orbital's editor sends
  `version` per item on the propose path; a create sends none, since there is no version to match.
- **A stale change request now says which entity moved.**
  `base_hash` anchors a request to the version vector of its scope, so it has always refused a
  merge whose intent moved — but it is a fingerprint, so it could only ever say "something
  changed", leaving an operator to go and find out what. Requests now also store the vector that
  hash is computed from, and a stale merge returns `409 MVCC_CONFLICT` with a `problems[]` entry
  per moved entity carrying its orbId and both versions. This applies to every request, including
  ones whose author sent no precondition, and the error still wraps the same sentinel so the
  status, code and existing clients are unchanged. The vector is re-captured wherever the hash is
  — create, amend, the rebase on approval, the rebase after a partial merge — so re-reviewing a
  stale request still clears it in one click. A merge also now carries the version it planned
  against into each write, so an edit landing between planning and the write refuses that item
  rather than overwriting it.
- **A change-request item can carry `version` — the same concurrency token `/graphql` accepts.**
  `POST /api/v1/change-requests` items take an optional `version`: the entity's `version` as you
  read it. If the entity has moved since, the request is refused with `409 MVCC_CONFLICT` naming
  the item and both versions, instead of being accepted and only failing at merge — where
  `base_hash` refuses wholesale and can never say which entity moved. It is entity-level where
  the existing `before` is field-level, and the two stay distinguishable: an entity-level problem
  carries no `field`. Honoured on `op: delete`, which `before` skips by construction — a delete is
  where a stale precondition costs most, since it destroys work with no diff to recover it from.
  One per item, never per field. Omission stays legal and leaves the item guarded by the scope
  anchor alone. Supplying it against an entity that does not exist is refused at validation rather
  than ignored — a caller that asked for a check and silently did not get one is worse off than
  one that never asked. Duplicate orbIds in a changeset were already refused for ordering reasons;
  the message now also gives the second reason, which is that two preconditions on one entity
  cannot both hold.
- **Change Requests — maker-checker approval for config mutations (Spike 36, backend).**
  `POST /api/v1/change-requests` proposes a set of ConfigItem changes as target end-state;
  a peer other than the author approves it; `POST .../merge` applies it MVCC-guarded.
  `GET .../diff` returns a flat, render-ready diff of current intent vs the proposal.
  Protected classes are declared via `/api/v1/approval-policies` — **one policy per
  namespace**, covering either every type or a named list — and are **opt-in**: with no
  policy, nothing changes. Enforcement is per policy; there is no global switch.
  Notable properties: a changeset is validated against the *deployed* schema at creation, so a
  proposal that could never apply never reaches a reviewer; staleness and approval validity are
  derived on every read rather than stored, so no hook can miss a write; a partially applied
  merge stays open with a recorded per-item attempt and is safe to retry without re-approval
  unless someone else wrote to a covered entity.
  **Enforcement is live**: a mutation on a protected class is refused with
  `403 APPROVAL_REQUIRED` and a hint pointing at the change-request endpoint, unless the
  caller's role is in the policy's `bypass_roles` — in which case the write goes through
  and is logged as a *privileged write*. The check sits in the single function every
  DGraph write passes through, so it covers `/graphql` and internal dispatch alike;
  merging an already-approved change request is the one exemption. A divergence
  **Accept** on a protected class now returns `202 Accepted` with a `changeRequestId`
  and leaves the entry pending instead of mutating intent.
  `GET /api/v1/change-requests?status=` is **repeatable and OR-ed** (`status=merged&status=rejected&status=closed`);
  an unrecognised value is refused with `400` rather than silently matching everything.
  Every change-request response carries an **`effect`** — how many entities and fields it
  changes, and the field with its before/after when there is exactly one — so a client can
  render a queue row without walking the changeset. It reports what the request would DO, not
  what it says:
  the delta is computed once at creation against the same snapshot the staleness anchor comes
  from, so a client that posts a complete end-state (a reconcile flow) still gets `1 field`
  when one field differs.
  **A proposal now carries only the fields the author actually changed.** The editor previously
  sent an entity's whole scalar payload, so a one-field edit opened a request claiming six
  fields: the diff was right, every count derived from the payload was not, and merging would
  have written all six.
  **UI**: a *Change Control* section with the review queue and an admin policies page;
  a review view showing the diff, the approvals (including "approved an earlier version"
  when one no longer counts) and only the actions the caller may actually take; and the
  config editor relabels **Save → Propose change** on a protected class, opening a change
  request instead of writing, and leaving you on the entity — where a banner names the
  request and links to it. A caller whose role may bypass the policy gets **both** actions —
  *Propose change* as the primary and *Save directly* beside it, so holding the bypass role no
  longer means losing access to review. An entity with a change already
  in flight says so before you start editing it, including when the change targets an owned
  child such as its iDRAC or maintenance window — named by request id, which never goes stale.
  Field marks now cover a server's own fields on the Server Summary table, not only its
  maintenance panel, and sit in a column of their own.
  **A detail tab whose panel holds a proposed field now carries a dot**, so a change to a
  server's iDRAC or maintenance window is visible without clicking through every panel —
  red when two requests inside disagree. A proposal that already matches current state
  does not light it.
  The review queue shows three tabs — *Needs my review* (hidden for roles that cannot approve),
  *Open*, *Closed* — and lists each request by **title**, with a narrow column giving the change's
  size (`1 field`, `12 fields`) beside it.
  The review view follows the server detail page's shape: the title, then a row of rounded
  actions, then the diff in a wide panel with a Details summary beside it and one Activity panel
  below, with status rendered the same way as on the queue. Its activity is one chronological
  table rather than separate reviews and merge-attempt lists. The queue remembers which tab you
  last opened.
  **The proposer now writes the title.** The propose footer carries a title input, prefilled with
  the entity name and replaceable in one gesture; leaving it alone or clearing it stores that same
  entity name. The generated title no longer appends a field count — the queue's `Change` column
  states that, and requests created before this carry the old `<entity> · N fields` form.
  Titles are capped at 255 characters — the length the `title` column always enforced, which until
  now surfaced as a 500 instead of a 400 — and an open request can be **renamed in place** from
  the review page, which patches the title alone and
  leaves the changeset, the staleness anchor and every approval untouched.
  **A review view now warns when another active request proposes the same field**, naming and
  linking each competing request and the fields it overlaps on, and distinguishing a competitor
  proposing a *different* value — whichever merges first wins the field, and the other is refused
  until re-reviewed — from one proposing the *same* value, where one of them merges as a no-op.
  Until now the collision was invisible until a merge, at which point the other request silently
  went stale and its approvals stopped counting.
- **Config-item edits are guarded against concurrent writes again.** The editor sends
  `version`, so saving a form someone else has changed underneath you is refused with
  `409` and a reload prompt instead of silently overwriting their edit. This had been lost
  since 2026-06-20, when the per-page edit modals — each of which passed `version` — were
  replaced by the shared editor module, which did not carry it forward. Nothing failed in
  between: the check is opt-in server-side, so a client that omits it looks exactly like one
  that declined it. Covers a data center, cluster, network device, server and its iDRAC and
  maintenance settings.

- **Approval policies can govern every namespace.** A policy is now created with either a
  namespace or `allNamespaces: true`; the second covers every namespace, **including data
  centers onboarded after the policy was written**. Resolution is **fallback**: a namespace
  with its own policy uses that one instead, even when it is weaker, and a namespace whose
  policy is **disabled** is not gated at all. So an all-namespaces policy is a **default,
  not a floor**. Exactly one policy still governs any write, so "which policy did this?"
  keeps a single answer. The policies page marks a namespace row that is overriding the
  global one.
- **Artifact-to-artifact compare** — `GET /api/v1/export/compare?from=&to=` returns the
  desired-state delta between any two published artifacts of a data center, pulled by immutable
  digest and diffed in memory. Surfaced as a **Compare** tab on Publish History with linkable
  `?from=&to=` URLs.
- **Export preview + guarded apply** — `POST /api/v1/export/preview` returns the content diff
  between current intent and the last published artifact; `expectedContentHash` on
  `POST /api/v1/export` rejects with `409 MVCC_CONFLICT` when intent moved since the preview.
- **Filters on `GET /api/v1/oci/artifacts`** — `dc` (data-center orbId), `status`, `limit`.
  Without them the endpoint was capped at 100 rows across all data centers, so a busy data center
  pushed others out of the response entirely.
- **Divergence badge** in the navigation, showing unresolved divergence entries.
- **`GET /api/v1/proposed-changes?orbId=…`** — what is proposed for a set of entities, keyed by
  orbId and indexed by field, so a client can overlay proposals onto entities read from
  `/graphql` with a map lookup rather than inverting the change-request list itself. Reads
  PostgreSQL only, so it is safe on every page load. Powers per-field marks on the server
  detail page: a field with a change in flight names the proposed value, its author and a link
  to the review; several proposals show a count, and ones that disagree are called out.
- **Change requests are identified as `<namespace>-<number>`** (`colo-42`) instead of a UUID —
  per-namespace numbering, so each data center counts from 1 and an id says which site it is
  about. Accepted and returned everywhere, including URLs. The queue lists the id first, titles
  each request by what it changes (`server-CWJHDX3 · maintenance.enabled → true`) rather than
  which entity it touches, and shows relative age.
- **Approval-policy administration is audited** — create, update and delete write `management`
  audit events carrying before/after and an explicit flag when enforcement stopped. Previously a
  write that *bypassed* a policy was recorded while changing the policy itself left no trace.
- **`?orbId=` is repeatable on `/api/v1/change-requests` and `/api/v1/audit-log`** (max 128),
  matching requests or events touching any of them — so a view can ask about an entity and
  everything it owns in one call.
- **`ORBITAL_CHANGE_CONTROL_ENABLED`** removes the change-control feature entirely — queue,
  policies page, endpoints and nav — for adopters running their own change management. Deletes
  no data; see `docs/reference/CONFIG.md`.
- **`ServerMaintenance` config item** (schema v6) — maintenance-window flag per server.
- **Request payload-size guard and rate limiting** on the API surface.

### Changed
- **`GET /api/v1/change-requests` filters `orbId` and `namespace` in SQL** (jsonb
  containment, GIN-indexed) instead of after rendering each row. Rendering derives
  staleness, so the old order paid several DGraph round-trips per row before discarding
  it — fine for a queue page, ruinous for the pending-change lookup that now runs on
  every detail view. A lookup matching nothing costs zero DGraph calls.
- **New filter value `status=active`** — not-terminal (open plus approved). Neither
  existing value answers "does this entity have a change in flight", because `approved`
  is derived and `status=open` excludes it.
- **Divergence Accept no longer needs the report to carry a type.** An entry whose
  `type_name` is empty used to fail with "update intent manually"; orbital now resolves
  the type from the entity's `orbId` (`@id` on the `ConfigItem` interface, so globally
  unique). The remaining failure — an `orbId` orbital has never seen — reports that
  instead, since it is a different problem with a different remedy.
- **Audit tables renamed** `events` → `audit_events` (with `audit_event_resources`,
  `audit_event_resource_types`); ent types and handler renamed to match. The API path
  `/api/v1/audit-log` is unchanged. Rationale in `docs/reference/AUDIT.md`.
- **Audit retention is now operator-configured** rather than an assumed 12 months. Orbital ships no
  prescribed period.
- **`ROADMAP.md` reduced to a one-page status view**; spike definitions moved to
  `docs/planning/backlog.md` and technical debt to `docs/planning/debt.md`.

### Fixed
- **Concurrent writes to one entity could be silently lost even when the caller asked for a
  concurrency check.** `version` was compared in Go against a snapshot fetched by a separate
  request, then the mutation was written with a filter on `orbId` alone — two DGraph transactions,
  so a writer committing in between was overwritten with no trace. Reproduced in the test suite:
  12 concurrent writers all reported success against the same starting version, and 11 writes
  vanished while `version` advanced once. Orbital now injects `version: { eq: $version }` into the
  mutation's own filter, so the comparison happens inside the write; the loser gets
  `409 MVCC_CONFLICT` instead of a success it did not get. Requires `schema/VERSION` **v7**, which
  adds `@search` to `ConfigItem.version` — **apply the schema before rolling out the code**; the
  wrong order refuses mutations with a clear message rather than writing anything. A mutation whose
  shape cannot carry the predicate is now refused rather than sent unguarded, which also means an
  `version` sent with a create is rejected instead of quietly dropped. Change-request merge
  inherits the guard. Repeated transaction aborts — more likely now that writers contend on the same
  predicate — are retried, and a persistent one surfaces as `503 WRITE_CONTENTION`, deliberately
  distinct from `MVCC_CONFLICT`: the request is still valid, the store was busy.
  The same change closes a second hole: a supplied `version` that orbital could not evaluate —
  a failed pre-read, or a mutation touching more than one type — used to be dropped and the write
  reported success. It is now either enforced by the database or refused, so there is no path where
  a caller asks for a concurrency check and silently does not get one.
- **The cascade-delete endpoint bypassed approval policies entirely.**
  `DELETE /api/v1/config-items/{type}/{id}` planned its cascade and wrote to DGraph directly, so it
  never passed through the function where the approval check lives. Measured on the same entity,
  same policy and same caller seconds apart: `updateDataCenter` was refused `403 APPROVAL_REQUIRED`
  while the `DELETE` returned `200` and removed it. The delete now asks the same policy question,
  with the same status, code and hint the GraphQL path returns — and it asks it about every type the
  cascade would remove, read from the planned nodes rather than declared per branch, so a policy
  protecting only an owned child refuses the parent delete that would take it. The same endpoint
  also accepts `?version=`, so a delete confirmed from a dialog that has been sitting open is
  refused `409` rather than landing on whatever the entity has since become; the delete modal sends
  the version it displayed and keeps itself open on a conflict. Omitting it stays unconditional, and
  a namespace with no policy is unaffected.
- **A divergence Accept wrote intent without bumping `version`, so it was invisible to change-request staleness.**
  `base_hash` is a hash of the scope's `orbId@version` vector — a write that does not move a
  version does not move the hash. An open change request covering the accepted entity kept
  reporting `stale: false`, an approval cast *before* the Accept kept counting, and the merge
  proceeded against a state nobody had reviewed. The Accept was in the audit log throughout, so
  nothing looked wrong; it was found only by asking why an approved request still looked fresh.
  Two causes, both fixed. The **write pre-flight** — the before-fetch, the
  `version`/`updatedAt`/`updatedBy` stamping and the opt-in `version` check — lived in the
  `/graphql` handler, which internal dispatchers do not go through, so each was left to the
  caller and both callers forgot a different one (the change-request merge had passed an
  incomplete before-state, rendering raw variables where a field diff belongs). All three now
  live in `writeToDGraph`, the single function every write already passes through for the
  approval-policy check, so no caller opts in and none can forget. And Accept dispatched its
  mutation with a `$filter` object, while the row to stamp is resolved from the `orbId`
  variable — it now uses the canonical `update{Kind}($orbId, $set)` shape every other writer
  uses. An Accept consequently bumps the version, stamps the acting identity on the node, and
  carries a field-level diff in the audit log; Reject and Ignore still touch nothing.
  A caller-supplied before-state is now a *fallback* merged under the fetch rather than a
  replacement for it, which keeps the diff for fields outside a type's `BeforeFields` selection.
- **A change request could overwrite an edit it never reviewed, and could merge as a no-op.**
  Staleness was entity-level: `base_hash` fingerprints a version vector, so it answered "did
  anything in scope move" but never "what was it", and a write that changed a value without
  bumping `version` — a direct DQL write, a restore, `make seed` — left it matching. A changeset
  item can now carry **`before`**, the values the caller read, and orbital records the ancestor
  (`base_values`) for the fields the changeset touches. Every field then resolves to *applies*,
  *already satisfied*, or *conflicts*: a conflict refuses the whole merge before anything is
  applied, naming the field; an already-satisfied field is dropped from the write, so merging a
  request someone else beat you to costs no version bump and writes no audit row for a change that
  changed nothing. The write is narrowed to match the guard — a field-level check paired with a
  whole-`set` write would push stale values over other people's edits. Orbital's own editor now
  sends `before` for the scalars it changes, so an edit landing while someone has a modal open is
  refused at creation and named rather than silently absorbed. A refused action renders the
  per-field detail it already receives — a merge conflict now says which field moved, from what,
  to what, instead of "state moved since you read it".
- **A merged change request logged no field diff in the audit trail.** Merge read only `version`
  plus the fields it was about to clear, so the audit event's `before` never contained the fields
  being *set* — the diff had nothing to intersect and the row fell back to dumping raw mutation
  variables, while the same edit saved directly through `/graphql` showed `-false / +true`. Merge
  now reads the scalar fields it writes, so a merged change and a direct save produce the same
  audit row.
- **The Config Item inventory went permanently empty once a namespace filter was saved.** With
  `stateSave: true`, DataTables restores saved state *during* the constructor, so `initComplete`
  ran before `const inventoryTable` had bound and threw a TDZ `ReferenceError` out of the
  constructor — taking the data fetch with it. Only reproduced once both a saved DataTables state
  and a saved namespace filter existed, which is why a fresh profile looked fine. `initComplete`
  now uses `this.api()` rather than the outer binding.
- **The Config Item inventory could also show "No data available in table" indefinitely.** A page loaded
  while the graph was empty — between `make up` and `make seed`, or before a restore finished —
  cached `[]` in `sessionStorage`, and because `"[]"` is a truthy string every later load treated
  it as a cache hit and skipped the fetch. Servers and Data Centers looked fine because they always
  fetch. An empty cached list is now a cache miss.
- **`/api/v1/audit-log` silently truncated an over-cap `orbId` filter**, so a resource panel whose
  subtree exceeded the limit dropped its overflow children with no signal — a Server audit tab
  looked complete and was not. Both filters now refuse over the cap instead of narrowing the
  answer, and the cap is sized above a real subtree (measured at 35 orbIds for a populated
  server; it was 32).
- **JSON-editor field clearing** — clearing a field is now a DGraph `remove`; `set: null` was a
  silent no-op, so cleared fields never persisted.
- **Handler integration suite** — 10 failures and an intermittent flake. Tests called handlers
  directly, bypassing Echo's error handler, so the JSON error envelope they asserted on was never
  rendered.
- **Stale end-to-end test fixtures** — specs referenced pre-migration server orbIds and failed
  after the `server-<serial>` convention landed.
- **Removed a dead restore-availability flag** and documentation for an `ORBITAL_RESTORE_BACKEND`
  environment variable that does not exist.
