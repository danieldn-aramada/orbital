# Roadmap

## Development Timeline

```mermaid
%%{init: {'theme': 'base', 'themeVariables': {'doneTaskBkgColor': '#22c55e', 'doneTaskBorderColor': '#16a34a', 'activeTaskBkgColor': '#3b82f6', 'activeTaskBorderColor': '#2563eb', 'taskBkgColor': '#e5e7eb', 'taskBorderColor': '#d1d5db', 'taskTextColor': '#6b7280', 'taskTextDarkColor': '#fff'}}}%%
gantt
    dateFormat YYYY-MM-DD
    axisFormat %b %Y

    Section Completed
    Req Gather & Solution Eval (DCIM, PLM, ITSM)    :done, 2026-01-01, 2026-03-04
    Req Gather (Digital Twin in Atlas)              :done, 2026-03-04, 2026-04-10
    Research & Technology Selection                 :done, 2026-04-10, 2026-04-14
    Architecture Design                             :done, 2026-04-14, 2026-05-08

    Section Current
    Prototyping                   :active, 2026-04-14, 2026-05-20
    
    Section Upcoming
    MVP                           :2026-05-20, 2026-06-20
    General Availability          :2026-06-20, 2026-07-20
```

**Note:** All future dates are subject to change.


## Current Phase: Architecture Design + Prototyping

Architecture Design closes out this week (2026-05-08) — final design review meetings in progress. Prototyping is running in parallel; spike scope may still evolve as design concludes.

Goal of prototyping is learning, not shipping. Each spike below is a question to answer, not a feature to build. Results from these spikes define the MVP.

| # | Spike | Key Question | Owner | Status | Depends On |
|---|---|---|---|---|---|
| 1 | AKS Deployment Validation | Can we deploy orbital and DGraph on AKS and reach a working baseline? | Daniel | ✅ Done (4/20) | — |
| 2 | BMC Discovery end-to-end | Can we scan a real fleet and get clean data into the graph? | — | 🔄 In progress | — |
| 3 | DGraph performance and cost | Does DGraph hold up at scale, and what does it cost on AKS? | — | Not started | — |
| 4 | DGraph operations | Can our team operate DGraph on AKS without prior experience? | — | Not started | — |
| 5 | Schema migration — build vs runbook | Do we need automation or is a runbook sufficient? | — | Not started | Spike 4 |
| 6 | Air-gap sync round-trip | Does orbital's config export work reliably as a complete, importable payload for orb? | — | Not started | — |
| 7 | Orb import API | What is the right API contract for orb's local config import endpoint? | — | Not started | — |
| 8 | Backup, DR, and availability | What is the right backup and DR strategy for orbital as a tier-0 service? | — | Not started | Spike 4 |
| 9 | Authentication | How do we implement JWT bearer auth in orbital for Atlas UI consumers? | — | Not started | — |
| 10 | Report intake API | What is the right transport-agnostic API for orbital to receive drift and divergence reports? | — | Not started | — |

---

### Spike 1. AKS Deployment Validation ✅
**Question:** Can we deploy orbital and DGraph on AKS and reach a working baseline?

**Context:** First end-to-end deployment of the stack — validates that orbital, DGraph Alpha, DGraph Zero, and supporting networking can run together in our shared AKS dev environment.

**Completed:** April 20, 2026 — orbital and DGraph deployed in AKS dev. GraphQL endpoint reachable. NetworkPolicy applied to restrict DGraph access to orbital only.

### Spike 2. BMC Discovery end-to-end
**Question:** Can we scan a real server fleet over Redfish, build a graph, export it from orb, and import it into orbital cleanly?

**Success criteria:**
- Orb CLI scans BMCs and produces a valid DGraph-importable file
- Orbital ingests the file and the graph is queryable via the Topology API
- Data is accurate against known hardware

### Spike 3. DGraph performance and cost
**Question:** Does DGraph hold up at realistic scale for graph traversal queries, and what does it cost to run on AKS?

**Context:** There are unsubstantiated reports of high CPU usage under unknown conditions. This spike reproduces and characterizes that before any optimization work begins.

**Success criteria:**
- Define a realistic query mix: expected patterns from the digital twin UI (deep traversals — DataCenter → Servers → StorageControllers → StorageDevices), read/write ratio, and target dataset size for v1
- Seed DGraph with a representative dataset and benchmark query latency under increasing concurrency
- Identify which specific queries are expensive and whether they correlate with the reported CPU spikes
- Determine if Valkey caching is sufficient mitigation or if DGraph is a hard bottleneck
- Map peak CPU/memory profile to an AKS node SKU and produce a cost estimate for v1 workload

### Spike 4. DGraph operations
**Question:** Can our team operate DGraph reliably on AKS without prior experience?

**Context:** The team has strong Go/Java and PostgreSQL experience but no DGraph operational background. Schema migrations, backup/restore, and cluster behavior during restarts are all unknowns. This spike must be completed before building any automation around these processes.

**Success criteria:**
- Perform a full backup and restore cycle on AKS — validate data integrity after restore
- Apply a schema change to a live DGraph instance — document the process, failure modes, and rollback steps
- Test DGraph behavior during a rolling restart on AKS (pod eviction, zero downtime feasibility)
- Evaluate blue/green deployment viability — determine if DGraph cluster state makes this practical or prohibitively complex
- Produce a runbook: what the on-call engineer does for each of the above scenarios

### Spike 5. Schema migration — build vs runbook
**Question:** Do we need a built-in schema migration tool in orbital, or is a well-maintained runbook sufficient?

**Context:** The architecture calls for orbital to own schema versioning and apply changes to DGraph automatically on startup. But this is non-trivial to build correctly. Spike 4 (DGraph operations) will reveal how painful schema changes are in practice — this spike uses those findings to decide whether automation is worth the investment or whether operational discipline (runbooks, manual apply, version tracking in PostgreSQL) is good enough for the foreseeable future.

**Do not start until spike 4 is complete.**

**Success criteria:**
- Assess the real operational cost of manual schema migrations based on spike 4 findings
- Determine if the frequency and risk of schema changes justifies building automation
- If yes — produce a design doc for the migration tool (not code)
- If no — produce a runbook that covers schema apply, rollback, and version tracking in PostgreSQL

### Spike 6. Air-gap sync round-trip
**Question:** Does orbital's config export work reliably as a complete, importable payload?

**Context:** Orbital must expose a data center-scoped export endpoint (`POST /api/v1/datacenters/{id}/export`) that returns a `json.gz` + `schema.gz` pair for that data center's subgraph. This is not a raw pass-through of DGraph's export mutation — orbital must partition the graph by data center. In deployments using `configbundle`, its Bundle Generator calls this endpoint to produce a ConfigBundle. This spike builds the endpoint and validates the export is reliable and loadable.

**Success criteria:**
- Implement `POST /api/v1/datacenters/{id}/export` — returns scoped `json.gz` + `schema.gz`
- Orb receives and loads the `json.gz` into local DGraph (simulating what `configbundle`'s edge agent does)
- Orb serves the graph correctly offline after import
- Validate export sizes are reasonable (reference point: USB/manual transfer)

### Spike 7. Orb import API
**Question:** What is the right API contract for orb's local config import endpoint?

**Context:** In deployments using `configbundle`, config reaches orb via the edge agent calling orb's local `/import` API with the `json.gz` payload — not by orb polling orbital directly. Orb has no direct connection to orbital; the delivery mechanism is the deployment layer's concern. This spike defines and validates that local API contract between the delivery layer and orb.

**Success criteria:**
- Define the `/import` API: endpoint, payload format, auth model (local loopback — what, if any, auth is appropriate)
- Validate that orb correctly loads the `json.gz` into local DGraph and serves it offline after import
- Confirm the import is idempotent and safe to re-run on the same or newer payload
- Confirm behaviour on a stale or older payload (should orb reject, warn, or accept?)
- Produce an API design doc covering the endpoint contract

### Spike 8. Backup, DR, and availability
**Question:** What is the right backup and DR strategy for orbital as a tier-0 service?

**Context:** Orbital is the authoritative intent store for the fleet — if it is unavailable or its data is lost, no configuration exports can be produced and no new modular data centers can be onboarded. This places stronger availability and recovery requirements on orbital than a typical service. DGraph community edition only has the export mutation (`json.gz` + `schema.gz`) for full snapshots — no incremental backups. PostgreSQL (orb registry, audit logs, schema versions) also requires a backup strategy. Both must be addressed together.

**Approaches to evaluate for DGraph:**

| Approach | Notes |
|---|---|
| DGraph export + blob upload | CronJob triggers export mutation, uploads to Azure Blob. Simple, portable, same format as orb sync payload. Full snapshots only. |
| Velero | Backs up DGraph PVCs at the Kubernetes storage layer. More atomic but heavier dependency. |
| Azure Disk snapshots | VolumeSnapshot via CSI driver. Near-instant but Azure-specific and restore process needs validation. |

**Success criteria:**
- Define RTO and RPO targets appropriate for a tier-0 service
- Validate DGraph backup approach end-to-end: trigger backup, store in Azure Blob, restore into a fresh DGraph instance
- Validate PostgreSQL backup approach end-to-end: backup and restore for all orbital operational data
- Measure backup size and restore time against a representative dataset; confirm they meet RTO/RPO targets
- Assess whether single-region Azure Blob storage is sufficient or geo-redundancy is required
- Define retention policy and storage cost estimate
- Design how orbital tracks backup records in PostgreSQL (`backup_records` table)

**Do not start until Spike 4 (DGraph operations) is complete.**

### Spike 9. Authentication
**Question:** How do we implement auth in orbital?

**Context:** The primary consumer is Atlas UI — users authenticate with their OIDC provider, get a JWT, and send it as a Bearer token to orbital. Orbital validates the token against the provider's JWKS endpoint. The implementation must be IdP-agnostic — orbital should not be wired to any specific provider.

**Success criteria:**
- JWT bearer validation working end-to-end — token validated against OIDC provider JWKS
- IdP-agnostic: works with any OIDC-compliant provider
- Covered by E2E tests

### Spike 10. Report intake API
**Question:** What is the right API for orbital to receive drift and divergence reports?

**Context:** Orbital exposes a transport-agnostic report intake API. The edge writes signed reports to a shared external location (deployment layer concern); a delivery agent reads from that location and calls orbital's intake API. Orbital never knows or cares about the transport — it just receives and verifies structured reports.

Orbital must receive these reports and expose divergence to cloud administrators, who resolve field-level conflicts by publishing a new ConfigBundle with one of three directives: **Force** (cloud intent wins), **Accept overrides** (incorporate local values), or **Ignore** (acknowledge divergence, leave as-is).

**Success criteria:**
- Define the intake API: endpoint(s), payload schema, Ed25519 signature verification against the orb's registered public key
- Validate that reports are actionable — orbital can surface which modular data centers have diverged, on which fields, by whom, since when
- Define how orbital stores report state and orb public keys in PostgreSQL and how they are queried by admins
- Confirm orbital imposes no constraints on how the report reached the intake API — transport is the caller's concern
- Produce an API design doc covering the endpoint contract, data model, and the three resolution modes

---

## MVP Definition

> Working draft — final scope will be confirmed once spikes complete.

### Orbital (cloud)
- GraphQL Topology API — proxy DGraph with auth, rate limiting, and caching
- Schema management — versioned schema apply with backwards compatibility validation on startup
- Export API — `POST /api/v1/datacenters/{id}/export` returning scoped `json.gz` + `schema.gz`
- Orb registry — register, authenticate, and revoke orbs
- Audit log — record all config mutations with actor and timestamp
- Backup — DGraph and PostgreSQL backup to Azure Blob with tracked records

### Orb (edge)
- Local DGraph — hold a complete copy of its data center's intended state, fully offline
- Config import — load `json.gz` from export API or file (air-gap)
- Drift reporting — observe actual state, compare to intended state, report the gap to orbital
- Discovery — scan local BMC and inventory APIs; export discovered graph for orbital import

### Explicitly out of scope for v1
- Network infrastructure config items (owned externally)
- PLM and ITSM integrations — design TBD, vendor selection in progress
- Multi-DGraph instance per data center

---

## External Integration Dependencies

These are integration touchpoints that orbital must support but does not own. Vendor selection and design are being driven by other teams. Orbital's API-first design should remain flexible enough to accommodate them — no orbital work is blocked on these, but MVP scope may be affected by their timelines.

| System | Role | Status |
|---|---|---|
| **Atlas UI** | Customer-facing digital twin — queries orbital via GraphQL to visualize modular data center topology | Integration approach defined. Atlas calls orbital; orbital proxies DGraph. |
| **Product Lifecycle Management (PLM)** | Source of bill of materials for data center hardware — orbital may query PLM to enrich or validate configuration items | Vendor evaluation in progress by another team. Integration design TBD. |
| **IT Service Management (ITSM)** | Links customer support tickets to configuration changes in the data center — ITSM may call orbital to correlate incidents with config state | Vendor evaluation in progress by another team. Integration design TBD. |
