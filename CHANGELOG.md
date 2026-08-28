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
- **Change approval design (Spike 36)** — generic approval engine with per-action-type adapters;
  v1 targets config mutations. Design only; no runtime behaviour yet.
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
