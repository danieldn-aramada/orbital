---
description: >-
  A tour of the Orbital UI for the people who use it day to day — the menu, list views, detail views, relationships, the editor and the audit log.
slug: /using-orbital-ui
sidebar_label: Tour
sidebar_position: 0
---

# Using the Orbital UI

Every ConfigItem page in Orbital works the same way, whatever type it shows. Learn one and you know them all.

The screenshots use the `docs-example` data center that `make seed` loads. Your pages may have different tabs and columns: what each page shows is configured per deployment. See [Defining pages](../defining-pages).

## The menu

**Config Items** lists one entry per page — Data Centers, Servers, Clusters, Network Devices, and any your deployment adds. **Inventory**, at the top, is every node of every type in one table.

The rest of the menu is operational: exporting and publishing (**Edge**), reviewing proposed changes (**Change Control**), and the audit log, backups and users (**Operations**).

## List views

![The Servers list view, searched for docs-example](/img/ui/list-page.png)

A list view is a table of every node of one type.

- **Search** filters on any visible text. The dropdown on the left, where a page has one, filters by a single column — here, data center.
- **Links** in a row lead to another page. Text that is not a link belongs to a type with no page of its own.
- **Excel, CSV and Copy** export the table's rows.
- **New** creates a node of this type.

**Double-click** a row to open it. A single click selects it.

## Detail views

![The detail view of server docs-node-01](/img/ui/detail-page.png)

A node opens as a tab beside **Summary**, so you can hold several open and move between them. Up to five stay open; opening a sixth closes the one you used least recently. The URL records which tabs are open, so you can share it.

Every detail view has the same three parts.

| Part | What it shows |
|---|---|
| **Attributes** | The node's own values. |
| **Metadata** | What every node carries: its namespace, its `orbId`, its version, and who created and last changed it. |
| **Relationships** | One tab for each relationship to another node, then the **Audit Log**. |

### Relationships

A relationship to a single node shows that node's attributes in a panel:

![The iDRAC Settings panel on a server](/img/ui/relationship-panel.png)

A relationship to many nodes shows them as a table:

![The Network Interfaces table on a server](/img/ui/relationship-table.png)

A tab that says **None.** means the relationship exists in the schema, but nothing is on the other end for this node.

### Audit Log

The last tab lists every change to the node, and to the nodes it owns, with who made it, when, and the values before and after. A change to a server's iDRAC settings appears on the server.

## Editing

**Edit** opens the editor for the node and everything the page lets you change with it. **Edit**, **New** and **Delete** appear for users with the `dev` or `admin` role; a `readonly` user sees the same pages without them.

![The editor for docs-node-01](/img/ui/editor.png)

- Attributes are edited as one JSON document, including the attributes of nodes the server owns, such as its iDRAC settings.
- Relationships you can re-point, such as the server's **Rack**, are selectors above it. They list nodes in the same data center.
- **Save** writes every change together. If someone else changed the node after you opened the editor, Orbital refuses the save rather than overwrite their work. Reload and try again.

Editing changes **intent**. Nothing reaches out to hardware; the change reaches the data center the next time its configuration is exported.

:::note[Change control]
If your deployment protects a type with an approval policy, **Save** proposes a change request for a colleague to approve instead of applying it straight away. Without a policy, saves apply immediately.
:::

## Deleting

**Delete** shows a preview of everything that will be removed — the node, and the nodes it owns — before anything is. If removing them would leave another node holding a required relationship to nothing, the preview names that node as **orphaned**.

## Orb

Orb, the edge service inside each data center, serves the same pages for that data center's configuration. They are read-only: there is no Edit, New or Delete. Intent is changed in Orbital.
