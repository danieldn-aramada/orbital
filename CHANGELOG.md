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
  Protected classes are declared per namespace (optionally per type) via
  `/api/v1/approval-policies`, and are **opt-in** — with no policy, nothing changes.
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
  **UI**: a *Change Control* section with the review queue and an admin policies page;
  a review view showing the diff, the approvals (including "approved an earlier version"
  when one no longer counts) and only the actions the caller may actually take; and the
  config editor relabels **Save → Propose change** on a protected class, opening a change
  request and taking you to its review instead of writing. An admin keeps Save, with a
  visible notice that the write bypasses review. An entity with a change already in
  flight says so before you start editing it.
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
- **JSON-editor field clearing** — clearing a field is now a DGraph `remove`; `set: null` was a
  silent no-op, so cleared fields never persisted.
- **Handler integration suite** — 10 failures and an intermittent flake. Tests called handlers
  directly, bypassing Echo's error handler, so the JSON error envelope they asserted on was never
  rendered.
- **Stale end-to-end test fixtures** — specs referenced pre-migration server orbIds and failed
  after the `server-<serial>` convention landed.
- **Removed a dead restore-availability flag** and documentation for an `ORBITAL_RESTORE_BACKEND`
  environment variable that does not exist.
