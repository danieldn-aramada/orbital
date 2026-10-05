# Playbook: Add a new ConfigItem to orbital

Use this when you're adding a new GraphQL type to `schema/schema.graphql` that
implements the `ConfigItem` interface — something with an orbId that lives in the
relationship graph (`EtcdBackup`, `IdracSettings`, a future `PvBackup`).

**This used to be a 13-place touch-list with silent failure modes.** It is TWO
steps now, in two artifacts that are deliberately separate:

| Step | Artifact | Decides |
|---|---|---|
| 1 | `schema/schema.graphql` | what EXISTS — fields, relationships, identity, **what dies with what** |
| 2 | `config/views.yaml` | what RENDERS — which pages show it, and how |

There is no Go to edit. The audit allowlist, the editable field set, the page,
the edit targets and the delete cascade are all derived from those two.

---

## Step 1 — Declare it in the schema

```graphql
type MyNewKind implements ConfigItem {
    enabled: Boolean
    schedule: String

    """jsonString"""
    rawPayload: String       # declared String, holds a JSON document

    # Parent back-ref. NON-NULL if this child has no existence without its
    # parent — that is how containment is declared, and the only way.
    clusterBackupMyNewKind: ClusterBackup! @hasInverse(field: myNewKind)
}
```

**Non-null back-edge = CONTAINED.** It is the whole declaration: the child is
fetched with the parent, rolled up onto the parent's audit tab, reachable by the
parent's editor, and **deleted with it**. Make the edge **nullable** to opt out —
that is a schema decision, not a view one (`ServerConfigurationProfile.server`
went nullable in v13 for exactly this reason).

**Cardinality gotcha:** wrong `@hasInverse` cardinality silently corrupts data on
the inverse side. If multiple parents can share this child, use `[T]`.
⚠️ **`@hasInverse` does NOT backfill** — a newly added inverse field is empty for
every row already in the graph until something writes it.

Bump `schema/VERSION` if this is a deployment-time schema change, and remember
the schema only takes effect once it is APPLIED to DGraph — orbital never applies
it on startup.

**Define the `orbId` convention** — `<namespace>:<kind>-<natural-key>`, never a
UUID. Add a row to the per-type table in `docs/reference/DGRAPH.md`.

**TWO annotations exist, and both are about data or identity:**

| Annotation | Scope | Meaning |
|---|---|---|
| `jsonString` | field | this String CONTAINS a JSON document |
| `orbIdSuffix: mynew` | type | the token a derived child orbId ends with (default: lower-cased type name) |

⚠️ **One description block per declaration.** Two `"""…"""` in a row is a GraphQL
syntax error DGraph refuses; put several annotations on separate lines inside one
block. ⚠️ **A misspelled annotation is silent** — `orbIdSufix:` is a valid
docstring that derives the wrong orbId for every child. Orbital reports
unrecognised ones at boot; read that line after applying.

**Everything about RENDERING is step 2.** Slug, menu position, column order,
labels, filters, which relationships show — none are schema facts, and none
belong in a docstring.

---

## Step 2 — Decide where it renders

`config/views.yaml` has two sections, and they answer different questions.

### Does it need a PAGE?

Usually **no**. Four types have pages; the other sixteen render as rows on
somebody else's. A pageless type has no URL, and its rows are text rather than
links — which is correct, not a gap.

Give it a page only if someone would navigate to it directly:

```yaml
pages:
  MyNewKind:
    menuWeight: 50          # orders the menu; `pages:` decides membership
    # slug: my-new-kinds    # omit unless it must differ from the kebab plural
    summary:
      refs: [clusterBackupMyNewKind]
    tabs: []
```

### How does it RENDER, wherever it renders?

```yaml
types:
  MyNewKind:
    order: [name, enabled]                 # partial order; rest alphabetical
    fields:
      scannedAt: { editable: false }       # a scanned fact a human must not type
      rawPayload: { detailOnly: true }     # a document: never a table column
```

Scalars are **subtractive** — every scalar renders unless something removes it,
so a field added to the schema appears on its own. Relationships are **listed** —
nothing shows until a page names it in `summary.refs` or `tabs`.

### Show it on its PARENT's page

```yaml
pages:
  ClusterBackup:
    tabs:
      - { path: myNewKind, editable: true }
```

`editable: true` means **only** "this page's editor may write it". It gates the
TOP level; below that, containment governs — which is why a cluster's etcd
schedule is editable even though `ClusterBackup` has no page of its own.
⚠️ **A LIST cannot be editable** (an edit target is addressed by path, and a path
cannot name one row). Declaring it is refused at load with a logged reason, and
the tab renders read-only.

**For a MULTI-PARENT type** — one several pages can show — also declare which
page is its home, because that ordering is the one thing nothing else supplies:

```yaml
canonicalParent:
  MyNewKind:
    - { type: ClusterBackup, field: clusterBackupMyNewKind, down: myNewKind }
```

**For an XOR parent** — a child that belongs to A *or* B *or* C, so every edge
must be nullable and none can carry the containment statement — declare it:

```yaml
containment:
  MyNewKind: [adapterEdge, serverEdge, deviceEdge]
```

`NetworkInterface` is the only real instance. Reach for this only when
nullability genuinely cannot express the relationship.

---

## What you do NOT need to touch

- **Any Go at all.** The generic renderer builds the list page, the detail page,
  the tabs, the edit modal, the audit panel and the delete cascade from the two
  artifacts above.
- **The audit allowlist.** The set of auditable types is read from the DEPLOYED
  schema, so a type present in a deployment's graph is gated and audited there
  whether or not the binary shipped knowing about it.
- **`configitem-editor.js`.** It consumes the edit-targets blob the view
  produces; there is no per-kind JS.
- **The audit before-fetch.** Generated from the same editable-field set the
  editor uses, so the two cannot disagree.
- **The export / subgraph selection.** Orbital's export is schema-driven:
  `fetchUIDPredicates` derives the edge list from the live DGraph schema and
  scalars come via `expand(_all_)`. A new type flows in automatically once the
  schema is applied — **no export-code change, ever.**
  ⚠ This is only *orbital's* export. A downstream consumer with a hand-enumerated
  query — notably the configbundle bundler's `ConfigBundleByOrbID` — must add the
  field on *its* side. Don't conflate the two.

---

## Validating end to end

```bash
go test ./internal/configitems/   # the schema <-> views drift guard
go build ./...
make seed                         # applies the schema to DGraph — editing the file is not enough
make run-orbital
```

Read the boot log: `resolved views against the deployed schema` carries the view
and tab counts, and every dropped declaration is a `WARN` naming itself.

In the browser:

1. If you gave it a page, `/<slug>` lists it and `/<slug>/<orbId>` opens one.
   If you did not, its rows render as plain text and clicking does nothing.
2. On the PARENT page it appears in the tab strip, and — if `editable: true` —
   in the JSON editor under its path.
3. Edit a field, save. The parent's Audit Log tab shows an `update<MyNewKind>`
   row with a green/red field diff.
4. Clear the JSON key and save, then configure it again → `add<MyNewKind>`.

| Symptom | Cause |
|---|---|
| audit row missing | the type is not in the deployed schema — apply it |
| audit row with no diff | the field is `editable: false`, so it is not in the before-fetch |
| not in the editor | the parent's tab is missing `editable: true`, or the edge is nullable so nothing contains it |
| its own children not in the editor | their back-edges are nullable — containment stops there |
| not on the page at all | no tab on the parent's page, or the schema was never applied |
| its rows don't link | it has no `pages:` entry — that is the dead-row rule, and it is correct |
| `editable` ignored on a tab | it is a LIST; check the boot log for the refusal |
| deleted with its parent unexpectedly | its back-edge is non-null — that IS the containment declaration |
| child orbId looks wrong | `orbIdSuffix:` missing from the schema |

---

## Reference reading

- `docs/reference/UI.md` — the view model, the editor, the JSON editor convention
- `docs/reference/DGRAPH.md` — schema conventions, annotations, `@hasInverse`
- `docs/reference/AUDIT.md` — audit pipeline architecture
- `config/views.yaml` — the shipped config, with the format documented at the top
- `internal/configitems/viewconfig.go` — the format's Go definition and its rules
- `internal/handler/delete_cascade.go` — how containment becomes a delete
