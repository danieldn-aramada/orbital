# UI Reference

Read this before: Go template changes, HTMX interactions, JavaScript, CSS/SCSS, any frontend work.

## Settled Decisions

### Detail tabs (list pages)

- **Closing a tab lands on its LEFT neighbour; closing a background tab moves nothing.** *(2026-10-05.)* The leftmost tab's neighbour is Summary, so no special case. ⚠️ `replaceCurrentTab` must get the same neighbour, or the next arrival restores a removed tab.
- **The active tab is stored per slug (`genericTabCurrent:<slug>`) and restored on return.** *(2026-10-05.)* Read it through `getCurrentTab()`, never a literal key. `clearTabStateOnFresh` sweeps these by prefix. ⚠️ A restore test must assert ONE fetch, not zero — zero also passes for a restore that does nothing.
- **Tab restoration is LAZY: rebuild the strip, fetch on first selection.** *(2026-10-05.)* `loadGenericTab(..., activate = false)`. Set `dataset.loaded` only after the swap resolves (with a separate `loading` guard), or a dropped request leaves a tab blank forever.
- **Five tabs max, evict least-recently-USED, show a toast — never refuse.** *(2026-10-05.)* `MAX_GENERIC_TABS = 5` in `shared.js`, deliberately not configurable. Safe because a tab holds no unsaved state.
- **The edit modal lives INSIDE `generic-detail-content`**, which is served both as a page and as the tab fragment. *(2026-10-05.)* ⚠️ Any swap must `genericEditors.delete(id)` first, or Edit drives an editor bound to a detached node. ⚠️ Post-save reload is tab-aware, like the Reload button.
- **`displayTabContent` touches only panels that HAVE an id.** *(2026-10-05.)* `.tab-content` is also the layout wrapper; hiding an id-less one is permanent. ⚠️ Tests must assert the PANEL is visible, not just the strip.
- **A ConfigItem is shown only as a tab. `/{slug}/{orbId}` is the tab's fetch URL and 404s for a human.** *(2026-10-05.)* Rows link via `/{slug}?open={orbId}`; nothing links to the detail path. Every `Detail` render is a fragment, error paths included. `?open=` is stripped once honoured — no copyable per-entity link, by design.
- **Double-click opens a row; single click is DataTables row selection** (used by Copy/Excel/CSV export). *(2026-10-05.)* ⚠️ Before binding a gesture on a DataTable, check `select` — the conflict is silent.
- **A list row's name is plain text; the row carries `data-orb-id`.** *(2026-10-05.)* Blue in a table means "you leave this page". Reference cells still link; references to pageless types are text.
- **Detail action row sits at the top of the shared body; Edit is `is-rounded is-link`.** *(2026-10-05.)* Order: action row → pending-changes banner → Fields.
- **One Reload button resolves page-vs-tab at click time** (inside a `.tab-content[id^="tab-content-generic-"]` ⇒ re-fetch that panel, else full reload). *(2026-10-05.)* Not a server-rendered flag. ⚠️ A full reload from inside a tab silently closes every other tab.
- **Detail layout: Fields left, Metadata right, everything else in ONE tab strip.** *(2026-10-03.)*
- **The audit log is the LAST tab and loads on click.** *(2026-10-03.)* `initDetailTabs` finds it as the `<li>` with `data-orb-id`.
- **A tab whose panel holds a pending change shows a dot.** *(2026-10-03.)* `panelOrbIds` derives the set from the panel's `[data-field-orbid]` elements unless `data-panel-orbids` is given; no extra request.
- **A tab panel is not a box** — the tabbed `article.box` around it already is. *(2026-10-03.)*
- **The tab strip wraps when tabs do not fit; it never scrolls sideways.** *(2026-10-07.)* Bulma's nowrap+scroll clipped the last tab with no affordance. Wrapping breaks the `is-boxed` underline visually; an overflow affordance is the scalable fix.

### Views config (`config/views.yaml`)

- **Two sections: `pages:` (URL + menu entry + layout) and `types:` (how a type renders wherever it renders).** *(2026-10-04.)* Nesting them would duplicate a type's display config on every page that shows it. `types:` inherits along interfaces (`ConfigItem.orbId` label applies to all).
- **A page and a menu entry are one fact** — `v.Slug != ""`. *(2026-10-04.)* `menuWeight:` orders the menu, never decides membership.
- **A row links if and only if its type has a page.** *(2026-10-04.)* Templates branch on empty `Slug` in three places (summary rows, tab rows, list reference columns). Promoting a type to a page is a one-line `pages:` entry.
- **A type with no view entry still resolves and renders as rows**; it just has no URL or menu entry. *(2026-10-04.)*
- **The schema keeps only data annotations (`jsonString`, `orbIdSuffix:`); everything about a page lives in views.yaml.** *(2026-10-04.)* DGraph rejects unknown directives, so view config in docstrings was a smuggle.
- **A page DECLARES its subgraph (`pages.<T>.subgraph:`): what it shows, edits, audits and deletes.** *(Settled 2026-10-07.)* Nothing is derived from the schema; `!` means non-null, not ownership. Unlisted = not in it, including intermediate hops.
- **Delete is literal: the root plus its declared paths, never another page's.** Survivors' edges are cleared (from `@hasInverse`); a survivor left with a broken non-null edge is shown as **orphaned**, and the delete proceeds.
- **Delete lives on the page URL: `DELETE /{slug}/{orbId}`, preview at `.../delete-preview`.** UI-only, not `/api/v1`, not in swagger; API clients delete via `/graphql`. Auth is applied explicitly in `server.go` — the root group has none.
- **Scalars and link rows are subtractive; the subgraph is listed.** Single relationships outside the subgraph show as link rows; `summary.ignoreFields` hides either. `tabs:`, `summary.refs:` and `ownerReferences:` are retired and warn if present.
- **`editable:` = "this page's editor writes it".** Not on lists, not beyond two hops. Create links through the field's `@hasInverse`.
- **Single subgraph members render inline (even when absent); list members are tabs.** One-hop-down singles nest inside their box (`backup` → `backup.etcd`).
- **A multi-hop subgraph path renders the far type as one table** (`storageControllers.storageDevices`). The query is built from a path tree, so dotted names never reach GraphQL. Each relationship is selected exactly once, with the union of what the page needs.
- **Column paths must be single-hop all the way; subgraph paths may cross lists, at any depth.** One produces a cell, the other rows. Don't harmonise.
- **`tableHidden` is type-wide**: it hides a field from every table (list page and relationship tabs), never from the detail summary. *(2026-10-05; renamed from `detailOnly` 2026-10-06.)* Not `summaryHidden`. Where pages would disagree, keep the column. ⚠️ An interface list page unions implementations' `ColumnFields()`, never `Display`.
- **Placement is declared (`tableHidden`), never inferred from content.** *(2026-09-29.)* `jsonString` says only that a String holds JSON — it drives parsing and pretty-printing, not placement.
- **Field editability is one key, `fields.<name>.editable`, in both directions.** *(2026-10-04.)* Default: the type's own scalars, minus ConfigItem's. Editor-scoped, not API-scoped. A key naming a missing field is inert.
- **Computed columns are declared paths (`columns: [servers.count]`) on the type, and apply wherever it renders.** *(2026-10-02.)* `count` is fetched as `<field>Aggregate { count }`; an interface hop needs `... on ConfigItem`; the label comes from the type the leaf field belongs to.
- **Column/field order is a partial pin: `order: [a, b, c]`, rest alphabetical.** *(2026-09-26.)* Never a complete list — new fields would never appear. Interfaces' pins are inherited; list pages re-apply the pin after the interface union. A pinned field the type lacks is skipped.
- **One label mechanism: `fields.<name>.label`.** *(2026-09-29.)* Labels are title-cased field names; declare only what title-casing gets wrong (acronyms). Resolved server-side onto `View.Labels`. No second override map.
- **A view's label derives from its slug** (`slug: clusters` renames page and nav together). *(2026-09-26.)* Never hand-write a ConfigItem page into the menu.
- **`filterBy:` is declared, one field per page, never inferred.** *(2026-09-29.)* Resolved server-side. A `filterBy:` naming a non-column is dropped and logged; "column" is `View.ColumnFields()`.
- **Two layers, both files: shipped `config/views.yaml` + a PARTIAL overlay (`ORBITAL_VIEWS_OVERLAY_PATH`) replacing per view.** *(2026-10-04.)* A whole-file copy freezes that deployment. ⚠️ Mount the ConfigMap DIRECTORY, not a `subPath`, or changes are not picked up.
- **Views are not editable in the product and not audited by orbital** — git and Kubernetes RBAC govern the ConfigMap. *(2026-10-04.)*
- **The views config is validated against live introspection: never fatal, never silent.** *(2026-10-04.)* Bad declarations are dropped or refused and logged. `schemaVersion` is a coarse hint only.
- **A save whose views hash moved since the editor opened is refused (409).** *(2026-10-04.)* The editor sends `X-Orbital-Views`; the proxy compares against the RE-CHECKED hash. Requests without the header (API clients) proceed.
- **`/views` and `GET /api/v1/views` are deleted.** *(2026-10-05.)* The boot log's `resolved views …` line lists the resolved slugs. If AEP needs view metadata, add an endpoint shaped for AEP.
- **A view may be backed by a GraphQL INTERFACE; it lists every implementation.** *(2026-09-25.)* There is no `get<Interface>`, so the detail route resolves the concrete type via `queryConfigItem { __typename }`. Select identity with `idNameSel` (`... on ConfigItem`) for any possibly-interface target. The implementation is demoted out of the nav — never solve this with a slug alias.

### Detail page content

- **Detail layout: link rows for single relationships outside the subgraph; boxes for single members; tables for list members.**
- **Every detail page has a metadata box (`View.Meta`, the ConfigItem interface fields) and an audit tab.** *(2026-09-25.)* The audit tab's orbIds are `data-subgraph-orb-ids`.
- **A relationship table drops the column pointing back at the page's entity** — only when the type matches AND every row points back. *(2026-09-26.)* A row missing the reference keeps the column; merely-empty columns are never dropped.
- **Proposed-change marks are driven by DOM attributes** (`data-field-orbid`, `data-field-values` with RAW values, `data-field` + `.js-field-mark`; banner `[data-pending-changes-for]`). *(2026-09-26.)* Only editable rows get a slot. A member's table carries its OWN orbId.
- **In a relationship table, marks key on the ROW** — each row is its own entity. *(2026-10-02.)* The editable set is a `map[string]bool` so the shared template can `index` it (no FuncMap).
- **A `jsonString` field is parsed server-side into the editor tree** (`editableSubtree`); unparseable values pass through raw. *(2026-10-02.)*
- **Do NOT bolt `data-field` rows onto a list table to reuse the field-mark renderer.** Lists need their own mark shape.

### List pages

- **A list table is a DataTable, fed by a server-capped fetch and paged client-side.** *(2026-09-15.)* Copy the closest existing config. Do NOT add a `<colgroup>` to a DataTable. `scrollX` only for tables that overflow.
- **A capped list says so, with the real total from the SAME request** (`aggregate<T> { count }` alongside the rows). *(2026-10-02.)* Show the notice only when `rows >= limit && total > rows`; a zero count shows nothing.
- **The truncation notice states the limitation** — search and filter cover only loaded rows — and names the setting that raises the cap. *(2026-10-02.)* Never suggest something the page cannot do.
- **Generic table state is saved per slug (`dt:<slug>`), not per table id.** *(2026-09-29.)* The filter dropdown persists with it for free.
- **DataTables filter/sort/type data is the VISIBLE TEXT, not cell markup.** *(2026-09-29.)* `render` returns raw markup only for `display`.
- **Sort on the raw value, render the display form** (`type === 'display' ? pretty(v) : v`). *(2026-09-15.)*
- **Overriding a DataTables style: read its CSS first and scope to `#generic-table_wrapper`**, never `#generic-table` (`scrollX` clones the header). *(2026-10-05.)* `dt-type-numeric` both right-aligns cells and reverses the header; fix both. `columnDefs className: 'dt-left'` does not work.
- **Table toolbar sizing is one global `div.dt-container` rule.** *(2026-09-28.)* Opt a table out by id below it; never maintain an id list.
- **A list page keeps its tabs bar under the heading** — it is the mount point for detail tabs, and `#tab-summary` is how closing returns to the list. *(2026-10-02.)*
- **Inventory namespace filter is page-level** (regex on orbId), not a DataTable column filter.

### Editing

- **One JSON editor at the parent; no per-child Configure/Edit/Delete UI.** The page exports edit targets (`configitems.BuildEditTargets`); `configitem-editor.js` snapshots, diffs and dispatches one `update{Kind}` per affected type — a DGraph constraint, since nested child updates are silently discarded. First-time creation of a wrapper + children is ONE nested `update{Root}`; never parallel wrapper-link + child-add mutations. Reference: Server → IdracSettings; Cluster → ClusterBackup → Etcd/Velero/S3Sync.
- **A multi-entity Save pre-flights every version, then dispatches SEQUENTIALLY, fail-fast.** *(2026-09-16.)* Do NOT restore `Promise.all`. ⚠️ This is not atomicity; a mid-sequence outage still partials (now reported).
- **The policy pre-flight asks about EVERY type in the edit tree**, via the repeatable `type` param. *(2026-09-16.)*
- **Clearing a field emits `remove`, not `set: null`.** DGraph ignores `null` in `set` and rejects `""` on typed scalars. Always send a `set` (even `{}`) alongside — `remove`-only is rejected.
- **ONE edit-modal template** (`shared/components/edit-modal.gohtml`, from `component.EditModal`). *(2026-09-23.)* `html/template` won't interpolate an attribute NAME, hence the prefix-free `data-modal-close`.
- **`data-modal-close` closes any modal via one delegated handler in `shared.js`.** *(2026-09-23.)*
- **Adding a ConfigItem touches two files: `schema/schema.graphql` and `config/views.yaml`.** *(2026-10-04.)* Audit allowlist, editable fields, page, tabs, edit targets and delete all derive from them. See `docs/playbooks/add-configitem.md`.

### Errors and requests

- **Render the server's `error` and `hint`; never a bare status code, never `code`.** *(2026-09-15.)* Use `apiErrorText` / `apiErrorFromBody` from `shared.js`; don't re-implement inline.
- **An aborted request (status 0) is not an error to display** — `apiErrorText` returns `''`. Resolve a table's initial filter before constructing it, so there is one request.
- **A capped fetch must say it was capped.** *(2026-09-15.)*

### Rendering and templates

- **All HTML rendering goes through `webrender.RenderHTML` (buffer, then write)** — never `Execute` into `c.Response()`, which commits a truncated 200 on a mid-render error. `TestNoDirectTemplateExecuteIntoResponse` enforces it.
- **Templates are parsed from `web.Dir()`, never `ParseFiles("web/…")`** — that breaks outside the repo root. *(2026-10-02.)*
- **A page served by both apps lives in `shared/pages/` and gates auth on `.UI.ShowAuth`.** *(2026-09-23.)* Orb sets it false. Login notice: `shared/components/login-gate.gohtml`.
- **Do NOT convert one page's graphdiff rendering from JS to Go templates in isolation.** Convert a whole surface or none.
- **Go template `range` does not propagate assignments outward** — compute aggregates server-side.
- **JS module cache-busting lives in `head.gohtml`'s import map; every cross-file import needs an entry.** `TestImportMapCoversAllModuleImports` enforces it.
- **Vendor libraries live in named subdirectories of `web/shared/static/`**, browser files only.

### Copy and visual style

- **A page description is one line saying what the page contains** — no usage instructions, no roadmap status. *(2026-09-28.)* Use `class="has-text-grey page-description"`. If a control needs explaining, fix the control.
- **Every page titles itself, above any permission gate.** *(2026-10-02.)*
- **User-visible strings use vendor-neutral category terms** ("Object Store", not "S3"/"MinIO"). Env vars, Go names and log keys are exempt.
- **Field labels stay faithful to the schema name**, version suffixes included (`assetDataV2` → "Asset Data V2"). A label that must differ means rename the field. Detail-view keys are title-cased, not monospace. *(2026-09-23.)*
- **Field order in detail views: hierarchy fields, then identity fields, then alphabetical.** Expressed via `order:` pins — no per-type lists in this doc.
- **Bulma colour classes are reserved for state** (`is-danger` error/destructive, `is-warning` attention, `is-success` healthy, `is-info` neutral). `<code>` is typographic, not coloured.
- **List-table cells are plain text, uniformly styled; only a real status column carries colour** (plain coloured text, never a pill). Uniform monospace across all cells is fine; per-cell differentiation is not.
- **An exception column leaves the expected case blank** (Changes table `Note`: blank for `applies`).
- **A banner states the situation and whose move it is; the table names what.**
- **A binary state a row can be in uses a segmented control**, active side coloured by how much attention it deserves. Not a toggle switch, not an action button. Confirm turning protection OFF, not on.
- **New primary actions use `is-link`; destructive use solid `is-danger`; Cancel/Close are plain.** ⚠️ Do NOT sweep existing `is-primary` buttons to `is-link` *(reverted 2026-09-23)* — they were chosen; changing them is a visual decision made by looking.
- **Tooltips are `tooltip` + `data-text`, never native `title=`** (`is-wide` for long text). `TestNoNativeTitleTooltips` enforces it.

## House style — measured, not invented

**Copy the closest existing page's markup; the templates are the authority.**

| Element | Exact markup |
|---|---|
| Page heading | `<p class="is-size-4 mb-2" data-testid="page-heading">` — 24px |
| Description under it, on a LIST or TOOL page | `<p class="has-text-grey page-description">` |
| Section label inside a box | `<p class="is-size-6 has-text-weight-semibold mb-3">` — 16px |
| Every table | `class="table is-striped is-hoverable is-fullwidth is-size-7"` |
| Wide table wrapper | `<div class="table-container">` |
| Record-scoped actions | `<button class="button is-rounded is-small is-link mt-1">` in a top toolbar |
| List-page actions | `<button class="button is-small is-link">` — square |

| Building | Copy |
|---|---|
| A list page | `publish-history.gohtml` |
| A page of boxed sections | `restore.gohtml` (stacked) or `schema.gohtml` (narrow + wide box) |
| A record detail view | `shared/pages/generic-detail.gohtml` or `change-request-detail.gohtml` |
| A key/value summary | `schema.gohtml`'s "Active Version" |

- **A wide table inside a Bulma `.column`/`.cell` needs `min-width: 0`** on it (set in `main.scss` for `.tab-content`).
- **Box titles are 24px when the box IS the page, 16px under a page heading.**
- **A standalone page wraps content in `<div class="tab-content">`** to get the app-wide box treatment (otherwise boxes vanish in dark mode). No bespoke `.box.<page>` overrides.
- **Boxes are fine for bounded summaries**; leave a page's main subject (a diff, a long log) as a bare table.
- **Description lines belong on list/tool pages, not detail pages.**
- **Status is plain coloured text in row tables**; a `tag` pill only for a single prominent scalar in a box. Same value, same rendering on list and detail.
- **No breadcrumbs.** "Back to parent" is a rounded `is-warning` button with a left arrow.
- **Do NOT build page structure in JavaScript** — structure in `.gohtml`, JS fills ids.
- **Do NOT dim text inside a table; do NOT use `<strong>` for both headings and emphasis; do NOT add per-cell `is-size-7`.**

## Core rules

- **Inside a DataTables `initComplete`, use `this.api()`** — with `stateSave` the callback can run before the outer `const` binds.
- **A cached EMPTY list (`"[]"`) is a cache miss** — branch on the parsed length.
- **UI state across navigation uses `localStorage`, and every key must be cleared by `clearTabStateOnFresh`** (runs on login's `?fresh=1`). A restored value must fall back when it names something no longer on the page.
  Live keys besides the per-slug ones: `datacenterTabs`, `serverTabs`, `dcTabCurrent`, `srvTabCurrent` (orb), `crTabCurrent`.
- **JavaScript is ES modules, no bundler**: `shared.js` (both apps), `orbital.js`, `orb.js`. Never inline `<script>` in templates.
- **Cross-app navigation/reload handlers live in `shared.js`**; orbital-only edit handlers stay in `orbital.js`.
- **DataTables AJAX reload must guard the error path:**
  ```js
  const onError = () => reloadButton.removeClass('is-loading')
  dt.one('error.dt', onError)
  dt.ajax.reload(() => { dt.off('error.dt', onError); reloadButton.removeClass('is-loading') })
  ```
- **Click handlers use event delegation on a `.js-*` class, not inline `onclick`:**
  ```js
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('.js-divergence-publish')
    if (!btn) return
    divergencePublishForDC(btn)
  })
  ```
  Allowed exceptions: per-instance modal submit bindings inside an init function re-run on `afterSettle`; inline browser-API calls (`event.stopPropagation()`); tab-activation closures in `initDetailTabs`.
- **Never add new `window.X` bridges.** Allowlist: the editor maps and `window.reloadClusterFragment`. A module that imports from `shared.js` imports; it does not bridge.
- **Never call `getElementById` at module top level**; modules are deferred, use delegation.
- **Go template + HTMX is the primary rendering pattern**; JS only for what templates can't do (polling, DataTables, JSON editors, tab lifecycle).
- **All styles go in `web/sass/main.scss`** — never edit the generated `main.css`. `make build-css` / `make watch-css`.
- `make run-orbital` uses version `v0.0.0-dev`.

## HTMX patterns

- **Never use `htmx.ajax()` for programmatic reloads** — use `fetch` with `HX-Request: true`:
  ```js
  fetch(url, { headers: { 'HX-Request': 'true' } })
    .then(r => r.text())
    .then(html => { el.innerHTML = html; htmx.process(el); initXxx(...) })
  ```
- **HTMX does not re-execute `<script type="module">` in swapped content** — load the library once in `head.gohtml` and expose it on `window` (e.g. `window.JSONEditor`).
- **Initialize JSONEditor lazily, in a visible container** (on first Edit click).
- **HTMX declarative attributes (`hx-get`, `hx-post`) include `{{.BasePath}}`.**
- **Two separate `afterSwap` listeners** — global init vs. the detail-page one.

## URL construction (BASE path)

- `data-*` attributes hold the bare path (`data-url="/servers/{{.ID}}"`); JS prepends `BASE`.
- **Never put `{{.BasePath}}` in `data-*`** — it double-prefixes on AKS.
- Exception: HTMX declarative attributes include `{{.BasePath}}`.

## GraphQL responses

- **GraphQL returns 200 even for errors** — check `resp.ok`, then `result.errors`.

## Recurring display patterns

- **Digest display** — `digest.substring(0, 19) + '…'` with a copy button:
  ```js
  `<div style="display:flex;align-items:center;gap:0.25rem;">
    <span class="is-family-monospace is-size-7">${digest.substring(0, 19)}…</span>
    <button class="button is-small is-white tooltip" data-text="Copy digest"
      data-copy-value="${digest}">
      <span class="icon"><i class="fas fa-copy"></i></span>
    </button>
  </div>`
  ```

## Canonical button patterns

Match the pattern for the button's category; do not invent a new spinner or status mechanism.

### Refresh / reload button (data appears in-place)

- Reference: `loadArtifactsTable()`, `refreshDivergenceReports()` (orbital.js), `loadOrbTags()` (orb.js). Helper: `fetchWithMinDelay(url, minMs = 500)`.
- Server branches on `HX-Request: true` and returns a fragment (`UI.renderFragment`).
- Button id `btn-refresh-*`; Bulma `is-loading`; minimum 500ms. The swap must hang off the `Promise.all`, not the bare fetch:
  ```js
  // WRONG — skeleton flashes when fetch < 500ms because the swap fires inside the bare-fetch .then
  fetch(url).then(r => r.text()).then(html => { container.innerHTML = html })  // fires at t=fetch
    .finally(() => minDelay.then(() => btn.classList.remove('is-loading')))    // fires at t=max(fetch,500)
  ```
- Show skeleton rows immediately, with a `colgroup` mirroring the real table (`divergenceSkeletonHTML()`).
- `htmx.process(container)` after every swap. No `window.location.reload()`.

### Test connection / probe button (single status indicator)

- Declarative HTMX, no JS:
  ```html
  <button hx-post="{{.BasePath}}/api/v1/backup/test-connection"
          hx-target="#backup-connection-result"
          hx-swap="innerHTML"
          class="button is-light is-small">
    <span class="icon"><i class="fa-solid fa-plug"></i></span>
    <span>Test Connection</span>
  </button>
  <span id="backup-connection-result" class="is-size-7"></span>
  ```
- Server returns an HTML-escaped success/error span (`renderTestConnectionFragment`). Spinner via `.button.htmx-request`. Do NOT add `hx-disabled-elt`.

### Inline status — not toast

Action results show in an existing `*-status` slot that stays until the next action (`showPublishStatus()` in `orb.js`). No toasts.

### Inside an action row, a notice is TEXT — not a `.notification` panel

Colour in an action row belongs to the actions. A notice there is `is-size-7` text with an `icon-text` glyph (`has-text-warning` + `fa-triangle-exclamation`, or `has-text-info` + `fa-circle-info`). A `.notification` is fine where it owns its space. Reference: `applyGateState` in `configitem-editor.js`.

## Iterating on CSS / templates

- **Hard-refresh first** when a change doesn't appear — `?v=` only busts on real deploys.
- **Templates hot-reload; Go handlers need a restart.**

## Horizontal scroll inside flex/grid layouts

When a scroll container clips instead of scrolling, every flex/grid ancestor must be allowed to shrink:

- **Flex item** (Bulma `.column`) → `min-width: 0`.
- **Grid track** → `minmax(0, 1fr)`, not `1fr`. `min-width: 0` on the item does not override the track.

Check the whole ancestor chain at once.

## DataTables + Bulma

- **Wrap the page-length `<select>`** with `dtWrapLengthSelect(this.api())` in `initComplete`.
- **Size with Bulma modifier classes, not CSS** — Bulma sizes via custom properties.
- **`stateSave: true` on main page tables**, not on embedded per-tab tables.
- `.field` margins break toolbar flex alignment — avoid there.

## Dark mode

Driven by `prefers-color-scheme` through Bulma's `--bulma-*` variables; no toggle.

- **Never hardcode a colour literal** — derive from scheme vars. Target WCAG AA in both modes.
- **DataTables CSS assumes a light page.** `.modal .modal-content{background:transparent;padding:0}` in `main.scss` neutralises its modal collision (never edit vendored CSS). `shared.js` sets `<html class="dark">` so DataTables' dark rules fire — don't remove it.

## Storage conventions

- **sessionStorage** → API response data. **localStorage** → UI state.
- **Logout clears both.**
- **Inventory** feeds DataTables from the sessionStorage cache with the saved filter as `searchCols`; Reload clears the cache first.

## Tab state conventions

- **Detail-tab active panel persists under `tab-active:{tabContainer.id}`**, wired by `initDetailTabs`; never written by hand.

## Detail tabs with audit log — canonical pattern

One function, `initDetailTabs(tabContainer, options?)` in `shared.js`; reference template `shared/pages/generic-detail.gohtml`.

- Tab container: `<div class="tabs is-boxed" id="{prefix}-detail-tabs-{{.DomID}}">` — the id is the storage namespace.
- Each tab: `<li data-panel="…">`; the first carries `is-active`. Panels are siblings; non-default ones start `display:none`.
- **The audit `<li>` is the only one with `data-orb-id`** — that is how it is detected. It carries `data-subgraph-orb-ids`. Its panel starts empty and fills on first activation.
- Options: `scoped` (default `true`, panel lookups scoped to the parent — keep it), `storage` (`'local'` default, or `'session'`).
- **Audit panel row cap** — `AUDIT_PANEL_LIMIT`, from `layout.AuditPanelDefaultLimit`.

```js
const tabContainer = root.querySelector('[id^="newprefix-detail-tabs-"]')
if (tabContainer) initDetailTabs(tabContainer)
```

## Template conventions

- **Page titles**: `{{.PageTitle}} | Orbital`; every handler sets `PageTitle`.
- **Never redeclare fields of embedded types** (`layout.Base`) — the outer field shadows it.
- **Single-tab pages** use a heading, not `<nav class="tabs is-boxed">`; keep the `.tab-content` wrapper if they contain boxes.
- **`updatedBy`/`updatedAt` are hidden from audit variable display** (`skipVars`); they stay in the database.
- **REST-triggered audit events have no child row.**
- **Startup logging uses slog**, set first in `cmd/orbital/main.go`.
- **Standalone pages still include `head.gohtml`** for cache-busting.
- **Full-document templates live in `pages/`, fragments in `partials/`.**

## Timestamp rendering

- **`data-timestamp` uses RFC3339**; the visible fallback is short:
  ```gohtml
  <span data-timestamp="{{.SomeTime.Format "2006-01-02T15:04:05Z07:00"}}">{{.SomeTime.Format "2006-01-02 15:04"}}</span>
  ```
- **`renderTimestamps(document)` runs at DOMContentLoaded**; swapped content calls `renderTimestamps(panel)`.
