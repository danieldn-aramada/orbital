# CMDB Project Context

This file distills the full pre-work history, design decisions, and current state of the Orbital CMDB project for use in AI sessions. It is the authoritative single-load context for any session touching this project's history, strategy, or management tracking.

**Last updated:** 2026-05-22 (session 5 — deep design read of SDD v0.3 + Sedar's proposal)

---

## What This Is and Why It Exists

Orbital is Armada's graph-native CMDB and configuration management system for modular data centers (Galleons). It was designed and built from scratch after an 8+ week evaluation concluded that no off-the-shelf DCIM or CMDB tool met Armada's core constraint: **air-gap first**. The evaluation also revealed that Netbox (the incumbent) does not natively model the configuration domains required — iDRAC profiles, storage devices, Kubernetes cluster config, application config, BIOS settings.

The immediate consumer is the **Atlas Digital Twin** — the Atlas UI queries orbital via GraphQL to visualize the topology of any Galleon. Longer-term consumers include PLM (bill of materials) and ITSM integrations, both vendor-pending.

**The project has three origin documents:**
- *Data Center Inventory and Configuration Management Requirements – FY27 Q2.docx* — formal requirements authored by Daniel; defines stakeholder needs per config domain (servers, storage, clusters, network, power, cooling)
- *Notes on Digital Twin for Modular Data Centers.docx* — meeting notes from 3/2/26; captures Atlas digital twin integration scope, Anish/Baker/Cass/Sedar input, and requirements from connected teams
- *SDD: DCIM & CMDB for Galleon Digital Twin in Atlas (v0.1–v0.3)* — formal design document authored by Daniel; presented 4/20; reviewed by Sedar, Artem, John 4/30

---

## Full Project Timeline

| Phase | Dates | What Happened |
|---|---|---|
| Requirements & Tool Evaluation | Jan 1 → Mar 4 | Evaluated DCIM, PLM, ITSM tools against Armada requirements. Air-gap requirement eliminated most commercial options. Formal requirements doc produced. |
| Digital Twin Scoping (Atlas) | Mar 4 → Apr 10 | Scoped how orbital must serve the Atlas digital twin. Meeting with Anish, Baker, Cass, Sedar. Notes doc produced. |
| Technology Selection | Apr 10 → Apr 14 | Selected DGraph (graph DB), Go, PostgreSQL (operational data), Valkey (cache). All decisions documented in SDD. |
| Architecture & SDD | Apr 14 → May 4 | SDD v0.1 drafted Apr 16, presented Apr 20. Sedar's architectural proposal (CCP-authored, edge-enforced pattern) reviewed Apr 21, incorporated into SDD v0.2 (Apr 27) and v0.3 (May 4). |
| Prototyping | Apr 14 → May 27 | Spikes 1–13, 17 completed Apr 20 – May 22. See table below. |
| MVP | May 27 → Jul 27 | Target (conservative). |
| General Availability | Jul 27 → Aug 28 | Target. |

---

## Five Key Design Decisions (from SDD)

These were formally evaluated, documented, and reviewed. They are settled.

**KD1 — Air-gap first.** All DCIM/CMDB solutions must operate in disconnected Galleon environments. Primary filter that eliminated most vendor tools and drove the custom build decision. Also positions Armada for customer data centers with strict connectivity constraints (e.g., MHI Japan deployments).

**KD2 — Netbox stays for network; new CMDB for everything else.** Netbox does not natively model iDRAC, storage, Kubernetes cluster config, or application config. Expanding via plugins evaluated and rejected. New graph-native CMDB for all non-network config items.

**KD3 — Graph database (DGraph).** Config items are nodes; relationships are edges. Core queries are inherently graph-shaped: traversal, impact analysis, change lineage, observability correlation. SQL alternatives evaluated and rejected.

**KD4 — GraphQL API.** Flexible, client-driven queries. Atlas UI requests only what it needs. Schema-first, reduces need for multiple REST endpoints.

**KD5 — K8s controller pattern for edge actuation.** Actuation extends the NCP/ZTP CR-based controller model. CMDB is never in the reconciliation path. Four invariants (verbatim from SDD):
1. Nothing in the cloud executes directly against a Galleon. The cloud publishes intent. Galleons pull and apply locally.
2. Desired state and observed state are represented explicitly and may diverge during disconnection windows. Divergence is data, not an error condition.
3. Authoritative reconcilers run locally within the Galleon as domain-specific Kubernetes controllers. The cloud is never part of the reconciliation path. CMDB is NOT part of the reconciliation path.
4. CMDB at both cloud and edge tiers serves as a graph index and relationship store. Configuration actuation and enforcement flow through Kubernetes controllers and CRDs.

---

## Full System Architecture — Three Separate Concerns

The SDD Section 3.2 edge architecture diagram shows three distinct concerns. **These are separate repositories/deployables.** Conflating them is a design error.

### Concern 1: CMDB (orbital at cloud, orb at edge)

**Cloud — orbital** (`github.com/armada/orbital`)
- Go backend service on AKS: auth/authz middleware, rate limiting, request validation, DGraph orchestration, Valkey caching
- DGraph (blue live + scratch export instances): stores all config items and relationships
- PostgreSQL: all operational data — users, orbs registry, export jobs, backup records, audit log, schema versions, OCI artifacts
- Valkey: cache-aside for read-heavy graph queries; orbital operates correctly without it
- Export API: `POST /api/v1/datacenters/{id}/export` → scoped `json.gz` + `schema.gz` per data center
- Topology API: proxies DGraph GraphQL with auth/rate-limit/cache
- Management UI: HTMX + Go templates + Bulma

**Edge — orb** (`github.com/armada/orbital`, `cmd/orb/`)
- Local DGraph: holds intended design state mirrored from cloud via most recent import
- Serves design intent queries offline — powers the local digital twin for Edge Atlas
- **Does not scan hardware. Does not read K8s resources. Not a K8s controller.**
- Exposes a divergence report intake API: other edge components (e.g., ConfigBundle controller) POST divergence reports; orb publishes them to external storage (S3/OCI) for orbital to consume
- **Orb is the Edge CMDB — a graph index and divergence report relay. It does not detect divergence itself.**

### Concern 2: ConfigBundle project (separate repo, not yet started)

**Cloud — Bundle Generator**
- Reads orbital's export API to get the DGraph subgraph (`json.gz` + `schema.gz`)
- Assembles full ConfigBundle OCI artifact (3 layers — see below)
- Signs with cosign private key, pushes to cloud registry (ACR)
- May validate and run schema checks before publishing

**Edge — Galleon Agent**
- Polls edge registry (Zot) for new ConfigBundle artifacts
- Verifies cosign signature; rejects bundles that fail
- Writes ConfigBundle CR to Galleon's etcd (does NOT decompose it)
- Does not interpret bundle contents

**Edge — ConfigBundle Controller**
- Reconciles ConfigBundle CR into domain-specific child CRs: `ServerConfig`, `ClusterConfig`, `NetworkConfig`, etc.
- Owns child CRs via ownerReferences; creates/updates/deletes to match bundle
- Uses Server-Side Apply with field manager `config-bundle-controller`
- **Respects any field whose manager is something other than itself** (local override)
- Does NOT actuate hardware directly

### Concern 3: Domain Controllers (NCP/ZTP extension, other teams)

- `ServerConfigController`, `ClusterConfigController`, `NetworkConfigController`, etc.
- Extend the NCP/ZTP CR-based controller pattern to all config domains
- Event-driven, level-triggered reconciliation against their specific CRs
- Report observed state back via CR status subresource and events

---

## ConfigBundle OCI Artifact Structure (SDD Section 4.8)

Each ConfigBundle is a single OCI artifact with three layers:

| Layer | Media Type | Description |
|---|---|---|
| `configbundle.yaml` | `application/vnd.armada.configbundle.manifest.v1+yaml` | ConfigBundle CR for target Galleon |
| `data.json.gz` | `application/vnd.armada.configbundle.data.v1+json` | DGraph export of ConfigItem subgraph |
| `schema.gz` | `application/vnd.armada.configbundle.schema.v1+json` | DGraph schema |

**Important:** Orbital's export API currently produces layers 2 and 3 only (`data.json.gz` + `schema.gz`). Layer 1 (`configbundle.yaml`) is the ConfigBundle generator's responsibility. Orb currently imports raw orbital exports, which bypasses the ConfigBundle layer — this is correct for prototyping but is NOT the production import path.

Artifacts are tagged monotonically (v1, v2, ...) and never overwritten. References after push use digest, not tag.

**Signing:** Cloud signs each ConfigBundle with cosign private key after pushing to ACR. Signature stored as OCI referrer attached to bundle digest. Galleons hold only the public key. Verification never requires reaching ACR.

**Transport:**
- **Connected:** Edge registry (Zot) polls ACR on configurable interval, syncs Galleon's repo prefix automatically
- **Air-gapped:** Admin pulls bundle from ACR to laptop, pushes directly to edge registry. Galleon agent picks up on next poll.
- Galleon agent always pulls from local edge registry — never ACR directly.

---

## ConfigBundle CRD Design (SDD Section 4.6)

ConfigBundle is the top-level orchestration object that aggregates and scopes the full configuration set for a target Galleon. Example:

```yaml
apiVersion: armada.ai/v1
kind: ConfigBundle
metadata:
  name: colo-galleon
spec:
  servers:
    - name: server-01
      biosProfile: performance
      powerLimit: "500w"
      pxeBoot: enabled
      raidConfig: non-raid
  clusters:
    - type: workload
      kubernetesVersion: "1.28"
      nodeCount: 7
      storageClass: ceph-rbd
```

The ConfigBundle controller decomposes this into child CRs (`ServerConfig`, `ClusterConfig`, etc.) via Server-Side Apply with field manager `config-bundle-controller`. Child CRs reference the ConfigBundle via `ownerReferences`.

---

## Local Overrides & Conflict Resolution (SDD Section 4.7)

**Conflict resolution principle (verbatim from SDD):** *Cloud authored intent wins for desired state; observed state is always reported faithfully; conflicts are surfaced but not auto resolved.*

### Mechanism: Kubernetes Server-Side Apply field managers

1. **Edge admin edits a field:** Via local CLI or UI, applies via SSA with field manager `local:<admin-identity>`:
   ```
   kubectl apply --server-side --field-manager=local:<admin-id> ....
   ```

2. **Ownership recorded:** K8s `managedFields` metadata records that field X on CR Y is owned by the local admin. The `config-bundle-controller` field manager no longer owns that field.

3. **Subsequent bundles respect it:** ConfigBundle controller applies as `config-bundle-controller`; SSA automatically leaves fields owned by other managers untouched.

4. **Divergence surfaces at CCP:** ConfigBundle controller reads `managedFields` from its managed CRs, detects fields owned by `local:*` managers, packages field ownership metadata into a signed divergence report, and POSTs it to orb's intake API. Orb publishes the report to external storage (S3/NFS). Cloud polls the external location and imports the report.

5. **Cloud admin resolves:**

| Action | Description |
|---|---|
| **Accept Overrides** | Review local overrides in divergence report. Publish new ConfigBundle incorporating those values. Edge admin releases SSA ownership on accepted fields. |
| **Force** | Publish new ConfigBundle with explicit takeover directive on specific fields. Galleon agent removes local SSA ownership on arrival. Cloud intent wins regardless of local override. |
| **Ignore** | Publish new ConfigBundle with no takeover directives. Local overrides persist. Divergence remains visible in report. |

**v1 scope:** Field-level divergence only. Structural divergence (local admin creates or deletes CRs the bundle doesn't know about) is out of scope for v1.

### What this means for orb's role in divergence

**Orb does not detect divergence.** Divergence detection is ConfigBundle controller's responsibility — it already has full visibility into `managedFields` on the CRs it manages. ConfigBundle controller packages divergence reports and POSTs them to orb's intake API.

**Orb's role is transport relay:** accept divergence reports from edge components, sign and publish to external storage (S3/OCI) for orbital to consume. Orb has no K8s API access and no awareness of K8s CRD schemas.

---

## Divergence Report Format (Settled)

The canonical divergence report format is what orb's intake API accepts and what orbital displays. It is defined in orbital's terms — DGraph node + field — regardless of how the override originated:

```json
{
  "orbId": "netbox:server-01",
  "field": "sshEnabled",
  "intendedValue": false,
  "overrideValue": true,
  "who": "local:admin-sedar",
  "when": "2026-05-22T14:00:00Z"
}
```

**Source of reports (all produce the same format):**
- ConfigBundle controller (Path A): detects SSA field ownership on ConfigBundle CRs, translates to orbId + field terms, POSTs to orb's intake API
- Orb UI override button (Path B, future): orb records override locally, report is native — no translation needed
- Manual API call: admin or tooling POSTs directly to orb's intake API

**Field names:** must match DGraph schema field names exactly. Cb-generator uses orbital's field names when building ConfigBundle CR spec — no separate vocabulary.

**Path A is the first ConfigBundle implementation:** cb-controller reads `managedFields` on its managed CRs, maps ConfigBundle field paths back to orbIds (embedded by cb-generator at build time), and emits reports in the canonical format. The SSA mechanism is a ConfigBundle implementation detail invisible to orb and orbital.

## Divergence Report Transport (SDD Section 4.9)

Reports describe field-level divergence between active ConfigBundle intent and observed state.

**Transport options (edge admin chooses based on connectivity):**
- **Opt 1 (Courier/Air-gapped):** Edge admin downloads divergence report and manually uploads to cloud backend via UI or API
- **Opt 2 (Connected):** Orb writes signed divergence report to external location (S3/NFS) on schedule or on demand. Cloud polls, verifies orb's Ed25519 signature, imports.

Divergence reports are observability artifacts only. They inform cloud admin decisions but do not drive actuation.

---

## CMDB's Earned Role in the System

Per Sedar's proposal, CMDB earns its keep in **cross-domain relationships that K8s ownerReferences cannot express**:

- **Cross-cluster dependencies** — Scout cluster workload depends on Triton cluster service; ownerReferences stop at cluster boundary
- **Physical-to-logical mapping** — "for PDU-3, give me all downstream pods" requires walking: PDU → chassis → server → K8s node → pods. Cheap in graph, expensive in label-selector joins.
- **WAN/QoS cascade** — prioritizing 5G over satellite changes QoS policy across compute fabric; no single CRD owns the relationship
- **BIOS influences scheduling** — BIOS power profile affects node performance SLOs; "influences" is not expressible as ownerReference
- **Hardware BOM / supply chain** — failed DIMM part number correlates with batch, predicts other failures fleet-wide
- **Change lineage across systems** — "user X modified autoscaler Tuesday, HPA Wednesday, app OOMed Thursday"
- **External-to-cluster dependencies** — workload depends on Fortanix DSM, step-ca, Azure AD — none are K8s objects

**ownerReferences encode lifecycle cascade within a cluster. CMDB encodes semantic relationships across arbitrary boundaries.** They do different jobs.

---

## What CMDB Is NOT

- Not in the reconciliation path
- Not a monitoring or observability system (uses existing Prometheus/Grafana stack)
- Not a control plane that executes against Galleons
- Not an ITSM or PLM system (integrations pending vendor selection)
- Not an observed-state monitor — orb does not scan hardware to detect drift
- **Not a ConfigBundle generator** — that is a separate project
- **Not a domain controller** — actuation belongs to K8s controllers

---

## Full DGraph Schema (from SDD Section 4.3)

The schema is richer than `schema-demo.graphql`. Full type list from SDD:

**Interface:** `ConfigItem` (id, namespace, name, createdBy, createdAt, updatedBy, updatedAt, version)

**Config Item Types:** `DataCenter`, `Server`, `SystemSettings`, `IdracSettings`, `BiosSettings`, `PxeDevice`, `ServerConfigurationProfile`, `StorageController`, `StorageDevice`, `StorageVolume`, `Processor`, `Memory`, `EthernetInterface`, `PowerSupply`, `Fans`, `KubernetesCluster`, `ClusterConfig`, `ApplicationConfig`, `KubernetesNode`, `Chassis`, `PowerSystem`, `CoolingSystem`, `StructuralComponent`, `SpareComponent`

**Non-ConfigItem types:** `Namespace`, `User`

Types not yet in `schema-demo.graphql` (to be added before MVP): `SystemSettings`, `BiosSettings`, `PxeDevice`, `Processor`, `Memory`, `EthernetInterface`, `PowerSupply`, `Fans`, `Chassis`, `PowerSystem`, `CoolingSystem`, `StructuralComponent`, `SpareComponent`, `ClusterConfig`, `ApplicationConfig`, `KubernetesNode`

---

## ConfigBundle Project Boundary (Settled Decisions)

ConfigBundle is a separate project, built after orbital MVP. The same person owns both projects. Orbital comes first — ConfigBundle is designed around orbital's APIs.

**What orbital provides to ConfigBundle:**
- Export API (`POST /api/v1/datacenters/{id}/export`) — produces `data.json.gz` + `schema.gz`
- Publish API — pushes signed OCI artifact to ACR (cb-generator calls this or calls export directly)
- Divergence report intake API on orb — accepts canonical `{orbId, field, intendedValue, overrideValue, who, when}` reports

**What ConfigBundle does with orbital's output:**
- Cb-generator: reads orbital export, assembles 3-layer ConfigBundle OCI artifact, signs, pushes to ACR
- Cb-agent: polls edge Zot registry, verifies signature, writes ConfigBundle CR to etcd
- Cb-controller: reconciles ConfigBundle CR → domain child CRs via SSA; detects `local:*` SSA overrides; translates to canonical report format; POSTs to orb

**What orbital never does:**
- Generate ConfigBundle artifacts
- Import or parse ConfigBundle CRs
- Know whether cb-agent or cb-controller exist
- Have any Go import dependency on the configbundle project

---

## Open Design Questions (as of 2026-05-22)

**Q1. Orbital's publish button vs. cb-generator trigger**
Orbital's publish button produces a 2-layer OCI artifact (data.json.gz + schema.gz). Cb-generator wraps it into 3 layers. Should cb-generator call orbital's export API directly (not rely on the publish button), or should it watch ACR for the 2-layer artifact? No implementation decision needed until ConfigBundle project starts.

**Q2. Orb's production import path vs. current prototype path**
Orb currently imports raw orbital exports (data.json.gz + schema.gz). In production, it should import full 3-layer ConfigBundle artifacts. Should both paths coexist permanently (dev/bootstrap vs production), or does the raw path get replaced? Decide before Spike 13 follow-up.

**Q3. Does orb need K8s API access? — RESOLVED: No.**
Orb does not detect divergence. ConfigBundle controller detects divergence and POSTs reports to orb's intake API. Orb has no K8s awareness.

**Q4. Path A vs Path B for local overrides (defer until ConfigBundle project)**
Path A: local admin patches ConfigBundle CR via `kubectl` SSA → cb-controller translates → orb. Path B: local admin uses orb UI → orb records + drives cb-agent SSA patch. Path A is the first implementation (simpler, aligns with Sedar's doc). Path B is better UX but requires orb to orchestrate cb-agent. Decide when ConfigBundle is being designed.

---

## What Has Been Built (Prototyping Spikes)

| # | Spike | Completed | Key Deliverable |
|---|---|---|---|
| 1 | AKS Deployment Validation | Apr 20 | Orbital + DGraph on AKS; NetworkPolicy; pod recovery validated |
| 2 | Orb CLI Structure | Apr 22 | Single binary: `orb start/scan/export/import`; `internal/cli/` scaffolding |
| 3 | PostgreSQL / ent Data Model | May 5 | 9 tables: users, orbs, namespaces, backups, export_jobs, registry_artifacts, events, restore_jobs, schema_versions |
| 4 | Management Web UI | Apr 20 – May 14 | DC tab view (HTMX, inline edit, audit diff); Servers cross-DC DataTable + drill-down (iDRAC/Storage/Config Profile tabs); Export, Backup, Restore, Audit Log, Signed Artifacts, Schema, Divergence pages; Playwright E2E suite |
| 5 | Authentication | May 8 | OIDC + local auth; orbital-cli with macOS keychain; bearer token validation end-to-end with real Azure AD v2 tokens |
| 6 | DGraph Backup to Azure Blob | May 9 | Async backup pipeline; SHA-256 dedup; configurable retention; Azure Blob + S3-compatible; validated on AKS |
| — | Config Export + OCI Pipeline | May 9 – May 18 | 8 endpoints; blue-green DGraph export topology; per-job scratch dirs; oras-go v2 + cosign signing; air-gap safe OCI publish — orbital side complete |
| 7 | DGraph Restore from Backup | May 14 | Full restore via dgraph-live idle pod; client-go exec; conflict detection; validated on AKS |
| — | Audit Log System | May 5 – May 13 | GraphQL mutation interceptor; before-state capture; LCS line diff; three-source orbId extraction; per-entity audit tabs |
| 8 | AKS Dev Environment | May 18 | Full deploy manifests, Helm charts, seed scripts, step-by-step deploy guide |
| 9 | Hardware Data Modeling & Validation | May 15 | 4 new iDRAC schema fields; 9 data centers modeled from real Netbox hostnames and rack topology |
| — | orbital-cli | May 11 | `orbital get datacenter/datacenters`; bearer auth; macOS keychain; kubectl-style output |
| 13 | Orb import API | May 21 | OCI puller (oras-go v2), cosign verify, dgraph live import, polling loop, Zot local registry |
| 17 | Orb UI | May 22 | Shared template infrastructure, PageActions (read-only mode), orb Echo server, status page (pre/post import), import subgraph, inventory (Config Items), schema version, DC + servers (read-only DataTables + HTMX tabs), import history, divergence report |

---

## Current State (as of 2026-05-22)

**Phase:** Prototyping → MVP (target July 27, 2026; GA August 28, 2026)

**Active:**
- **Spike 11 (Authorization)** ← blocks MVP — bearer validation done; remaining: Azure AD App Roles, DGraph `@auth` directives, Echo middleware role enforcement, offline JWT integration tests ⚠️ Opus design session first

**MVP gaps remaining:**
- Authorization (Spike 11) ← next priority
- Valkey cache-aside (Spike 9b)
- Schema management — versioned apply with backwards compat check on startup
- Orb registry — register, authenticate, revoke orbs
- Orb deployment model (Spike 15), API surface & authN/Z (Spike 16), divergence reporting (Spike 14)
- Orb local overrides / config actuation abstraction — belongs to ConfigBundle domain + Spike 14
- Testing foundations, security hardening, production deployment

---

## Stakeholders

| Person | Role |
|---|---|
| Daniel | Author (DRI) — requirements, SDD, all prototyping spikes |
| Sedar | Architectural review — CCP-authored, edge-enforced proposal (Apr 21); K8s controller pattern |
| Artem | SDD reviewer (Apr 30) |
| John | SDD reviewer (Apr 30) |
| Anish | Atlas digital twin — senior stakeholder; initiated digital twin scoping meeting (3/2/26) |
| Baker | Atlas — drafted PRD for Atlas XP |
| Cass | Atlas — Propel demo and meeting; PLM source of truth discussion |
| Samir | Drafting Atlas PRD |

---

## Document Index

| Document | Location | What It Contains |
|---|---|---|
| Requirements | `~/Documents/Data Center Inventory and Configuration Management Requirements – FY27 Q2.docx` | Formal requirements per stakeholder and config domain |
| Digital Twin Notes | `~/Downloads/Notes on Digital Twin for Modular Data Centers.docx` | Meeting notes from 3/2/26; Atlas integration scope |
| SDD (v0.3) | `~/Downloads/SDD DCIM & CMBD for Galleon Digital Twin in Atlas (3).pdf` | Formal design doc — 5 key decisions, full architecture, GraphQL schema, ConfigBundle CRD, local overrides, data transport. **Read CMDB_CONTEXT.md instead — all key content is distilled here.** |
| Architectural Proposal | `~/Downloads/CMDB_Architectural_Proposal.docx` | Sedar's CCP-authored/edge-enforced proposal. **Key content distilled into this file.** |
| Orbital CHANGELOG | `CHANGELOG.md` | Completed spike detail — API contracts, what was validated, key decisions |
| Orbital ROADMAP | `ROADMAP.md` | Gantt chart; spike table; What We've Built; MVP Planning; MVP Definition; Technical Debt |
| Excel Tracker | `~/Downloads/Edge Platform Q4-Q1.xlsx` — sheet `cmdb` | Management-facing progress tracker |
