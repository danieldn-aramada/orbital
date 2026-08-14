# Orbital & Orb Architecture Cheatsheet

A mental model for "who owns configuration, and where does a change go?"

Deeper context: `docs/reference/ORB.md`, `DIVERGENCE.md`, `SDD-CONTEXT.md`, `network-model.md`.

## Configuration stores and their roles

Most edge-configuration questions reduce to identifying which of these three stores is meant — each has a distinct owner and role.

| Layer | What it is | Mutable? | Source of truth for |
|---|---|---|---|
| Orbital DGraph (cloud) | Authoritative design intent — the one place config is authored | Yes | intended state across all data centers |
| K8s CRs in etcd (edge) | Desired state projected to the edge + local overrides; reconciled by ConfigBundle + domain controllers | Yes — via K8s SSA field ownership | actuation at the edge |
| Orb DGraph (edge) | Read-only, derived mirror of orbital's intent for a specific data center (subgraph) | No | none — derived read cache of orbital's intent (per data center) |

## How configuration flows

```text
                               CLOUD
  ┌──────────────────────────────────────────────────────────────────┐
  │  ORBITAL DGraph  —  authoritative INTENT                         │
  │  the one place configuration is authored                         │
  └──────────────────────────────────────────────────────────────────┘
           │                                          ▲
           │  intent — one-way, via ConfigBundle      │  divergence reports
           ▼                                          │  (observed overrides)
  ════════════════════════════════════════════════════════════════════  EDGE
  ┌──────────────────────────────┐    ┌──────────────────────────────┐
  │  ORB DGraph                  │    │  K8s CRs  (in etcd)          │
  │  read-only MIRROR            │    │  actuation source of truth   │
  │  local query + UI            │    │  → controllers → real state  │
  └──────────────────────────────┘    └──────────────────────────────┘
```

- Cloud → Edge: intent. Orbital (authored) → ConfigBundle → orb mirror + K8s CRs. One-way.
- Edge → Cloud: divergence — orb's only edge→cloud responsibility. Orb aggregates field-ownership + observed state and publishes a divergence snapshot (observed field overrides) to S3; orbital polls and imports it.
- Discovery is not through orb. In the past, we have used CLIs and scripts to scan infra and generate GraphQL mutations for seeding data to orbital. Discovery should not be conflated with divergence.

## Where an edge change belongs

| What you want | Where it goes | Why |
|---|---|---|
| Change the intended config | Author it in orbital → flows to the edge via ConfigBundle; orb mirrors it | Orbital is the only place intent is authored (the normal path) |
| Urgent local override now | SSA on the K8s CR: `kubectl apply --server-side --field-manager=local:admin …` | Actuates, survives reconciles, and surfaces as divergence for a cloud admin to accept / reject / ignore |
| Discovered new hardware / topology | Propose it to orbital as intent upserts for review (scan infra via CLIs/scripts → GraphQL mutations) | Observed data *proposes* intent; it's reviewed and lands at orbital — not written at the edge |
| Just see what's drifted | Read-only display at the edge | Looking is fine; writing to orb is not |

## The load-bearing invariants (SDD §2)

1. Nothing in the cloud executes against the edge. The cloud publishes intent; the edge pulls and applies it locally.
2. Desired & observed state are explicit and may diverge during disconnection. Divergence is data, not an error.
3. Reconcilers are local K8s controllers. The CMDB — orbital *or* orb — is never in the reconciliation path.
4. One source of truth per job. Orbital = intent. K8s CRs (etcd) = actuation. Orb DGraph = derived, read-only.
