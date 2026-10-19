---
description: >-
  Orbital runs in the cloud and Orb runs inside a data center. The three configuration stores, their owners, and which way intent flows.
sidebar_label: Orbital and Orb
sidebar_position: 3
---

# Orbital and Orb

Orbital runs in the cloud. **Orb** runs inside a data center. They are separate products with separate jobs.

## Orbital

Orbital holds authoritative intent for every data center you operate. It is where configuration is authored, where the graph is queried, and where divergence is reviewed.

It is the only place intent is written.

## Orb

Orb is a self-contained edge service. It keeps a read-only mirror of Orbital's intent for its own data center, serves that locally, and accepts divergence reports from other edge components.

Orb is built for disconnection. Once it has imported an export, it needs nothing from the cloud to answer questions about its own data center.

:::warning
Orb never mutates its graph. It has no local override mechanism, and a change written there would be silently discarded on the next import. Local overrides belong on the Kubernetes resources, where they surface as divergence.
:::

## Three stores, three owners

Most confusion about edge configuration resolves into asking which of these is meant.

| Store | Role | Source of truth for |
|---|---|---|
| Orbital's graph (cloud) | Authored design intent | Intended state, everywhere |
| Kubernetes resources (edge) | Desired state plus local overrides | Actuation in the data center |
| Orb's graph (edge) | Read-only mirror of one data center's intent | Nothing — it is a derived cache |

Only the first is authored. The second is reconciled by local controllers. The third is derived and can be rebuilt from an import at any time.

## Intent flows one way

Cloud to edge carries intent. Orbital exports a namespace, something packages and delivers it, and Orb imports it.

Edge to cloud carries exactly one thing: divergence reports. Nothing else travels upward, and Orbital never initiates contact with a data center.
