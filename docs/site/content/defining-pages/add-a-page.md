---
description: >-
  A worked example — give the Rack type its own page with a four-key overlay, and see what changes across the UI.
sidebar_label: Add a page
sidebar_position: 1
---

# Add a page

Out of the box, `Rack` has no page. Racks appear inside a data center's **Racks** tab, and a server row shows its rack as plain text, but there is no `/racks` and no way to open a rack on its own.

This example gives `Rack` a page with an overlay, without touching the shipped file. The overlay is in the repository at `examples/docs/views-overlay.yaml`.

## 1. Write the overlay

```yaml title="examples/docs/views-overlay.yaml"
pages:
  Rack:
    menuWeight: 15
    filterBy: dataCenter

types:
  Rack:
    columns:
      - dataCenter
      - servers.count
    fields:
      name: { editable: true, createOnly: true }
      uHeight: { label: Height (U), createDefault: "42" }
```

Line by line:

| Key | Effect |
|---|---|
| `pages.Rack` | Rack now has a page. The URL is `/racks`, the kebab-cased plural of the type name, and the menu label follows from it. |
| `menuWeight: 15` | Places **Racks** in the menu between Data Centers (10) and Servers (20). It orders the menu; it does not decide what is in it. |
| `filterBy: dataCenter` | The list view offers a data center dropdown. It must name a column, which is why `columns` carries `dataCenter`. |
| `columns: [dataCenter, servers.count]` | `dataCenter` is a relationship, so it becomes a column of links: a rack row shows its data center, everywhere a rack row appears. `servers.count` is a computed column: how many servers each rack holds. |
| `fields.name` | The name can be set when a rack is created and not changed afterwards, because the rack's orbId is built from it. |
| `fields.uHeight` | A clearer label, and a value pre-filled on the create form. |

There is no `subgraph:`. A rack does not own its servers, so nothing is edited or deleted along with it. See [The subgraph](./subgraph).

:::note[Why `types.Rack` repeats what ships]
The shipped file already declares `columns`, `name` and `uHeight` for `Rack`. An overlay entry **replaces** the shipped entry wholesale, so an overlay that declared only the label would lose the other two. Copy across what you want to keep.
:::

## 2. Point Orbital at it

```bash
ORBITAL_VIEWS_OVERLAY_PATH=examples/docs/views-overlay.yaml make run-orbital
```

On a running deployment, update the overlay's ConfigMap instead. Orbital picks the change up within 30 seconds.

## 3. See the result

**Racks** is in the menu, and `/racks` lists every rack. Open **R01**:

![The detail view of rack R01](/img/ui/overlay-rack-page.png)

- **Attributes** shows `Height (U)`, from the overlay's label.
- **Relationships** has a tab for the rack's data center, its servers and its network devices. None of these were declared: every relationship gets a tab.
- **Audit Log** is last, as on every page.

Two things changed on pages the overlay never mentioned:

- On the **Servers** list, the **Rack** column is now a link. A row links when its type has a page.
- In a data center's **Racks** tab, each rack now opens its own detail view.

![The Servers list, with rack names now linking to the Racks page](/img/ui/list-page.png)

## 4. Check the log

Orbital validates every declaration against the deployed schema when it loads the overlay. If `filterBy` named a field that is not a column, or a path named a field the schema does not have, you would see a warning naming it, and the page would render without that declaration.

## Adding a new type

This example used a type the schema already had. A brand-new ConfigItem type is a change to the schema as well — its fields, its relationships and its [orbId pattern](../concepts/identity#declaring-it) — and then a `pages:` entry like the one above.
