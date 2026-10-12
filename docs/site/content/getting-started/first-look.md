---
description: >-
  Walk the configuration graph, compare the cloud service against the edge service, query it with GraphQL, and change a field.
sidebar_label: Your first look
sidebar_position: 2
---

# Your first look

With the stack running and seeded, here is what to look at and why it matters.

## Start at a data center

Open http://localhost:8001 and pick a data center.

You are looking at a **ConfigItem** — one node in the graph. Its page is not hand-written. Orbital derives the fields, the tables and the tabs from the deployed schema, so a new type gets a working page without anyone building one.

Follow a link to a rack, then to a server. Each hop is a relationship stored in the graph.

## Look at the same site in Orb

Open http://localhost:8010 beside it.

Orb shows one site's configuration, served locally. The pages look the same because they are the same templates.

The difference is authority. Orbital's copy is authored and can be changed. Orb's is a read-only mirror of what Orbital published, and a site keeps serving it whether or not the cloud is reachable.

## Ask the graph a question

Open http://localhost:8001/graphql and run:

```graphql
{
  queryDataCenter {
    name
    racks { name servers { name serviceTag model } }
  }
}
```

You chose that shape. There is no endpoint behind it built for this question — you described the traversal you wanted and Orbital walked it.

Change the query to follow a different path, and that works too. This is what makes the graph a digital twin rather than a reporting database.

## Edit something

Open a server and edit a field.

Orbital records who changed what and when. The audit entry appears on the item's own page, showing the before and after values.

:::note
Editing changes **intent** — what the server should be. Nothing reaches out to the hardware. The change travels to a site when configuration is exported and that site imports it.
:::

## Where to go next

- [Design intent](../concepts/design-intent) — why Orbital never reconciles
- [The graph](../concepts/graph) — what ConfigItems and relationships are
- [Divergence](../concepts/divergence) — what happens when a site disagrees
