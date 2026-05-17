# Audit & Events Reference

Read this before: touching `internal/handler/graphql.go`, `internal/handler/event.go`, the `events` ent table, or audit log UI work.

## Event model

- **One event per HTTP request**, not per entity. A compound GraphQL mutation touching multiple entities produces one event row.
- `operations` (JSON array): DGraph operation names extracted from the query body — e.g. `["updateDataCenter","updateServer"]`.
- `resource_types` (JSON array): all DGraph types touched.
- `resource_ids` (JSON array): all orbIds touched — extracted from three sources (see below).
- `details` jsonb: full raw payload `{operationName, query, variables}`.
- **Events are always recorded** for mutations touching known types regardless of `ifVersion` presence — MVCC is opt-in and orthogonal to eventing.

## extractResourceIDs — three sources

1. `variables["orbId"]` — single string (single-entity mutations)
2. `variables["input"]` array walk — bulk add mutations (`addServer(input: [...])`)
3. Recursive walk of the DGraph response JSON (`collectOrbIDs`) for every `"orbId"` value — covers nested creates and any entity the client selected orbId for

**Known gap:** mutations filtered by a non-orbId field (e.g. `filter: { hostname: {...} }`) where the client selects only `{ numUids }` — these record empty `resource_ids`. The full query is still in `details`. Post-MVP fix: post-mutation DGraph UID lookup.

## Operation detection

- `knownMutationRe` regex matches `(add|update|delete)(DataCenter|Server|...)` in the raw query string.
- **Adding a new mutable type requires updating this regex** in `internal/handler/graphql.go`.
- Tech debt: `vektah/gqlparser` AST walking is the right long-term fix. Add when regex causes real problems. Tracked in ROADMAP.md technical debt.

## writeAuditEvent helper

- Package-level function in `internal/handler/event.go`. Shared by `GraphQL.writeEvent`, `Export.Trigger`, `BackupHandler.Trigger`.
- Arguments: `*ent.Client`, `*slog.Logger`, actor, opName, operations, resourceTypes, resourceIDs, details map.
- **Failures are logged and swallowed** — audit writes must never block or fail a request.

## ent conventions for events

- **Do not use `Immutable()` on ent schema fields** — immutability enforced at app layer (never call `.Update()` on event records). `Immutable()` causes migration pain: changing a field requires drop/recreate rather than ALTER.
