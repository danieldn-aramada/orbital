# 001 — Mutation Audit Recording

**Status:** Decided — current approach accepted with known tech debt. Revisit before MVP.

**Date:** 2026-05-12

---

## Problem

Every config mutation in orbital should produce an audit event: who changed what, when, and what the before/after values were. This is table stakes for a CMDB — tools like Netbox maintain a changelog on every device regardless of whether the change came from the UI, the API, or a script.

---

## What We Built

### Phase 1 — Named mutation registry + before/after diff (superseded)

The first prototype intercepted named GraphQL mutations (`UpdateServer`, `UpdateDataCenter`) in the proxy, fetched before-state from DGraph, and stored a structured `{before, after}` diff in the `events` table. MVCC conflict detection used an `ifVersion` token stripped before forwarding to DGraph.

This gave us rich diffs but only covered UI-originated mutations. API clients using compound mutations (aliases), anonymous mutations, or inline filters were invisible to the audit log.

### Phase 2 — Regex-based payload capture (current)

Replaced the named registry with a regex scan of the query body for known DGraph type operations (`addX`, `updateX`, `deleteX` on `Server`, `DataCenter`, etc.). One event per HTTP request regardless of how many entities the mutation touches.

What is recorded per event:
- `operation_name` — GraphQL `operationName` field (may be empty for anonymous mutations)
- `resource_types` — all DGraph types touched, extracted from the query body (e.g. `["Server"]`)
- `resource_ids` — all orbIds touched, extracted from variables and inline `orbId: { eq: "..." }` filter patterns
- `actor` — from `updatedBy` variable or session
- `details` — full raw payload: `{operationName, query, variables}`

MVCC (`ifVersion`) is decoupled from eventing — it remains opt-in for single-entity mutations and has no effect on whether an event is recorded.

### Why we like the current UI approach

The edit modal presents entity fields as a JSON object in a JSONEditor. The user edits JSON directly and submits as GraphQL mutation variables. This is clean, self-describing, and maps naturally to the GraphQL API. Introducing typed REST endpoints for mutations would break this UX.

---

## Known Tech Debt

### 1. Regex parsing instead of a proper GraphQL AST

`extractResourceTypes` and `extractResourceIDs` use regular expressions against the raw query string. This is fragile:

- A string literal containing a type name (e.g. a description field containing "updateServer") could produce a false positive
- `orbId: { eq: "..." }` extraction assumes a specific filter shape — other valid filter forms (e.g. `in`, `regexp`) are missed
- Adding a new entity type requires updating `knownMutationRe` manually

**Proper fix:** use `vektah/gqlparser` (the parser underlying `gqlgen`) to parse the query into an AST and walk the selection set. This gives exact operation names, argument values, and variable usages without regex fragility. It is a new dependency — add it when the regex approach starts causing problems.

### 2. No before/after diff

The current approach records what was *sent* but not what changed *from*. A diff would require fetching entity state before forwarding the mutation, which adds latency and complexity. Deferred until there is a clear product need.

### 3. Inline orbId extraction is best-effort

OrbIds inlined in query filters (not passed as variables) are extracted via regex. If an API client uses a different filter shape or passes orbIds as a list (`in: [...]`), those IDs will be missing from `resource_ids`. The event is still recorded — the payload in `details` always contains the full query.

---

## Options Considered

### Option A — Typed REST mutation endpoints
`PATCH /api/v1/servers/:orbId`, `PATCH /api/v1/datacenters/:orbId`, etc.

**Rejected (for now)** — breaks the JSON edit modal UX. The modal submits GraphQL variables directly; a REST layer would require a translation layer and lose the direct GraphQL connection.

### Option B — GraphQL proxy + mutation registry
Intercept named mutations in the proxy. Requires all API clients to use named operations.

**Superseded by Phase 2** — too narrow; compound and anonymous mutations were invisible.

### Option C — DGraph change feed / subscription
Use DGraph's subscription or CDC to detect mutations after the fact.

**Not evaluated.** DGraph community edition's subscription support is limited. Would not give us before-state without additional complexity.

### Option D — Regex payload capture (current)
Scan the query body for known type operations. One event per request.

**Adopted.** Full coverage for any mutation through the proxy. Acknowledged tech debt around regex fragility and missing before/after diff.

---

## Open Questions

1. **Before/after diff** — worth the latency cost of a pre-mutation fetch? Deferred. Revisit when operators ask "what did it change from?"

2. **AST parsing** — when does regex fragility become a real problem? Likely when new entity types are added frequently or when API clients use non-standard filter shapes.

3. **DGraph network isolation** — the audit gap for direct DGraph access is mitigated by AKS NetworkPolicy restricting DGraph to orbital-only. This must remain enforced by the deployment layer.
