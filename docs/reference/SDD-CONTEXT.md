# SDD + Architectural Proposal + SSA Notes — Consolidated Context

Read this before: questions about original design intent ("what does the SDD say about X?"), invariants that constrain new spikes, or SSA semantics. Grep this doc for the keyword rather than reading top-to-bottom — it's a reference lookup, not a doc to read sequentially.

> **Purpose:** Single technical-context reference for `orbital`/`orb` work. Consolidates content from three foundational documents so future sessions can grep-and-read instead of re-extracting `.docx`/`.pdf` files.
>
> **Sources:**
> - **SDD v0.3** — `~/Downloads/SDD DCIM & CMBD for Galleon Digital Twin in Atlas (3).docx` (Daniel Nguyen, 2026-04-16 → 2026-05-04)
> - **Architectural Proposal** — `~/Downloads/CMDB_Architectural_Proposal.docx` (Sedar, 2026-04-21) — peer review of the SDD; introduces the "CCP-authored, edge-enforced" pattern.
> - **Notes on Server-Side Apply** — `~/Documents/Notes on Server-Side Apply.pdf` (Daniel Nguyen, 2026-04-28)
>
> **Relationship to `docs/project-background.md`:** That file is the narrative onboarding doc; this one is the technical lookup. They overlap; when in doubt, this file is the source of truth for "what does the SDD/Proposal/SSA notes actually say."

---

## 1. The Five Key Design Decisions (SDD §2)

| # | Decision | Rationale |
|---|---|---|
| 1 | **Air-gapped first.** DCIM/CMDB must run in disconnected environments. | Eliminated most COTS vendors during eval; positions us for MHI Japan and similar deployments with strict isolation. |
| 2 | **Netbox stays as source of truth for network infrastructure only.** Everything else (servers, iDRAC, K8s, app config) goes in the new CMDB. | Many Edge Platform services depend on Netbox's network model; Netbox doesn't natively model storage, iDRAC profiles, K8s, app config. |
| 3 | **Configuration items as a graph database.** Nodes = config items, edges = relationships. | Core use cases (traversal, impact analysis, change lineage, observability correlation) are inherently graph-shaped. SQL-only remains viable but punishes connected-data workloads. |
| 4 | **GraphQL as the primary API.** | Flexible client-driven queries; nested relationships and variable traversal depth; reduces REST endpoint count. |
| 5 | **Edge actuation follows the K8s controller pattern, not a CMDB-driven reconciler.** | Anchors actuation in NCP/ZTP pattern already in use. Gives cloud mutations clean "intent-only" semantics. |

## 2. The Four Invariants (SDD §2, Decision 5)

These are **load-bearing**. Every architectural choice in orbital/orb must preserve them.

> 1. **Nothing in the cloud executes directly against a Galleon.** The cloud publishes intent. Galleons pull and apply configuration locally.
> 2. **Desired state and observed state are represented explicitly and may diverge during disconnection windows.** Divergence is data, not inherently an error condition.
> 3. **Authoritative reconcilers run locally within the Galleon as domain-specific Kubernetes controllers.** The cloud is never part of the reconciliation path. **CMDB is NOT part of the reconciliation path.**
> 4. **CMDB at both cloud and edge tiers serves as a graph index and relationship store.** Configuration actuation and enforcement flow through Kubernetes controllers and CRDs.

## 3. Architecture — Three Separate Concerns

| Concern | Owner | What it does |
|---|---|---|
| **CMDB** | This repo (`orbital` cloud, `orb` edge) | Graph index over intent (cloud) and projected intent + observed state (edge). NOT a reconciler. |
| **ConfigBundle** | Separate repo (not started) | cb-generator reads CCP CMDB, produces signed OCI artifacts. cb-controller (Galleon-local) decomposes bundles into domain CRs. |
| **Domain controllers** | NCP/ZTP team and successors | One controller per config domain (server BIOS/iDRAC, cluster, app, network, power, cooling). Each watches its CRs and reconciles into real state. |

**ConfigBundle is the seam.** It's how cloud intent reaches the edge. orbital's contract ends at producing the export (`json.gz` + `schema.gz`); cb-generator wraps that into the deliverable bundle.

## 4. ConfigBundle OCI Artifact Structure (SDD §4.8)

Single OCI artifact, three layers, monotonically tagged (`v1`, `v2`, …, never overwritten):

| Layer | Media type | Contents |
|---|---|---|
| `configbundle.yaml` | `application/vnd.armada.configbundle.manifest.v1+yaml` | ConfigBundle CR for the target Galleon |
| `data.json.gz` | `application/vnd.armada.configbundle.data.v1+json` | DGraph export of ConfigItem subgraph for the target |
| `schema.gz` | `application/vnd.armada.configbundle.schema.v1+json` | DGraph schema |

- **Signing:** cosign signature stored as OCI referrer artifact. Public key only on the Galleon; verification is fully offline.
- **Transport:** Connected path = Galleon's zot polls upstream ACR. Air-gapped path = admin pulls to laptop, pushes to local zot. **Galleon agent always pulls from local zot, never ACR directly.**
- **References after push always use digest, not tag** (immutability).

## 5. ConfigBundle CRD (SDD §4.6)

- Single CR per Galleon, holds desired state across all configured domains.
- cb-controller decomposes it into per-domain child CRs (`ServerConfig`, `ClusterConfig`, `NetworkConfig`, etc.) via SSA with manager `config-bundle-controller`.
- Child CRs owned via `ownerReferences` — bundle update → cb-controller adds/updates/deletes children.
- cb-controller respects any field whose manager is something other than itself (the local-override path; see §6).

## 6. Local Overrides via Kubernetes SSA (SDD §4.7 + Notes on SSA)

The **mechanism that makes divergence work**. Built on K8s Server-Side Apply field-manager semantics — no bespoke versioning required.

### 6.1 The override workflow

| Step | What happens | Where it's recorded |
|---|---|---|
| Edge admin edits a field | `kubectl apply --server-side --field-manager=local:<admin-id> ...` | K8s API server merges; `managedFields` on the CR now lists `local:<admin-id>` as owner of that field path |
| Subsequent ConfigBundle reconcile | cb-controller does its own SSA as `config-bundle-controller`; SSA respects existing field ownership | The local override **survives the reconcile** with no special handling |
| Divergence surfaces to cloud | cb-controller reads `managedFields`, detects `local:*` ownership, POSTs report to orb's intake API | orb writes to local `DataDir/divergence/current.json`, then publishes to S3 |

### 6.2 The three SSA conflict resolutions (verbatim from K8s docs, validated in the SSA Notes PDF)

When an apply tries to change a field another manager claims, K8s returns 409 Conflict with `FieldManagerConflict`. Three resolutions:

1. **Overwrite value, become sole manager** — `--force-conflicts`. Removes other managers from `managedFields` for that field. The field is now exclusively owned by the forcing manager.
2. **Don't overwrite value, give up management claim** — apply the manifest **with that field omitted**. The field is left unchanged; the manager is removed from `managedFields` for that field.
3. **Don't overwrite value, become shared (co-owner) manager** — apply the manifest with the field set to **the exact current server-side value**. Manager is added alongside the existing one; **neither can change the value unilaterally afterward.**

### 6.3 Behavioral details from the SSA Notes walkthrough

The notes prove these behaviors with a concrete ConfigMap example (fields: `biosProfile`, `powerLimit`; managers: `upstream`, `local-user`):

- **Atomic, no partial applies.** If upstream applies with both `biosProfile` and `powerLimit` set, and `local-user` owns `powerLimit`, the **whole apply fails 409**. K8s does NOT silently apply the non-conflicting field. This is critical: cb-controller cannot do "best-effort partial reconcile."
- **Resolution #2 is the right pattern for upstream after detecting divergence.** cb-controller's apply must **omit** the locally-owned fields (not include-them-with-current-value, which would force co-ownership).
- **Co-ownership is sticky.** Once both managers own a field, neither can change it without conflict. Resolving requires one of them to deliberately drop ownership (Resolution #2: omit + apply).
- **Controllers should always `--force-conflicts` on objects they own.** Per K8s docs and validated in the notes — controllers are non-interactive and must converge.

### 6.4 The three cloud admin actions (SDD §4.7)

| Action | Mechanism | Result |
|---|---|---|
| **Force** | Publish new bundle with explicit takeover directive on specific fields | cb-controller does `--force-conflicts` on those fields. Local SSA ownership is removed; cloud intent wins. For escalation. |
| **Accept Overrides** | Cloud admin updates CCP CMDB to match the local value. Next bundle carries the new intent. | Edge admin releases SSA ownership on accepted fields (Resolution #2). Cloud and local converge cleanly. |
| **Ignore** | Publish next bundle with NO takeover directives. | Local overrides persist for as long as edge admin retains SSA ownership. Divergence remains visible in the report. |

### 6.5 v1 scope boundary

**Field-level divergence only.** Structural divergence (local admin creates or deletes CRs that the bundle doesn't know about) is **out of scope for v1**. Restrictive but tractable; covers emergency local-reconfiguration cases.

## 7. Divergence Report (SDD §4.9 — settled format)

### 7.1 Canonical entry shape

```json
{
  "orbId":         "namespace:item-name",
  "field":         "fieldNameMatchingDGraphSchema",
  "intendedValue": <value from bundle intent>,
  "overrideValue": <observed local value>,
  "who":           "local:<admin-id>",
  "when":          "<RFC3339 timestamp>"
}
```

- **Field names must match DGraph schema exactly.** cb-generator embeds the K8s-path → orbId+field mapping at bundle build time. orb knows nothing about K8s field paths.
- **All sources produce the same format.** cb-controller (Path A, primary), orb UI override (Path B, future), manual API call — all post the same shape.

### 7.2 Transport invariants

- **Orb never sends to orbital directly over HTTP.** Air-gap invariant. Transport is S3/OCI.
- **Two transport modes** (admin chooses by connectivity):
  - **Opt 1 — Courier/Air-gapped:** Admin downloads divergence report, manually uploads to cloud via UI/API.
  - **Opt 2 — Connected:** Orb writes signed snapshot to S3/NFS on schedule or on demand. Cloud polls, verifies Ed25519 signature, imports.
- **Reports are observability artifacts only.** They inform cloud admin decisions; they don't drive actuation.

### 7.3 Orb's role boundary

**Orb is a transport relay. It does NOT detect divergence.**

- ConfigBundle controller has full visibility into `managedFields` on its managed CRs; it detects the divergence and POSTs to orb.
- Orb accepts the canonical format, persists locally, publishes to S3/OCI.
- Orb has no K8s API access, no awareness of K8s CRD schemas.

### 7.4 Replace-not-merge semantics

Orb's `POST /api/v1/divergence` **replaces** the pending set. cb-controller is responsible for posting the full current picture every time. If a field stops appearing in subsequent POSTs, it's no longer diverging and disappears from the next published snapshot.

## 8. What CMDB Earns Its Keep On (Sedar's Architectural Proposal)

The Architectural Proposal pushes back on "CMDB-driven reconciler" framing in the SDD. Reconciliation belongs to K8s controllers. But CMDB still has a real, distinct job: **encoding semantic relationships across boundaries that K8s ownerReferences cannot express.** Concrete examples Sedar cited (all valid; all motivate continued CMDB investment):

- **Cross-cluster dependencies** — Scout workload depends on a Triton service in the same Galleon. ownerReferences stop at the cluster boundary; CMDB models the dependency.
- **Physical-to-logical mapping** — "for PDU-3, give me all downstream pods." Walk: PDU → chassis → server → K8s node → pod. Trivial in a graph, expensive in label-selector joins. **Sedar called this the strongest case.**
- **WAN/QoS cascade** — 5G-vs-satellite preference on a Cruiser affects QoS policy across compute. No single CRD owns the relationship.
- **BIOS influences scheduling** — BIOS power profile affects node performance SLOs. "Influences" isn't expressible as ownerReference.
- **Hardware BOM / supply chain** — failed DIMM part number → batch → fleet-wide other-failures.
- **Change lineage across systems** — "user X modified autoscaler Tuesday, HPA Wednesday, app OOMed Thursday." ownerReferences don't track authorship or temporal links.
- **External-to-cluster dependencies** — workload depends on Fortanix DSM, step-ca, Azure AD. None are K8s objects.

The pattern across all: **ownerReferences encode lifecycle cascade within a cluster. CMDB encodes semantic relationships across arbitrary boundaries.** They do different jobs.

## 9. What CMDB Is NOT (SDD + Architectural Proposal, both)

- Not in the reconciliation path.
- Not a monitoring or observability system (uses existing Prometheus/Grafana stack).
- Not a control plane that executes against Galleons.
- Not the authoritative state for actuation — domain controllers + their CRs are.
- Not a versioning system — SSA tracks field ownership; bundles are content-addressed; CMDB has audit history. Parallel version control is unnecessary.

## 10. DGraph Schema (SDD §4.3)

The SDD §4.3 schema is the **target** model. The current `schema/schema.graphql` in this repo is a working subset; field names and ConfigItem types align but not all SDD types are implemented yet.

> **Don't treat the SDD schema as the live source of truth.** The repo's `schema/schema.graphql` is what's deployed. When SDD types are added, they land in `schema/schema.graphql` and bump `schema/VERSION` if predicates change.

Notable SDD types not yet in this repo: `PxeDevice`, `Chassis`, `PowerSystem`, `CoolingSystem`, `StructuralComponent`, `SpareComponent`, `Memory`, `EthernetInterface`, `Processor`, `PowerSupply`, `Fans`, `BiosSettings`, `SystemSettings`, `ApplicationConfig`, `ClusterConfig`. Most are post-MVP.

## 11. ConfigBundle Repo (`~/armada/configbundle`)

The companion repo exists and is partway through implementation. Module path `github.com/armada/configbundle`. kubebuilder-based. **Read its `CLAUDE.md` and `ROADMAP.md` before designing anything that crosses the boundary.**

### 11.1 Architecture as it actually shipped (differs from the original SDD/Proposal in key ways)

| Aspect | SDD/Proposal said | What configbundle actually built |
|---|---|---|
| Edge agent | Separate "Galleon agent" sidecar that pulls bundle and writes CR to etcd | **No separate agent.** ConfigBundle Controller owns the full pipeline. |
| OCI pull at the edge | cb-controller polls Zot directly | **Orb is the single artifact ingress.** Orb polls Zot, cosign-verifies, imports graph layers to DGraph, then **dispatches** the manifest layer to CB Controller via `POST /consume` on `:8095`. CB Controller never holds OCI credentials. |
| Bundle generator deployment | (unspecified) | **Sidecar in the Orbital pod** for MVP (separate image, `localhost:8020`). Exposes `POST /bundle` enricher API. |
| Where intent is read from | (unspecified) | cb-generator queries orbital's GraphQL during the publish flow; orbital is the sole OCI producer (cb-generator returns bytes to orbital). |

### 11.2 The ConfigBundle CRD (current `api/v1/configbundle_types.go`)

```
ConfigBundleSpec
├── Datacenter (string, required)             // matches orbital namespace name
└── Servers []ServerSpec  (+listType=map +listMapKey=serviceTag)
    ├── ServiceTag (string, required)         // identity key
    ├── Hostname   (string, required)         // CR-naming basis
    ├── OobIP      (string, required)         // for Redfish actuation
    └── Idrac IdracSpec
        ├── FirmwareVersion
        ├── SSHEnabled
        ├── IPMIEnabled
        ├── LockdownModeEnabled
        ├── OsToIdracPassThroughEnabled
        ├── UsbManagementPortEnabled
        ├── DHCPEnabled
        └── RacadmEnabled
```

- **`+listType=map +listMapKey=serviceTag` IS in place.** Per-server-entry SSA field ownership works. Prerequisite for Spike 7 is met.
- Other domains (Cluster, Application, Network, etc.) are post-MVP.

### 11.3 Settled in configbundle (the boundary rules)

- **`local:admin` is the single fixed field manager string for MVP.** Not dynamic. Not per-user. Post-MVP RBAC will introduce per-person `local:<admin-id>`.
- **Local overrides are on ConfigBundle CR ONLY.** Child CRs (ServerConfig, etc.) are derived state and never an override surface. Decomposition Reconciler uses `--force-conflicts` (ForceOwnership) on child CRs — they always reflect the ConfigBundle CR faithfully.
- **ConsumeServer does NOT use ForceOwnership on the ConfigBundle CR.** That's what makes overrides survive across dispatch cycles.
- **`omitAdminOwnedServers` pattern in the consume path.** Before applying a new manifest, ConsumeServer inspects `managedFields`, finds server entries where `local:admin` owns any field, and **omits those entire entries** from the SSA patch. This is "SSA Resolution #2 (give up management claim)" applied per-entry. Mandatory — skipping it causes 409s that block legitimate config changes.
- **Bundler is per-request, not server-configured.** Orbital's publish API accepts enricher URLs in the request body. cb-bundler is one of those URLs.
- **Enrichment is all-or-nothing.** A non-2xx from the bundler causes orbital to fail the publish; no partial artifacts.

### 11.4 Spike status in configbundle

| Spike | Status | Notes |
|---|---|---|
| D1–D3 | ✅ Done | Architecture design, integration contract, edge agent decision |
| 1 — Scaffold | ✅ Done | kubebuilder, `armada.ai/v1`, Makefile |
| 2 — bundle package (media types) | ✅ Done | `bundle/mediatype.go` — `MediaTypeManifest`, `MediaTypeData`, `MediaTypeSchema` |
| 3 — Bundler service | ⬜ Not started — next priority | `POST /bundle`; queries orbital GraphQL; returns ConfigBundle manifest YAML layer |
| 4 — ConfigBundle CRD (iDRAC) | ✅ Done | Schema above; cluster + app domains post-MVP |
| 5 — OCI pipeline | ✅ Done then superseded by 5a | Originally a Puller; replaced with consumer model |
| 5a — ConsumeServer | ✅ Done | `POST /consume` on `:8095`; orb dispatches; oras-go/cosign deps removed |
| 6 — Decomposition Reconciler | ✅ Done (iDRAC) | ForceOwnership on child CRs; ownerReferences; cascade delete |
| 7 — Divergence reporting | ⬜ Not started | **The current design session.** See §12 below. |
| 8 — E2E integration test | ⬜ Not started | Multi-repo pipeline; lives in cb repo |

## 12. Divergence Reporting — The Open Architectural Question (active design session)

There is a **real mismatch** between configbundle's current plan and orbital/orb's current implementation. This needs settling before either side builds Spike 7.

### 12.1 What configbundle's docs say it will do (`docs/claude/edge-context.md`)

The Divergence Reporter is a `ctrl.Runnable` (scheduled) inside CB Controller. It:
1. Inspects `managedFields` on the ConfigBundle CR (not child CRs)
2. Identifies fields owned by `local:admin`
3. Compares against the last applied manifest for field-level diff
4. **Publishes directly to `DIVERGENCE_REPORT_DEST` (S3/NFS path env var)**

That last bullet is the mismatch.

### 12.2 What orbital/orb's implementation already does

- Orb exposes `POST /api/v1/divergence` (canonical format intake)
- Orb persists to `DataDir/divergence/current.json`
- Orb has a `Publisher` (S3 writer) ready to push to S3 on demand
- Orb's design intent: **be the relay**. cb-controller POSTs to orb; orb publishes to S3; orbital ingests from S3.

### 12.3 Two valid architectures — which is correct?

**A) CB Controller publishes directly to S3** (what configbundle's docs currently say)
- cb-controller bypasses orb entirely for divergence
- cb-controller needs S3 credentials and bucket configuration
- orb is not involved in divergence transport at all

**B) CB Controller POSTs to orb; orb publishes to S3** (what SDD §4.9 and orbital's code assume)
- cb-controller has zero S3 awareness; just HTTPs to orb's local intake
- Orb is the single edge→cloud transport boundary; matches "orb is single artifact egress" symmetry with "orb is single artifact ingress"
- orb already has the S3 publisher built

### 12.4 Recommendation: B

The "orb is the single edge↔cloud boundary" property is already established for ingress (orb is the single artifact ingress, dispatches to CB Controller). Symmetry for egress (orb is the single divergence-report egress, CB Controller pushes to orb) is the cleaner shape:

- One place at the edge that holds S3 credentials (orb), not two
- One place that knows the cloud's S3 endpoint and prefix (orb), not two
- One place that signs reports (post-MVP — orb), not two
- CB Controller's surface shrinks: it's a K8s controller that talks K8s + local HTTP, never cloud
- Matches the dispatch-direction precedent: orb→CB Controller for ingress, CB Controller→orb for egress

**This means configbundle's `DIVERGENCE_REPORT_DEST` env var should be repurposed as `ORB_DIVERGENCE_INTAKE_URL` (e.g. `http://orb:8010/api/v1/divergence`).** The Divergence Reporter POSTs the canonical entry array there.

### 12.5 What the reporter actually sends

Already-settled canonical format (SDD §4.9 + `internal/divergence/divergence.go`):

```json
[
  {
    "orbId":         "colo:srv-001",
    "field":         "sshEnabled",
    "intendedValue": false,
    "overrideValue": true,
    "who":           "local:admin",
    "when":          "<RFC3339>"
  }
]
```

Replace-not-merge semantics: every POST is the full current set.

### 12.6 The translation problem

cb-controller works in K8s field-path terms (`spec.servers[serviceTag=3RK3V64].idrac.sshEnabled`). The canonical format works in orbital terms (`orbId: colo:srv-001`, `field: sshEnabled`).

cb-bundler knows the mapping at build time (it generated the bundle from orbital's intent and knows which orbital ConfigItem each server entry came from). For divergence reporting, the simplest path is: **cb-bundler embeds the K8s-path → orbital `orbId+field` map in the bundle**, and cb-controller looks it up when emitting reports.

The mapping is essentially: for each server entry, what `orbId` does it correspond to? (The field name is identical between K8s and orbital, e.g. `sshEnabled` — there's no per-field renaming.)

Possible embed locations:
- New OCI layer (`mapping.json`) — recommended
- Inline in the ConfigBundle CR spec — pollutes CR
- Annotations on the CR — same problem

### 12.7 Status of Ed25519 signing

**Post-MVP, deferred.** Snapshots go to S3 unsigned for now. Orbital ingestion will trust whatever it polls. Re-add when the threat model justifies — the air-gap-via-courier path (Opt 1) is where signing matters most, and that path isn't built yet either.

### 12.8 Open design questions to settle in this session

1. **A vs B for divergence transport?** Recommendation: B (orb as relay), per §12.4.
2. **Mapping embed location?** Recommendation: new OCI layer.
3. **Reporter cadence?** Configurable, default ~5 min. Single env var on cb-controller.
4. **What does "Accept Overrides" do across the boundary?** Recommendation: it's a GraphQL mutation on orbital intent. cb-bundler's next run reads new intent, builds new bundle. Edge admin releases SSA ownership when the new bundle arrives (Resolution #2). No special "accept" wire format between orbital and configbundle.
5. **What does "Force" do?** Recommendation: new `spec.takeover[]` block on ConfigBundle CR. cb-bundler emits this from orbital's recorded resolution decisions. cb-controller does `--force-conflicts` on listed fields.
6. **Where do resolution decisions live in orbital?** New ent entity `DivergenceResolution{orbId, field, action, actor, decidedAt}`. cb-bundler queries this when assembling the next bundle.

**Do not start implementing Spike 7 in configbundle, or the orbital-side ingestion, until questions 1, 2, 4, 5, 6 are settled.**

## 12. Operational Notes (SDD §5–§8)

Brief lookup table; details in the SDD itself.

| Topic | Approach |
|---|---|
| Backend storage (operational) | Azure managed PostgreSQL |
| Graph storage | DGraph on K8s PVs backed by Azure managed disks |
| Caching | Valkey (Redis-compatible, no licensing constraints). Read-heavy workload; aggressive caching acceptable. |
| Backup | Dgraph data export (logical backup) to Azure Blob; rehydration from snapshot for restore |
| Auth | Service-layer (JWT/OAuth2), platform standard |
| Authz | Service-layer RBAC; DGraph never exposed to end users |
| Encryption | Azure managed disk encryption + DGraph native encryption-at-rest; keys via K8s secrets backed by Azure Key Vault |
| Observability | Existing Prometheus/Grafana stack; OpenTelemetry for distributed tracing; service-layer logging (not DGraph's enterprise-gated audit log) |

## 13. Open Design Questions (snapshotted from SDD as of 2026-05-22)

- **Bundle transport choice** — recommended OCI; settled in this repo. (Resolved.)
- **Bundle cadence** — per-change vs batched; affects how quickly intent reaches a connected Galleon. (Open in cb-generator design.)
- **Local override authorization** — who can claim a field manager, via what workflow. Needs RBAC design. (Open; not in MVP scope for this repo.)
- **Divergence report UX** — what does CCP show; how does cloud admin act on it. (orbital-side ingestion + UI is a future spike.)
- **CCP CMDB tier-0** — backup, DR, availability requirements stronger than current SDD addresses. (Open.)
- **Atlas integration contracts, performance SLOs, onboarding workflows** — alignment with Atlas team needed.

## 14. End-to-End Data Flow (Architectural Proposal)

### Cloud → Edge (intent propagation)
1. Intent authored into CCP CMDB (Atlas UI, GraphQL mutation, upstream systems).
2. cb-generator reads CCP CMDB, emits signed ConfigBundle artifact per Galleon.
3. Bundle published to transport (OCI/courier).
4. Galleon agent receives bundle, writes ConfigBundle CR to local etcd. Edge CMDB indexes it.
5. cb-controller reconciles: creates/updates/deletes child CRs via SSA, respecting existing field managers.
6. Domain controllers reconcile child CRs into hardware, cluster, network state.
7. Observed state reported back into Edge CMDB via CR status + events.

### Edge → Cloud (observed state + divergence)
1. Edge CMDB (orb) aggregates CR state, observed state, field-ownership metadata.
2. When connectivity allows: orb publishes signed divergence snapshot to S3/NFS.
3. Cloud (orbital) polls, verifies signature, imports.
4. CCP CMDB surfaces divergences to cloud admins.
5. Cloud admin resolves: accept / force / ignore.

## 15. Document Index

| Doc | Path | Purpose |
|---|---|---|
| This file | `docs/reference/SDD-CONTEXT.md` | Consolidated technical-context lookup |
| Project background | `docs/project-background.md` | Narrative onboarding; complements this file |
| SDD | `~/Downloads/SDD DCIM & CMBD for Galleon Digital Twin in Atlas (3).docx` | Authoritative spec, v0.3 |
| Architectural Proposal | `~/Downloads/CMDB_Architectural_Proposal.docx` | Sedar's review/extension introducing CCP-authored + SSA pattern |
| SSA Notes | `~/Documents/Notes on Server-Side Apply.pdf` | Concrete walkthrough of SSA conflict resolutions with ConfigMap example |
| ROADMAP | `ROADMAP.md` | Spike status, recent accomplishments |
| CLAUDE.md | `CLAUDE.md` | Settled cross-cutting platform decisions |
