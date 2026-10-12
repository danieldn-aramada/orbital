---
description: >-
  ConfigItems, relationships and namespaces — why Orbital stores configuration as a traversable graph rather than as tables.
sidebar_label: The graph
sidebar_position: 2
---

# The graph

Orbital stores configuration as a graph, not as rows in tables.

A data center contains racks. A rack holds servers. A server has network adapters, each with interfaces, each cabled to a switch port. Those are relationships, and in Orbital they are first-class.

## Why a graph

The questions that matter at a data center are traversals.

*What runs on this server?* *If this switch fails, which workloads lose connectivity?* *Which servers share this power feed?*

In a relational model each of those is a join you have to write in advance. In a graph they are walks across edges that already exist, and a consumer can ask for a shape nobody anticipated.

## ConfigItems

Every entity in the graph is a **ConfigItem**: a server, a rack, a cluster, an IP address, a backup schedule.

ConfigItems share a small set of fields — an identity, a name, a namespace, and audit stamps recording who changed them and when. Beyond that each type declares its own.

New types are added by editing the schema. Orbital derives the API, the pages and the audit behaviour from that declaration, so adding a type does not mean writing code.

## Namespaces

A **namespace** scopes a graph to one data center.

Namespaces keep identities from colliding between sites, and they are the unit Orbital exports. Publishing configuration for one site means publishing its namespace.

## Querying it

The graph is served as a GraphQL API. Consumers define the shape they want and the depth they need, rather than calling an endpoint built for one caller.

:::note
This is why Orbital has no per-use-case endpoints. A digital twin, a capacity report and a dashboard ask different questions of the same graph, and each writes its own query.
:::
