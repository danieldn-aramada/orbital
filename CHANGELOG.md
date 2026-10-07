# Changelog

Notable changes to **orbital** (cloud), **orb** (edge), and **orbctl** (CLI).

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are
[semantic](https://semver.org/), tagged per component:

| Component | Tag prefix |
|---|---|
| orbital (server) | `v*` |
| orb (edge) | `orb/v*` |
| orbctl (CLI) | `cli/v*` |

**This file is the source of truth**, not the GitHub release page. Orbital targets air-gapped
deployments — an operator holding the tarball and no network connection must still be able to read
what changed. GitHub Release bodies are generated from this file, never the other way round.

> Releases before **v0.0.31** (2026-08-12) predate this changelog. For that history see
> `git log` and `git tag --sort=-creatordate`; the delivery timeline is in `ROADMAP.md`.

---

## [Unreleased]

### Added
- **A page declares its subgraph** (`subgraph:` in `config/views.yaml`): one list
  drives what the page shows, edits, audits and deletes. Replaces ownership
  derived from non-null edges. `tabs:`, `summary.refs:` and `ownerReferences:` are
  retired (warned if present); single relationships outside the subgraph become
  link rows automatically. The delete preview now names entities left
  **orphaned** with a broken non-null edge.
- **Per-deployment view overrides, as a partial ConfigMap overlay**
  (`ORBITAL_VIEWS_OVERLAY_PATH`). A deployment declares only the views it changes;
  every view it does not name keeps receiving whatever ships in later releases.
  Changes are picked up **without a process restart** — the overlay is hashed on
  the same rate-limited loop that already watches the deployed schema.
  ⚠️ Mount the directory, not a `subPath`: the kubelet does not update `subPath`
  mounts in place.
- **The views config is validated against live introspection at load.** A tab or
  reference naming a field the deployed schema lacks, a path deeper than two
  segments, a `canonicalParent` edge that does not exist, a page whose type is
  gone, `editable:` on a list relationship (an edit target is addressed by path,
  and a path cannot name one row) — each is dropped or refused, and logged at
  WARN. Never fatal: a stale view must not stop a
  page rendering. Never silent: a declaration that reads as correct and does
  nothing is worse than one nobody wrote.
- **A save composed against an older view is refused with 409.** The editor
  decides a field was *cleared* by diffing the tree it opened against the tree it
  submits, so a member dropped from a view while a modal sat open would have
  emitted a `remove` for fields nobody touched. The editor now declares which
  views document it was built from, and the GraphQL proxy refuses the write if
  that has moved. Scoped to the editor: a caller that declares nothing proceeds,
  because an API client sending an explicit `remove` is doing it deliberately.

### Changed
- **Bearer provider selection reads RFC 9068's `client_id`, not only `azp`.**
  `client_id` is REQUIRED on a JWT access token by RFC 9068 §2.2; `azp` is OIDC
  Core §2 and is specified for *ID tokens*, which Keycloak and Entra v2 emit on
  access tokens by convention rather than by spec. Orbital read `azp` alone, so a
  conformant issuer emitting `client_id` only selected no provider and was refused
  with "token issuer is not trusted by this server" — an error naming the issuer
  rather than the cause. Resolution order is now `client_id` → `azp` → `appid`
  (Entra v1), applied to both the unverified peek that selects the provider and
  the verified path that classifies app principals. Keycloak emits `client_id`
  and `azp` identically, so nothing changes for the issuer in use today; `appid`
  is additionally peeked now, so an Entra v1 token selects its own entry instead
  of falling through to the issuer-wide one.
- **Schema v14.** `ServerConfigurationProfile.server` is nullable (a scanned
  document a server page need not carry), `StorageController.storageVolumes`
  completes the inverse of an edge that previously had no way back, and the four
  backup back-edges (`ClusterBackup.cluster`, `EtcdBackup.clusterBackupEtcd`,
  `VeleroBackup.clusterBackupVelero`, `S3Sync.clusterBackupS3Sync`) are non-null —
  a backup record has no existence without the cluster it backs up, and that is
  now what the schema says. ⚠️ Apply the schema to DGraph; an image deploy does
  not reach a running instance by itself.
- **A ConfigItem page is now configured in `config/views.yaml`, not in the GraphQL
  schema.** What each page shows and edits — its URL slug, menu position, column
  order, labels, filters, which relationships render, and which of those are
  editable — moved out of `schema/schema.graphql` into a views document that ships
  in the image. The schema keeps the two annotations that are about data and
  identity (`jsonString`, `orbIdSuffix:`); the other eleven moved, and
  `derivesIdFrom:` was deleted. A view change needs no schema apply, no version
  bump and no reindex. The annotation vocabulary went from thirteen words to two.
- **The views config has two sections, `pages:` and `types:`.** `pages:` is the
  list of pages, and its top level **is** the menu — four entries, four URLs, four
  nav items. `types:` says how a type renders wherever it renders, declared once,
  because a server appears in four other pages' tables and repeating its column
  order in each is how those copies drift apart. `types:` inherits along the
  schema's interfaces, so a label declared on `ConfigItem` applies to all twenty.
- **Two rules, not one per field: scalars are subtractive, relationships are
  listed.** A page shows every scalar its type has unless `summary.ignoreFields`
  removes it, so a field added to the schema appears on its own; a relationship
  shows only where `summary.refs` or `tabs` names it, so a new edge never changes
  a page's shape uninvited. `viewIgnored` is an edge nobody listed, and `include:`
  is a tab whose path has two segments.
- **A row links if and only if its type has a page.** `/idrac-settings/<id>` and
  `/storage-devices/<id>` were never pages before this work and are not pages now —
  they existed only because the old derivation made every type routable. A storage
  device renders as a row on its server's page, scanned fields and all, and
  clicking it does nothing. Promoting a type to a page is a one-line change that
  turns its dead rows into links everywhere at once.
- **Containment is derived from the schema's non-null back-edges, and the delete
  cascade follows it.** `Child.parent: Parent!` is the schema saying the child has
  no existence without the parent; thirteen of orbital's fourteen containment
  relations already said it, and the fourteenth — a network interface belonging to
  an adapter *or* a server *or* a device — is an exclusive choice that nullability
  cannot express, so it is the only declared one. `editable:` now means one thing
  only: this page's editor may write this child. A display decision can no longer
  move a delete boundary. This replaced three hand-written GraphQL traversals and a
  three-way switch on literal type names that consulted no model at all, and it
  fixes four silent defects they had drifted into: a server delete orphaned its
  NICs, its configuration profile and its Kubernetes node; a data-centre delete
  orphaned its network devices; and the same IP address was preserved by one
  delete and destroyed by another depending only on which page you started from.
  Every one of those left a node holding a non-null edge to something that no
  longer existed — the failure mode that can break export for an entire data
  centre, silently, surfacing later in an unrelated subsystem.
  **An IP address is now owned by no page**, so it survives every delete that
  reaches it.
- **A type absent from the views config is a statement, not a gap.** It stays
  routable and renders its own fields, with no relationships and no menu entry.
  Leaving something out is now how you say you do not want it on a page.
- **A page and a menu entry are one fact.** Everything in `pages:` has a URL and a
  menu entry; `menuWeight` orders the menu and does not decide membership. This
  replaces a derivation that guessed from containment, admitted the guess in its
  own comment, and produced twenty-three routable types that then had to be
  filtered back down to four.

- **`containment:` is now `ownerReferences:`.** It declares which node owns
  another — Kubernetes' `metadata.ownerReferences` by another route — and three
  things read it: the delete cascade, the audit before-fetch that builds a
  diff, and the audit roll-up that shows a child's changes on its owner's page.
  "Containment" named none of those and read as a container, not an owner.
- **View-config field keys name the surface they govern.** `detailOnly` is now
  `tableHidden`, `default` is `createDefault`, and a field can be kept off the
  create form with `createHidden`. Previously a key's name did not say which of
  the editor, the tables or the create form it affected, and "hide from X" was
  spelled three different ways.

### Removed
- **`DELETE /api/v1/config-items/{type}/{id}` and `GET /config-items/delete-preview`.**
  The cascade delete is a UI route on the page URL now (`DELETE /{slug}/{orbId}`,
  `GET /{slug}/{orbId}/delete-preview`); API clients delete through `/graphql`.
- **The Views page and `GET /api/v1/views`.** Both existed to show what orbital
  derived from schema annotations, back when that was the only way to see it.
  Page configuration is a file now — `config/views.yaml` — and nothing consumed
  the endpoint except the page. What they answered between them, the resolved
  result for a deployment that overrides part of its configuration, is now in
  the startup log, which names the pages it resolved rather than only counting
  them.

### Fixed
- The delete preview required no login and returned hostnames and IPs.
- **A parent's Audit Log tab shows its owned children's events again — all of
  them.** The walk that builds the tab's orbId list read only single-object
  children, so every list the entity owns was skipped in silence: a server's
  network adapters, interfaces, storage controllers, drives and volumes changed
  with nothing on its tab, while deleting the server removed all of them. A
  populated server went from 3 orbIds to 34. The roll-up stops at any entity
  that has its own page — a server's events belong on the server's page, not
  duplicated onto its data centre's — which keeps a data centre at 5 rather
  than the 1,159 its full subtree holds.
- **Change requests touching a data centre pin their real scope.** The owned-
  subtree query selected `orbId` directly off an interface-typed relationship,
  which DGraph rejects; the expander batches every entity in a request into one
  query and treats a failure as "owns nothing", so one data centre made every
  entity in that request expand to nothing. Staleness detection was reading a
  narrower base than the request actually covered.
- **List pages show the columns they used to.** The reference columns on a table
  — Data Center, Rack, OOB IP — were derived from the schema, so every
  single-cardinality relationship became one: the servers list carried Idrac
  Settings, Server Configuration Profile, Server Maintenance and Kubernetes
  Node, none of which any earlier version showed, and adding a relationship to
  the schema widened every table of that type with nobody deciding to. They are
  declared now, in `types.<T>.refColumns`.
- **A field marked detail-only no longer reappears on a list that unions several
  types.** The clusters list showed Description because the union took each
  implementation's full field set rather than its column set.
- **There are no individual ConfigItem pages.** A server, cluster, data centre or
  network device is shown as a tab on its list page and nowhere else, which is
  how orbital worked before the schema-derived pages landed. Rows link to
  `/servers?open=<orbId>`; `/servers/<orbId>` is the address a tab fetches its
  own body from and is no longer reachable as a page.
- **Closing a tab lands on the tab to its left, not back on the list.** Closing
  the last of several tabs used to send you to Summary every time. Closing a tab
  you are not currently viewing now leaves you where you are.
- **Returning to a list page puts you back on the tab you were on.** It always
  landed on Summary instead: the active tab was written under one key and read
  back under another that nothing ever wrote, so the restore quietly did nothing.
  The key is also per page now — every list page was sharing one, so the tab open
  on clusters was looked up on servers.
- **Returning to a list page no longer re-fetches every open tab.** Tabs were
  rebuilt by selecting each one, so arriving at the page — from the menu, the
  back button, or a reload — issued one detail query per open tab, for panels
  nobody had looked at. They are now fetched the first time you actually open
  them. The rule you see is the same: one fetch per tab per page load.
- **An open tab no longer comes back permanently blank.** Those restore requests
  went out all at once and one was dropped, but the tab had already been marked
  loaded, so it stayed empty with no error until you pressed Reload.
- **Edit works inside a detail tab again.** The editor's modal shipped only with
  the full page, so the Edit button in a tab resolved nothing and did nothing —
  silently. Saving from a tab now re-renders that tab rather than reloading the
  whole page.
- **A list page holds at most five detail tabs.** Opening a sixth closes the one
  you have looked at least recently and says which, rather than refusing to open
  it. The strip has no overflow menu or tab search, so past a handful it stops
  being navigable.
- **Detail tabs no longer go blank when you switch between them.** Showing one
  tab hid every element carrying the shared `tab-content` layout class, not just
  the other tab panels — including the wrapper inside each panel's own body. That
  could never be undone, so the panel stayed visible around content that was
  permanently hidden, and the second tab you opened blanked the first. The tab
  strip kept working throughout, which is why it looked like the content had
  simply not loaded.
- **Double-clicking a row on a list page opens it as a tab again.** The derived
  table rendered the name as a link and a click navigated away, so the tab strip
  never grew — "Summary · server1 · server2" was gone. The name is plain text
  again, as it was before: single click selects the row (which is what the
  Copy/Excel/CSV buttons export), and double click opens it.
- **Edit and Delete are back at the top of a detail view, in the colours they
  had.** They had moved into the Fields box header, and Edit had turned green.
  The detail body is served both as a full page and as a tab, so the move
  affected both at once; green also contradicts orbital's own rule that primary
  actions are `is-link`.
- **The Reload button is back on detail views.** It refreshes the panel it is in
  — a tab reloads only itself and leaves the other open tabs alone.

- **The list-page filter dropdown did nothing.** It rendered and could be
  changed, but filtered zero rows — the data attribute carrying its column index
  was spelled in mixed case, and HTML lowercases attribute names, so the index
  read back as undefined.
- **Unset fields are now offered in the editor.** A nullable field that happened
  to be empty was omitted entirely, so scheduling a maintenance window meant
  typing the field names from memory. They appear as empty values; leaving one
  alone writes nothing.
- **A change proposed against a child now shows on the parent page that lists
  it.** Field rows, the metadata box and inline child panels all marked a
  pending change; a child rendered as a table — a data centre's racks, a
  cluster's nodes — did not, so a proposal on one was invisible from the parent.
- **orb's list pages gained the table controls they were missing.** Every
  schema-derived list page in orb rendered as a plain table — no search,
  sorting, paging, column picker, filter dropdown, or double-click to open a
  row — while the same pages in orbital had all of them.
- **orbital runs from any working directory.** Templates and HTML fragments
  were read from disk by relative path, so the binary only worked when started
  from the repo root — outside it, orbital panicked at startup and orb returned
  errors for some fragments. Both now read from the files baked into the image.
- **List pages say when they are showing only part of the data, and the cap is
  now yours to set.** Every list page capped at 500 rows and said nothing about
  it, so a truncated table read as the complete set — and the filter dropdown
  made that worse, since its options come from the rows on the page. Pages now
  state the real total **and the limitation**: search and filtering are
  client-side, so they only cover the rows that were loaded. The cap is
  `ORBITAL_LIST_MAX_ROWS` (orb: `ORB_LIST_MAX_ROWS`), default **2000** — raised
  from 500. Set it far above that and orbital warns at startup rather than
  silently serving a page no browser can render. See `docs/reference/CONFIG.md`
  for what the cap costs and why server-side paging is not the answer yet.
- **A data center's asset data is editable as a tree again.** `assetDataV2`
  holds a JSON document in a String field; since the detail pages became
  schema-derived it had been reaching the editor as a single line of escaped
  quotes rather than a navigable structure. Nothing errored — submits were
  re-stringified either way — so the field simply became impractical to edit.

### Changed
- **Detail pages are laid out again, not stacked.** Fields and Metadata sit
  side by side, and owned configuration, related items and the audit log are
  one set of tabs below them instead of ten boxes in a column — a server page
  went from 3.2 screens of scrolling to just over one. The audit log now loads
  when you open its tab rather than with every page, and a tab containing a
  pending change shows a dot so you can see it without opening it.
- **Every page now carries a title.** List pages (`/servers`, `/clusters`,
  `/data-centers`, `/network-devices`, and any type added to the schema) showed
  only a "Summary" tab where every other page has a heading; they now name
  themselves in the same style as the rest of the UI. `/backups` used a
  different heading element from every other page and only showed it to users
  who could run a backup — it now matches, and titles the page whoever is
  looking.

### Added
- **Columns can show a value from further away in the graph.** A type annotated
  `column: <path>` gains a column whose value is fetched along that path — a
  rack's server count (`servers.count`), or a server's Kubernetes cluster and
  node role (`kubernetesNode.cluster.name`). Declared once per type, it appears
  wherever that type is listed: its own page and any relationship table showing
  it. This restores the rack server-count and the cluster/role columns that the
  network device page had before these pages became schema-derived.
- **List pages can offer a filter dropdown, declared in the schema.** A type
  annotated `filterBy: <field>` renders a select in its table toolbar, filtering
  on that column. `Server` and `KubernetesCluster` carry it, which restores the
  "All Data Centers" dropdown that `/servers` and `/clusters` each had before
  those pages became schema-derived. The chosen column and its options are
  resolved server-side and published on the view, so an API consumer building
  its own table gets the same control without re-deriving it.


- **`/clusters` is derived from the schema, and lists every kind of cluster.**
  The Clusters page and cluster detail page were ~820 lines of hand-written Go,
  templates and JS; both are now rendered by the generic renderer from the
  deployed schema, along with a metadata box and an audit log that every
  ConfigItem detail page gets for free.

  The page is backed by the **`KubernetesCluster` interface**, not by
  `EksaKubernetesCluster`. DGraph generates `queryKubernetesCluster` but no
  `getKubernetesCluster` — an interface has no `@id` field — so the list is
  interface-backed while each detail page resolves the row's concrete type from
  the stored data. A second provider (maas, metal3) therefore appears on
  `/clusters` without anyone editing a query, which is what the interface was
  put in the schema for. Columns are the union of the interface's fields and
  each implementation's own, so a provider-specific column is not lost because
  another provider has no such field.

  This also fixes relationships **typed as an interface**, which the generic
  renderer had been dropping silently: a Kubernetes node now links to its
  cluster, and a cluster backup to its parent.

  Known losses, accepted: the per-page Reload button, the inner switchable
  sub-tabs (the same content is stacked as boxes), and workload clusters as
  nested rows in the list — they are a table on the management cluster's page.

### Changed
- **`GET /api/v1/change-requests?namespace=` is now repeatable** — pass it more
  than once (`?namespace=foo&namespace=bar`) to list requests in any of several
  namespaces, OR-ed, matching how `status` and `orbId` already behave. A single
  value is unchanged.

### Fixed
- **`/servers` and `/clusters` had silently lost their data center filter.**
  Both dropdowns lived in JavaScript that survived the deletion of the pages
  it drew them on, so the code read as present while the controls were gone.
- **Generic list tables fill the viewport again** instead of scrolling inside a
  fixed 400px box, and rows show the pointer cursor and "Double-click to open"
  tooltip that the bespoke pages had.
- **Sort, page size, search and column visibility persist per page again.**
  They had been switched off entirely because DataTables keys saved state on
  the table id and every generic page shares one; state is now keyed by slug,
  so `/racks` and `/servers` no longer overwrite each other.
- **Table search no longer matches on link markup.** A reference cell is a
  link, and its href was part of what the search box compared against — so
  searching "datacenters" matched every row that had a data center.

### Removed
- **~1,650 lines of unreachable UI JavaScript** left behind when the bespoke
  DataCenter, Server, Cluster and NetworkDevice pages were deleted. Every
  entry point guarded on a DOM id that no longer exists in any template, so it
  ran on no page. A stale `release-check` spec driving the same removed markup
  was retargeted at the generic page.
### Added
- **Every ConfigItem page is now derived from the schema.** NetworkDevice was
  the last bespoke one; `/network` redirects to `/network-devices`. Its
  Connections panel needed no new machinery at all —
  `networkInterfaceConnectedNetworkDevice` is an ordinary list edge, so it was
  already a relationship tab and only wanted a readable name. With it go the
  last two shared partials (`metadata-box.gohtml`, `audit-tab.gohtml`), which
  existed to stop four hand-written pages drifting apart.

  **Known loss:** the Connected Servers panel showed each connected server's
  Kubernetes cluster, node role and GPU flag — a three-hop projection. The
  Connections table shows the server, port, MAC and remote port; the cluster
  context is one click away on the server. See the note in
  `docs/spikes/spike-38-configurable-views.md`.

- **The Server page is derived from the schema.** The last bespoke ConfigItem
  page — 715 lines of Go plus a 286-line template — is gone; `/servers` and
  `/servers/{orbId}` are served by the generic renderer. iDRAC, maintenance and
  configuration-profile panels render as owned-child boxes; storage and network
  as relationship tables; metadata, audit log, editor, MVCC and delete all come
  from the shared machinery.

  Two capabilities became generic on the way, and both now apply to every page:
  **proposed-change field marks** (a pending change request annotates the exact
  field it targets, on the root AND on each owned child, keyed to the child's
  own orbId) and the **change-in-flight banner**. Neither was ever
  Server-specific — both are driven by DOM attributes — they had simply never
  been emitted anywhere else.

- **`include:` adds a relationship reached through a path.** A server's disks
  hang off its storage controllers, and a controller has no scalars of its own,
  so the controller table was a list of bare names while the disks — the reason
  anyone opens the page — appeared nowhere. `"""include:
  storageControllers.storageDevices"""` renders them as one table, with the
  controller named in a column.

  Deliberately **not** a new kind of panel: it widens what a tab's *source* may
  be, from a field to a path. The rows are still a list of one type rendered as
  a table, by the same code. It also costs no extra query — an owned child's
  grandchildren are already fetched for the editor.

- **`label:` overrides a field's displayed name.** Labels are derived by
  title-casing, which handles almost everything and cannot know an acronym:
  `cni` became "Cni", `oobIP` became "Oob IP". The exception now lives with the
  field it describes rather than in a Go list of special cases.

- **Column order is pinned in the schema: `"""order: name, provider, clusterType"""`.** Named fields
  lead; everything else falls in behind them alphabetically. Previously every
  generic page was strictly alphabetical, which put `cni` second on the Clusters
  table.

  **Deliberately a partial order, never a complete list.** A complete ordered
  list is a frozen view — a field added in a later release has no place in it
  and silently never appears. NetBox carries exactly that cost with its saved
  column lists. A pinned prefix leaves the tail open, so new fields still
  arrive. The pin lives in the schema rather than a database because a default
  belongs in version control: it ships with the build, it diffs, it reverts.

### Added
- **View placement is declared in the schema, not decided in Go.** Two new
  annotations complete the vocabulary, and both replace policy that had been
  compiled in:

  - **`"""detailOnly"""`** keeps a field off table columns while still rendering
    it on a detail page. This had been inferred from `jsonString` — conflating
    WHAT a field holds with WHERE it may appear, which left "actually, show this
    JSON as a column" unexpressible without a code change and a release.
    `jsonString` now means only that the String holds JSON: editor parsing and
    pretty-printing, nothing about placement.
  - **`"""editable: name"""`** re-admits a ConfigItem interface field for
    editing on one type. It is type-level because DGraph forbids redeclaring an
    interface field on an implementor. It replaces a hardcoded
    `map[string][]string{"DataCenter": {"name"}, "Rack": {"name"}}` whose own
    comment had promised an annotation since the day it was written.

  Also folded in: the metadata box's private Go label map (`{"orbId": "Orb
  ID"}`) is gone — `ConfigItem.orbId` carries `label: Orb ID`, so there is one
  label mechanism rather than two.

  **Per-deployment overrides in Postgres (spike 38 P1) are deferred**, not
  dropped: annotations keep one source of truth, version-controlled, with no
  RBAC/audit/cross-replica-invalidation machinery. The trigger for revisiting is
  recorded in `docs/planning/backlog.md` — an adopter needing to change a view
  *without* applying a schema.

### Fixed
- **The Schema page pushed its own SDL panel off the screen.** The orphaned-type
  block was added inside the narrow "Applied Schema" sidebar box, and
  `is-narrow` sizes to its content — the explanatory paragraph widened the
  column until the SDL panel sat past 1440px and was simply not there. It is now
  full width above the columns, which is also the right home for it: it is a
  statement about the DATA, not about the applied schema.

- **The Schema page's three metadata rows used three different typefaces** — a
  monospace checksum, a blue tag pill for the version, plain coloured text for
  the state — so one record read as three unrelated things. All plain text now,
  with colour only on the one row that is genuinely a status. (`UI.md`: value
  cells are plain; only a status column carries colour.)

- **Table toolbars are sized by ONE rule instead of a list of table ids.** The
  info line and paging buttons were matched to table content via
  `div.dt-container:has(#some-table)`, and a table whose id was not on the list
  rendered at DataTables' default 14px beside 12px rows. That list was wrong
  three times, always the same way — a new table is exactly the one nobody
  remembers to add. The rule now applies to every `dt-container`; a table
  wanting different chrome opts out below it, so the exception is visible
  instead of the norm.

- **The inventory notice named a count but not a type.** "1 item is hidden" told
  nobody which, and said nothing useful at all if there were fifty. It is
  rendered server-side now and names the types — "Hidden from this list: Foo
  (1)" — capped at three with "and N more". The client could never have done
  this: for those rows every field resolves to nothing, `__typename` included,
  which is precisely what the removed type took with it.

- **The nav listed "Servers" twice, and `/clusters` under two different names.**
  The Config Items menu declared Servers and Clusters by hand while the derived
  entries were also being appended, so the same page appeared as both "Clusters"
  and "Kubernetes Clusters". A view's label is now derived from its **slug**, so
  `"""slug: clusters"""` renames the page and its menu entry together.

- **IPv4 columns on generic pages sorted lexicographically**, putting
  `10.20.21.100` before `10.20.21.41`. The hand-written pages declared which
  column held an address; a derived page cannot know that, so the test is on the
  VALUE and any cell that is not an address passes through untouched.

- **Generic list pages rendered their table toolbar oversized.** The info line
  ("1 to 27 of 27 rows") and paging buttons on `/clusters`, `/data-centers` and
  every other schema-derived page came out at DataTables' default 14px next to
  12px table content. The sizing rule in `main.scss` is a hand-maintained list
  of table ids and `#generic-table` was not on it — a table that misses the list
  is silently oversized, with nothing failing.

### Changed
- **Relationship tables no longer repeat the entity whose page they are on.** A
  cluster's Nodes table carried a `cluster` column reading the same name on
  every row, and its Workload Clusters table a `managementCluster` column doing
  the same — you got there by clicking that entity, so the column could only
  ever name it. It was also part of what pushed the workload table to 13 columns
  and past its container. Dropped only when the column's target type matches the
  page AND every row actually points back, so a column carrying anything real is
  kept. Nodes 9 → 8 columns, Workload Clusters 13 → 12; neither overflows now.

### Added
- **Orbital detects nodes whose type the schema no longer declares.** Removing a
  type from `schema.graphql` and applying it does NOT remove its nodes — they
  keep their `dgraph.type`, stay reachable through the `ConfigItem` interface,
  and render as nothing. The relational equivalent is dropping a table and
  leaving its rows. Reported at boot as an **ERROR** naming each type and its
  node count, and on the **Schema** page beside the existing drift list.

  **It never deletes.** Orbital surfaces divergence and does not auto-resolve
  it; "make the schema apply succeed by removing data" is the exact shape of a
  silent loss. The operator deletes the nodes or restores the type.

  Driven by the **data**, not by diffing two schema texts. A first attempt
  compared the shipped schema against the live one and found nothing — the
  orphaning type is normally absent from both, since removing it from the file
  and applying that file is precisely how the nodes get stranded. The live
  schema says what is *declared*; only the graph says what *exists*.

  This also corrects `dgraphschema.Drift`'s stated rationale for checking one
  direction only: *"predicates present in DGraph but not in the shipped file are
  harmless."* True of a field, false of a type — and the assumption is what made
  this a blind spot.

### Fixed
- **One leftover node made the whole inventory page unusable.** A node keeps its
  `dgraph.type` when its type is removed from `schema.graphql`, so
  `queryConfigItem` still matches it through the `ConfigItem` interface while
  every field on it — `__typename` included — resolves to nothing. DataTables
  treats a missing column as fatal and blocks the page with an `alert()` naming
  only its own documentation: *"Requested unknown parameter 'type' for row
  2064"*. Nothing about orbital, the schema, or which item was at fault.

  Such rows are now dropped and the drop is **stated** — "1 item is hidden: the
  deployed schema has no type for it" — following the same rule as the
  capped-fetch notice, since a list that quietly omits rows looks exactly like a
  complete one. The session cache is filtered on the way out as well as in, and
  rewritten when it was carrying bad rows: the cached path performs no fetch, so
  a cache poisoned once would otherwise stay poisoned for the whole session,
  surviving reloads even after the offending node was deleted.

- **Deleting a cluster from its page silently did nothing.** The delete
  endpoint's cascade plans are a switch on three type names, and it expected the
  INTERFACE name `KubernetesCluster`. A page that sends the type it actually
  resolved — `EksaKubernetesCluster` — got `unsupported type`, and because the
  PREVIEW uses the same switch, the confirmation modal opened **empty** rather
  than saying anything was wrong. The endpoint now accepts either spelling by
  mapping a concrete type onto the plan that covers it.


### Fixed
- **`IPAddress`'s hand-maintained mutation payload field was wrong.** The
  registry declared `ipAddress`; DGraph generates **`iPAddress`**, because it
  lower-cases only the FIRST character and an acronym-initial type therefore
  keeps its second capital. A query using the declared value does not degrade —
  it errors outright: *Cannot query field "ipAddress" on type
  "AddIPAddressPayload". Did you mean "iPAddress"?*

  Latent rather than live: `PayloadField` reaches the editor, and `IPAddress` has
  no editable fields yet, so nothing queried it. It would have surfaced the
  moment IPAddress editing was opened up — which is one of the 29 fields
  currently held back, so this was waiting directly on the next planned change.

  Found by deriving the value from `Add<Type>Payload` and comparing: 19 of 20
  agreed, and the disagreement was the hand-maintained one being wrong. The
  registry's own comment had flagged the risk — *"it doesn't always pluralize
  cleanly (S3Sync's payload is s3Sync)"* — and then got a different irregular
  case wrong anyway, which is the argument for deriving it rather than listing it.

### Fixed
- **The config editor silently discarded unknown JSON keys.** Typing a key the
  type does not declare gave a success toast, a `version` bump and an audit row
  with the value dropped, so a typo was indistinguishable from a real edit
  (`debt.md`, Hi; hit live 2026-09-22 with `serialNumber`). The editor now
  refuses before sending anything, naming the offending key.

  **This only became correct after derivation.** An earlier attempt was reverted
  because `t.fields` was then the hand-maintained `FormFields`, so refusing would
  have blocked real schema fields the list had not caught up with — the same bug
  pointing the other way. Now that the field set comes from the deployed schema,
  a key outside it is genuinely undeclared.

- **The cluster editor showed two fields it could not save.** `cluster.go`
  flattened the `controlPlaneEndpoint` and `tinkerbellIP` IPAddress REFERENCES to
  their `.address` string and put them in the edit tree. Neither was ever in the
  editable set, so an edit to either was discarded in silence — the same
  unknown-key bug, on two fields, in the shipped UI. Proven by forwarding them
  once: DGraph answered `must be a IPAddressRef`, which is only possible if they
  had never been sent. Both are now absent from the editor and remain read-only
  in the Cluster Summary, which is the honest place for a value you cannot change
  here. Editing an IPAddress means editing that entity.

- **The editor now states WHY it is unavailable instead of rendering an empty
  form.** The field list comes from the deployed schema, so with DGraph
  unreachable orbital does not know what is editable. Rendering the editor anyway
  showed an empty tree, which reads as "this entity has no editable fields" — a
  different and misleading claim that a user would act on by assuming their data
  had gone. The modal now explains the cause, says it self-heals without a
  restart, and hides Save; the rest of the page still renders. This is the cost
  of rendering the instance rather than the build, and it is now visible rather
  than silent.

### Added
- **Reference columns**: a single, non-owned relationship renders as an extra
  column showing WHICH entity it points at — a server's rack and OOB IP, one hop
  away. Derived, not declared: single means one value fits a cell, non-owned
  because an owned child is edited inline rather than referenced. It is the
  difference between a useful table and one you have to click through.

### Fixed
- **Every generic page hung, with no error and nothing in the access log.**
  `ResolveViews` called `OwnedChildren`, which reads the interface lookup, which
  reads through the schema resolver — and `ResolveViews` runs INSIDE that
  resolver's resolve step. So it re-entered, re-resolved, and called itself
  again. Requests never finished, which presents as "slow" rather than "broken"
  and is the worst shape a bug can take. `ResolveViews` now uses the snapshot it
  already holds; `OwnedChildrenWith` takes an explicit lookup for exactly this,
  and a test asserts the package lookup is never reached from there.

- **Deleting a cluster left every associated server pointing at a deleted
  node.** The cluster delete removes its KubernetesNodes, but nothing cleared
  `Server.kubernetesNode` — and the server SURVIVES the delete. The earlier
  dangling-edge fix covered `DataCenter.servers`, `Rack.servers`,
  `KubernetesNode.server` and `DataCenter.kubernetesClusters`; this is the same
  edge from the other end, and it was missed. It recurred on every cluster
  delete and was found in local data, where one such server made an entire
  DataCenter page unrenderable.

  `TestDelete_ClusterLeavesNoDanglingServerEdge` reproduces it and is verified
  to fail without the fix. Writing it turned up a schema constraint worth
  knowing: a `KubernetesNode` can ONLY be created nested inside its cluster's
  mutation, because `KubernetesNode.cluster` is required and typed by the
  `KubernetesCluster` INTERFACE — DGraph generates no ref input for an
  interface-typed field, so `AddKubernetesNodeInput` has no `cluster` at all and
  yet refuses the mutation without one.

- **One dangling edge took out a whole page.** DGraph propagates a missing
  non-nullable field to the ROOT, so a single server pointing at a deleted
  KubernetesNode made the entire DataCenter page unrenderable. The generic
  renderer traverses far more edges than a hand-written page, so it meets such
  data much more often. The detail page now retries without reference columns
  and renders without them, logging loudly — the data IS corrupt and someone
  should fix it, but not by staring at a blank page.

### Changed
- **A relationship tab now shows the related type's OWN fields**, not just a list
  of names. A DataCenter's servers table carries hostname, model, service tag
  and rack position — the child's data, which is what the hand-written pages
  show and what makes the tab worth opening. Columns come from the target type's
  derived display set, so nothing is per-type.

### Removed
- **DataCenter's bespoke page is gone — 815 lines across 7 files.** `/data-centers`
  and `/data-centers/{id}` are served by the generic renderer in BOTH apps;
  `/datacenters` and `/datacenters/{id}` 301-redirect to them, because a slug is
  a contract and moving one needs a forwarding address. `datacenter.go` (331
  lines), `datacenter-tab.gohtml` (148), `datacenters.gohtml` (43), their tests
  and the orb fragment test are deleted. The nav entry is now the DERIVED one.

  **Parity kept:** the Servers and Racks tables with the child's own fields,
  reference columns (rack, OOB IP, dataCenter), editing with MVCC on every
  reachable target, row→tab opening, and **Delete** — whose machinery
  (`data-cfg-delete-id`, the global handler, the modal) was already generic, so
  only the markup was per-page.

  **Parity LOST, deliberately, and each cost a test:**
  - Servers and Racks were switchable inner TABS; they are stacked panels now.
  - A relationship table is a plain table — DataTables runs on the list page
    only, so no sorting or filtering inside a detail tab.
  - No per-tab reload button.
  - The rack "Servers" count is gone: it is an aggregation, and per the
    2026-09-25 decision it goes to the panel registry rather than being
    generalised.

  Also not carried over: the deleted `configitem-editor` case asserted the
  `updateDataCenter` AUDIT ROW and its diff. The generic editor has no e2e cover
  for audit integration yet.

  Orb's OWN `/datacenter` page is untouched — that is orb's single-DC view, a
  different page that happens to share a table id, and its JS stays with it.

### Added
- **Orb serves the generic pages too**, read-only, from the SAME renderer. The
  generic handlers moved off orbital's `UI` into a `GenericRenderer` that takes
  `base` and `render` as parameters — those were the only orbital-specific
  things they touched, since each app owns its template map and page chrome. Two
  apps, one renderer; a second copy is how they drift.

  Orb needs no read-only variant: the editor is already gated on `CanMutate`,
  which orb never sets.

  This was a PREREQUISITE, not a follow-up: all four bespoke detail handlers are
  shared with orb, so none could be deleted while orb depended on them.

### Fixed
- **A param route at the root made unrelated 404s into 405s.** Registering
  `GET /:slug` meant Echo matched the param node for paths that should not match
  at all, and answered "method not allowed" instead of "not found" — caught by an
  orb route test asserting `/api/v1/graphql` does not exist. The generic renderer
  is now Echo's `RouteNotFound` fallback, so it cannot affect any real route's
  resolution: "a static page always wins" is structural rather than a
  consequence of registration order.

  It must be registered on the root GROUP, not the Echo instance — the group
  carries the session middleware, and registering on the instance bypassed it,
  so every generic page rendered the login gate to a logged-in user. Curl could
  not see it (curl is unauthenticated either way); the e2e suite could.

### Added
- **Generic pages open detail in a tab below the list**, the workflow every
  hand-written list page has: double-click a row, it opens as a tab; several can
  be open at once; they survive a reload. One implementation replaces four
  per-type sets of `loadXTab` / `saveXTab` / `deleteXTab` /
  `initXTabRestoration` that differ only by a prefix and a localStorage key —
  roughly 200 generic lines against ~980 per-type ones.

  The tab body is the **same URL the row links to**, returning just the body on
  `HX-Request`. One handler and one template block serve both, per the house
  rule against a sibling `/fragment` route, so the page and the tab cannot drift.

  Tabs are **slug-qualified**: a rack tab restored onto `/storage-devices` would
  fetch a 404 and render an error where a tab should be.

  The hint banner is back, because double-click now does what it advertises.

### Fixed
- **Closing the browser did not clear cluster or network-device tabs on a shared
  machine.** `clearTabStateOnFresh` runs at the login boundary and removed
  `datacenterTabs` and `serverTabs` but not `clusterTabs` or `networkTabs`, so
  tabs opened by one user reappeared for the next — the exact thing the function
  exists to prevent, missed on two of the four tab-bearing pages because each
  page added its own key and nothing enumerated them. All four are cleared now,
  along with the generic pages' single key.

### Added
- **The generic detail page can EDIT.** `/{slug}/{id}` now renders the shared
  edit modal for any ConfigItem type, driven by one opener in `orbital.js`
  instead of the four near-identical per-type copies the bespoke pages carry.
  Nothing in the path is per-type: the field list, the edit targets, the orbId
  suffixes and the payload field all come from the deployed schema. Verified end
  to end by editing a Rack — a type with no bespoke page — and asserting the
  change survives the reload.

  `version` is selected explicitly even though it is not an editable field: it
  is a ConfigItem interface field, and without it a concurrent edit would
  overwrite silently rather than being refused.

  **Owned children are editable too.** The detail query fetches each owned
  child's own fields — recursing one level through a wrapper like
  `ClusterBackup`, whose GRANDchildren are the real edit targets — and the edit
  tree nests them at the paths `BuildEditTargets` addresses. Non-owned
  relationships still select only enough to render a link, and each relationship
  is selected exactly once so the two never collide.

  Two bugs surfaced while wiring it, both the kind that do not announce
  themselves:

  - **A wrapper offered no Edit button.** The gate was the type's OWN editable
    fields, and `ClusterBackup` has none — but its etcd/velero/s3Sync children
    are precisely what you edit there. It now gates on whether the edit tree has
    anything in it at all.
  - **Owned-child targets were reachable but carried no OCC version**, which
    means written UNGUARDED: a concurrent edit overwritten silently rather than
    refused. `StampEditTargetVersion` only ever stamped the root; every entity
    the fetch returned is now stamped. The same pass overrides each child's
    DERIVED orbId with the stored one where it exists — a derived id that
    differs would upsert a phantom entity instead of editing the real one.

  An e2e test mirrors `edit_targets_invariant_test` for the generic page, and
  additionally asserts that at least one owned child is reachable — an all-inert
  tree satisfies "no unguarded targets" while testing nothing.

### Fixed
- **A type whose fields are all `editorIgnored` rendered an empty page.** The
  generic pages used the EDITABLE field set for DISPLAY, so `StorageDevice`,
  `NetworkAdapter` and `NetworkInterface` — whose every field is annotated —
  showed nothing but a name. `editorIgnored` is editor-scoped, not
  display-scoped: a scanned hardware fact is precisely what someone opens the
  page to read, even though nobody should type into it. `View` now carries
  `Display` (every scalar) alongside `Fields` (the editable subset); pages render
  the first, the editor writes the second.

  Caught by a test asserting the Edit button is absent for such a type, which
  failed because the fields table was *hidden* — an empty table has no height.
  The missing button was correct; the empty table was the bug behind it.

### Added
- **A Views page** (`/views`, Config Items → after Schema Version) listing every
  page orbital derives from the deployed schema, so the view list can be checked
  in the UI instead of by curling the API. Rows come from
  `GET /api/v1/views` via JS — orbital's UI reads the same public endpoint an
  integrator would, rather than a private server-side path that could drift from
  it.

  The expanded panel lays the relationships out as a GRID, not a nested table.
  Bulma scopes `.is-striped` and `.is-hoverable` with DESCENDANT selectors, so a
  `<table>` inside a DataTables child row inherits both from the parent table —
  its rows alternate white/grey and highlight on hover, which reads as the parent
  table having stray selection. Out-specifying that takes four classes of
  nesting; not nesting a table avoids the question.

  Expandable rows (`dt-control`, the pattern the audit log established) show the
  two short lists the summary columns only COUNT: a count says a view has eight
  fields, and never which. Relationships link through by the target type's slug.
  An `In nav` column answers "why is this in the menu, and why isn't that" —
  nav membership is derived from containment, not declared.

  It is a SETTINGS page, so it follows `approval-policies.gohtml` (heading +
  description + table) rather than `servers.gohtml` (tabs bar, no heading).
  Those are the house's two list patterns and they are not interchangeable.

  **Read-only.** The `Source` column reads `derived` for every row today and
  gains the value `overridden` when the P1 override layer lands — the column
  exists now so it gains meaning rather than appearing later.

### Added
- **A generic renderer: defining a type in DGraph now yields a working page.**
  `/{slug}` and `/{slug}/{id}` serve EVERY ConfigItem type from two templates,
  with columns, fields and relationship tabs all derived from the deployed
  schema. No per-type handler, no per-type template, no configuration —
  configuration is the exception, not the mechanism.

  Verified end to end: adding a `Foo` type to the deployed schema with a `rack`
  edge, then creating one node, produced `/foos`, `/foos/demo:foo-1`, a `rack`
  relationship tab linking through to `/racks/...`, a `foos` tab on Rack from the
  inverse side, and a **Foos entry in the nav** — within one cache window, with
  no restart and no code change.

  **That last part was nearly missed.** Nav membership asked the Go registry
  whether a type was a root, so a type the registry had never heard of defaulted
  to NOT a root: `Foo` got pages reachable only by typing the URL, which is half
  a feature. A type nothing declares ownership of is now treated as a root —
  containment direction is not derivable from `@hasInverse`, so "no declared
  owner" is the only honest answer, and erring toward visible beats erring
  toward hidden.

  **The page uses the house furniture, not a bare table.** It mirrors
  `shared/pages/servers.gohtml` — hint banner, tabs bar, `.tab-content`
  tabpanel — and initialises a DataTable with the same toolbar the hand-written
  pages get: search, page length, colvis, Excel/CSV/Copy. It shipped first as a
  plain `<table>`, which made a derived page read as a lesser one; the migration
  replaces the hand-written pages with this, so it has to be their equal.
  `stateSave` is off deliberately: every generic page shares the id
  `generic-table`, so a saved sort from `/racks` would be restored on
  `/storage-devices` against different columns.

  The table carries no `data-testid`. DataTables CLONES it for its scroll head
  and foot, copying `data-*` attributes but not the `id` — three elements then
  shared one test id and every strict selector broke. The unique id is the
  handle.

  Routes are registered last so static pages still win, and a guard test pins
  that no derived slug collides with one — with an explicit allowlist for
  `servers`, which a bespoke page owns until the migration. An unknown slug is a
  404 rather than an empty page, because an empty page says "there are none of
  these" rather than "there is no such kind". With the schema unreadable, both
  pages state the reason instead of rendering an empty table.

### Added
- **`GET /api/v1/views`** — orbital's equivalent of Kubernetes' `APIResourceList`.
  Every ConfigItem type the deployed schema declares, with the URL slug its pages
  live under, its editable fields, and its relationships as tabs. A type added to
  the schema appears here with no code change and no restart. The UI will consume
  this rather than reaching into the registry, which keeps orbital's own UI a
  first-class consumer of its public API and hands `orbctl` and AEP the same map.
  Readable by anyone authenticated; writing overrides is a P1 concern.

  Slugs derive as `kebab(TypeName)` pluralized, from the TYPE NAME and never from
  stored orbIds — legacy ids like `<ns>:<address>` (IPAddress) and
  `<ns>:<rackName>` (Rack) carry no kind token, so id-derivation would fail on
  exactly the types that have not caught up. Three names break a naive
  implementation: `IPAddress` needs `-es` (`ip-addresses`), `S3Sync` has a digit
  mid-name (`s3-syncs`), and `IdracSettings` is ALREADY plural, which a blind
  `+s` turns into `idrac-settingss`. A `"""slug: clusters"""` annotation
  overrides derivation — in the SCHEMA, because a slug is a contract and a URL
  that moves from a preferences screen breaks every integrator.

  Nav membership is roots only, derived from containment rather than declared.
  It reproduces the four types that used to carry `IsRoot: true`, which is what
  made deleting that field safe. **`IPAddress` is the case that breaks the
  obvious rule**: it is owned by four types and declares that through
  `OwnerEdges` ALONE with `OwnerType` empty, so testing `OwnerType` by itself
  put "IP Addresses" in the nav as a top-level page.

  Two types claiming one slug is refused, naming both — a duplicate silently
  shadows one type's pages and which one wins would depend on map iteration
  order. `fields` and `tabs` serialise as `[]` rather than `null`, so clients
  iterate without a null check.

### Removed
- **`PayloadField`, `Implements`, `JSONStringFields` and the orbId-suffix switch
  statements are gone from `registry.go`.** All four were hand-maintained
  per-type data; all four now come from the deployed schema. `registry.go` holds
  **only containment and logic** — a root type's entry is now
  `{ Name: "Server" }` and an owned child's is its name plus which type owns it.

  - `PayloadField` (22 entries) — derived from `Add<Type>Payload`, matching on
    the field whose unwrapped type IS the type. That is what gets the irregular
    casings right without anyone listing them, and it found one the list had
    wrong (see IPAddress above).
  - `Implements` — derived from `interfaces { name }`. Read through a
    package-level hook, because `Children()` and `downwardEdges()` are package
    functions with no resolver to thread; production wires it once from the
    SHARED source, so no handler can silently decide what every caller sees.
  - `JSONStringFields` — now a `"""jsonString"""` annotation. Nothing about the
    field's TYPE says its String holds JSON, so this is declared rather than
    derived — but it is declared WITH the field instead of in a Go list.
  - orbId suffixes — two switch statements naming six irregular types
    (`IdracSettings` is `idrac`, not `idracsettings`) became
    `"""orbIdSuffix: ..."""` on the type. Everything else falls back to the
    lower-cased name. A wrong value here does not error: it builds an orbId for
    an entity that does not exist and upserts a phantom.

  Annotation-only schema change, so `schema/VERSION` stays at v12.
  `UnknownAnnotations` now knows the whole vocabulary — without that it reported
  `jsonString` as a typo, which is noisy and, worse, trains whoever reads the
  startup log to ignore the one report that matters.

  Removing `Implements` from the struct broke two ownership tests, correctly:
  interface-typed ownership silently stopped resolving once the hook was nil.
  A package `TestMain` now supplies it, so unit tests see the one interface
  relationship the registry actually uses.

### Removed
- **The hand-maintained per-type field lists are gone from Go.** `FormFields`,
  `BeforeFields` and `IsRoot` are deleted from `internal/configitems/registry.go`
  — 3,075 bytes of data that had to be edited by hand for every schema change and
  whose drift was silent. The editor's field list and the audit before-fetch
  selection are derived from the deployed schema; suppression comes from
  `"""editorIgnored"""` annotations in that same schema. `editorIgnoredBridge`,
  the temporary Go stopgap, is deleted too — its own test reported it had stopped
  suppressing anything once the annotations were deployed, which is exactly what
  it was written to detect.

  `TestDerivedFieldsVsRegistry_ExactlyMatchesToday` still reports **19/19 types
  deriving exactly as before**, now with zero Go-side field data on either side
  of the comparison: its baseline is a dated snapshot fixture rather than the
  registry it replaced.

  Four tests were deleted rather than migrated, because generation made their
  regression class structurally impossible: two asserting `BeforeFields` covered
  `FormFields`, one measuring whether `BeforeFields` was derivable (it was), and
  one pinning `BeforeFields` against a legacy snapshot. The mark-slot guards moved
  from unit to integration — their baseline stopped being a constant and became
  the running schema. What remains in `registry.go` is containment policy, which
  introspection cannot supply: `@hasInverse` is declared on both ends of an edge
  and says nothing about which end is the parent.

### Added
- **`"""editorIgnored"""` annotations in `schema/schema.graphql`** — 29 fields
  across 8 types that are schema scalars but have never been editable. They ride
  docstrings because DGraph rejects unknown directives outright, and docstrings
  both round-trip through `getGQLSchema` and surface in introspection as
  `description`. `schema/VERSION` does NOT bump: the diff is 29 insertions and no
  deletions, and no data contract moves.

  **Two of them exposed a silent failure mode.** `description` and `provider` are
  declared on the `KubernetesCluster` INTERFACE, and an interface annotation does
  not reach the implementing type through introspection — it reads back as
  `"editorIgnored"` on the interface and `""` on `EksaKubernetesCluster`. DGraph
  also forbids redeclaring an interface field on the implementor, so the
  annotation could not be moved down. The annotations were present, correct and
  reviewed, and suppressed nothing. `DGraphSchemaClient` now inherits annotations
  from every interface a type implements, with the type's own annotation winning.

  `editorIgnoredBridge` stays until every deployment runs the annotated schema —
  an environment on an older schema gets NO suppression from annotations, which
  is the environment-coupling hazard. `TestEditorIgnoredAnnotationsMatchTheBridge`
  holds the two in step: it fails if they disagree, and reports plainly when a
  deployment carries no annotations at all.

### Changed
- **The config editor's field list now comes from the RUNNING DGraph, not from
  compiled-in Go.** `internal/configitems/derive.go` derives each ConfigItem
  type's editable scalars by introspecting the deployed schema — type fields,
  minus the `ConfigItem` interface's fields, minus anything that is not a plain
  scalar — and `BeforeSelection` generates the audit before-fetch selection from
  that same set, so the two can no longer disagree (a Hi-severity debt row: drop
  a field from one and the mutation still succeeds while the audit event carries
  no `changes` at all). Verified live: the resolver logs `types=19` from inside
  the `/servers/:orbId` request that triggers it.

  **It changes nothing an operator can do.** `TestDerivedFieldsVsRegistry_ExactlyMatchesToday`
  asserts the derived set equals the previous hand-maintained `FormFields`
  exactly, for all 19 types, and fails on any difference in either direction —
  so a field added to the schema now forces an explicit editable-or-not decision
  instead of silently becoming editable. Two cases it pins: `name` is editable
  on `DataCenter` and `Rack` but lives on the `ConfigItem` interface, so naive
  derivation would have silently REMOVED it; and 29 fields across 8 types are
  schema scalars that were never editable, held back by `editorIgnoredBridge`.

  Resolution is continuous, not a boot snapshot: DGraph may come up after
  orbital. The derived set is cached and re-derived only when a hash of the
  DEPLOYED SDL moves, so a page load does **zero** network work. With DGraph
  unreachable and nothing cached the editor refuses with a stated reason rather
  than an empty field list — empty reads as "this type has no fields", which is
  a different and misleading claim — and it self-heals with no restart.

  Annotations ride `"""docstrings"""`, which round-trip through `getGQLSchema`
  AND surface in introspection as `description`; DGraph rejects unknown
  directives outright, so `@orbital(...)` is not an option. The resolved
  annotation count is logged on every re-derive, because editability now comes
  from the deployed schema rather than the binary — an environment on an older
  schema silently gets MORE editable fields, and the count is what makes
  zero-when-expecting-29 visible.

### Fixed
- **A change request's scope was re-derived on every read, so a later grouping
  change would have retroactively altered what a past review covered.**
  `baseScope` walks owned-child grouping, and that grouping is view
  configuration — it is about to become editable. Recomputing it on every
  Create, Amend, Approve and queue render meant "which entities did this
  reviewer look at" was answered from present-day configuration rather than
  recorded at the time of review, and it would have changed under readers with
  nothing saying it had moved. The scope is now PINNED as `base_scope` on the
  request, on exactly the terms `base_values` already is: captured at Create
  **and** at Amend (an amend re-proposes against a newly captured base, so a
  carried-forward scope would name entities the request no longer targets), and
  READ by `State`, which every render and decision goes through. Staleness
  detection is unchanged — the hash is still recomputed, now over the stored
  scope. Rows predating the field are deliberately **not** backfilled: pinning
  an old request with today's grouping would assert a review covered something
  nobody can show it covered. A pinned row also skips one DGraph round-trip per
  rendered request, on the nav badge's whole-queue path.
  `TestChangeRequest_ContainmentChangeDoesNotMovePinnedScope` deletes an owned
  child, proves re-derivation really would drop it, and asserts the stored scope
  is unmoved; verified to fail when `State` re-derives and when `Create` stops
  pinning.
- **A cascade delete left the surviving parent pointing at the node it removed,
  which permanently broke export for that data centre.** orbital deletes through
  a DQL upsert (`bulkDeleteGuarded`) because that is the only way to get a
  version-guarded CAS — but `@hasInverse` is a GraphQL-layer construct that
  DGraph maintains only for mutations through its GraphQL endpoint. A DQL
  `S * *` delete cleared the child and left `DataCenter.kubernetesClusters`
  pointing at an empty uid. Any later query walking that edge and selecting a
  non-nullable field then failed **entirely**, because DGraph propagates the
  error to the root:

      Non-nullable field 'orbId' (type String!) was not present in result from Dgraph.

  The export subgraph query is exactly that shape, so one cluster delete broke
  export for its whole DC — while the delete returned `200` with a correct audit
  event, and the damage surfaced later, in another subsystem, as an error naming
  neither the delete nor the node. Each delete path now clears the edges held by
  nodes that SURVIVE it, in the same guarded transaction: `DataCenter.servers`,
  `Rack.servers` and `KubernetesNode.server` for a server;
  `DataCenter.kubernetesClusters` for a cluster. (A data centre is the top of its
  own subtree, so nothing above it holds an edge.)
  `TestDelete_LeavesNoDanglingParentEdge` reproduces the original failure and is
  verified to fail without the fix.
- **Orb's import page showed a blank area instead of "No versions available."**
  when the OCI registry was empty or unreachable. `import_handlers.go` wrote a
  bare `<tr>` on that path, and `orb.js` assigns the response into a `<div>`'s
  `innerHTML` — where the HTML parser discards a `<tr>` that is not inside a
  table, so the empty state rendered as nothing at all. Both paths now render
  the same fragment, whose template already carries the message inside `<tbody>`.
  The e2e suite had been reporting this the whole time; it was read as a known
  failure.
- **The Data Center reload button fetched the wrong URL under a base path.**
  `shared.js` called `fetchWithMinDelay(BASE + '/datacenters/…')`, but that
  helper already prepends `BASE` — so on a deployment served under `/orbital`
  the reload requested `/orbital/orbital/datacenters/…` and 404'd. Invisible
  locally, where `basePath` is empty. The network-device and cluster reloads
  were already correct; only this one double-prefixed. Found while moving the
  network-device reload into `shared.js`.

### Fixed
- **A proposal on `Server.serialNumber` marked the wrong field state.** The
  `SummaryValuesJSON` map in `server.go` — which supplies the CURRENT value each
  proposed-change mark compares against — omitted `serialNumber`, directly under
  a comment reading "Exactly the Server FormFields in configitems/registry.go".
  A redundant proposal therefore rendered a mark it should not, and a proposal
  clearing the field rendered none at all. It is the third hand-maintained copy
  of one field list; `TestFieldMarkValuesMatchRegistry_AllTypes` now compares it to
  the registry, as `TestFieldMarkSlotsMatchRegistry_AllTypes` already did for the
  template.
- **Two mark guards were Server-only and silently unextendable.** The
  proposed-change mark needs three hand-maintained lists to agree — the
  template's `data-field` slots, the type's `FormFields`, and the handler's
  `*ValuesJSON` map. The guards added 2026-09-23 hardcoded `server-tab.gohtml`
  and `SummaryValuesJSON`, so the sibling maps `IdracValuesJSON` and
  `MaintenanceValuesJSON` were **never checked at all**, and a mark table added
  to any other template would have shipped unguarded. Both guards now DISCOVER
  their subjects — every `data-field-orbid` table under `web/templates`, every
  `rawFieldValues(...)` site in `internal/handler` — and fail on an undeclared
  subject, on a declared one that disappears, and on any list disagreement.
  Coverage went from 1 subject to 6. Verified against four injected faults:
  a removed slot, a key dropped from the previously-unguarded `IdracValuesJSON`,
  an undeclared new table, and a deleted table.
- **Nested detail tables stayed pale in dark mode.** orb's publish-history
  expansion used Bulma's `has-background-white-bis`, an absolute near-white that
  does not flip with the colour scheme, so light text landed on a light box.
  Replaced with a scheme-derived `.table-nested`.

### Changed
- **`make release-check` had been failing since 2026-07-28 and nobody knew.** Its
  step-7 mutation passed `set` as an inline literal, which orbital's proxy
  correctly refuses (`VARIABLE_FORM_REQUIRED`) because it cannot stamp
  `updatedAt`/`updatedBy` or bump `version` into inline values. The reject
  shipped three weeks after that spec was last touched. Rewritten to the
  variable form the error's own hint prescribes.
- **The release-check gate no longer accepts a consumer dispatch failure as
  success.** `partial` means `dispatchErrors > 0` — a consumer returned non-2xx —
  but the spec's comment claimed it meant "no consumer registered" (a layer with
  no consumer is skipped and never counted), and asserted only
  `.not.toBe('failed')`. Every run *was* partial: `ORB_CONSUMERS` defaults to
  cb-controller on `localhost:8095`, which inside the orb container is the
  container itself. The compose file now states `ORB_CONSUMERS="[]"` for an
  environment that genuinely has no consumers, and the assertion requires
  `done`, printing per-consumer status codes on failure.
- **`make release-check` fails in one second when the bundler is not running**,
  instead of ~20 minutes in with `connection refused`. The bundler lives in the
  sibling configbundle repo and the target cannot start it; the spec's
  prerequisites block never mentioned it and still described host processes from
  before this ran against containers.

### Added
- **`e2e/release-check/y-delete-then-export.spec.ts`** — deletes a cluster, then
  exports its data centre. This is the seam neither gate covered: the fast e2e
  suite deletes clusters but never exports, and release-check exported but never
  deleted, which is how a delete that silently broke export shipped. Verified to
  fail against a rebuilt image with the fix reverted.

### Changed
- **`ORBITAL_DEV` is gone, split into per-capability flags.** One boolean named
  after an audience decided three unrelated things — template hot-reload,
  whether bearer auth was installed, and whether the placeholder session HMAC
  key was accepted. A developer who wanted hot-reload also got auth off, and
  nothing in the name said so. It defaulted to `true`, so the coupling was
  **fail-open**: configure nothing and you got no API auth, and the fail-closed
  guard in `server.go` could not catch it because that guard only fires when
  auth is *required*.

  Replaced by `ORBITAL_TEMPLATE_HOT_RELOAD_ENABLED` (default `false`),
  `ORBITAL_API_AUTH_ENABLED` (**default `true`**, inheriting from nothing), and
  an unconditional refusal of the placeholder session key — which is published
  in this repo and so is a secret nobody has. With `ORBITAL_SESSION_HMAC_KEY`
  unset, an ephemeral key is generated and said out loud at startup, so a fresh
  clone still runs with no setup. `ORBITAL_COOKIE_SECURE` was already independent.

  **Developer posture moved into the Makefile**, where it is visible, instead of
  living in the binary's defaults: `make run-orbital` opts into hot-reload, opts
  out of API auth, and writes a persistent `deploy/local/session-hmac.key`
  (gitignored) so sessions survive restarts. Local, containerised and Kubernetes
  runtimes are unchanged — each now states both halves explicitly.
  orb's `ORB_DEV` became `ORB_TEMPLATE_HOT_RELOAD_ENABLED` in the same pass; it
  only ever controlled hot-reload.

### Added
- **Orb serves network devices.** `/network` and `/network/:orbId` reuse
  orbital's `NetworkDeviceHandler` with `layout.OrbActions`, matching how orb
  already serves Data Center, Server and Cluster detail tabs — read-only, no
  Edit or Delete controls. The Config Items menu gains a Network Devices entry.
- **`internal/webrender`** — the buffer-then-write HTML render helper, lifted out
  of `internal/handler` so both server packages can reach it. It was unexported
  there, so `internal/orbserver` structurally could not call it and every
  orb-owned page rendered straight into the response: a template referencing a
  field its render struct lacks committed a `200` with a truncated body, no
  `500`, nothing logged. Orb's ten pages and its HTMX fragments now buffer like
  orbital's. (Orb's DC/Server/Cluster detail tabs were already safe — they run
  through orbital's handlers.)
- **Regression guards for template drift**, each verified to fail on an injected
  fault rather than merely passing:
  `TestAllTemplatesReachable` (a `.gohtml` no parse set references fails the
  build — seven such files had accumulated), `TestNoDirectTemplateExecuteIntoResponse`
  (the render rule is enforced in source, not prose), `TestOrbitalPages_AllPathsReturn200`
  (orbital's counterpart to orb's, which had no equivalent; renders **authenticated**,
  since an unauthenticated render exercises only the login gate) and
  `TestOrbitalPages_LoginGateWhenUnauthenticated` for the other branch.

### Changed
- **One edit-modal template replaces four.** `shared/components/edit-modal.gohtml`
  renders from `component.EditModal`. The four per-family copies had already
  drifted into two defects: the network-device modal used `networkdevice` for
  the modal id and `network-device` for every inner id — the opener keys on one,
  the editor on the other — and `data-reload-url`/`-target` were carried by
  cluster and network-device where nothing reads them (only the server opener
  does). Adding a ConfigItem family is now a handler struct plus one opener,
  not a five-file copy. Two tests pin it.
- **One modal-close handler replaces four**, via a prefix-free
  `data-modal-close` in `shared.js`. The inline `<script>` in `login-modal.gohtml`
  that previously owned this closed on `.modal-card-foot .button` — harmless as
  a load-time bind, but as delegation that selector closes an edit modal when
  **Save** is clicked.
- **The metadata box is one partial**, not four byte-identical copies across the
  detail tabs.
- **`title=""` tooltips are gone** — all 47, across 15 templates and two JS
  modules, replaced by the house `tooltip` + `data-text` (with a new `is-wide`
  variant, since the base is `nowrap` and several were full sentences).
- **Page descriptions use a `.page-description` class** instead of an inline
  `font-size`, which had produced three different values across three spacings.
- **Record-list tables all carry the house class** (16 distinct class strings → 9,
  the remainder being key/value summary tables, which are a different shape).
- **Inline `style="overflow-x:auto"` replaced by `.table-container`** at all 25 sites.
- **Four orb pages now get the house `.box` treatment.** `main.scss` scopes box
  padding, shadow, border and tint to `.app-main .tab-content .box`, so a page
  putting a `.box` straight into `.app-main` rendered an unstyled Bulma default.
  Orb's status, schema, import-history and publish-history pages did exactly
  that. On publish-history the class goes on `#publish-history-content` — the
  HTMX swap target — so it survives pagination swaps. Orbital's approval-policies
  page was on the same list but does NOT qualify: its only `.box` is inside a
  `.modal-content`, where the treatment would be wrong.
- **The Servers, Clusters and Network Devices pages are now one template each,
  shared by orbital and orb**, in `web/templates/shared/pages/`. The former
  copies differed only by orbital's login gate — 39 of orb's 39 `servers.gohtml`
  lines were identical to orbital's — so the gate became conditional on
  `.UI.ShowAuth` (orb sets it false) via a new `login-gate.gohtml` partial.
  Template duplication fell from 163 duplicated 8-line blocks to 96.
- **List-page wiring moved into `shared.js` as `initListPages()`.** `orbital.js`
  and `orb.js` each carried their own near-identical `DOMContentLoaded` blocks
  wiring the datacenter, server and cluster tables — ~60 duplicated lines across
  two files, while `shared.js` already exported every function they called.
  Cross-file JS duplication between the two app modules is now zero (was 26
  blocks). Network devices join the same descriptor list, so orb gets that page
  automatically.

### Changed
- **The Playwright suite is green again — 106 passing, 0 failing.** Four tests
  had been failing continuously; each is now fixed at its cause rather than
  muted. One was a real product bug (orb's empty state, above). One pinned
  `expect(slots).toBe(6)` on the Server summary table and broke when
  `serialNumber` and `uHeight` were added *correctly* — a magic number cannot
  distinguish a properly-added field from a dead mark slot, so the invariant
  moved to `TestFieldMarkSlotsMatchRegistry_AllTypes`, which compares the template's
  `data-field` rows against `configitems.Types` FormFields and names the
  offending field in either direction. The remaining two asserted against a
  state the app cannot reach on a freshly-seeded stack: with no approval policy
  matching, a change request is created already approved, never enters
  `StatusOpen`, and offers merge/close rather than approve/edit. Both now create
  the policy they depend on via the existing `policy-snapshot` helpers, so the
  precondition is stated instead of inherited from whatever a developer had
  lying around.

### Removed
- **Seven dead templates.** `form-{cluster,server,user}-create.gohtml`,
  `delete-modal.gohtml`, `delete-modal-user.gohtml`, `register-modal.gohtml` and
  orb's `navbar.gohtml` stub — none referenced by any parse set. Two of them
  both declared `id="delete-modal"`, the id `orbital.js` uses for the live
  backups delete flow, so adding either to a parse set would have silently bound
  that flow to the wrong modal. `register-modal.gohtml` also implied a local
  registration path that `AUTH.md` forbids.

### Fixed
- **Three browser-login failures redirected to the wrong page under a base path.**
  `invalid_state`, `no_id_token` and the new nonce refusal redirected to
  `/?error=…` while every other login error used `basePath + "/?error=…"`. On a
  deployment served under `/orbital` those three bounced to the cluster root, so
  the user saw someone else's 404 rather than orbital's sign-in message.

### Changed
- **Browser-login error codes joined the registry.** `invalid_state`,
  `no_id_token` and `invalid_nonce` were raw strings while every neighbouring
  refusal used a registry code, so the login modal rendered them bare instead of
  as `error — hint`. Now `INVALID_STATE`, `NO_ID_TOKEN` and `INVALID_NONCE`, each
  with an operator-readable explanation and a row in `ERROR-RESPONSES.md`.
- **~22 comments citing `ADR 0NN` now cite the domain docs.** `docs/decisions/`
  was removed on 2026-07-07 and the pointers went with it, leaving code that
  referred readers to files that had not existed for months — ADR 012 in the
  divergence paths, ADR 010 in the app-principal paths, 007 in the schema-version
  note, 002 in the delete handler. Two citations were *removed* rather than
  repointed: `DIVERGENCE.md` cited the ADR it had absorbed, and a `UI.md` bullet
  about shared JS navigation cited an ADR about shared ConfigItem handlers in Go —
  a wrong pointer is worse than a dangling one. The runbook mention stays, since
  it explains the removal and how to recover the file from git.

- **CSRF protection on cookie-authenticated API calls no longer depends on the
  client attaching a token** (`internal/auth/csrf.go`). The token guard added in
  the auth refactor required `X-CSRF-Token` on every non-GET request to
  `/graphql` and `/api/v1`. `shared.js` attached it for `fetch` and for htmx, but
  not for the jQuery XHR behind DataTables — so **v0.0.46 returned 403 on the
  Data Centers, Servers, Clusters and Network list pages**. A token has to be
  wired into every client transport, and this app has three.

  The guard now reads what an honest client already sends. A stated
  `Origin` (or `Referer`) is authoritative — same host passes, another host is
  refused. If a request states neither *and* carries a content type only an HTML
  form can produce (`application/x-www-form-urlencoded`, `multipart/form-data`,
  `text/plain`), it is refused; that backstop is what makes the first rule's
  fail-open safe. `application/json` is unreachable from a cross-site form, and a
  cross-origin `fetch` sending it triggers a CORS preflight that fails because
  orbital configures no CORS policy — the same approach as Apollo Server's
  `csrfPrevention`. No client-side code is involved, so a future transport cannot
  forget it.

  The order is deliberate: **htmx encodes as `application/x-www-form-urlencoded`
  by default**, and two `hx-post` attributes target `/api/v1`, so testing content
  type ahead of `Origin` would have 403'd every htmx mutation — the same failure
  as the token, from the other direction.

  **Bearer callers stay exempt** — orbctl, AEP Fleet Commander and cb-bundler are
  unaffected, as before. **Login, logout and register are unaffected**: they POST
  form-encoded to `root` UI routes outside this group and keep their hidden
  `csrf` field. Requests with neither `Origin` nor `Referer` are allowed
  deliberately — a browser always sends `Origin` cross-origin, so their absence
  means the call did not come from a browser form. `SameSite=Lax` is unchanged.

  `ORBITAL_CONFIG.csrfToken` and the `fetch`/htmx wrappers in `shared.js` are
  removed. See `docs/reference/AUTH.md` § CSRF on cookie-authenticated API calls.

### Added
- **`Rack.uHeight`** — rack height in units (`schema/VERSION` → **v11**, apply
  per cluster). Editable from the DataCenter page and shown as **Height (U)**;
  unset renders as an em dash, never `0`. Nullable by design — 42U is common but
  not universal, so orbital records it rather than assuming it.

  **Do not compute a rack's valid unit range as `1..uHeight`** — that needs
  `startingUnit` and `descUnits`, which orbital does not model yet. See
  `DGRAPH.md` § Schema rules.

- **`Server.uHeight`** — how many rack units a server occupies (`schema/VERSION`
  → **v12**, apply per cluster). Pairs with `Rack.uHeight`: how many units the
  rack has, versus how many a server takes. `Float` and nullable — `0` means a
  rack-mounted device that consumes no unit, which is not the same as unset.
  Sizes vary from 1U to 6U, so there is no default. Editable in the config editor
  and shown on the server page.

- **`version` is shown in the Metadata panel** on the Server, Data Center, Cluster
  and Network Device tabs. It is the value a caller needs to guard a write
  (`"version": <n>` in the mutation's variables → `409 MVCC_CONFLICT` on a
  concurrent edit), and it was the one ConfigItem field the UI never surfaced.
- **`Server.serialNumber`** is editable in the config editor, shown on the server
  detail tab, and carried in audit diffs — added to `FormFields` and
  `BeforeFields` in `internal/configitems/registry.go` and to the `GetServer`
  query. Without the registry entry the editor silently dropped the key: the save
  reported success and bumped `version`, but nothing was written. See the two new
  rows in `docs/planning/debt.md`.
- **`Server.serialNumber`** (`schema/VERSION` → `v10`). Holds Redfish
  `ComputerSystem.SerialNumber` verbatim, alongside the existing `serviceTag`.
  The two are **not** interchangeable: an R450 reports `DLP6K74` for both, while
  an R650 reports `SKU=CFRHDX3` and `SerialNumber=MXFC400359006Z` — confirmed
  against live iDRAC 7.20.10.05. Added so consumers can compute serial-number
  drift per server, which was impossible while orbital stored only the service tag.

  `serviceTag` remains the `orbId` natural key and is unchanged; `serialNumber`
  is a plain attribute and must never be used for identity. **Requires a DGraph
  schema alter on deploy** (`deploy/README.md` § 8) — the field is
  indexed `@search(by: [hash])`, so servers can be looked up by serial.

### Changed
- **Local DGraph export directories moved from `/tmp/orbital-test-*` to
  `.local/exports/{blue,scratch,test}`.** **Every developer must recreate their
  stack once** (`make down && make up`, then `make seed`) — until they do, their
  containers stay bound to the old paths while orbital reads the new ones, which
  produces exactly the failure this change removes.

  `/tmp` was the wrong home on two counts. macOS prunes it periodically, and
  removing an export *directory* under a running container leaves a stale bind
  mount: the mount was resolved at container start, so the container holds the
  old inode and the two sides stop agreeing about that path. Re-creating the
  directory on the host does not repair it — only restarting the container does,
  which is why `make up`'s `mkdir` could never help. The symptom landed much
  later as a backup failing with *"no json.gz found after export"*, and it was
  diagnosed as a product bug more than once. Second, `/tmp` is shared between
  checkouts: two clones of orbital clobbered each other's exports.

  `.local/` is already gitignored and already the home for local-only files, so
  ignored artifacts now live in the ignored tree rather than inside tracked
  `deploy/local/`. Paths are relative in both places but spelled differently —
  compose resolves against its own directory (`../../.local/exports/blue`), Go
  against the process working directory (`./.local/exports/blue`).

  Two incidental fixes carried along: the containerized orbital no longer needs a
  path-identity mount (`/tmp/x:/tmp/x`) now that it sets
  `DGRAPH_SCRATCH_EXPORT_DIR` explicitly, and the constant `blueExportDir` —
  which pointed at the *test* alpha's directory, not blue's — is now
  `testAlphaExportDir`.

- **`docs/auth.md` rewritten for the provider list.** The integrator-facing guide
  still described Entra: MSAL libraries, tenant GUIDs,
  `api://<client>/user_impersonation` scopes, an On-Behalf-Of code sample, and
  `orbctl login` as the primary way to get a token. Every one of those produces a
  token current orbital rejects, and the CLI's login is broken pending redesign.
  It now says what an integrator actually needs: that orbital is a resource server
  which issues nothing, the four things to ask the operator for (issuer, the
  client id whose `azp` must match, the required audience, the group-to-role
  mapping), the three caller shapes (client credentials as an app principal,
  delegated for a backend acting for its own users, PKCE for a human), how a role
  is derived, and the errors they will actually hit. Provider-neutral throughout —
  no vendor is named, because the adopter's IdP is theirs. 176 → 120 lines.

- **The sign-in button no longer says "Sign in with Microsoft".** It reads
  `Sign in with {ORBITAL_OIDC_DISPLAY_NAME}`, default **`SSO`**, with a neutral
  icon; `static/logo/microsoft.svg` is deleted. The button named a vendor orbital
  no longer trusts — the deployed IdP is Keycloak — but the fix is not to hardcode
  the new one: the adopter's IdP is theirs, so an operator sets `Okta`,
  `Keycloak`, `Entra ID` or whatever their users recognise. Follows ArgoCD's
  `oidc.config.name` and Grafana's generic-OAuth `name`, both of which default to
  something provider-neutral. If browser login ever accepts several providers,
  the same field scales to one button each — the Grafana and GitLab shape.

  An adopter who wants a branded button sets `ORBITAL_OIDC_ICON_URL` to an image
  **they** host or mount; empty renders a neutral glyph. **Orbital ships no vendor
  logos on purpose** — "Sign in with Microsoft" and its Google equivalent are
  specified brand treatments with mandated wording and dimensions, and shipping
  those marks in an open-source repo hands a trademark obligation to every adopter
  and fork. The operator has the relationship with their IdP, so they supply the
  asset and the label. Gitea takes the same approach with its per-source icon.

- **BREAKING — `trustedService` is now `delegatedAuthorization`, `claimValidationRules`
  is removed, and unrecognised fields fail startup.** Three changes to
  `ORBITAL_AUTH_PROVIDERS`, which shipped in v0.0.44; each needs a config edit.

  `"trustedService": { "assignedRole": "admin" }` becomes
  `"delegatedAuthorization": { "role": "admin" }`, with behaviour unchanged. The old
  name described what the caller *is* while its two siblings describe what orbital
  *does*, and "trusted" named nothing — every provider in the list is trusted. The
  new name is RFC 8693 §1.1's: this is delegation rather than impersonation, since
  both identities survive (the human in the audit actor, the client in
  `acting_client`), and only *authorization* is delegated — orbital still
  authenticates the token itself against the issuer's JWKS. The inner key is `role`
  because the parent already supplies the qualifier, the same reason Kubernetes
  writes `issuer.url` and RFC 8693 puts `sub` inside `act`.

  **`claimValidationRules` is removed.** It existed to anchor `azp`; `clientID`
  superseded that job and nothing else ever used it — both its tests still asserted
  the `azp` case. Use `clientID` for `azp` and `issuer.audiences` for `aud`; a rule
  on either was redundant at best, and `{"claim": "aud", …}` in particular rejected
  every Keycloak token, because the comparison is string-only while Keycloak issues
  `aud` as an array.

  **Unrecognised fields now fail startup.** `encoding/json` drops what it does not
  know, so without this both changes above would silently disarm a working config
  instead of refusing it — and `client_id`, the OAuth spelling of `clientID`, would
  leave an entry matching every client of its issuer. Kubernetes decodes
  `AuthenticationConfiguration` strictly for the same reason.

  **Roll the config and the image together.** A v0.0.44 orbital reading a
  `delegatedAuthorization` config ignores the field and then refuses to start
  (*"set defaultRole, roleMapping or trustedService"*), and this build refuses a
  `trustedService` config by name.

### Removed
- **BREAKING — the single-issuer bearer path is gone, and with it
  `ORBITAL_APP_TOKEN_ALLOWED_APPIDS`.** Bearer verification now exists only
  through `ORBITAL_AUTH_PROVIDERS`: **a deployment with API auth enabled and no
  provider list refuses to start** rather than falling back to a second path.
  `ORBITAL_OIDC_*` is unchanged and still drives the browser login flow — orbital
  is an OAuth *client* there and a resource server here, and only the second one
  moved.

  The allowlist existed because the old path trusted exactly one
  `(issuer, client)` pair, so "which application minted this token" needed a
  separate global list. A provider list is keyed on `(iss, azp)`, so the entry
  **is** the allowlist: a client-credentials caller authenticates iff some entry's
  `clientID` matches its `azp`, and is refused before any role logic otherwise.
  Its one lasting rule survives in `AUTH.md` — an unset allowlist must never mean
  "allow everything" — and under the provider list an empty config cannot be
  permissive, since there is no entry to match. Pinned by
  `TestProviderSet_AppTokenIsGatedByClientID`, which asserts both the listed and
  the unlisted case.

  **`deploy/base` no longer names an identity provider at all.** The issuer,
  client id, token URL and provider list moved to the overlays, because base must
  not point an adopter's deployment at someone else's IdP. `dev-netbox` carries
  them for both the orbital container and the in-pod cb-bundler; an overlay
  without a provider list will not start.

- **BREAKING — `ORBITAL_AUTH_MODE=external-jwt` is gone**, along with
  `ORBITAL_JWT_ISSUER`, `ORBITAL_JWT_AUDIENCE`, `ORBITAL_JWT_CLIENT_ID` and
  `ORBITAL_JWT_DEFAULT_ROLE`. `ORBITAL_AUTH_PROVIDERS` supersedes it: a
  `delegatedAuthorization` entry is the same behaviour (every valid token gets one
  role, no user row) keyed on `(iss, azp)` rather than globally, so the
  dual-issuer fallback the mode carried for AAD service and orbctl tokens becomes
  one more entry in the list. **A deployment still setting these vars will not
  start** — envconfig ignores a variable with no matching field, so they
  contribute nothing, and with no provider list the fail-closed guard refuses
  startup rather than serving unauthenticated. Migration is one provider entry;
  the shape is in `docs/reference/AUTH.md` § `delegatedAuthorization`.

  One convention the mode owned is recorded in AUTH.md rather than lost: an
  auth failure never logs an identity decoded from an unverified token.
  `ProviderSet` logs the issuer, reason and `request.id` only.

  `make run-orbital-aep` is removed with it — it hardcoded an internal Keycloak
  host and client into a build file. `make run-orbital` sources
  `deploy/local/orbital.env` (gitignored) when present, and
  `deploy/local/orbital.env.example` now carries provider-agnostic placeholders
  plus a commented `ORBITAL_AUTH_PROVIDERS` example of both shapes. It also sets
  `ORBITAL_API_AUTH_ENABLED=true`, without which `ORBITAL_DEV=true` bypasses
  bearer verification and a provider list looks broken when it is simply not
  being consulted.

### Security
- **State-changing API calls authenticated by session cookie now require an
  `X-CSRF-Token` header.** A cookie is an ambient credential — the browser
  attaches it to any request to this origin, including one a third-party page
  caused — so authentication alone never proved the user intended the call.
  Until now the entire defence was `SameSite=Lax` on the session cookie: a real
  control, but one attribute, and setting `SameSite=None` (to embed orbital's UI
  in another product's frame, say) would have made every mutation forgeable with
  nothing in the code to notice. **Bearer callers are exempt and must be** — a
  token is not ambient, and an API client has no session to fetch a token from.
  Reads are untouched.

  Client-side the header is added by a single `fetch` wrapper in `shared.js`,
  which `orbital.js` and `orb.js` import, plus an `htmx:configRequest` listener
  for htmx's own XHRs — rather than at ~25 call sites, so one added later is
  covered without anyone remembering. Same-origin requests only; the token is
  never attached to a third-party URL. Verified live: a cookie-authenticated
  `POST /api/v1/backup` returns **403** without the header and **202** with it,
  while `GET` is unaffected. Pinned by `TestRequireCSRFOnCookieAuth`, including
  the bearer exemption and a wrong-token case.

### Changed
- **`ORBITAL_AUTH_PROVIDERS` is a privilege grant, not just an identity list** —
  documented, not changed. Any app (client-credentials) caller whose `azp`
  matches an entry is `dev`-equivalent on every mutating route, so listing a
  client grants it write access to everything a dev can write. Reviewed
  2026-09-22 and kept: per-client scoping is cheap to add later (a role on the
  entry, the shape `delegatedAuthorization.role` already uses) and no second
  machine caller needs it yet. `AUTH.md` § App callers now states the blast
  radius instead of leaving it in a code comment.

- **Browser login now uses PKCE, binds the ID token with a `nonce`, and compares
  `state` in constant time.** OAuth 2.1 makes PKCE mandatory for **all** clients,
  confidential included — the client secret proves which application is redeeming
  the code, the verifier proves it is the same party that started the flow.
  orbctl had been doing this correctly while orbital's own login did not. The
  `nonce` is what makes a replayed ID token detectable: a token lifted from
  another login attempt verifies perfectly on signature, issuer, audience and
  expiry, and nothing else catches it. **An ID token with a missing or mismatched
  nonce is refused** (`?error=invalid_nonce`), the absent case explicitly, so an
  implementation that skipped the check when the claim is absent cannot pass.
  Pinned by `TestOIDCLogin_SendsPKCEChallengeAndNonce`,
  `TestOIDCCallback_NonceMismatchIsRefused` and
  `TestOIDCCallback_MissingNonceIsRefused`.

  The three values are stored and cleared as **one** record (`auth.OIDCLogin`) —
  clearing the state while leaving a verifier or nonce behind is how a stale
  value gets reused on a later attempt. Round-tripped by
  `TestOIDCLogin_StateVerifierAndNonceRoundTripTogether`, which asserts all three
  rather than just the state: a verifier lost in transit turns the exchange into
  an unexplained 400 at the IdP, and a lost nonce silently disables the replay
  check.

- **Rate limiting is enabled in `deploy/base`.** `ORBITAL_RATE_LIMIT_ENABLED`
  defaults to `false` in code, which is right for local dev and wrong for every
  real deployment: the mechanism existed, attached a tighter bucket to
  `POST /user/login`, and was never switched on — so the one credential-guessing
  surface orbital has ran unguarded. Local password login is the break-glass path
  and cannot be disabled, which is what makes it worth guarding. Verified: at
  `ORBITAL_LOGIN_RATE_LIMIT_RPS=1` a burst of six attempts returns
  `200 200 429 429 429 429` with `Retry-After: 1`.

  Per-pod and in-memory, so the ceiling is `RPS x replicas`. It slows one source;
  it deliberately does **not** lock accounts — lockout on a break-glass path is a
  denial-of-service anyone can trigger with an admin's email address.

- **The OIDC callback no longer logs claims at INFO on every login.**
  `oidc.go` logged email, name and `preferred_username` unconditionally, while
  `AUTH.md` documented claim logging as debug-only and off by default. The
  `logger.Debug("oidc id token claims", …)` twenty lines below it — the one the
  docs describe — is unchanged.

### Fixed
- **A human bearer token could impersonate a service account and gain write
  access.** Orbital labels machine callers `app:<azp>` in `user_name`, and both
  `ResolveUser` and `RequireRole` classified callers by prefix-matching that
  string. For a human, `user_name` is the token's `name` claim — which Keycloak
  lets the end-user edit in their own account console — so a token whose name
  began `app:` took the app-principal branch: provisioning skipped, the
  provider's role never applied, and `RequireRole` granting dev-equivalent
  access. A readonly user could write.

  The verifier now records the principal kind on the request context
  (`auth.MarkAppPrincipal` / `auth.IsAppPrincipal`), where no token claim can
  reach it, and both branches read that. `AppPrincipalPrefix` remains as the
  display label for audit records, which is all it was ever for. Kubernetes
  solves this by RESERVING its `system:` prefix, which it must because it is
  stateless and the username string is the identity; orbital has a context and
  can carry the fact instead — the same reasoning that rejected `usernamePrefix`
  for identities.

  Pinned by `TestRequireRole_ForgedAppNameIsNotAnAppPrincipal` (integration —
  the grant happens at `RequireRole`, which a nil-db unit test short-circuits
  before reaching) and `TestProviderSet_HumanTokenCannotForgeAnAppPrincipal`,
  with the real client-credentials path kept as the positive control. Both were
  run against the previous implementation and fail there.

## [v0.0.44] - 2026-09-20

### Added
- **Orbital accepts bearer tokens from multiple identity providers.**
  `ORBITAL_AUTH_PROVIDERS` takes a JSON array of providers, each with its own
  issuer, audiences, claim validation and role handling. A token selects its
  provider by the `(iss, azp)` pair, which must be unique, so exactly one provider
  ever attempts cryptographic validation and there is no "try each until one
  accepts" fallback. Keying on the pair rather than the issuer alone lets one
  Keycloak realm host several clients orbital treats differently. A token whose
  `azp` matches no entry is refused, which makes `clientID` the app-token gate —
  `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` is ignored when the provider list is set, and
  orbital warns rather than leaving it silently inoperative. Unset, everything
  behaves as before; setting it alongside `ORBITAL_AUTH_MODE` is a startup error.

  Each provider says where its callers' roles come from: `defaultRole` (orbital's
  users table owns roles; a new user is created with it and existing users keep
  theirs), `roleMapping` (the provider's group claim owns roles, re-derived at
  every login; no matching group is denied rather than dropped to readonly), or
  `trustedService` (a caller whose authorization happens upstream: every valid
  token gets the assigned role and no user row is provisioned). `trustedService`
  combines with neither of the others and setting none of the three is a startup
  error; `defaultRole` alongside `roleMapping` is legal and is described below.
  `trustedService` replaces `ORBITAL_AUTH_MODE=external-jwt` and its
  `ORBITAL_JWT_DEFAULT_ROLE` — same behaviour, named as the deliberate trust
  delegation it is rather than a default nobody overrode.

  Orbital keys users by email, so one address cannot be shared across providers —
  a second provider asserting an existing address is refused rather than
  inheriting the row.

  `defaultRole` may be set alongside `roleMapping`, where it is the floor for a
  token whose groups match nothing — without it an unmatched login is refused
  (Grafana's `role_attribute_strict`). `users.role_source` records whether the
  provider or an admin last set a role: a matched group always wins, but the floor
  applies only to users the provider already owned, so an admin's promotion is not
  reverted at the next login. The users page renders provider-set roles read-only
  with provenance and leaves locally-set ones editable — Grafana and NetBox
  overwrite manual changes silently, which is the behaviour their users complain
  about.

  Role changes driven by a provider write an audit event naming the provider and
  the causing group — the transition only, not every login. The users page shows
  provider-owned roles read-only with their provenance, because an editable field
  that reverts at the next login is worse than one that says who owns it.

  Client-credentials tokens are treated as app principals: no user row, gated by
  `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` as before. Design and rationale in
  `docs/reference/AUTH.md` § Multiple identity providers.

  Browser sign-in uses the same mapping as bearer callers, so one identity from
  one provider cannot end up with two different roles depending on whether it
  arrived with a cookie or a token. A refused sign-in now shows a reason —
  `NO_ROLE_MAPPED` or `IDENTITY_INCOMPLETE`, rendered as `error — hint` per
  `ERROR-RESPONSES.md` — where it previously bounced silently to the home page.
  `ORBITAL_LOG_LEVEL=debug` logs the decoded ID token claims (never the raw
  token) for diagnosing a mapping, since the ID token arrives back-channel and
  is otherwise invisible to an operator.

  Audit events gain `acting_client`, recording the OAuth client that presented
  the token when it differs from the human in `actor` — so a request made by a
  trusted upstream service on a user's behalf is distinguishable from that user
  acting directly. Empty when the caller is their own actor. Inferred from the
  verified `azp`; RFC 8693's `act` claim supersedes it when token exchange lands.

  `users` gains a nullable `issuer` column recording which provider owns each
  row. A provider only ever resolves rows it owns — a login for an address owned
  by a local account or another provider is refused with `IDENTITY_CONFLICT`
  rather than claiming the row. Without that, any configured provider could mint
  a token for an existing address and inherit that user's role, including the
  local break-glass admin. Local password accounts have no issuer, which is what
  keeps break-glass representable and unclaimable. The one exception is a row with
  neither an issuer nor a password — unowned and never a local account — which the
  first provider to resolve it claims, so users predating the column are not
  locked out of SSO.

- **`ORBITAL_API_AUTH_ENABLED` splits API authentication out of `ORBITAL_DEV`.** `ORBITAL_DEV` bundled
  three unrelated switches — template hot-reload, the API bearer-auth bypass, and permission to use
  the placeholder session key — so you could not verify bearers locally without also giving up
  hot-reload, and the startup log blamed `dev:true` for auth being off. The new variable is
  hierarchical, not independent: **unset it follows `!ORBITAL_DEV`**, so no existing deployment
  changes. Set explicitly it wins, making `ORBITAL_DEV=true` + `ORBITAL_API_AUTH_ENABLED=true` a
  valid combination for the first time. An explicit `false` disables auth in every auth mode;
  an inherited `false` does not, because `external-jwt` never consulted `ORBITAL_DEV` and quietly
  dropping auth there would be a regression. Auth resolving to enabled with no usable verifier now
  **refuses startup** — the fail-closed guard keys on intent rather than on `ORBITAL_DEV`. Both auth
  log lines carry `decided_by`, so the deciding setting is stated rather than inferred.
  `ORBITAL_DEV` keeps only what its name implies.

### Changed
- **BREAKING (deployment) — orbital trusts Keycloak only; Entra is gone from
  `deploy/base`.** The UI's OIDC client, the bearer providers and cb-bundler's
  client-credentials grant all point at one Keycloak realm. cb-bundler needed no
  code change — it already took `ORBITAL_TOKEN_URL` and `ORBITAL_TOKEN_SCOPE`
  overrides for non-Entra providers; both are now set, and the scope matters
  because Keycloak rejects Entra's `api://{clientID}/.default` form.
  **`orbctl login` is broken by this** and is tracked in
  `docs/planning/backlog.md` — it builds Entra URLs by string concatenation
  instead of using OIDC discovery.

### Removed
- **BREAKING — device-code browser SSO is gone.** `ORBITAL_OAUTH2_DEVICE_CODE` (which defaulted to
  `true`), `GET /auth/device`, `POST /auth/device/poll` and `pages/device-code.gohtml` are all
  removed; the login modal now always uses Authorization Code via `GET /auth/login`. **A deployment
  relying on device code must register a redirect URI with its IdP and set
  `ORBITAL_OIDC_REDIRECT_URL` to match** — byte-for-byte, since OAuth compares it exactly.
  Device code existed to work around one Azure AD constraint (orbital sits behind an Internal Load
  Balancer on private DNS, and Entra would not accept a redirect URI it could not resolve). It was
  vendor-locked in the worst way: the endpoint was built by string surgery on the Azure URL shape,
  `TrimSuffix(issuer, "/v2.0") + "/oauth2/v2.0/devicecode"`, which 404s against any other provider —
  so the `true` default silently broke browser SSO for anyone pointing orbital at a non-Entra IdP.
  Keycloak accepts `http://` and private-hostname redirect URIs, so the original constraint does not
  exist there. It had a single consumer, orbital's own login modal, which is a browser and therefore
  always had the standard flow available. **orbctl is unaffected** — it uses Authorization Code +
  PKCE with a loopback listener (RFC 8252) and never called these endpoints.

## [v0.0.42] - 2026-09-17

### Added
- **`DataCenter` gained a `model` field, typed as the `DataCenterModel` enum**
  (`Beacon`, `Cruiser`, `Triton`, `Leviathan`; `schema/VERSION` → `v9`). An enum
  rather than a free string, so the set of Galleon models is declared in the schema
  and reachable by introspection — a client renders a dropdown without orbital
  publishing a separate list. The field is optional; existing data centers read back
  `null` until it is set. DGraph enforces the enum at the GraphQL layer only, so a
  `dgraph live` bulk load can still store an off-list value, which then reads back
  as `null` **alongside an `errors` entry** rather than failing the query — a client
  that reads `data` and ignores `errors` cannot tell unset from corrupt. Query,
  introspection and update examples are in `docs/api-cheatsheet.md`.

- **Orbital now reports when DGraph is running an older schema than the build ships.** It logs a
  `WARN` at startup naming every missing declaration, and the Schema page states whether the applied
  schema matches. Orbital still does not *apply* the schema — that remains a manual deploy step —
  but forgetting it used to produce no signal at all until users hit 404s or silently truncated
  pages (v0.0.25 queried `retentionDays` against a DGraph on v3; every cluster 404'd, 2026-07-27).
  The Schema page previously labelled the shipped `schema/VERSION` as the "Active Version" beside
  DGraph's real SDL, so it could caption a v8 graph "v9".

### Changed
- **BREAKING — `ORBITAL_APP_TOKEN_ALLOWED_APPIDS` empty now DENIES every app-only bearer token.**
  It previously skipped the allowlist check when empty, so any AAD app token bound to orbital's
  audience was accepted regardless of which application minted it — the same shape AWS eliminated
  from IAM/GitHub-Actions OIDC trust policies after it was found exploitable. The wildcard is now
  explicit: set `*` for the old permissive behaviour. The default is no longer orbital's own app id
  and is now empty, so **a deployment that authenticates any service with client credentials must
  list its application ids or publish will 401** on the legacy single-issuer path. It is inert where
  `ORBITAL_AUTH_PROVIDERS` is set (each entry's `clientID` is the gate), which is why
  `deploy/base/deploy.yaml` no longer sets it. User (non-app) tokens are unaffected.
- **Job tables gained a `status` index** (`export_jobs`, `backups`, `restore_jobs`). Every job
  trigger runs a conflict check filtering `status IN (pending, running)`, which previously scanned
  the whole table. Applied by the boot migration; additive, no data change.
- **`ORBITAL_OIDC_ISSUER_URL` and `ORBITAL_OIDC_CLIENT_ID` no longer have code defaults.** They
  shipped defaulted to Armada's AAD tenant and app id — public identifiers rather than secrets, but
  a default that silently points another organisation's deployment at our identity provider. Empty
  disables SSO cleanly (password login is unaffected), so nothing breaks at zero configuration.
  **A deployment relying on those defaults must now set both explicitly, or orbctl bearer tokens
  will 401** — `deploy/base/deploy.yaml` already sets them. For local SSO, copy
  `deploy/local/orbital.env.example` to `deploy/local/orbital.env`; `make run-orbital` sources it
  when present and runs normally when it is absent.
- **`orbId` is now unique across every ConfigItem type, enforced by DGraph** (`schema/VERSION` → `v8`).
  It was `@id`, which DGraph scopes *per implementing type* — a `Rack` and a `Server` could hold the
  same `orbId`, and only the `<kind>-` prefix convention kept them apart. A collision breaks reads
  (`getConfigItem` returns *"A list was returned, but GraphQL was expecting just one item"*) and makes
  one of the two nodes vanish silently from every diff, export preview and change-request base
  capture, because `internal/graphdiff` keys its snapshot by `orbId`. Now `@id(interface: true)`, and
  a second type reusing an `orbId` is refused.

  **The constraint is not retroactive** — applying the schema succeeds with duplicates already stored,
  without scanning or rejecting them. Audit an existing graph before applying it and after any bulk
  import; the query is in `docs/reference/DGRAPH.md` § orbId convention. Orbital does not apply the
  DGraph schema at startup, so this reaches a running cluster only when the schema is applied.

### Removed
- **`orb scan` is gone from the orb CLI.** It never scanned anything — it slept, printed a
  hardcoded "Found 3 BMC interfaces" and a fake progress bar, then reported "Scan complete".
  An operator running it against real hardware would have believed a discovery had happened.
  The feature remains post-MVP (`docs/reference/ORB.md` § orb scan); only the placeholder
  command is removed.

## [v0.0.41] - 2026-09-16

### Added
- **Orbital runs safely at any replica count.** Previously `deploy/base/deploy.yaml` carried
  `replicas: 1` with a written warning not to raise it, and nothing enforced that — a one-line
  kustomize patch deployed a broken configuration with no error. Every long-running job (export,
  backup, restore) is now *claimed* with a conditional `UPDATE` so exactly one runner executes it,
  proves liveness through a refreshed `heartbeat_at`, and is *fenced* on `locked_by` so a runner
  that stalls and resumes aborts instead of racing its replacement. Job admission runs under a
  transaction-scoped advisory lock, and a divergence report is claimed before it is applied rather
  than after — the previous order let two replicas both apply a report, which silently drops
  operator resolutions via the supersede branch. No new dependency, no worker tier, no leader to
  configure: coordination is PostgreSQL, which orbital already requires.
  See `deploy/README.md` § High availability and `docs/reference/OCI.md` § Job leases.
- `GET /api/v1/export/jobs/:id` now returns `lockedBy` and `heartbeatAt`, so an operator
  investigating a stuck or reaped job can tell which pod is running it.
- `ORBITAL_JOB_HEARTBEAT_INTERVAL` (`10s`), `ORBITAL_JOB_STALE_AFTER` (`60s`) and
  `ORBITAL_JOB_ORPHAN_GRACE` (`1h`) govern job liveness.
- **Schema migration is serialized across replicas.** Migration runs in every replica at boot, so
  replicas starting together raced the same DDL — and the failure is fatal. This was not
  theoretical: a **fresh install at `replicas: 2` crashlooped one pod on 4 of 4 attempts**, because
  an empty database means both pods try to create every table (`create "orbs" table: duplicate key
  value violates unique constraint "pg_type_typname_nsp_index"`). It is now taken under a
  PostgreSQL advisory lock — the same approach Rails, Flyway and Alembic use — with a bounded retry
  so a holder that died mid-migration produces a diagnosable error rather than a silent hang.
  `ORBITAL_MIGRATION_LOCK_TIMEOUT` (`5m`) bounds the wait.

- **Staleness is now two signals, and only the author can clear the one that matters.**
  `stale` means at least one change object's `version` no longer matches its node — a fact about
  what the *author* proposed. `subtreeChanged` means the reviewed scope moved without any change
  object going out of date, typically an edit to an owned child — a fact about what the *reviewer*
  looked at. Both block merge. Previously these were one flag, which let a reviewer clear, in one
  click, a proposal written against a value that had since moved: approving re-anchored the request,
  `stale` went false, and the merge wrote an outdated value over someone else's edit. The two facts
  have different remedies, so a single flag could only ever offer the wrong one to somebody.
  **Re-approving no longer clears `stale`** — the author rebases, by `PATCH`ing the changeset with
  the current `version` or dropping the object. A request can now be `approved` *and* `stale`, in
  which case `availableActions` drops `merge` and offers `edit` to the author. A rebase dismisses
  approvals **even when the graph has not moved**, because the reviewer approved a different
  proposal — the same rule as GitHub's "dismiss stale approvals on push", with the version vector
  standing in for the commit sha. An item sent without a `version` can never be item-stale and is
  guarded by the scope anchor alone, exactly as before.
- **The concurrency precondition is `version`, not `ifVersion`** *(breaking; renamed before any
  external client adopted it).* A precondition named after the node's own field is what Kubernetes
  (`resourceVersion`), Google Cloud (`etag`), Firestore, DynamoDB, Hibernate and plain SQL all do;
  `if`-prefixing is an HTTP **header** idiom (`If-Match`) that reads wrong on a body field. It could
  not collide with the `version` orbital stamps, because a changeset rejects `version` inside `set`
  outright and on `/graphql` the two sit at different nesting levels — the same separation
  Kubernetes relies on between `metadata` and `spec`. Applies everywhere the token appears:
  `/graphql` variables, change-request items, and `DELETE /api/v1/config-items/{type}/{id}?version=`.
  **A client still sending `ifVersion` is refused `400` naming the new field** rather than having its
  guarantee silently dropped. A mutation that declares its own `$version` is also refused — orbital
  adds the predicate itself, and a hand-written one gets no conflict detection.
- **Change-request preconditions are now one concept, not two — `before` was removed.**
  An item's optional precondition is `version`, the entity's `version` as you read it: the same
  token `/graphql` mutations accept, meaning the same thing. The field-level `before` a client used
  to send is gone. It existed because server-side version stamping was unreliable on one write path,
  and that is fixed — every writer now stamps — so it had become a second concurrency vocabulary for
  a question already answered. **Field-level protection is unchanged at merge**, where it comes from
  the ancestor orbital records itself (`base_values`) rather than from anything a client asserts:
  a merge still refuses per field, naming the field, and still drops already-satisfied fields from
  the write. **What this costs:** a creation-time refusal is now entity-grained, so a third party
  editing a *different* field of the same entity while you compose refuses your proposal where
  `before` would have accepted it — reload and propose again. It also retires the whole-value
  false-conflict on composite JSON fields that `before` could not express. Orbital's editor sends
  `version` per item on the propose path; a create sends none, since there is no version to match.
- **A stale change request now says which entity moved.**
  `base_hash` anchors a request to the version vector of its scope, so it has always refused a
  merge whose intent moved — but it is a fingerprint, so it could only ever say "something
  changed", leaving an operator to go and find out what. Requests now also store the vector that
  hash is computed from, and a stale merge returns `409 MVCC_CONFLICT` with a `problems[]` entry
  per moved entity carrying its orbId and both versions. This applies to every request, including
  ones whose author sent no precondition, and the error still wraps the same sentinel so the
  status, code and existing clients are unchanged. The vector is re-captured wherever the hash is
  — create, amend, the rebase on approval, the rebase after a partial merge — so re-reviewing a
  stale request still clears it in one click. A merge also now carries the version it planned
  against into each write, so an edit landing between planning and the write refuses that item
  rather than overwriting it.
- **A change-request item can carry `version` — the same concurrency token `/graphql` accepts.**
  `POST /api/v1/change-requests` items take an optional `version`: the entity's `version` as you
  read it. If the entity has moved since, the request is refused with `409 MVCC_CONFLICT` naming
  the item and both versions, instead of being accepted and only failing at merge — where
  `base_hash` refuses wholesale and can never say which entity moved. It is entity-level where
  the existing `before` is field-level, and the two stay distinguishable: an entity-level problem
  carries no `field`. Honoured on `op: delete`, which `before` skips by construction — a delete is
  where a stale precondition costs most, since it destroys work with no diff to recover it from.
  One per item, never per field. Omission stays legal and leaves the item guarded by the scope
  anchor alone. Supplying it against an entity that does not exist is refused at validation rather
  than ignored — a caller that asked for a check and silently did not get one is worse off than
  one that never asked. Duplicate orbIds in a changeset were already refused for ordering reasons;
  the message now also gives the second reason, which is that two preconditions on one entity
  cannot both hold.
- **Change Requests — maker-checker approval for config mutations (Spike 36, backend).**
  `POST /api/v1/change-requests` proposes a set of ConfigItem changes as target end-state;
  a peer other than the author approves it; `POST .../merge` applies it MVCC-guarded.
  `GET .../diff` returns a flat, render-ready diff of current intent vs the proposal.
  Protected classes are declared via `/api/v1/approval-policies` — **one policy per
  namespace**, covering either every type or a named list — and are **opt-in**: with no
  policy, nothing changes. Enforcement is per policy; there is no global switch.
  Notable properties: a changeset is validated against the *deployed* schema at creation, so a
  proposal that could never apply never reaches a reviewer; staleness and approval validity are
  derived on every read rather than stored, so no hook can miss a write; a partially applied
  merge stays open with a recorded per-item attempt and is safe to retry without re-approval
  unless someone else wrote to a covered entity.
  **Enforcement is live**: a mutation on a protected class is refused with
  `403 APPROVAL_REQUIRED` and a hint pointing at the change-request endpoint, unless the
  caller's role is in the policy's `bypass_roles` — in which case the write goes through
  and is logged as a *privileged write*. The check sits in the single function every
  DGraph write passes through, so it covers `/graphql` and internal dispatch alike;
  merging an already-approved change request is the one exemption. A divergence
  **Accept** on a protected class now returns `202 Accepted` with a `changeRequestId`
  and leaves the entry pending instead of mutating intent.
  `GET /api/v1/change-requests?status=` is **repeatable and OR-ed** (`status=merged&status=rejected&status=closed`);
  an unrecognised value is refused with `400` rather than silently matching everything.
  Every change-request response carries an **`effect`** — how many entities and fields it
  changes, and the field with its before/after when there is exactly one — so a client can
  render a queue row without walking the changeset. It reports what the request would DO, not
  what it says:
  the delta is computed once at creation against the same snapshot the staleness anchor comes
  from, so a client that posts a complete end-state (a reconcile flow) still gets `1 field`
  when one field differs.
  **A proposal now carries only the fields the author actually changed.** The editor previously
  sent an entity's whole scalar payload, so a one-field edit opened a request claiming six
  fields: the diff was right, every count derived from the payload was not, and merging would
  have written all six.
  **UI**: a *Change Control* section with the review queue and an admin policies page;
  a review view showing the diff, the approvals (including "approved an earlier version"
  when one no longer counts) and only the actions the caller may actually take; and the
  config editor relabels **Save → Propose change** on a protected class, opening a change
  request instead of writing, and leaving you on the entity — where a banner names the
  request and links to it. A caller whose role may bypass the policy gets **both** actions —
  *Propose change* as the primary and *Save directly* beside it, so holding the bypass role no
  longer means losing access to review. An entity with a change already
  in flight says so before you start editing it, including when the change targets an owned
  child such as its iDRAC or maintenance window — named by request id, which never goes stale.
  Field marks now cover a server's own fields on the Server Summary table, not only its
  maintenance panel, and sit in a column of their own.
  **A detail tab whose panel holds a proposed field now carries a dot**, so a change to a
  server's iDRAC or maintenance window is visible without clicking through every panel —
  red when two requests inside disagree. A proposal that already matches current state
  does not light it.
  The review queue shows three tabs — *Needs my review* (hidden for roles that cannot approve),
  *Open*, *Closed* — and lists each request by **title**, with a narrow column giving the change's
  size (`1 field`, `12 fields`) beside it.
  The review view follows the server detail page's shape: the title, then a row of rounded
  actions, then the diff in a wide panel with a Details summary beside it and one Activity panel
  below, with status rendered the same way as on the queue. Its activity is one chronological
  table rather than separate reviews and merge-attempt lists. The queue remembers which tab you
  last opened.
  **The proposer now writes the title.** The propose footer carries a title input, prefilled with
  the entity name and replaceable in one gesture; leaving it alone or clearing it stores that same
  entity name. The generated title no longer appends a field count — the queue's `Change` column
  states that, and requests created before this carry the old `<entity> · N fields` form.
  Titles are capped at 255 characters — the length the `title` column always enforced, which until
  now surfaced as a 500 instead of a 400 — and an open request can be **renamed in place** from
  the review page, which patches the title alone and
  leaves the changeset, the staleness anchor and every approval untouched.
  **A review view now warns when another active request proposes the same field**, naming and
  linking each competing request and the fields it overlaps on, and distinguishing a competitor
  proposing a *different* value — whichever merges first wins the field, and the other is refused
  until re-reviewed — from one proposing the *same* value, where one of them merges as a no-op.
  Until now the collision was invisible until a merge, at which point the other request silently
  went stale and its approvals stopped counting.
- **Config-item edits are guarded against concurrent writes again.** The editor sends
  `version`, so saving a form someone else has changed underneath you is refused with
  `409` and a reload prompt instead of silently overwriting their edit. This had been lost
  since 2026-06-20, when the per-page edit modals — each of which passed `version` — were
  replaced by the shared editor module, which did not carry it forward. Nothing failed in
  between: the check is opt-in server-side, so a client that omits it looks exactly like one
  that declined it. Covers a data center, cluster, network device, server and its iDRAC and
  maintenance settings.

- **Approval policies can govern every namespace.** A policy is now created with either a
  namespace or `allNamespaces: true`; the second covers every namespace, **including data
  centers onboarded after the policy was written**. Resolution is **fallback**: a namespace
  with its own policy uses that one instead, even when it is weaker, and a namespace whose
  policy is **disabled** is not gated at all. So an all-namespaces policy is a **default,
  not a floor**. Exactly one policy still governs any write, so "which policy did this?"
  keeps a single answer. The policies page marks a namespace row that is overriding the
  global one.
- **Artifact-to-artifact compare** — `GET /api/v1/export/compare?from=&to=` returns the
  desired-state delta between any two published artifacts of a data center, pulled by immutable
  digest and diffed in memory. Surfaced as a **Compare** tab on Publish History with linkable
  `?from=&to=` URLs.
- **Export preview + guarded apply** — `POST /api/v1/export/preview` returns the content diff
  between current intent and the last published artifact; `expectedContentHash` on
  `POST /api/v1/export` rejects with `409 MVCC_CONFLICT` when intent moved since the preview.
- **Filters on `GET /api/v1/oci/artifacts`** — `dc` (data-center orbId), `status`, `limit`.
  Without them the endpoint was capped at 100 rows across all data centers, so a busy data center
  pushed others out of the response entirely.
- **Divergence badge** in the navigation, showing unresolved divergence entries.
- **`GET /api/v1/proposed-changes?orbId=…`** — what is proposed for a set of entities, keyed by
  orbId and indexed by field, so a client can overlay proposals onto entities read from
  `/graphql` with a map lookup rather than inverting the change-request list itself. Reads
  PostgreSQL only, so it is safe on every page load. Powers per-field marks on the server
  detail page: a field with a change in flight names the proposed value, its author and a link
  to the review; several proposals show a count, and ones that disagree are called out.
- **Change requests are identified as `<namespace>-<number>`** (`colo-42`) instead of a UUID —
  per-namespace numbering, so each data center counts from 1 and an id says which site it is
  about. Accepted and returned everywhere, including URLs. The queue lists the id first, titles
  each request by what it changes (`server-CWJHDX3 · maintenance.enabled → true`) rather than
  which entity it touches, and shows relative age.
- **Approval-policy administration is audited** — create, update and delete write `management`
  audit events carrying before/after and an explicit flag when enforcement stopped. Previously a
  write that *bypassed* a policy was recorded while changing the policy itself left no trace.
- **`?orbId=` is repeatable on `/api/v1/change-requests` and `/api/v1/audit-log`** (max 128),
  matching requests or events touching any of them — so a view can ask about an entity and
  everything it owns in one call.
- **`ORBITAL_CHANGE_CONTROL_ENABLED`** removes the change-control feature entirely — queue,
  policies page, endpoints and nav — for adopters running their own change management. Deletes
  no data; see `docs/reference/CONFIG.md`.
- **`ServerMaintenance` config item** (schema v6) — maintenance-window flag per server.
- **Request payload-size guard and rate limiting** on the API surface.
- **Audit events record where a write came from.** Every event now carries `eventSource`
  (`graphql`, `rest`, or `internal` for work done by a background job), `sourceIpAddress`, and
  `requestId` — all three exposed through `GET /api/v1/audit-log` and indexed, so "what else
  happened in that request" and "what came from that address" are answerable without scanning.
  All three are **omitted rather than blank** when there is no HTTP request behind the event: a
  scheduled backup or an internally dispatched write reports `internal` and no address, because an
  empty string in a column an operator filters on reads as a recorded fact rather than as "not
  applicable". Existing events keep rendering with the fields absent; no backfill.
  `userAgent` is deliberately NOT added — it stays on session-boundary events
  (`loginSuccess`/`loginFailed`/`logout`) where orbital already records it.


- **`limit` and `offset` on `GET /api/v1/change-requests`.** `total` always counts every match,
  never the page. Both are opt-in: a request with neither returns exactly what it did before.
- **The change-request queue is now a sortable, searchable, paged table** like every other list in
  the UI — 25 rows a page, a page-length menu and a search box. It fetches the 200 most recent
  matches and **says so** when more matched ("Showing the 200 most recent of 531"), rather than
  showing a prefix that looks like the whole list.

### Changed
- **A job interrupted by a restart is now failed by a reaper on a ticker, not at the next boot.**
  The startup sweep failed *every* pending or running job on the assumption that a process starting
  meant none could be alive — true at one replica, destructive at two, where a second pod booting
  would mark the first pod's in-flight restore failed while it was still executing `drop_all`. The
  visible difference at `replicas: 1` is that an interrupted job is marked failed a minute or so
  later rather than instantly, and its error now names the runner that died.
- Rate limiting and the database connection pool are **per pod**, so their effective values scale
  with replica count. Divide `ORBITAL_RATE_LIMIT_RPS` by the expected number of replicas; see
  `docs/reference/CONFIG.md` § Multi-replica notes.
- **A config-item edit no longer half-saves.** Saving a server (or cluster, or data center) can touch
  several entities at once, and every mutation was dispatched in parallel — so if one failed, the
  others had already been written while the message read "Conflict — please reload and try again".
  A save now checks every entity for concurrent edits **before writing anything** and refuses the
  whole save if any moved, naming them; the remaining writes go one at a time, so a failure stops
  the rest. If something does fail partway, the message says which entities were saved. The
  approval check also now covers every type in the edit tree rather than the top-level one, which
  removes a case where a policy on a child refused *after* the parent had been written.


- **The change-request queue no longer computes staleness, and is ~130x faster.** Listing requests
  derived each row's staleness from the graph — 1,222 DGraph queries and 1.21s for one unfiltered
  page of 507 requests, scaling linearly with the queue. The list now answers from PostgreSQL
  alone: **0 DGraph queries, 0.009s.** `stale`, `subtreeChanged`, `staleEntities` and
  `missingTargets` are **omitted from list items** — absent rather than `false`, because the
  endpoint no longer asks the question; read a missing `stale` as "open the request to find out".
  `GET /api/v1/change-requests/{id}` is unchanged and still reports all four. This follows GitHub,
  where `mergeable` is returned by the single-PR endpoint and never by the list. One accepted
  divergence: a list item's `approvals` counts decisions cast against the current changeset
  revision, while the detail view additionally requires the scope hash to match.


- **`GET /api/v1/change-requests` filters `orbId` and `namespace` in SQL** (jsonb
  containment, GIN-indexed) instead of after rendering each row. Rendering derives
  staleness, so the old order paid several DGraph round-trips per row before discarding
  it — fine for a queue page, ruinous for the pending-change lookup that now runs on
  every detail view. A lookup matching nothing costs zero DGraph calls.
- **New filter value `status=active`** — not-terminal (open plus approved). Neither
  existing value answers "does this entity have a change in flight", because `approved`
  is derived and `status=open` excludes it.
- **Divergence Accept no longer needs the report to carry a type.** An entry whose
  `type_name` is empty used to fail with "update intent manually"; orbital now resolves
  the type from the entity's `orbId` (`@id` on the `ConfigItem` interface, so globally
  unique). The remaining failure — an `orbId` orbital has never seen — reports that
  instead, since it is a different problem with a different remedy.
- **Audit tables renamed** `events` → `audit_events` (with `audit_event_resources`,
  `audit_event_resource_types`); ent types and handler renamed to match. The API path
  `/api/v1/audit-log` is unchanged. Rationale in `docs/reference/AUDIT.md`.
- **Audit retention is now operator-configured** rather than an assumed 12 months. Orbital ships no
  prescribed period.
- **`ROADMAP.md` reduced to a one-page status view**; spike definitions moved to
  `docs/planning/backlog.md` and technical debt to `docs/planning/debt.md`.

### Fixed
- **A cascade delete can no longer destroy a record that changed while it was being planned.**
  Deleting a data center, server or cluster collects the whole owned subtree and then removes it;
  the removal carried no concurrency check at all, and the `?version=` precondition covers only the
  top-level entity, check-then-act, with planning and the approval check inside the window. An edit
  to any child in that window was destroyed silently — `DELETE`, the irreversible operation, had a
  weaker guarantee than `update`, which returns `409` in the same situation. The delete is now a
  compare-and-swap over the entire planned set: if anything moved, **nothing** is deleted and the
  `409 MVCC_CONFLICT` names which records changed. Unchanged when nothing has moved.


- **A governed namespace no longer locks out clients that name their orbId variable something
  other than `orbId`.** The approval gate resolves the governing policy from the orbIds a mutation
  names, and it could only see them as literals or behind a variable called exactly `orbId`. Any
  other spelling — `$clusterOrbId`, or a compound mutation, which *cannot* have two variables of
  one name — resolved to no namespace and was refused `400 VARIABLE_FORM_REQUIRED`, telling the
  caller to use the variable form they were already using. Three shapes now resolve:
  `orbId: { eq: $anyName }`, `orbId: { in: [...] }` with literals and variables mixed, and a whole
  filter object behind `filter: $anyName`. Resolution descends only through the `orbId` key, so a
  filter on another field is never mined for a resource id; and it only ever *adds* orbIds, so the
  gate can become stricter but never laxer — the fail-closed default is unchanged. The same lookup
  feeds the audit log, so these mutations now record their `resource_ids` and are findable through
  `GET /api/v1/audit-log?resource_id=` instead of landing with an empty one.
  **`update` mutations are covered too.** The write pre-flight resolved its target by variable name
  to bump `version` and stamp the audit before-state, so a renamed-variable update was refused
  before the gate ever saw it — correctly, since it could not otherwise have been stamped. It now
  resolves the selector and the patch by reference as well, and a renamed update is stamped,
  version-guarded and diffed identically to the `$orbId`/`$set` spelling. A shape that still cannot
  be pinned to exactly one row — two orbIds, an `in:` list, an inline literal, or a filter behind a
  variable — is refused exactly as before: resolution can turn a refusal into a fully stamped
  write, never into an unstamped one.
- **The audit log no longer renders an empty diff when the patch variable is not called `set`.**
  The field differ read after-values from `variables["set"]` and otherwise fell back to the whole
  variables map, which for `set: $patch` intersects `{orbId, patch}` with the before-state, matches
  nothing, and renders an audit row with **no changes at all** — while the write itself lands
  normally, with no error anywhere. The differ and the stamper now resolve the patch variable
  through one helper, so they cannot disagree about which map holds the new values. Audit events
  persist the mutation query alongside its variables, so historical rows render correctly too.
- **A merged change request no longer tells you to re-approve it.** Merging bumps the version
  vector by definition, so the scope-moved signal fired on every terminal request and the detail
  view rendered "Changed since review. Re-approve to merge." above a request already applied. The
  terminal branch cleared the other two staleness flags and not this one.
- **A multi-entity change request now lists what it did.** The record on a merged request rendered
  one aggregate row — "2 entities / 2 fields" — because it was built from the queue-row summary,
  which carries an orbId only when there is exactly one. The response now carries `record`: one
  entry per change object with its fields, the value each had when last reviewed, and whether it
  applied. Derived from the stored changeset, so every request already in the database renders
  correctly with no backfill.
- **The review table's Note column rendered raw markup** (`&lt;span class="is-family-monospace"&gt;`)
  inside the conflict explanation — the value formatter returns escaped HTML and the whole sentence
  was being escaped a second time around it.
- **Concurrent writes to one entity could be silently lost even when the caller asked for a
  concurrency check.** `version` was compared in Go against a snapshot fetched by a separate
  request, then the mutation was written with a filter on `orbId` alone — two DGraph transactions,
  so a writer committing in between was overwritten with no trace. Reproduced in the test suite:
  12 concurrent writers all reported success against the same starting version, and 11 writes
  vanished while `version` advanced once. Orbital now injects `version: { eq: $version }` into the
  mutation's own filter, so the comparison happens inside the write; the loser gets
  `409 MVCC_CONFLICT` instead of a success it did not get. Requires `schema/VERSION` **v7**, which
  adds `@search` to `ConfigItem.version` — **apply the schema before rolling out the code**; the
  wrong order refuses mutations with a clear message rather than writing anything. A mutation whose
  shape cannot carry the predicate is now refused rather than sent unguarded, which also means an
  `version` sent with a create is rejected instead of quietly dropped. Change-request merge
  inherits the guard. Repeated transaction aborts — more likely now that writers contend on the same
  predicate — are retried, and a persistent one surfaces as `503 WRITE_CONTENTION`, deliberately
  distinct from `MVCC_CONFLICT`: the request is still valid, the store was busy.
  The same change closes a second hole: a supplied `version` that orbital could not evaluate —
  a failed pre-read, or a mutation touching more than one type — used to be dropped and the write
  reported success. It is now either enforced by the database or refused, so there is no path where
  a caller asks for a concurrency check and silently does not get one.
- **The cascade-delete endpoint bypassed approval policies entirely.**
  `DELETE /api/v1/config-items/{type}/{id}` planned its cascade and wrote to DGraph directly, so it
  never passed through the function where the approval check lives. Measured on the same entity,
  same policy and same caller seconds apart: `updateDataCenter` was refused `403 APPROVAL_REQUIRED`
  while the `DELETE` returned `200` and removed it. The delete now asks the same policy question,
  with the same status, code and hint the GraphQL path returns — and it asks it about every type the
  cascade would remove, read from the planned nodes rather than declared per branch, so a policy
  protecting only an owned child refuses the parent delete that would take it. The same endpoint
  also accepts `?version=`, so a delete confirmed from a dialog that has been sitting open is
  refused `409` rather than landing on whatever the entity has since become; the delete modal sends
  the version it displayed and keeps itself open on a conflict. Omitting it stays unconditional, and
  a namespace with no policy is unaffected.
- **A divergence Accept wrote intent without bumping `version`, so it was invisible to change-request staleness.**
  `base_hash` is a hash of the scope's `orbId@version` vector — a write that does not move a
  version does not move the hash. An open change request covering the accepted entity kept
  reporting `stale: false`, an approval cast *before* the Accept kept counting, and the merge
  proceeded against a state nobody had reviewed. The Accept was in the audit log throughout, so
  nothing looked wrong; it was found only by asking why an approved request still looked fresh.
  Two causes, both fixed. The **write pre-flight** — the before-fetch, the
  `version`/`updatedAt`/`updatedBy` stamping and the opt-in `version` check — lived in the
  `/graphql` handler, which internal dispatchers do not go through, so each was left to the
  caller and both callers forgot a different one (the change-request merge had passed an
  incomplete before-state, rendering raw variables where a field diff belongs). All three now
  live in `writeToDGraph`, the single function every write already passes through for the
  approval-policy check, so no caller opts in and none can forget. And Accept dispatched its
  mutation with a `$filter` object, while the row to stamp is resolved from the `orbId`
  variable — it now uses the canonical `update{Kind}($orbId, $set)` shape every other writer
  uses. An Accept consequently bumps the version, stamps the acting identity on the node, and
  carries a field-level diff in the audit log; Reject and Ignore still touch nothing.
  A caller-supplied before-state is now a *fallback* merged under the fetch rather than a
  replacement for it, which keeps the diff for fields outside a type's `BeforeFields` selection.
- **A change request could overwrite an edit it never reviewed, and could merge as a no-op.**
  Staleness was entity-level: `base_hash` fingerprints a version vector, so it answered "did
  anything in scope move" but never "what was it", and a write that changed a value without
  bumping `version` — a direct DQL write, a restore, `make seed` — left it matching. A changeset
  item can now carry **`before`**, the values the caller read, and orbital records the ancestor
  (`base_values`) for the fields the changeset touches. Every field then resolves to *applies*,
  *already satisfied*, or *conflicts*: a conflict refuses the whole merge before anything is
  applied, naming the field; an already-satisfied field is dropped from the write, so merging a
  request someone else beat you to costs no version bump and writes no audit row for a change that
  changed nothing. The write is narrowed to match the guard — a field-level check paired with a
  whole-`set` write would push stale values over other people's edits. Orbital's own editor now
  sends `before` for the scalars it changes, so an edit landing while someone has a modal open is
  refused at creation and named rather than silently absorbed. A refused action renders the
  per-field detail it already receives — a merge conflict now says which field moved, from what,
  to what, instead of "state moved since you read it".
- **A merged change request logged no field diff in the audit trail.** Merge read only `version`
  plus the fields it was about to clear, so the audit event's `before` never contained the fields
  being *set* — the diff had nothing to intersect and the row fell back to dumping raw mutation
  variables, while the same edit saved directly through `/graphql` showed `-false / +true`. Merge
  now reads the scalar fields it writes, so a merged change and a direct save produce the same
  audit row.
- **The Config Item inventory went permanently empty once a namespace filter was saved.** With
  `stateSave: true`, DataTables restores saved state *during* the constructor, so `initComplete`
  ran before `const inventoryTable` had bound and threw a TDZ `ReferenceError` out of the
  constructor — taking the data fetch with it. Only reproduced once both a saved DataTables state
  and a saved namespace filter existed, which is why a fresh profile looked fine. `initComplete`
  now uses `this.api()` rather than the outer binding.
- **The Config Item inventory could also show "No data available in table" indefinitely.** A page loaded
  while the graph was empty — between `make up` and `make seed`, or before a restore finished —
  cached `[]` in `sessionStorage`, and because `"[]"` is a truthy string every later load treated
  it as a cache hit and skipped the fetch. Servers and Data Centers looked fine because they always
  fetch. An empty cached list is now a cache miss.
- **`/api/v1/audit-log` silently truncated an over-cap `orbId` filter**, so a resource panel whose
  subtree exceeded the limit dropped its overflow children with no signal — a Server audit tab
  looked complete and was not. Both filters now refuse over the cap instead of narrowing the
  answer, and the cap is sized above a real subtree (measured at 35 orbIds for a populated
  server; it was 32).
- **JSON-editor field clearing** — clearing a field is now a DGraph `remove`; `set: null` was a
  silent no-op, so cleared fields never persisted.
- **Handler integration suite** — 10 failures and an intermittent flake. Tests called handlers
  directly, bypassing Echo's error handler, so the JSON error envelope they asserted on was never
  rendered.
- **Stale end-to-end test fixtures** — specs referenced pre-migration server orbIds and failed
  after the `server-<serial>` convention landed.
- **Removed a dead restore-availability flag** and documentation for an `ORBITAL_RESTORE_BACKEND`
  environment variable that does not exist.
