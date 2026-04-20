# Orbital

Orbital is an API-first, graph-native configuration management framework for
modular data centers.

## Motivation

Many solutions exist in the IT asset and data center infrastructure
management space (e.g. ITAM, DCIM).

Open source solutions such as [GLPI](https://www.glpi-project.org/en/), and
[Ralph](https://ralph-ng.readthedocs.io/en/stable/) are mature and solve the
common problem ofc tracking infrastructure, configuration, and asset lifecycle.

Commercial platforms like [Device42](https://www.device42.com/) and [Sunbird
DCIM](https://www.sunbirddcim.com/) extend many of these capabilities and even
offer compliance-ready deployments.

However, for the edge and modular data centers, all fall short in these areas:
- **Not built for disconnected environments** — every platform assumes
  persistent connectivity. None credibly support air-gapped facilities where
  config is served locally and sync happens on-demand or via physical media
- **Relational models treat relationships as an afterthought** — topology
  queries are expensive joins, not natural graph traversals. No foundation for a
  live digital twin
- **Drift detection is not a core concept** — tools track what exists, not the
  gap between design intent and discovered reality
- **Products, not frameworks** — opinionated SaaS stacks with fixed workflows.
  Not readily designed to be built on. 

## Goals 

- **Air-gapped ready** — operates in disconnected and edge environments
  without external dependencies  
- **Graph-first infrastructure model** — represent data centers as relationships
  between physical and logical resources  
- **Multi-source infrastructure discovery** — ingest infrastructure from bare
  metal systems (BMC) and external inventory systems via API
  integrations
- **Topology API (digital twin)** — build and query a live, traversable graph of
  infrastructure design intent

Non-Goals
- Full DCIM system with dashboards, alerting, and observability  
- End-to-end infrastructure control plane or management suite  

## Concepts

`orbital` - This project. Server runs in cloud. Holds design intent (i.e.
configuration items) for all modular data centers and serves APIs for digital
twin building. Pushes configuration down to `orbs`.

`orb` - Standalone binary running in modular data center. Serves configuration
and can detect drift. Suitable for air-gapped deployments.

## Architecture

### Orbital and Orb

`orbital` runs in the cloud and is the single source of truth for configuration
intent across all modular data centers. `orb` runs at each modular data center
and is the local authority for serving that data center's configuration.

For ongoing config management, the flow is **orbital -> orb**: orbital pushes
design intent down, orb serves it locally. Orb does not write back to orbital
directly over the network.

The exception is onboarding. When a customer has an existing modular data
center, orb discovers the local infrastructure and exports a graph of what it
finds. An admin carries that export to orbital (e.g. file upload), and orbital
imports it as the starting point for that data center's design intent. After
that, orbital takes over as the source of truth.

```
            +------------------------------------------+
            |                 Orbital                  |
            |                                          |
            |       Topology API (GraphQL proxy)       |
            |                    |                     |
            |          Go server (middleware)          |
            |         /          |           \         |
            |     DGraph       Valkey    PostgreSQL    |
            +--------------------+---------------------+
                                 |
                                 | config sync (orbital -> orb)
                                 |
                       +---------v---------+
                       |                   |
                  +----v----+         +----v----+
                  |  Orb A  |         |  Orb B  |
                  |  DC 1   |         |  DC 2   |
                  +---------+         +---------+
```

### Graph model

Infrastructure is modeled as a graph of configuration items — from physical
assets (racks, servers, switches, cables, door hardware) to logical constructs
(VLANs, IP ranges, Kubernetes clusters, application configs). Relationships
between these items are first-class: a server belongs to a rack, a rack belongs
to a row, a VLAN spans multiple switches.

The graph is stored in [DGraph](https://dgraph.io), which provides native
GraphQL on top of an RDF storage model. The schema is defined and versioned by
orbital and applied to DGraph on startup.

### Topology API

External consumers (e.g. a digital twin UI) query the topology via GraphQL.
Orbital proxies DGraph's auto-generated GraphQL API — no custom query layer.
Orbital's Go server sits in front as middleware, handling authentication, rate
limiting, and caching. Clients never talk to DGraph directly.

### Air-gap sync

Orb is designed for disconnected environments. Config sync works two ways:

1. **Scheduled polling** — when the modular data center has connectivity, orb
   polls orbital and receives a full DGraph export (`json.gz` + `schema.gz`),
   which it loads into its local DGraph instance
2. **Manual file import** — when the modular data center is fully air-gapped, an
   admin carries a config export in on a USB drive and imports it directly into
   orb

Either way, orb has a complete local copy of its data center's graph and can
serve the Topology API entirely offline.

### Discovery and onboarding

For modular data centers with existing infrastructure, orb runs local discovery
(BMC, inventory APIs) and builds a graph of what it finds. The admin exports
this discovered graph to a file, carries it out of the data center, and uploads
it to orbital. Orbital imports it as the starting point for that data center's
design intent.

This is the primary onboarding path — discovered reality flows from orb into
orbital once, then orbital becomes the source of truth going forward.

### Orb registration

For now, Orb registration follows the GitHub Actions runner pattern:

1. Orbital admin creates an orb slot for a modular data center and generates a
   short-lived one-time registration token
2. Token is handed to the on-site admin
3. Orb presents the token to orbital's registration endpoint and receives a
   long-lived API key
4. Orb uses that key for all future polling

API keys are long-lived and revocable from orbital. No expiring tokens — an orb
that is offline for months must still be able to reconnect without manual
intervention.

### Schema versioning

The DGraph GraphQL schema is defined in versioned files under `schema/` and
managed exclusively by orbital. Schema changes must always be backwards
compatible — orbs may be running an older version while orbital has advanced.
Orbital tracks the active schema version in PostgreSQL and applies migrations on
startup after validating compatibility.

## Examples

TODO

## Deploy

### Local

Setup dependencies
```bash
docker-compose -f deploy/local/docker-compose.yml up -d
```

Build
```bash
docker build -t orbital:v0.0.1 .
```

Run
```bash
docker run -p 8001:8001 \
  -e DGRAPH_URL=http://host.docker.internal:8080/graphql \
  orbital:v0.0.1
```

### AKS devcc

Deploy Dgraph
```bash
helm install dgraph dgraph/dgraph \
  --version 24.1.4 \
  --namespace <namespace>
  -f deploy/charts/values-dev.yaml 

helm install dgraph ./deploy/charts/dgraph --namespace=<namespace> --values deploy/charts/values-dev.yaml
# or upgrade if need
helm upgrade dgraph ./deploy/charts/dgraph --namespace=<namespace> --values deploy/charts/values-dev.yaml
```

Build and push to test registry
[armadaeksatest](https://portal.azure.com/#@armada.ai/resource/subscriptions/212ddfb2-b7cf-4041-8eed-8882792f8d41/resourceGroups/eksa-acr-test/providers/Microsoft.ContainerRegistry/registries/armadaeksatest/repository)

```bash
# Assuming access to Sandbox Services Landing Zone
az login 
az acr login --name armadaeksatest

docker buildx build \
  --platform linux/amd64 \
  -t armadaeksatest.azurecr.io/orbital:v0.0.1 \
  --push .
```

