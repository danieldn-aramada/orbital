# 002 — API Design Philosophy

**Status:** Decided — settled convention for all new endpoints.

**Date:** 2026-06-02

---

## Context

Orbital is GraphQL-first. All CRUD operations on config items (DataCenter, Server, Rack, etc.) go through the GraphQL API at `/api/v1/graphql`. REST endpoints exist only for async operational workflows that do not fit the GraphQL model: export, backup, restore, publish, import.

When designing these REST endpoints, two questions came up:

1. Should endpoints be resource-centric (`POST /datacenters/:orbId/export`) or operation-centric (`POST /export`)?
2. Are operation-centric paths conventional REST, or do they smell like SOAP?

---

## Decision

**REST endpoints in orbital are operation-centric at the trigger level, resource-centric at the job level.**

Trigger endpoints create a job resource and return a job ID:

```
POST /api/v1/export          — creates an export job (body: {"orbId": "alaska:dc-01"})
POST /api/v1/backup          — creates a backup job
POST /api/v1/restore         — creates a restore job (body: {"backupId": "..."})
```

Job endpoints follow standard resource/collection patterns:

```
GET  /api/v1/export/jobs               — list export jobs (collection)
GET  /api/v1/export/jobs/:jobId        — get one export job (resource)
POST /api/v1/export/jobs/:jobId/publish — trigger publish on a completed job
```

---

## Why not resource-centric trigger paths?

The alternative was `POST /api/v1/datacenters/:orbId/export`. This was rejected because:

- Orbital does **not** expose a REST resource hierarchy for config items — there is no `GET /datacenters`, no `PUT /datacenters/:orbId`, no `DELETE /datacenters/:orbId`. Those operations go through GraphQL.
- A path like `/datacenters/:orbId/export` implies a full REST resource hierarchy that does not exist, which is misleading to API consumers.
- The datacenter is a *parameter* to the export operation, not the resource being acted upon. The job is the resource.

---

## Is operation-centric REST conventional?

Yes. This is the dominant pattern for async operations across all major APIs:

- **GitHub Actions:** `POST /repos/{owner}/{repo}/actions/workflows/{id}/dispatches` — verb in the path, run resources polled separately.
- **Stripe:** `POST /v1/charges`, `POST /v1/refunds` — operations modeled as resource creation.
- **Kubernetes:** `POST .../pods/{name}/eviction` — creates an Eviction sub-resource to trigger eviction.
- **AWS:** `POST /{bucket}?delete`, `POST /?restore` — pragmatic action paths where resource modeling adds no value.
- **Heroku:** `POST /apps/{app}/builds` — build triggered by creating a build resource.

The distinction that separates this from SOAP is not whether the URL contains a verb. It is:

| Property | SOAP/pure RPC | Orbital's approach |
|---|---|---|
| URL structure | Single endpoint (`POST /api`), verb in payload | Distinct URLs per operation |
| HTTP method semantics | Everything is POST | POST creates, GET reads |
| Resource identity | None — operations, not resources | Jobs are resources with IDs, listable and pollable |
| Self-describing | No — must read schema | Yes — URL structure is readable |

SOAP tunnels everything through one endpoint with an envelope describing the operation. Orbital creates real resources (jobs) that can be listed, fetched by ID, and polled for status — standard REST semantics.

---

## Rule for new endpoints

When adding a new operational REST endpoint:

1. **Trigger = POST to an operation namespace.** The body carries the parameters. The response returns a job ID.
2. **Jobs = standard resource/collection.** `GET /namespace/jobs`, `GET /namespace/jobs/:jobId`.
3. **Do not create resource-centric paths for operations that have no corresponding GET/PUT/DELETE.** A path like `/datacenters/:id/something` implies a full REST hierarchy. Only use it if the hierarchy actually exists.
4. **The job is the resource.** The thing being operated on (a datacenter, a backup) is a parameter, not the addressable resource.
5. **Sub-resource actions on jobs are valid.** When an action operates on an existing job resource rather than triggering an independent workflow, use `POST /namespace/jobs/:jobId/action`. This is the Kubernetes sub-resource pattern (`POST /pods/{name}/eviction`). The URL encodes the prerequisite (a specific job must exist) for free. Example: `POST /api/v1/export/jobs/:jobId/publish` — publish acts on a completed export job and has no independent lifecycle.
