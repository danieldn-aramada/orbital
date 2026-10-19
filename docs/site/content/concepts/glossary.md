---
description: >-
  The terms Orbital uses, each with one meaning. Every page on this site uses them as defined here.
sidebar_label: Glossary
sidebar_position: 9
---

# Glossary

Each term has one meaning, and every page on this site uses it in that meaning.

## The graph

| Term | Meaning |
|---|---|
| **ConfigItem** | The interface every configuration type implements. A type that implements it — `Server`, `Rack`, `NetworkDevice` — is a **ConfigItem type**. |
| **Node** | One instance of a ConfigItem type: one particular server, one particular rack. |
| **Attribute** | A field on a node that holds a value, such as `serviceTag`. |
| **Relationship** | A named, directed link from one node to another, such as `Server.rack`. **Single** when it points at no more than one node, **list** when it can point at many. |
| **orbId** | A node's identity: `<namespace>:<type>-<natural-key>`, derived from the node rather than generated. See [Identity](./identity). |
| **Namespace** | The convention that ties every node to the data center it belongs to. One namespace, one data center. |
| **Data center** | A modular data center, modelled as a node of the `DataCenter` type. The unit Orbital exports. |

## Intent and the edge

| Term | Meaning |
|---|---|
| **Intent** | What a data center's configuration should be. Authored in Orbital and nowhere else. Also *design intent*. |
| **Observed state** | What is actually true in a data center right now. It lives at the edge. |
| **Divergence** | A field where the edge disagrees with intent. Data, not an error. See [Divergence](./divergence). |
| **Orbital** | The cloud service. Holds intent for every data center. |
| **Orb** | The edge service inside one data center. Serves a read-only mirror of that data center's intent. |
| **The edge** | Everything inside a data center: Orb, the local controllers, the hardware. |

## Moving intent

| Term | Meaning |
|---|---|
| **Export** | Producing a versioned artifact of one data center's intent, with the schema that describes it. |
| **Publish** | Pushing an export to a registry, where the delivery layer picks it up. |
| **Import** | Orb loading an export into its own graph. |
| **Delivery layer** | Whatever carries an export to a data center and applies it. Not part of Orbital. See [Reference Architecture](../reference-architecture). |

## The UI

| Term | Meaning |
|---|---|
| **Page** | The list and detail views for one ConfigItem type, with an entry in the menu. Declared in `views.yaml`. See [Defining pages](../defining-pages). |
| **Subgraph** | The nodes connected to a page's node that the page edits, audits and deletes along with it. |
| **Metadata** | The fields every node carries because it is a ConfigItem: namespace, orbId, version, and who created and changed it. |
| **Orphaned** | A node left holding a required relationship to a node that has been deleted. |
| **Overlay** | A partial `views.yaml` that changes only the pages and types one deployment needs to differ. |
