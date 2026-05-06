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

Load example data (optional — paste into GraphQL playground at `http://localhost:8080`):

```
examples/colo-galleon.graphql    # colo namespace, 50 servers
examples/seattle-galleon.graphql # seattle namespace, 24 servers
examples/houston-galleon.graphql # houston namespace, 32 servers
examples/alaska-dot-galleon.graphql     # alaska-dot namespace, 13 servers
examples/alaska-unit-2-galleon.graphql  # alaska-unit-2 namespace, 15 servers
```

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
- **CMDB as a bottleneck, not an enabler** — traditional CMDBs sit in the
  reconciliation path, creating a tight coupling between the source of truth
  and how configuration reaches infrastructure. This breaks down in air-gapped
  and multi-deployment-model environments. A GraphQL mutation against a CMDB
  should update authoritative intent — it should never execute an action
  remotely or be part of the reconciliation path. Nothing in the cloud should
  execute directly against a modular data center. The CMDB provides the API
  surface; transport and reconciliation are the adopter's concern.

## Goals 

- **Air-gapped ready** — operates in disconnected and edge environments
  without external dependencies  
- **Graph-first infrastructure model** — represent data centers as relationships
  between physical and logical resources  
- **Multi-source infrastructure discovery** — ingest infrastructure from bare
  metal systems (BMC) and external inventory systems via API integrations
- **Topology API (digital twin)** — expose a live, traversable graph of
  infrastructure design intent via GraphQL. Consumers define their own query
  shape — no custom endpoints required for each digital twin use case
- **API-first transport enablement** — orbital provides the primitives (export
  API, report intake API, Topology API) that teams wire into their own delivery
  and reconciliation layer. Configuration actuation at the edge follows a
  controller pattern: ConfigBundles are pulled and reconciled locally, never
  pushed from the cloud. Divergence during disconnection windows is data, not
  an error condition. Orbital does not prescribe transport or reconciliation —
  those decisions belong to the adopting team's deployment model

Non-Goals
- Full DCIM system with dashboards, alerting, and observability built in
- Prescribing a data transport or reconciliation mechanism — orbital provides
  the API surface; how config is packaged, delivered, and applied is the
  deployment layer's concern

## Concepts

`orbital` — Cloud-side CMDB and Topology API. Single source of truth for configuration
intent across all modular data centers. Manages the configuration graph, exposes a
GraphQL Topology API for digital twin consumers, and produces scoped config exports
(`json.gz` + `schema.gz`) for edge consumption. Orbital's contract ends at the export
— how that payload is packaged, signed, and delivered is the consuming layer's concern.

`orb` — Self-contained edge service inside a modular data center. Holds a complete
local copy of its data center's intended state and serves it fully offline. Discovers
actual infrastructure state and produces signed divergence reports. Designed for
air-gapped and intermittently connected deployments.

`configuration item` — The fundamental unit of the graph. Anything in a modular data
center that can be named, related, and tracked — from physical assets (racks, servers,
cables) to logical constructs (Kubernetes clusters, application configs).

`divergence report` — A signed, structured report produced by the edge describing the
gap between design intent and observed reality. Delivered to orbital's report intake API
by the deployment layer. Orbital stores and surfaces divergence to administrators — it
does not act on it and is not in the reconciliation path.

## Architecture

`orbital` runs in the cloud and is the single source of truth for configuration
intent across all modular data centers. `orb` runs at each modular data center
and serves configuration locally — fully offline if needed.

Config flows **orbital → orb**. For reporting, orb writes drift and divergence reports to a shared location when connected; a delivery agent forwards them to orbital's report intake API — read-only telemetry, not configuration. The exception to the one-way config flow is onboarding: orb discovers existing infrastructure and exports a graph, which an admin imports into orbital to seed the source of truth.

```
  ┌─────────────────────────────────────────────────────┐
  │                       orbital                       │
  │                                                     │
  │   Topology API    Export API    Report Intake API   │
  └───────────────────────┬─────────────────▲───────────┘
                          │                 │
              signed config export          │ signed divergence
              (json.gz + schema.gz)         │ reports
                          │                 │
  ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌ data transport boundary ╌╌╌╌╌╌╌╌╌╌╌╌
                          │                 │
                   ┌──────▼─────────────────┴──────┐
                   │         delivery layer         │
                   └──────┬─────────────────┬───────┘
                          │                 │
                   ┌──────▼────┐   ┌────────▼──┐
                   │    orb    │   │    orb    │
                   │   DC 1    │   │   DC 2    │
                   └───────────┘   └───────────┘
```

For detailed architecture decisions see [CLAUDE.md](CLAUDE.md).

## Examples

TODO

## Deploy

See [deploy/README.md](deploy/README.md) for local and AKS deployment instructions.

