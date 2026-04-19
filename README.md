# Orbital

Orbital is a graph-native framework for continuously reconciling infrastructure
across modular, air-gapped data centers.

## Motivation

Many solutions exist in the IT and data center asset management space (e.g.
ITAM, DCIM).

Open source solutions such as [GLPI](https://www.glpi-project.org/en/), and
[Ralph](https://ralph-ng.readthedocs.io/en/stable/) are mature and solve
the problem of tracking infrastructure, configuration, and asset lifecycle at
scale.

Commercial platforms like [Device42](https://www.device42.com/) and [Sunbird
DCIM](https://www.sunbirddcim.com/) extend these capabilities with discovery,
dependency mapping, and compliance-ready deployments.

However, these systems have vendor-defined data models that lock users into a
tightly coupled stack. As a result, they are:
- not API-native platforms for defining and operating a custom digital twin of
  infrastructure
- not designed around drift detection and continuous reconciliation as a core
  concept  
- not optimized for onboarding existing, customer-owned infrastructure into a
  unified CMDB  
- not optimized for modular, edge, or disconnected data center deployments  

## Features 

Orbital provides a graph-native framework for modeling and operating
infrastructure as a continuously reconciled system.

- **Graph-first infrastructure model** — represent data centers as relationships
  between physical and logical resources  
- **Multi-source infrastructure discovery** — ingest infrastructure from bare
  metal systems (BMC) and external inventory systems via API
  integrations
- **Topology API (digital twin)** — build and query a live, traversable graph of
  infrastructure design intent
- **Air-gap ready design** — operates in disconnected and edge environments
  without external dependencies  

Non-Goals
- Full DCIM system with dashboards, alerting, and observability  
- End-to-end infrastructure management suite  

## Concepts

`orbital` - This project. Server runs in cloud. Holds design intent (i.e.
configuration items) for all modular data centers and serves APIs for digital
twin building. Pushes configuration down to `orbs`.

`orb` - Standalone binary running in modular data center. Serves configuration
and can detect drift. Suitable for air-gapped deployments.

## Example 

