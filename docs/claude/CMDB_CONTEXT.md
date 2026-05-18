# CMDB Project Context

This file distills the full pre-work history, design decisions, and current state of the Orbital CMDB project for use in AI sessions. It is the authoritative single-load context for any session touching this project's history, strategy, or management tracking.

**Last updated:** 2026-05-18

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
| Prototyping | Apr 14 → May 27 | 9 spikes completed Apr 20 – May 18. See table below. |
| MVP | May 27 → Jun 27 | Target. 4 remaining spikes + security hardening. |
| General Availability | Jun 27 → Jul 27 | Target. |

---

## Five Key Design Decisions (from SDD)

These were formally evaluated, documented, and reviewed. They are settled.

**KD1 — Air-gap first.** All DCIM/CMDB solutions must operate in disconnected Galleon environments. This was the primary filter that eliminated most vendor tools and drove the custom build decision.

**KD2 — Netbox stays for network; new CMDB for everything else.** Netbox does not natively model iDRAC, storage, Kubernetes cluster config, or application config. Expanding it via plugins was evaluated and rejected. A new graph-native CMDB was selected for all non-network config items.

**KD3 — Graph database (DGraph).** Configuration items are nodes; relationships between them are edges. Core queries are inherently graph-shaped: traversal ("show everything connected to X"), impact analysis ("if component X fails, what is downstream?"), change lineage ("what did user X change last week?"), observability correlation. SQL alternatives were evaluated and rejected for graph-oriented workloads at scale.

**KD4 — GraphQL API.** Exposes the graph model via a flexible, client-driven query interface. Atlas UI requests only the data it needs. Reduces need for multiple specialized REST endpoints. Supports schema evolution without frequent API changes.

**KD5 — K8s controller pattern for edge actuation.** Configuration actuation at the edge follows the Kubernetes controller pattern (extending NCP/ZTP). CMDB is a graph index and relationship store — it is never in the reconciliation path. Cloud mutations update authoritative intent only; actuation occurs when ConfigBundles are pulled and reconciled locally on the Galleon. Four invariants:
1. Nothing in the cloud executes directly against a Galleon
2. Desired and observed state may diverge; divergence is data, not error
3. Authoritative reconcilers run locally; cloud is never in the reconciliation path
4. CMDB is a graph index; actuation flows through K8s controllers

---

## Architecture Overview

### Cloud (orbital)
- **Go backend service** on AKS: auth/authz middleware, rate limiting, request validation, DGraph orchestration, Valkey caching
- **DGraph (community edition)**: graph storage engine — stores all config items and relationships; blue (live) + scratch (export-only) instances
- **PostgreSQL**: all operational data — users, orbs registry, export jobs, backup records, audit log, schema versions, OCI artifacts
- **Valkey**: cache-aside for read-heavy graph queries; orbital operates correctly without it
- **Export API**: scoped `json.gz` + `schema.gz` per data center, signed OCI artifacts via oras-go v2 + cosign
- **Topology API**: proxies DGraph GraphQL as-is; orbital adds auth, rate limiting, caching
- **Management UI**: HTMX + Go templates + Bulma; full server-side rendering, no SPA

### Edge (orb)
- Single Go binary (`orb start`, `orb scan`, `orb export`, `orb import`)
- Per-orb Ed25519 key pair; public key registered with orbital by admin
- Operates fully offline after import; serves local DGraph subgraph
- Reports divergence to orbital; orbital surfaces for human resolution — never auto-resolves

### What CMDB is NOT
- Not in the reconciliation path
- Not a monitoring or observability system (uses existing Prometheus/Grafana stack)
- Not a control plane that executes against Galleons
- Not an ITSM or PLM system (integrations pending vendor selection)

---

## What Has Been Built (Prototyping Spikes)

All completed by Daniel. 9 spikes in 28 days (Apr 20 – May 18).

| # | Spike | Completed | Key Deliverable |
|---|---|---|---|
| 1 | AKS Deployment Validation | Apr 20 | Orbital + DGraph on AKS; NetworkPolicy; pod recovery validated |
| 2 | Orb CLI Structure | Apr 22 | Single binary: `orb start/scan/export/import`; `internal/cli/` scaffolding |
| 3 | PostgreSQL / ent Data Model | May 5 | 9 tables: users, orbs, namespaces, backups, export_jobs, registry_artifacts, events, restore_jobs, schema_versions |
| 4 | Management Web UI | May 6 | Full UI: data centers, servers, export, backup, restore, audit log, signed artifacts, schema; Playwright E2E suite |
| 5 | Authentication | May 8 | OIDC + local auth; orbital-cli with macOS keychain; bearer token validation end-to-end with real Azure AD v2 tokens |
| 6 | DGraph Backup to Azure Blob | May 9 | Async backup pipeline; SHA-256 dedup; configurable retention; Azure Blob + S3-compatible; validated on AKS |
| 7 | DGraph Restore from Backup | May 14 | Full restore via dgraph-live idle pod; client-go exec; conflict detection; validated on AKS |
| 8 | AKS Dev Environment | May 18 | Full deploy manifests, Helm charts, seed scripts, step-by-step deploy guide |
| 9 | Hardware Data Model & Seed | May 15 | 4 new iDRAC fields; 9 data centers seeded with real hardware data (Netbox hostnames, rack positions) |

**Also completed as part of spikes (not separately numbered):**
- Config export pipeline + OCI publish (blue-green DGraph, per-job scratch dirs, oras-go v2 + cosign, air-gap safe)
- Audit log system (one event per HTTP request; operations, resource types/IDs, raw GraphQL payload, before-state diff via LCS)
- Cross-DC Servers page with DataTable, server detail view, iDRAC/Storage/Config Profile tabs
- orbital-cli with `orbital get datacenter/datacenters` commands; bearer auth; kubectl-style output
- 8 real data center seed files (Alaska DOT Cruiser/Galleon, Houston, Seattle, Colo, Grayling, Livermore, 2F UAE, Navy Cruiser)

---

## Current State (as of May 18, 2026)

**Phase:** Prototyping → MVP (target June 27, 2026)

**In progress:**
- **Spike 10 — Air-gap sync (orb side):** Orbital side complete (export API, OCI publish). Remaining: orb receives `json.gz`, loads into local DGraph, serves offline.
- **Spike 11 — Authorization:** Bearer validation done, `/api/v1/graphql` protected. Remaining: Azure AD App Roles, DGraph `@auth` directives, Go middleware role enforcement, offline JWT integration tests. ⚠️ Requires Opus design session before implementation.

**Not started (MVP blockers):**
- Spike 12: DGraph operations runbook
- Spike 13: Orb import API (completes the round-trip started in Spike 10)
- Spike 14: Divergence reporting (orb → orbital surface + admin resolve)
- Security hardening (critical/high findings: see `docs/security-and-deployment-findings.md`)
- Production deployment (ingress, TLS, CI/CD, `//go:embed`)

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
| SDD (v0.3) | `~/Downloads/SDD DCIM & CMBD for Galleon Digital Twin in Atlas (3).pdf` | Formal design doc — 5 key decisions, architecture diagrams, full GraphQL schema |
| Architectural Proposal | `~/Downloads/CMDB_Architectural_Proposal.docx` | Sedar's CCP-authored/edge-enforced proposal; K8s controller pattern rationale; field-level SSA ownership |
| Orbital CHANGELOG | `CHANGELOG.md` | Completed spike detail — API contracts, what was validated, key decisions |
| Orbital ROADMAP | `ROADMAP.md` | Gantt chart, spike table, in-progress detail, MVP definition |
| Excel Tracker | `~/Downloads/Edge Platform Q4-Q1.xlsx` — sheet `cmdb` | Management-facing progress tracker; Vertical=CMDB |
