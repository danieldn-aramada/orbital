---
description: >-
  Every page in the Orbital UI is declared in one configuration file, views.yaml. What a page is, what you declare, and what Orbital works out for itself.
slug: /defining-pages
sidebar_label: Overview
sidebar_position: 0
---

# Defining pages

Every ConfigItem page in the Orbital UI is declared in one file, `config/views.yaml`. Nobody writes a template or a handler for a page.

A deployment changes its pages by changing configuration. Adding a page for a type that has none is a few lines. Taking a tab off a page, relabelling a field, or deciding what the editor may change is one line each. None of it needs a new Orbital release.

## What a page is

A **page** belongs to one ConfigItem type. It gives that type:

- a **menu entry**,
- a **list view** at a URL, such as `/servers`, with a table of every node of the type,
- a **detail view** for each node, opened as a tab on the list view.

A type without a page still renders wherever it appears — as a panel or a table on another type's page. It just has no URL and no menu entry of its own.

![The Racks page, added by an overlay](/img/ui/overlay-rack-page.png)

## Two sections

`views.yaml` has two main sections, and the split is the most important idea in the file.

| Section | Answers | Example |
|---|---|---|
| `pages:` | Which types have a page, and what each page edits and deletes | `Server` has a page; editing a server also edits its iDRAC settings |
| `types:` | How a type renders **wherever** it renders | A `Server` row shows its rack and its OOB IP, on `/servers` and inside a rack's tab alike |

A `Server` appears on the Servers page, in a rack's **Servers** tab, and in a data center's. How a server row looks is declared once, under `types:`, and holds in all three places.

```yaml
pages:
  Server:
    menuWeight: 20
    filterBy: dataCenter
    subgraph:
      - { path: idracSettings, editable: true }
      - networkInterfaces

types:
  Server:
    columns: [dataCenter, oobIP, rack]
    fields:
      serviceTag: { createOnly: true }
      oobMAC: { tableHidden: true }
```

## What you declare, and what Orbital derives

The file states as little as possible. Anything Orbital can work out from the schema, it does.

| Orbital derives | You declare |
|---|---|
| Every attribute appears, labelled from its field name | Which to hide, relabel, or keep out of tables and forms |
| Every relationship appears as a tab | Which relationships to hide |
| A single relationship renders as a panel, a list as a table | — |
| Whether a row links: it does if its type has a page | Which types have a page |
| The **Audit Log** tab, always last | — |
| — | The **subgraph**: what a page edits, audits and deletes |
| — | Which relationship columns a row carries |

Attributes are **subtractive**: a field added to the schema shows up without anyone declaring it. That is deliberate. A page that listed its fields would silently never show a field added later.

The subgraph is the opposite, and always **declared**. A GraphQL schema has no concept of ownership — `!` means non-null and nothing more — so Orbital cannot tell from the schema whether deleting a server should delete its network interfaces. You say so. See [The subgraph](./subgraph.md).

## Changing it for one deployment

Don't edit the shipped file. Write an **overlay**: a partial `views.yaml` that names only what your deployment changes, and point `ORBITAL_VIEWS_OVERLAY_PATH` at it.

- Every page and type the overlay does not name keeps whatever Orbital ships, including changes in later releases.
- A page or type the overlay *does* name replaces the shipped one entirely. Copy across anything you want to keep.
- `labels:` merge field by field, so relabelling one field keeps the rest.

:::warning[Don't copy the whole file]
An overlay that copies the entire shipped file freezes your deployment at today's pages. Anything a later release adds never arrives, and nothing tells you.
:::

Orbital re-reads both files every 30 seconds, so a change takes effect without a restart. In Kubernetes, mount the overlay's ConfigMap as a directory: a `subPath` mount is never updated.

Views are configuration, not data. They are not editable in the UI and not in Orbital's audit log; your git history and Kubernetes RBAC govern them.

## When a declaration is wrong

Orbital checks the file against the deployed schema every time it loads it. A declaration the schema cannot support is dropped and logged as a warning, never fatal. A stale view must not stop a page rendering.

```text
WARN editable relationship dropped; the editor will not offer it  where=...
```

Watch for these after changing the schema or the overlay.

## Next

- [Add a page](./add-a-page.md) — a worked example, start to finish
- [The subgraph](./subgraph.md) — what a page edits, audits and deletes
- [Reference](./reference.md) — every key in the file
