---
description: >-
  Nodes, relationships and namespaces — how Orbital stores configuration as a traversable graph rather than as tables.
sidebar_label: The graph
sidebar_position: 2
---

# The graph

Orbital stores configuration as a graph of **nodes** joined by **relationships**, not as rows in tables.

A data center contains racks. A rack holds servers. A server has network adapters, each with network interfaces, each cabled to a port on a switch. Every one of those is a relationship, and in Orbital relationships are stored as first-class data.

## Why a graph

The questions that matter at a data center are traversals.

*What runs on this server?* *If this switch fails, which workloads lose connectivity?* *Which servers share this rack?*

In a relational model each of those is a join you write in advance. In a graph they are walks along relationships that already exist, so a consumer can ask a question nobody anticipated.

## ConfigItems and nodes

**ConfigItem** is the interface every configuration type implements: `DataCenter`, `Rack`, `Server`, `NetworkDevice`, `KubernetesCluster`, and the rest. A type that implements it is a **ConfigItem type**.

A **node** is one instance of a ConfigItem type — one particular server, one particular rack.

```graphql
type Server implements ConfigItem {
  hostname: String            # an attribute
  serviceTag: String          # an attribute
  dataCenter: DataCenter!     # a relationship to exactly one node
  rack: Rack                  # a relationship to at most one node
  networkInterfaces: [NetworkInterface]   # a relationship to many nodes
}
```

Every node carries the fields `ConfigItem` declares: its identity ([`orbId`](./identity)), a `name`, its `namespace`, a `version`, and stamps recording who created and last changed it. Beyond those, each type declares its own.

A node's fields are of two kinds:

| Kind | What it holds | Example |
|---|---|---|
| **Attribute** | A value | `Server.serviceTag` = `DOCS001` |
| **Relationship** | A named, directed link to another node | `Server.rack` → rack `R01` |

A relationship is either **single** (`rack: Rack` — at most one node) or a **list** (`networkInterfaces: [NetworkInterface]` — any number). That distinction carries all the way to the UI: a single relationship renders as a panel, a list as a table. See [Using the Orbital UI](../using-orbital-ui).

Most relationships have an inverse that Orbital keeps in step. Setting a server's `rack` adds that server to the rack's `servers`. It is one write, viewed from either end.

## Namespaces

Every node carries a **namespace**. It is a convention Orbital adopted to tie each node to the data center it belongs to, and the mapping is one-to-one: one namespace, one data center.

The namespace is what lets you treat a data center as a unit:

- **Query everything in it.** One filter returns every node, of every type, in a data center:

  ```graphql
  {
    queryConfigItem(filter: { namespace: { eq: "docs-example" } }) {
      __typename
      orbId
      name
    }
  }
  ```

- **Export it.** Exporting configuration for a data center means exporting its namespace. See [the contract](../reference-architecture/contract).
- **Keep identities from colliding.** The namespace is the first part of every orbId, so two data centers can each have a rack called `R01`.
- **Spot what is out of place.** A node whose namespace names no data center stands out from a single query, rather than surfacing later as a node no page can reach.

## Querying it

The graph is served as a GraphQL API at `/graphql`. A consumer describes the shape and depth it wants, rather than calling an endpoint built for one caller.

That is why Orbital has no per-use-case endpoints. A digital twin, a capacity report and a dashboard ask different questions of the same graph, and each writes its own query.

## Adding a type

A new ConfigItem type is declared in the GraphQL schema. The API for it, its audit trail and its orbId follow from that declaration.

Whether it gets a page in the Orbital UI, and what that page shows and edits, is a separate decision made in configuration. See [Defining pages](../defining-pages).
