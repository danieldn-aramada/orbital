# REST API conventions

Read this before: adding or changing a REST endpoint, a query parameter, or a list response.

Orbital's REST surface (`/api/v1/`, both apps) speaks one vocabulary. GraphQL (`/graphql`) follows the schema, not this doc.

## Settled Decisions

- **Query params are camelCase**, like the JSON bodies: `orbId`, `awaitingReview`, `dataCenter`. Paths are kebab-case nouns. *(2026-10-07.)*
- **One name per concept, on every endpoint:** `orbId` (an entity), `dataCenter` (a data-center scope; the value is its orbId), `namespace`, `type` (a ConfigItem type), `status`.
- **Time windows use `<field>_gte` / `<field>_lte`** (and `_gt` / `_lt` for exclusive bounds), where `<field>` is a timestamp field in the response: `createdAt_gte=2026-10-01T00:00:00Z`. RFC3339 values. Use `TimeFields` + `TimeFilters` in `internal/handler/timefilter.go` — never hand-parse a time param.
  - The operator suffix is for **timestamp fields only**. Every other filter is a plain param.
  - Chosen over `since`/`until` and `startTime`/`endTime` (both name no field, and most resources have several timestamps) and over `createdAfter`/`createdBefore` (the name hides whether the bound is included). The operator says it.
- **A param that can take several values is repeatable** (`type=A&type=B`). Values of one param are OR-ed; different params are AND-ed.
- **Refuse, don't ignore.** A malformed value, an unknown enum value, or a time operator on a field the endpoint does not filter is `400 BAD_USER_INPUT` in the standard envelope (`ERROR-RESPONSES.md`). An ignored filter returns the whole list, which reads exactly like a correct answer. `TestListEndpoints_DeclareTheirTimeFields` pins the time half.
- **An empty value is no filter**, for every param.
- **Renames keep the old name working, marked `Deprecated: use X.` in swagger,** until a removal is scheduled. Current deprecations: audit-log `since`, `until`, `resource_id`, `resource_type`, `operation_name`, `event_category`, and the `timestamp` response field; change-requests `awaiting_review`; divergences/OCI `dc`, `dcOrbId`.

## Not yet uniform

- List responses: `{items, total}` (change requests), `{events, total}` (audit log), `{users}`, `{rows, total, limit, offset}` (orb publish history), and bare arrays elsewhere.
- Paging: `limit`/`offset` on four lists, with different defaults (100, 25, all) and caps; the backup/export/restore lists stop at a hard-coded 50 with no `total`. Cursors (`continue`/`pageToken`) for the append-only audit log are undecided, and tie into the envelope choice.
