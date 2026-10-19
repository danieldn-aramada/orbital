---
description: >-
  Every key in views.yaml — pages, types, fields, labels and canonical parents — with what each one does.
sidebar_label: Reference
sidebar_position: 3
---

# Reference

Every key `views.yaml` accepts. A key Orbital does not recognise is ignored, and one the deployed schema cannot support is dropped with a warning.

## Top level

| Key | What it is |
|---|---|
| `pages` | Which types have a page, keyed by type name. |
| `types` | How a type renders wherever it renders, keyed by type name. |
| `labels` | Display text for field names, across every type. |
| `canonicalParent` | Where a type with no page of its own should be looked at. Reserved — see below. |
| `schemaVersion` | The schema version the file was written against. A hint only; Orbital validates against the live schema regardless. |

## `pages.<Type>`

A type named here has a page. The key can be an interface: the `KubernetesCluster` page lists every type that implements it.

| Key | Default | What it does |
|---|---|---|
| `slug` | Kebab-cased plural of the type name | The URL segment, and the source of the menu label. `slug: clusters` makes the page `/clusters`, labelled **Clusters**. Integrations and bookmarks depend on it, so treat a change as breaking. |
| `menuWeight` | Last | Orders the menu, lowest first. Ties sort by type name. |
| `filterBy` | *(none)* | One column the list view offers as a dropdown filter. Must name a column the list shows. |
| `hide` | `[]` | Fields that do not appear on this page. An attribute named here is not shown; a relationship named here has no tab at all. |
| `subgraph` | `[]` | The connected nodes this page edits, audits and deletes. See [The subgraph](./subgraph). |

### `subgraph` entries

```yaml
subgraph:
  - networkAdapters                          # short form: a path
  - { path: idracSettings, editable: true }  # mapping form
```

| Key | What it does |
|---|---|
| `path` | A relationship name, or a dotted path through several (`storageControllers.storageDevices`). |
| `editable` | The page's editor writes these nodes. Single relationships only, at most two hops. |

## `types.<Type>`

Applies wherever the type renders: its own page, and every tab and table on other pages. A type inherits what is declared for the interfaces it implements, so `types.ConfigItem` applies to every type.

| Key | What it does |
|---|---|
| `order` | Fields to show first, in this order. The rest follow alphabetically. A prefix, never a complete list — a complete list would hide every field added later. |
| `columns` | Extra columns. A single relationship on its own, such as a server row's `rack`, is a column of links to the node on the other end; a column pointing back at the page you are on is left out automatically. A path, such as `rack.uHeight`, is the value at its end, and a path ending in `.count` counts a list: `servers.count`. Link columns come before value columns. |
| `fields` | Per-field settings, below. |

### `types.<Type>.fields.<field>`

| Key | What it does |
|---|---|
| `label` | Display text for this field on this type. Overrides the top-level `labels`. |
| `tableHidden` | Keep the field out of every table — list views and relationship tabs. It still shows on the detail view. |
| `editable` | Whether the editor offers the field. Defaults to `true` for a type's own attributes and `false` for the metadata every node carries. On a **single relationship**, `true` lets the editor re-point it at another node in the same namespace. |
| `createOnly` | Settable when the node is created, fixed afterwards. Use it for every attribute the type's orbId is built from. |
| `createHidden` | Not on the create form, but editable afterwards. |
| `createDefault` | A value pre-filled on the create form, which the person creating the node can change. Not allowed on a field the orbId is built from. |

`editable` decides what the **editor** offers. It does not restrict the API: a scanner can still write a field the UI never edits.

## `labels`

```yaml
labels:
  orbId: Orb ID
  macAddress: MAC Address
  osToIdracPassThroughEnabled: OS to iDRAC Pass-Through Enabled
```

A field not listed here is labelled by splitting its name: `rackPosition` becomes **Rack Position**. List only what that gets wrong, usually acronyms. Keyed by field name, so one entry fixes the label on every type that has the field.

## `canonicalParent`

```yaml
canonicalParent:
  NetworkInterface:
    - { type: NetworkAdapter, field: networkAdapter, down: networkInterfaces }
    - { type: Server, field: server, down: networkInterfaces }
    - { type: NetworkDevice, field: networkDevice, down: networkInterfaces }
```

For a type with no page and more than one possible parent, the parent whose page a node of it should be looked at on — for example, when one turns up in an audit entry.

:::note[Reserved]
Orbital validates `canonicalParent` against the schema and warns about a bad entry, but no page uses it yet.
:::

| Key | What it is |
|---|---|
| `type` | The parent type. |
| `field` | The relationship on the child pointing up to the parent. |
| `down` | The relationship on the parent pointing back down, if there is one. |
