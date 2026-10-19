---
description: >-
  Walk the configuration graph, compare the cloud service against the edge service, query it with GraphQL, and change an attribute.
sidebar_label: Your first look
sidebar_position: 2
---

# Your first look

With the stack running and seeded, here is what to look at and why it matters. Everything below uses the `docs-example` data center that `make seed` loads.

## Start at a server

Open http://localhost:8001, sign in, and choose **Servers** from the menu. Search for `docs-example` and double-click **docs-node-01**.

![The detail view of server docs-node-01](/img/ui/detail-page.png)

You are looking at one **node** — one server — in the graph.

- **Attributes** are the server's own values.
- **Metadata** is what every node carries: its namespace, its `orbId`, its version, and who changed it.
- **Relationships** are the links to other nodes: its data center, its rack, its iDRAC settings, its network interfaces. Each tab is one hop across the graph.

Open the **Network Interfaces** tab and follow the link to the switch the server is cabled to. That is another hop.

## Look at the same data center in Orb

Open http://localhost:8010 beside it.

Orb shows one data center's configuration, served locally. The pages look the same because they are the same templates.

The difference is authority. Orbital's copy is authored and can be changed. Orb's is a read-only mirror of what Orbital exported, and Orb keeps serving it whether or not the cloud is reachable.

## Ask the graph a question

Open http://localhost:8001/graphql and run:

```graphql
{
  queryDataCenter(filter: { namespace: { eq: "docs-example" } }) {
    name
    racks {
      name
      servers {
        name
        serviceTag
        networkInterfaces {
          name
          connectedNetworkDevice { name }
        }
      }
    }
  }
}
```

That walks data center → rack → server → network interface → switch in one request, and you chose the shape. There is no endpoint behind it built for this question.

Change the query to follow a different path, and that works too. This is what makes the graph a digital twin rather than a reporting database.

## Edit something

Back on docs-node-01, choose **Edit**.

![The editor for docs-node-01](/img/ui/editor.png)

Change `rackPosition` and save. Orbital records who changed what and when, and the **Audit Log** tab shows the change with its before and after values.

Editing changes **intent** — what the server should be. Nothing reaches out to the hardware. The change travels to the data center when its configuration is next exported and Orb imports it.

## Where to go next

- [Using the Orbital UI](../using-orbital-ui) — the rest of the UI, page by page
- [The graph](../concepts/graph) — nodes, relationships and namespaces
- [Defining pages](../defining-pages) — how those pages are declared, and how to add your own
