# 001 — Mutation Audit Recording

**Status:** Open — no decision reached. Design is in progress.

**Date:** 2026-05-12

---

## Problem

Every config mutation in orbital should produce an audit event: who changed what, when, and what the before/after values were. This is table stakes for a CMDB — tools like Netbox maintain a changelog on every device regardless of whether the change came from the UI, the API, or a script.

---

## What We Built (Prototype)

The current implementation records events for UI-originated mutations only:

1. The edit modal (DataCenter, Server) sends a named GraphQL mutation (`UpdateServer`, `UpdateDataCenter`) through orbital's `/graphql` proxy with an `ifVersion` token in the variables.
2. Orbital's proxy middleware intercepts named mutations, performs an MVCC check using `ifVersion`, fetches the before-state from DGraph, forwards the mutation, and writes an event to PostgreSQL asynchronously.
3. The `ifVersion` token is stripped before forwarding — DGraph never sees it.

This gives us:
- MVCC conflict detection for concurrent UI edits
- Before/after diff stored in the `events` table
- Per-entity audit log tab in the DC and server detail views

### Why we like the current UI approach

The edit modal presents the entity's mutable fields as a JSON object in a JSONEditor. The user edits the JSON directly and submits it as GraphQL mutation variables. This is clean, self-describing, and maps naturally to the GraphQL API — there is no translation layer between what the user sees and what gets sent to DGraph. Introducing typed REST endpoints for mutations would break this UX.

---

## The Gap

Any mutation that does **not** go through orbital's `/graphql` proxy with a named operation is invisible to the audit log:

- Raw DGraph filter-based mutations: `updateServer(input: { filter: { orbId: { eq: "..." } }, set: {...} })`
- Direct DGraph admin endpoint access
- Any future API client that constructs its own GraphQL mutation without using orbital's named operation convention

This means the audit log is incomplete — it only reflects UI-originated changes, not API-originated ones.

---

## Options Considered

### Option A — Typed REST mutation endpoints
`PATCH /api/v1/servers/:orbId`, `PATCH /api/v1/datacenters/:orbId`, etc.

Orbital owns the full mutation path: fetch before-state, validate, apply to DGraph, record event. Every change goes through orbital regardless of client.

**Rejected (for now)** because it would break the JSON edit modal UX — the modal currently submits GraphQL variables directly, which is clean and self-describing. Replacing this with REST would require a translation layer and lose the direct GraphQL connection.

### Option B — GraphQL proxy + mutation registry (current approach)
Intercept named mutations in the proxy. Rely on DGraph's network isolation (NetworkPolicy in AKS already restricts DGraph to orbital-only access) to ensure all mutations flow through orbital.

**Partially implemented.** Works for the UI path. Does not catch unnamed or filter-based mutations. Requires all API clients to use orbital's named mutation convention.

### Option C — DGraph change feed / subscription
Use DGraph's subscription or a change data capture mechanism to detect mutations after the fact.

**Not evaluated.** DGraph community edition's subscription support is limited. Would not give us before-state without additional complexity.

---

## Open Questions

1. **Can we enforce the named mutation convention for API clients?** If all legitimate mutation paths go through orbital's `/graphql` and use named operations, and DGraph is network-isolated, the audit gap is eliminated in practice. This requires clear API documentation and discipline.

2. **Should `ifVersion` be required for API mutations too?** Currently opt-in (API clients can omit it). Making it required for all mutations would enforce optimistic locking everywhere but adds friction for scripting.

3. **Is the GraphQL edit modal approach extensible to new entity types?** Adding a new mutable type currently requires: a new named mutation in the registry (`mutationRegistry` in `graphql.go`), adding `version` to the DGraph query and template, and adding `ifVersion`/`version` to the JS submit handler. This is a small but non-trivial amount of boilerplate per type.

---

## Current State

The prototype records events for UI mutations. The MVCC flow works end-to-end. The gap (API mutations not recorded) is understood and accepted for the prototype phase. This decision note exists so the team can revisit it with full context when building toward MVP.
