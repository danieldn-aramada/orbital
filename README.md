# Orbital

Orbital is an API-first, graph-native configuration management system for
modular data centers.

For project status, see [ROADMAP.md](./ROADMAP.md)

## Quick Start

Clone this repo and run dependencies
```bash
docker-compose -f deploy/local/docker-compose.yml up -d
```

Load graphql schema
```bash
curl -s --data-binary '@./schema/schema-v1.graphql' --header 'content-type: application/octet-stream'  http://localhost:8080/admin/schema
```

Run orbital server
```
go run cmd/orbital/main.go
```

Browse API docs

http://localhost:8001/swagger/index.html

http://localhost:8001/graphql

Load example data (TODO)

Cleanup
```bash
docker-compose -f deploy/local/docker-compose.yml down
```
## Motivation

Many solutions exist in the IT asset and data center infrastructure
management space (e.g. ITAM, DCIM).

Open source solutions such as [GLPI](https://www.glpi-project.org/en/), and
[Ralph](https://ralph-ng.readthedocs.io/en/stable/) are mature and solve the
common problem of tracking infrastructure, configuration, and asset lifecycle.

Commercial platforms like [Device42](https://www.device42.com/) and [Sunbird
DCIM](https://www.sunbirddcim.com/) extend many of these capabilities and even
offer compliance-ready deployments.

However, for the edge and modular data centers, all fall short in these areas:
- **Not built for disconnected environments** — most platforms assume
  persistent connectivity. None were designed with air-gapped facilities first
  with strict compliance standards and config sync happens on-demand or via
  physical media
- **Relational models treat relationships as an afterthought** — topology
  queries are expensive joins, not natural graph traversals. Not flexible for
  client to define response schema which is desired for digital twin
  applications.
- **Drift reporting is not a core concept** — tools track what exists, not the gap
  between design intent and discovered reality
- **Limited flexibility** — existing solutions bundle monitoring,
  observability, and dashboards we already have. There is no path to adopt just
  the configuration management layer without the rest of the product.

## Goals 

- **Air-gapped ready** — operates in disconnected and edge environments
  without external dependencies  
- **Graph-first infrastructure model** — represent data centers as relationships
  between physical and logical resources  
- **Multi-source infrastructure discovery** — ingest infrastructure from bare
  metal systems (BMC) and external inventory systems via API
  integrations
- **Topology API (digital twin)** — expose a live, traversable graph of
  infrastructure design intent via GraphQL. Consumers define their own query
  shape — no custom endpoints required for each digital twin use case

Non-Goals
- Full DCIM system with dashboards, alerting, and observability built in
- Out of the box infrastructure automation with workflow orchestration and
  reconciliation

## Concepts

`orbital` — Server running in the cloud. Single source of truth for configuration
intent across all modular data centers. Serves the Topology API, manages schema,
and exposes a config export API consumed by `orbs` and the delivery layer above orbital.

`orb` — Self-contained edge service running inside a modular data center. Holds a
local copy of its data center's graph and serves it entirely offline. Reports drift
between design intent and discovered reality. Suitable for air-gapped deployments.

`configuration item` — The fundamental unit of the graph. Anything in a modular
data center that can be named, related, and tracked — from physical assets (racks,
servers, cables, door hardware) to logical constructs (VLANs, IP ranges, Kubernetes
clusters, application configs).

`drift` — The gap between design intent and discovered reality. Orb observes actual
state, compares it to intended state, and reports the gap. Orbital does not act on
drift and is not in the reconciliation path.

## Architecture

`orbital` runs in the cloud and is the single source of truth for configuration
intent across all modular data centers. `orb` runs at each modular data center
and serves configuration locally — fully offline if needed.

Config flows **orbital → orb**. For reporting, orb writes drift and divergence reports to a shared location when connected; a delivery agent forwards them to orbital's report intake API — read-only telemetry, not configuration. The exception to the one-way config flow is onboarding: orb discovers existing infrastructure and exports a graph, which an admin imports into orbital to seed the source of truth.

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

For detailed architecture decisions see [CLAUDE.md](CLAUDE.md).

## Examples

TODO

## Deploy

See [deploy/README.md](deploy/README.md) for local and AKS deployment instructions.

