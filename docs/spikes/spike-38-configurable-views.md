# Spike 38 — configurable views: the page structure as runtime configuration

**Status:** **DECIDED, NOT IMPLEMENTED** — model ratified 2026-09-24. P0 is sequenced AFTER Stage 1
of the schema-derived fields work, because its write-time validation ("does this type and field
exist?") reuses that introspection resolver rather than building a second source of truth.
**Date:** 2026-09-24 (rewritten same day; the original scoped views as per-user display filtering,
which is now deferred — see "What changed, and why").
**Related:** `.local/dynamic-schema-fields-plan.md` (schema-derived fields — the same move one layer
down, sequenced alongside), `docs/reference/DGRAPH.md` (Spike 33 amendment),
`docs/reference/CHANGE-CONTROL.md` (scope pinning).

---

## The model, in one line

> **Orbital's UI is like a shared Grafana dashboard. You change the view, you change what everyone
> else sees. For now.**

A **view** defines a detail page: its root type, and which children are included. It is
**deployment-wide**, **runtime-definable**, and **admin-edited** — not a per-user preference.

Two layers, and only two:

| Layer | Decides | Where | Who |
|---|---|---|---|
| **Schema** | what exists | DGraph | product |
| **View** | which roots exist, and what each includes | VC defaults + Postgres **overrides** | deployment admin |

There is no third per-user layer. There is no separate "ownership policy" tier underneath — see below.

## What changed, and why

The original note framed a view as a per-user *projection over a fixed structure*: hide the Storage
panel, store it in `localStorage`, change nothing about the model. It put "ownership policy" in a
tier of its own and ruled a page builder out of scope, "defer until a user asks".

Two things moved.

**A user asked.** Definable roots — any node type in the namespace subgraph becoming a page — is
exactly the deferred case, and it is now the requirement.

**Containment IS the view.** Every consumer of the old "ownership policy" is a view decision:
`BuildEditTargets` (what the editor groups), `related_orbids` (what audit rolls up), and `baseScope`
(what a reviewer is deemed to have looked at — whose own comment justifies the subtree as "the unit a
reviewer actually looked at", a statement about what the UI rendered). None of it reaches the data.
Orbital is API-first and its UI is an ergonomic tool over that API, so adopters configure what they
see and edit. `DGRAPH.md`'s Spike 33 block is amended accordingly: type-policy is view configuration,
shipped defaults in version control, per-deployment overrides in Postgres.

**The structural boundary is untouched and is not policy-driven:** export is everything reachable from
the Namespace node (DQL `expand(_all_)`), one DataCenter per namespace. Views never participate.

## Store overrides ONLY — do not seed the table

Resolution is: version-controlled default as the base, a Postgres row replacing it where one exists.
Reset is `DELETE FROM view_overrides WHERE root = 'Server'`.

Seeding the table with a copy of the defaults breaks two things:

- **Reset has nothing to reset to** — the row *is* the default, so restoring it means re-deriving the
  default from somewhere, and now two copies can disagree.
- **Shipped default changes never reach deployments.** Add a panel in a release and every deployment
  that seeded at install time silently keeps the old shape, forever, with no signal.

This gives the P0 success criterion: **an empty overrides table renders exactly what is hardcoded
today.** No seeding step to get wrong; the migration is provably a no-op until someone edits something.

## What deployment-wide costs

Per-user config is safe by construction — you can only break your own page. A shared runtime-editable
view means one person's edit changes everyone's page, which puts it in the same class as approval
policies and needs the same treatment, none of it optional:

- **admin-only** to edit
- **an audit event per change** — who reconfigured the Server page, and when
- **reset** as the recovery path when someone breaks the page for the whole team

### Governance: admin-only and audited, NOT approval-gated *(decided 2026-09-24)*

View edits do **not** go through the approval gate. The blast radius is real — everyone's page
changes — but the edit is instantly and completely reversible by deleting the override row, which is
not true of a graph mutation. A config change that one click reverts does not need two-person review.
Recorded here so it is not reopened.

"Matching approval policies" is precise, not a gesture. Two things to copy, both verified in the tree:

**1. RBAC — reuse the existing admin group, do not invent a mechanism.** `server.go:598`:

```go
adminAPI := root.Group("/api/v1", append(apiAuth, handler.RequireRole(db, user.RoleAdmin))...)
```

Reads sit on `apiReadonly` — the UI needs the resolved view on every page render, so **any
authenticated user can READ; only an admin can WRITE an override.** Same split
`/approval-policies` already uses.

**2. The audit event shape.** `updateApprovalPolicy` (`changerequest_api.go:1271`) records both sides,
not just the new state, and its comment says why: *"required_approvals is now 1 does not answer was
the bar lowered?, and that is the question an audit of a change-control system exists to answer."*
Two things to lift:

- **before/after of the view definition**, not just the resolved result. "The Server view now
  includes X, Y, Z" does not answer *"what did someone remove?"*, which is the question a view audit
  exists to answer. This matters more than it looks: a missing audit diff is the quietest failure in
  this codebase — an `updateRack` with `changes: None` looked entirely normal.
- **A called-out consequence flag.** `enforcementStopped` exists so nobody scanning a five-field diff
  has to spot the one change that turns the gate off. The view equivalent is **removing a child that
  was editable** — it withdraws an editing capability org-wide — and it belongs on the event as a
  named field (`editableRemoved: ["storageControllers"]`), never inferred by diffing two lists.

That flag is also what the UI should surface **at the moment of the toggle** — "Storage will no
longer be editable from this page" — not only in the audit log afterwards.

### P0 has no UI at all *(decided 2026-09-24)*

The API plus GraphiQL is the surface. This is consistent with API-first and avoids betting UX work on
a storage model that has not been exercised yet.

In-place edit mode on the detail page is **P1**. A separate admin form describing a page you cannot
see is the Jira screen-scheme failure and should not be built. When it comes it needs:

- explicit **Save/Discard**, not live toggles — shared config must never show half-states to other users
- a **"differs from default" marker per panel** — overrides are deltas, so the UI must show what YOU
  changed, not merely the resolved state
- **per-root reset**
- a child picker driven by the introspection resolver, so cycles are **greyed out** rather than
  rejected on save

## Prior art — the line is well established

Mature tools keep exactly this split, which is reassuring rather than novel:

- **ServiceNow** — *admin form layout* (structure, governed) vs *personalize form* (per-user field
  hiding). Closest analogue, and a CMDB, so adopters likely already know the distinction.
- **Kubernetes** — server-declared `additionalPrinterColumns` vs a client's own
  `kubectl get -o custom-columns`. Same line: the server declares what exists, the client picks.
- **Orbital already does this for tables.** `shared.js` ships DataTables **colvis** ("Select Columns")
  on every list table. This note extends an existing, accepted pattern to detail views — it does not
  introduce one.

**On differentiation:** saved views are table stakes in mature tools. This is a usability feature, not
a positioning one. Worth building for its own sake; not a moat.

---

## ⚠️ The hazard: filtering the EDITOR is not filtering a VIEW

This is the constraint the design must be built around, and it is a silent data-loss path.

`configitem-editor.js:163` decides a field was *cleared* by diffing the open-time snapshot against the
tree the user edited:

```js
function removePayload(t, before, sub) {
  for (const f of t.fields) {
    if (!isEmpty(before[f]) && isEmpty(sub?.[f])) out[f] = valueForMutation(t, f, before[f])
  }
}
```

A field that is **present in `before` but absent from the edited tree** reads as *"the user cleared
it"* and emits a `remove`. So naïvely hiding Storage from the editor would **delete storage data on
the next save** — silently, and only discovered when someone's `capacityBytes` vanished.

**Therefore:**

- **Views filter DISPLAY. The editor always loads and writes the whole tree it opened.**
- A hidden panel means "not rendered", never "not in the payload".
- If per-field edit filtering is ever wanted, `removePayload` must diff against the *visible* field
  set rather than `t.fields` — a deliberate change to the clearing contract documented in UI.md
  ("Clearing a field emits `remove`, not `set: null`"), not an incidental one.

**Under the deployment-wide model this shrinks but does not vanish.** With no per-user hiding, the
editor opens exactly what the view defines, so `before` and the edited tree cover the same set and
nothing reads as cleared. It resurfaces in one case: **a view edited while someone has an editor
open** — the tree they opened no longer matches the view, and the diff sees the dropped entity as
cleared. That is a version-check-on-save problem, not an architecture problem.

The test still earns its place, retargeted: **change a view, save an edit that was opened under the
old view, assert the dropped entity's fields are untouched.**

---

## ⚠️ Interface annotations do NOT propagate to implementing types

Verified 2026-09-25 against a live instance. `"""editorIgnored"""` on
`KubernetesCluster.description` reads back as `"editorIgnored"` when introspecting the **interface**
and as `""` when introspecting `EksaKubernetesCluster`, which implements it. DGraph also forbids
redeclaring an interface field on the implementor, so the annotation cannot simply be moved down to
the concrete type.

Left alone this is silent: the annotation is present, correct and reviewed, and suppresses nothing.
Two of the first 29 annotations landed on an interface and were inert until the reader was fixed.

**`DGraphSchemaClient.Introspect` now inherits annotations** from every interface a type implements,
with the type's OWN annotation winning so an implementor can still override its interface. This is
the general rule, not a `KubernetesCluster` special case.

Consequence for any future annotation: **where you put it changes whether it works.** A rule that
should apply to every ConfigItem belongs on the `ConfigItem` interface once, rather than being
repeated on 19 types — which is now possible precisely because inheritance is implemented.

## ⚠️ Environment coupling: editability comes from the DEPLOYED schema, not the binary

Once `Derive` reads the running graph, what is editable is a property of the schema **that
environment has applied**, not of the code that shipped. An environment still carrying an older
`schema.graphql` silently gets **more** editable fields, because the annotations that suppress them
are not there.

Nothing fails. No error, no refusal — the editor simply offers fields it should not, in one
environment and not another, and the first anyone knows is someone editing a scanned hardware fact in
staging that they cannot edit in prod. It is the same failure as a mistyped annotation (*a schema
that lies about its own intent*), moved from typo scope up to environment scope, and it is harder to
see because every individual piece looks correct.

**Defence, implemented 2026-09-24:** `Resolver` logs the resolved annotation count on **every**
re-derive — boot and every later schema change, since annotations going missing after a redeploy is
exactly as invisible as never having had them:

    resolved schema annotations from the deployed schema
      editor_ignored_total=29 editor_ignored_by_type={...} types=19

Zero-when-expecting-29 is then obvious. Unrecognised annotations are logged separately at WARN with
their `Type.field` location, because a typo'd annotation does not suppress anything and otherwise says
nothing at all. `Annotations()` computes the report; `TestAnnotations_AbsentAnnotationsReportZeroNotSilence`
pins that a missing annotation reports zero **and** that the field really is editable in that state —
the count and the consequence asserted together, so the signal cannot drift from what it signals.

The count is a smoke alarm, not a guarantee. It tells an operator the environment differs; it cannot
tell them which answer was intended. A deployment-time assertion ("expect N") would be stronger and is
worth considering if this bites.

## Containment stays with P1 — NOT schema annotations *(re-confirmed 2026-09-25)*

Asked again while emptying `registry.go` of hand-maintained data, and answered the same way: the
containment declarations (`OwnerType`/`OwnerField`/`ChildField`/`OwnerEdges`, 43 entries) do **not**
move into `"""docstring"""` annotations. They go to the P1 override layer — version-controlled
defaults plus Postgres overrides — because containment IS the view definition, which is this spike's
whole premise.

Annotating them now would be settling the same question twice and moving the same 43 entries twice.
`registry.go` therefore keeps containment (and the derivation logic) until P1 lands, and empties then.

**What DID leave the registry, and how** — the rest of its data is derivable or annotatable:

| Data | How it leaves | Status |
|---|---|---|
| `FormFields` / `BeforeFields` | introspection | **done** 2026-09-25 |
| `IsRoot` | derived from containment | **done** — deleted |
| `PayloadField` | `Add<Type>Payload`'s field typed `[<Type>]` | **done** 2026-09-25 |
| `Implements` | `interfaces { name }` | **done** 2026-09-25 |
| `JSONStringFields` | `"""jsonString"""` annotation | pending |
| orbId suffixes (`leafSuffix`/`wrapperSuffix`) | type annotation | pending |
| **Containment** | **P1 override layer** | deferred, deliberately |

## P0 acceptance list *(decisions folded in 2026-09-25)*

**P0 INVERTED 2026-09-25: defaults first, storage later.** The goal is that defining a new node type
in DGraph yields `/{slug}`, a nav entry and a working page with no code and no config. Configuration
is the EXCEPTION, not the mechanism — so the override layer is DEFERRED to P1 entirely: no Postgres
table, no view rows, no admin UI, and none of the RBAC or audit questions that belong to it.

**Decisions:** slug is `kebab(TypeName)` pluralized, derived from the type name and never from stored
orbIds (`IPAddress` is `<ns>:<address>` and `Rack` is `<ns>:<rackName>` — neither carries a kind
token, so id-derivation breaks on exactly them). Existing URLs **move, with redirects**. Pages are
**per concrete type**. Nav shows **roots only**; routes stay universal.

**Slug and discovery**
1. Slug derives from the type name alone. *(test)*
2. `GET /api/v1/views` returns the resolved list — slug, type, and what each page renders. Orbital's
   `APIResourceList`; `orbctl` and AEP get the same map for free. *(test)*
3. The UI builds nav and routes from that endpoint. No second source of slugs anywhere. *(test + live)*
4. A type added to the deployed schema appears in `/api/v1/views` with no code change, no restart. *(live)*

**Generic pages**
5. `/{slug}` renders a list for any ConfigItem type. *(test + live)*
6. `/{slug}/{id}` renders a detail page. *(test + live)*
7. Field lists come from the Stage 1 resolver; no per-type Go literals in the renderer. *(test)*
8. Relationships render as tabs, derived from introspection. *(test)*
9. DGraph unreachable → the page states why, never an empty list implying "no records". *(test)*

**Migration — the success signal**
10. `/servers`, `/datacenters`, `/clusters`, `/network` are served by the generic renderer. *(live —
    data-centers 2026-09-25, clusters + servers + network-devices 2026-09-26. **Item 10 is DONE** —
    no bespoke ConfigItem page remains, and `covered` in ui.go is empty.)*
11. The four bespoke handlers are deleted and net handler LOC goes DOWN, reported as a number. A
    generic renderer nothing real uses is one nobody trusts, and two rendering paths means every
    feature lands twice. *(live)*
12. Server detail still shows iDRAC, maintenance, storage, network, audit. *(live + test — done
    2026-09-26. Storage needed `include: storageControllers.storageDevices`; network needed
    nothing, `Server.networkInterfaces` being a direct edge.)*
13. Cluster detail still shows its backup sub-kinds, which are wrapper types with a different tree
    shape. *(test — `e2e/clusters-generic.spec.ts`, done 2026-09-25)*
14. Every reachable edit target still carries its MVCC `version`. *(test)*
15. Proposed-change marks still render on Server; the three-list guards still agree. *(test — done
    2026-09-26. The marks became GENERIC: every page emits them now. The three-list guard was
    rewritten, because two of its lists no longer exist — a derived template cannot drift from the
    field set it is generated from, so `derivedMarkTables` exempts it and says why.)*
16. The JSON editor still writes owned-child subtrees and emits NO spurious `remove`. *(test)*

**Routing**
17. `/{slug}` does not swallow the static routes — enumerated and asserted, not spot-checked. *(test)*
18. An unknown slug returns 404 in the error envelope, not a blank generic page. *(test)*

**Orb**
19. Orb serves the generic pages read-only against its own DGraph; both UIs still load. *(test + live)*

**DECIDED 2026-09-25 — slug customisation is the `"""slug: x"""` annotation.** It already existed
for the concrete types; the interface work made it load-bearing (`KubernetesCluster` would otherwise
be `/kubernetes-clusters`). Uniqueness is enforced where it always was — `ResolveViews` refuses two
views claiming one slug and names both — and that namespace now spans interfaces and concrete types
alike. This is the k8s spelling: the name is in the spec, checked at registration.

**DECIDED 2026-09-25 — a view may be backed by an INTERFACE, and item 10's `/clusters` is one.**
Pages are per concrete type *for detail*; a LIST may be interface-backed. The constraint is DGraph's:
`query<Interface>` exists, `get<Interface>` does not, so the detail route resolves each row's concrete
type from the data. An implementation covered by an interface view is demoted out of the nav.

⚠️ **A slug alias is NOT a substitute for this,** and the distinction is the one three products draw
separately: a slug names one view (k8s `plural`), an alias adds a second name for it (`shortNames`),
and a *category* fans out over several kinds (`categories`, i.e. `kubectl get all`). Aliasing
`EksaKubernetesCluster` to `/clusters` passes every uniqueness check and is still wrong — the page
would silently omit every other provider. Full rationale in `docs/reference/UI.md` § Settled Decisions.

## Scope sketch

**P0 — prove the mechanism, change no behaviour**
- Overrides in Postgres for the four roots that exist today, with their current includes
- Resolution: VC default, replaced by an override row where present
- Write-time validation against introspection: the type and field must exist, and the include graph
  must be **acyclic**
- Boot revalidation: warn, never refuse — a stale override must not stop orbital serving
- Cross-replica invalidation (`LISTEN/NOTIFY`); base runs multi-replica
- Admin-only, audited, resettable by deletion
- **Test: empty overrides table renders exactly what ships today**

**P1 — arbitrary roots**
- Any node type in the namespace subgraph can become a page
- A generic `/:kind` list + detail handler replacing four hardcoded route pairs
- Subsuming four genuinely different detail templates (Server has iDRAC panels, Cluster has backup
  sub-tabs) — or a generic renderer coexisting with them
- ⚠️ **Route collision**: `/:kind` swallows `/users`, `/audit-log`, `/export` and every other static
  page unless kinds are namespaced or reserved

**Out of scope (deliberately)**
- **Per-user view preference** — deferred, not rejected. Migration trigger is the first request to
  share a view, or the first support question about what a user is seeing. Not a scale threshold: a
  preference row per user is a few thousand rows at most, which is nothing beside `audit_events`.
- Changing edges or the schema — a view selects from whatever the schema says
- Reordering or relocating panels between tabs — composition, not selection

## DECIDED 2026-09-26 — the Grafana posture stands, and ordering is a schema pin

**Confirmed: editable shared views are the product, not a refinement.** The one-line model at the top
("like a shared Grafana dashboard — you change the view, you change what everyone else sees") is
ratified. A counter-argument was put and rejected: that the shared *editable* layer is the
highest-blast-radius and highest-cost one (RBAC, audit, write-validation, cross-replica invalidation)
and should therefore ship after the cheap, reversible per-user layer, as NetBox did — per-user
`UserConfig` first, shared `TableConfig` added later in 4.3. **Sequencing stays shared-first.**
Per-user layers on afterwards, and the "no third per-user layer" line above should be read as
*not yet*, not *never*.

**Ordering is NOT part of the override delta.** It is a `"""order: a, b, c"""` annotation in the
schema — a partial order, pinned prefix plus alphabetical tail, shipped in version control. This
resolves the out-of-scope line ("reordering — composition, not selection") for FIELDS: the delta
model genuinely cannot express order without freezing the view, so order does not go in the delta.
Rationale and the NetBox evidence are in `docs/reference/UI.md` § Settled Decisions.

⚠️ **Client-side per-user ordering is blocked, and the blocker is structural.** DataTables persists
column state in `localStorage`, and UI.md requires every such key to be cleared by
`clearTabStateOnFresh` on `?fresh=1` (the login redirect) so one user does not inherit another's view
on a shared machine. So a browser-stored column layout is either wiped at every login or exempt from
that rule. **Per-user ordering therefore has to be server-side, keyed to the user** — NetBox's
`UserConfig` shape — which makes it a real feature rather than a flag flip.

## OPEN — what the "panel registry" is actually for *(2026-09-26)*

The migration finished without it, and two of its three motivating cases evaporated:

- **NetworkDevice Connections** needed nothing. `networkInterfaceConnectedNetworkDevice` is an
  ordinary list edge, so it was already a relationship tab.
- **Server storage/network** needed `include:` (a tab sourced from a PATH), not a panel.

What is left is narrower than "a registry of panels", and both remaining cases are the same shape —
**a COLUMN computed across a path**, not a new kind of panel:

| Case | Wanted | Today |
|---|---|---|
| NetworkDevice → connected servers | each row's Kubernetes cluster, node role, GPU | server is shown; cluster context is one click away |
| Rack list | a server-count column | the count is `len(rows)` on the rack's own page |

So the question to settle is whether to build **computed columns** — `column: server.kubernetesNode.cluster.name`,
`column: servers.count` — reusing the column machinery, rather than a registry of Go functions that
would put per-type code back in the renderer. Deferred until someone asks for one of those two
columns specifically; see [[feedback-fewer-cases-over-parity]].

## Open questions

1. **Naming.** "View" for the definition, "subtree" for the resolved set — both provisional. Do not
   bake either into a table name, an exported type or an API path yet. `/api/v1/views` is the
   candidate, with the root named inside the object rather than in the path.
2. **Does a view define what the EDITOR writes, or only what is displayed?** P0 says both, because
   the editor tree IS the view. Worth restating if P1 ever adds read-only-only panels.
3. **Can a deployment lock a panel** so a future per-user layer could not hide it? Probably YAGNI,
   but precedence is two lines of code now and a migration later.
4. **Does it apply to orb?** Orb renders the same shared partials read-only and has no users or
   Postgres of its own. Views are an orbital concept; the shared templates must at least not break.

## Sequencing

**No longer independent of the schema plan.** The original note was right that per-user filtering
needed no parser, no introspection and no schema change — it filtered whatever panels existed. The
deployment-wide model does not have that property: write-time validation has to answer "does this type
and field exist", which is exactly the introspection resolver from Stage 1. **P0 sits downstream of
Stage 1** and should reuse that resolver rather than build a second source of truth about what exists.

## Store overrides as DELTAS, not replacements

The original note reached this for the per-user case — *"store views as 'hidden ids' rather than
'visible ids', so an unknown id is inert and a newly-added panel defaults to visible"*. The reasoning
survives the model change and matters more, because it is now the only thing standing between a
customised root and permanent staleness.

If an override **replaces** a root's include list, then a deployment that customises the Server view
stops receiving shipped changes to it — add a panel in a release and every customised deployment
silently keeps the old shape. That is the seeding problem again, scoped to customised roots instead of
all of them, and it is easy to miss because the uncustomised roots behave correctly.

If an override is a **delta** (`+include`, `-include` relative to the shipped default), new defaults
flow through to customised roots too, an include naming a field that later disappears is inert rather
than an error, and reset is still a row deletion.

Trade-off to accept knowingly: a delta cannot express "exactly these, in this order, and nothing a
future release adds". Anyone who genuinely wants that wants a frozen view, which is a different
feature and should be asked for explicitly.
