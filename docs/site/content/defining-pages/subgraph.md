---
description: >-
  A page's subgraph is the set of connected nodes it edits, audits and deletes along with its own. Why it is declared rather than derived, and how to declare it.
sidebar_label: The subgraph
sidebar_position: 2
---

# The subgraph

A page's **subgraph** is the nodes connected to its node that the page treats as part of it. One declaration drives three things:

| | What the subgraph decides |
|---|---|
| **Edit** | Which connected nodes the page's editor can change, alongside the node itself |
| **Audit** | Which nodes' changes appear in the page's **Audit Log** tab |
| **Delete** | Which nodes are deleted with it |

It does not decide what the page *shows*. Every relationship gets a tab whether it is in the subgraph or not. The subgraph decides what the page *owns*.

## Why it is declared

A schema says how nodes connect. It does not say which ones belong to which.

`Server.dataCenter` and `IdracSettings.server` are both required relationships. The first is a reference: deleting a server must not delete its data center. The second is ownership: a server's iDRAC settings mean nothing without the server. The schema writes both the same way, so Orbital cannot tell them apart. You tell it.

## Declaring it

The shipped Server page:

```yaml
pages:
  Server:
    subgraph:
      - { path: idracSettings, editable: true }
      - { path: serverMaintenance, editable: true }
      - { path: oobIP, editable: true }
      - kubernetesNode
      - storageControllers
      - networkAdapters
      - networkInterfaces
      - storageControllers.storageDevices
      - storageControllers.storageVolumes
```

Each entry is a **path** from the page's node:

- A field name, such as `networkInterfaces`, reaches the nodes one hop away.
- A dotted path, such as `storageControllers.storageDevices`, reaches further. Only the nodes at the end of the path are in the subgraph; the ones in between are in it only if their own path is listed too, as `storageControllers` is here.

Most entries are a bare path. The mapping form marks the exception: `editable: true`, which puts the nodes in the editor.

## Editing

An entry with `editable: true` is written by the page's editor. The server's editor shows its own attributes and its iDRAC settings in one JSON document, and saves them together.

![The server editor, with iDRAC settings nested inside](/img/ui/editor.png)

`editable: true` is refused, with a warning, on:

- a **list** relationship, because the editor cannot say which row it means;
- a path longer than **two hops**.

The page renders the relationship read-only instead.

### Owning a node vs pointing at one

The **Rack** selector at the top of the editor is a different thing. It comes from `types.Server.fields.rack.editable: true`, not from the subgraph, and it re-points the server at a different existing rack.

| | Subgraph member | Editable reference |
|---|---|---|
| Declared in | `pages.<Type>.subgraph` | `types.<Type>.fields.<relationship>.editable` |
| The editor | Changes the connected node's attributes | Chooses which node the relationship points at |
| On delete | Deleted with the page's node | Left alone |

Putting `rack` in a server's subgraph would delete the rack whenever a server was deleted. That is almost never what you mean, and it is the most important reason the two are kept apart.

A reference is fixed unless it is declared editable. Many references identify the node — a server's data center is its namespace, the first part of its orbId — so letting one change would leave the node disagreeing with its own id.

## Deleting

Deleting a node removes **exactly** the node and what its subgraph paths reach. Nothing else is consulted, including other pages' subgraphs.

Before anything is deleted, the UI shows a preview of every node that will go. A node that survives but was left holding a *required* relationship to something deleted is named there as **orphaned**. The delete still proceeds; the preview makes sure you know.

:::warning
Deletion is literal. If a node should go when its parent goes, it must be on the parent's subgraph. If it is not listed, it stays.
:::

## Auditing

The **Audit Log** tab on a page shows changes to the page's node and to every node in its subgraph. A change to a server's iDRAC settings appears on the server's page, because the iDRAC settings are part of the server.
