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
  The review queue shows three tabs — *Needs my review* (hidden for roles that cannot approve),
  *Open*, *Closed* — and states each row's change instead of its stored title.
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
