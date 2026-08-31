# UI Reference

Read this before: Go template changes, HTMX interactions, JavaScript, CSS/SCSS, any frontend work.

## Settled Decisions

- **JS module cache invalidation lives in `head.gohtml`'s `<script type="importmap">` — every cross-file relative import MUST have a matching map entry.** ES-module specifiers are static; `import from './shared.js?v={{.Version}}'` is not a legal specifier and can't be templated inside a `.js` file. Without the map, `orbital.js` gets cache-busted via `?v=` on its own `<script>` tag but its transitive `import from './shared.js'` hits stale browser cache after any deploy that added exports → runtime `SyntaxError: does not provide an export named X`. Adding a new module file `foo.js` with `import from './new.js'` REQUIRES adding `"/static/new.js": "/static/new.js?v={{.Version}}"` to the map. Cache-Control middleware on `/static/*` sends `max-age=31536000, immutable` only when the request includes `?v=` — safe because the URL itself changes on version bump. **`TestImportMapCoversAllModuleImports` in `web/importmap_test.go` fails at CI if any specifier lacks a map entry**; the test's failure message tells you exactly what line to add. See `internal/server/server.go` for the middleware and head.gohtml comment block for the reasoning.
- **Inventory namespace filter is page-level, not a DataTable column filter** — lives in the page header (above the table), uses regex search on the orbId column (`^namespace:`). Do not move it into the DataTable toolbar — namespace is a scope selector, not a column filter.
- **Vendor libraries in `web/shared/static/` use named subdirectories** — `datatables/`, `font-awesome-6.6.0/`, `vanilla-jsoneditor/`. Do not flatten into a single `vendor/` bucket. Each subdir contains only browser-required files — strip docs, type defs, LESS/SASS sources, and package.json before committing a new vendor library.
- **Go template `range` does not propagate variable assignments outward** — `$x = true` inside `{{range}}` does not affect `$x` after the range ends. Compute aggregates server-side (method on the struct). Example: `ImportRecord.DispatchErrors()` instead of a `$hasError` flag inside range.
- **Field-value row ordering in detail views: identity/hierarchy field(s) first, then alphabetical.** "Identity fields" are the non-boolean scalars that describe what the row IS (Server: `hostname`, `model`, `manufacturer`; IdracSettings: `firmwareVersion`; KubernetesCluster: `name`, `kubernetesVersion`). Hierarchy fields (parent references) come before identity fields when both apply — e.g. Server's summary starts with Data Center, THEN identity fields, THEN alphabetical for the rest. Boolean state flags always sort alphabetically. Rationale: identity-first reflects how operators scan ("which thing am I looking at?" then "what's its state?"); alphabetical fallback eliminates per-field judgment calls and stays stable as fields are added. Reference: Server summary in `server-tab.gohtml`; IdracSettings in `server-tab.gohtml`. Schema declaration order is NOT the source of truth — schema order reflects when fields were added, not display intent.
- **Field-value labels in detail views use the schema field name in monospace, not human-readable labels.** Section headers and tab labels stay human ("iDRAC Settings", "Storage", "Network" — these are navigation). The leftmost cell of a `key: value` row is the *field identifier* and uses the GraphQL schema name verbatim (`sshEnabled`, not "SSH Enabled"), rendered with Bulma's `is-family-monospace`. Reasons: (1) audit log, API, seed data, tests already use schema names — one vocabulary across surfaces eliminates mental translation; (2) orbital's promise is graph-native truth, and visible truth requires visible field identifiers; (3) audience is technical even when role-mixed. Excludes: column headers in tabular lists (`<th>Manufacturer</th>` is navigational), and derived fields not present in the schema verbatim (e.g. a formatted "Last Seen" timestamp). Do NOT add tooltip-style human-label translations — that recreates the vocabulary fork. Do NOT smooth or abbreviate schema names — if a name like `osToIdracPassThroughEnabled` feels long, fix the schema or accept the length; do not translate in the UI.
- **User-visible UI strings use vendor-neutral terms for category-level resources, not protocol/vendor names.** Buttons, labels, page titles, error messages, and status text use "Object Store" / "object storage" — NOT "S3", "Azure Blob", "MinIO". Reason: orbital runs against Azure Blob in production (via the AWS SDK's S3-compatible client), MinIO locally, and any S3 vendor on other deploys. Writing "S3" in the UI misleads operators who'd otherwise look in the wrong cloud console. **Exclusions:** env var names (`ORBITAL_S3_BUCKET`), Go struct field names (`S3Endpoint`), and internal log keys (`s3Key`) stay as-is — they're internal vocabulary that signals "S3 protocol API" rather than "AWS S3 product," and renaming has deployment cost. Apply this rule to: user-clickable button labels, page subtitles, help text, configuration-warning messages, and error messages surfaced to the UI. Same rule applies to other vendor names that have a category equivalent (e.g., "PostgreSQL" in user UI → "database" unless the user choice of engine is the point).
- **Edit pattern — one JSON editor at the parent, no per-child Configure / Edit / Delete UI.** Parent ConfigItems (Server, DataCenter, KubernetesCluster) render a SINGLE Edit button that opens a JSON editor whose tree IS the parent + every owned-child ConfigItem nested inline. The page handler exports a `targets` JSON blob (one entry per editable entity, built by `configitems.BuildEditTargets`); the generic `configitem-editor.js` module owns snapshot/diff/dispatch. **Edits to existing entities** fire canonical `update{Kind}(orbId, set)` mutations in parallel, one per affected concrete type. **First-time creation of a wrapper + its children** (e.g. adding backup config on a cluster where `backup: null`) is folded into ONE nested `update{Root}` mutation whose `set` carries the whole subtree — do NOT decompose into parallel wrapper-link + child-add mutations. Parallel dispatch races DGraph into auto-creating a partial wrapper node that fails NotEmpty on `namespace` (real burn 2026-07-08, backup-config first-time-configure). **Do NOT add a per-child modal, "Configure" flow, or per-row Edit/Delete buttons for an owned child.** Read-only sub-tabs displaying current state are fine (e.g. cluster's Backups tab). The pattern's intentional trade-off: lower field discoverability vs forms; if it becomes unacceptable, propose a hybrid (form-with-advanced-JSON toggle) and apply it codebase-wide, never deviate for one feature. Reference implementations: Server → IdracSettings; Cluster → ClusterBackup → EtcdBackup/VeleroBackup/S3Sync. See `docs/playbooks/add-configitem.md` for the recipe.
- **Adding a new ConfigItem family is registry-driven; no JS or audit-pipeline edits needed.** The `internal/configitems.Types` list is the single source of truth — append one entry per type (Name, OwnerType, FormFields, BeforeFields, PayloadField, Implements). The audit pipeline's allowlist regex, before-fetch fields, and the page handlers' edit targets all derive from it. `configitem-editor.js` consumes the targets emitted by the page handler, so adding a new sub-kind to an existing parent doesn't touch JS at all. See the playbook for the canonical 3-step recipe; this used to be ~13 places.
- **Clearing a field emits `remove`, not `set: null`.** On save, `configitem-editor.js` diffs each target's current JSON against its open-time snapshot: non-empty fields → `set`; fields that were set and are now empty — `null` / `""` / a deleted key, all equivalent — → `remove: { field: <snapshot value> }`. Required because DGraph **ignores `null` in a `set` patch** and **rejects `""` on typed scalars** (`DateTime`); `remove` matches on the value, and the open-time snapshot holds the current one. One `update{Kind}` carries both `set` and `remove` (one audit row), and the `set` still receives the server-stamped `version`/`updatedAt`/`updatedBy` — so a clear is a normal audited edit. See `removePayload`/`scalarPayload`/`buildUpdateCall` in `configitem-editor.js`. A `remove`-only mutation is rejected `VARIABLE_FORM_REQUIRED`, so the editor always sends a variable `set` (even `{}`) alongside. **API callers do this read-then-remove explicitly** — see "Clearing a field" in `docs/api-cheatsheet.md`.
- **Do NOT use native HTML `title=""` for user-facing tooltips.** Browser-controlled — ~1s delay before display, small system font, no theming, no smart positioning. For interactive UI use the existing `.tooltip` + `data-text=""` CSS class in `web/sass/main.scss:187` (instant on hover, themeable). Long-term: migrate to Tippy.js — tracked in ROADMAP technical debt. New code MUST NOT add `title=` attributes for user-facing tooltips; existing ones get migrated when touched.
- **UI field labels stay consistent with the schema field name — do NOT invent prettier divergent labels.** A label is the title-cased field name, **preserving version/legacy suffixes** (`assetDataV2` → "Asset Data V2", not "Asset Data"). The reason is API discoverability: the same people read the UI and query the GraphQL API, and a label that hides the real field name (or drops a `V2`) sends an integrator hunting for a field that doesn't exist. When a field name is awkward because it's legacy (a rename would be a breaking schema change), the UI carries the awkwardness rather than papering over it. Fixed `assetDataV2`'s "Asset Data" label 2026-07-24. If a label genuinely needs to differ from the field, that's a signal the field should be renamed (schema change) — not that the UI should diverge.
- **Color convention — Bulma color classes are reserved for state classification.** `is-danger` (errors, failures, destructive actions), `is-warning` (needs attention, unverified, degraded), `is-success` (completed, healthy, verified), `is-info` (informational tags, neutral highlights). Do NOT apply these to ordinary value text — a cell showing a config value is not "danger" just because it differs from intent. `<code>` is rendered in default text color with a subtle background — its differentiation from prose is typographic (monospace), not chromatic. Bulma's default `--bulma-code` is overridden in `main.scss` to neutralize the hot-pink red, which would otherwise consume the danger-color channel for purely typographic styling. If you reach for `has-text-danger` on something that isn't an error, find a different signal.
- **List-table cells are plain text; only a genuine status column carries color.** In a `<table>` of records, value cells get no per-cell `is-family-monospace`, `is-size-*`, `is-italic`, `<code>`, tag pills, or value-coloring — style the row uniformly. The one exception is a real state column, rendered as plain colored text (per the color convention above — `has-text-success`/`warning`/`grey`), never a pill. The detail-view monospace-key rule (schema field names) does **NOT** extend to list-table cells. Real burn: the Spike-36 approval-policies / change-requests tables rendered namespace in monospace, the type placeholder in grey, and one cell at `is-size-7`, so every column read as a different font/colour.
- **A binary state that a row can be IN uses a segmented control, never an action button.** Two adjacent buttons (`buttons has-addons`), both options visible, the active one highlighted and `disabled` — the Users-page role picker is the reference. A lone button labelled with the *action* (`Disable`) next to a column showing the *state* (`enforced`) encodes the same fact in opposite directions, so the reader has to invert one to reconcile them; and once the control shows the state, that column is redundant and should go. **Do not reach for a toggle switch**: it has the same defect the action button has — nothing on it says whether it depicts the current state or the change a click would make. Applied to approval policies 2026-08-29 (`Enabled | Disabled`). **Colour the active side by how much attention the state deserves**, the way the Users role picker ramps `is-primary` → `is-warning` → `is-danger`: green when the thing is doing its job, amber when it is half-on, `is-danger` when deliberately off. Red there does not mean "bad" any more than admin does on Users — it means *look at this*, and a switched-off control that still sits in the table looking like protection is exactly what a scan must catch. **If the state is security-relevant, confirm asymmetrically** — free to turn protection on, `confirm()` to turn it off, matching what Delete already does.
- **Primary-action buttons use `is-link`, not `is-primary`.** Add / Create / Save / Enable use `button is-link` (orbital standardized on `is-link` as its action colour — do not mix in `is-primary`). Destructive actions use solid `is-danger` (match the config-item Delete — not `is-danger is-light`); approve/reject verdict pairs use `is-success` / `is-danger`; secondary actions (Cancel, Close) are plain (no colour).
- **All HTML template rendering goes through `renderHTML` (buffer-then-write) — NEVER `tmpl.Execute` / `ExecuteTemplate` directly into `c.Response()`.** `html/template` streams output and only errors *after* partial bytes are written, so executing straight into the response commits a `200` + truncated body on any mid-render error (most commonly a template referencing a field the render struct lacks). Echo's error handler cannot touch a committed response, so the failure is **silent**: truncated HTML, no 500, nothing logged — it surfaces downstream as "a button does nothing." Real burn 2026-07-27: `cluster-tab.gohtml` rendered `.Backup.Etcd.RetentionDays` but the `backupKindTab` render struct was missing that field → the fragment truncated *before* the edit modal at the end of the template → the Edit button had no modal to open. `renderHTML(c, tmpl, name, data)` in `internal/handler/render.go` renders into a `bytes.Buffer` first: on error nothing is written and the error propagates to a real 500 (the access-log middleware escalates 5xx → ERROR, so it is visible); on success it writes the full body in one shot. Pass `name=""` to run the root template (`Execute`), or a defined-block name to run `ExecuteTemplate`. This buffering does NOT prevent struct/template field drift — it makes the drift fail *loud* (500 in dev/CI) instead of silently truncating in prod. New handlers MUST render through this helper.

## Core rules

- **JavaScript is split into ES modules — no bundler** — `web/shared/static/shared.js` (utilities used by both orbital and orb), `web/shared/static/orbital.js` (orbital-only features), `web/shared/static/orb.js` (orb-only features). `head.gohtml` conditionally loads `orbital.js` or `orb.js` based on `{{.UI.AppName}}`. Never inline `<script>` blocks in templates.
- **Cross-app navigation and reload handlers live in `shared.js`** — `initRowNavigation()`, `initLinkNavigation()`, and `initReloadButtons(opts?)` are exported from `shared.js` and called by both `orbital.js` and `orb.js`. Any new navigation pattern (e.g. dblclick on a new resource type) belongs in these functions so both apps get it automatically. Orbital-only edit-modal handlers (`[data-*-edit-id]` click handlers) stay in `orbital.js` — they are gated on `Actions.Edit` and must not move to `shared.js`. `initReloadButtons` accepts an optional `opts` object: `onDcReloaded(domId)` and `onSrvReloaded(target)` callbacks let `orbital.js` clean up its editor Maps on tab reload; `orb.js` passes nothing. See ADR 011 for the full rationale.
- **DataTables AJAX reload must guard the error path** — `dt.ajax.reload(callback)` only fires `callback` on success; errors swallow the spinner. Pattern:
  ```js
  const onError = () => reloadButton.removeClass('is-loading')
  dt.one('error.dt', onError)
  dt.ajax.reload(() => { dt.off('error.dt', onError); reloadButton.removeClass('is-loading') })
  ```
- **Click handlers default to event delegation, NOT inline `onclick=""`** — convention across `orbital.js` and `orb.js`. Endorsed by Hypermedia Systems Ch. 10 ("`addEventListener` is preferable to `onclick` for many reasons"). Survives HTMX swaps automatically; keeps functions inside the module (no `window.*` bridge needed). Pattern:
  ```html
  <button class="js-divergence-publish" data-dc-orbid="{{$dcId}}">Publish</button>
  ```
  ```js
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('.js-divergence-publish')
    if (!btn) return
    divergencePublishForDC(btn)
  })
  ```
  Class form (`.js-feature-action`) is the in-house convention. Co-locate the listener block with the function it calls, in the relevant section of `orbital.js` / `orb.js`. **Three documented exceptions** where `onclick=""` / per-element `addEventListener` is allowed:
  1. **Form-submit handlers inside per-instance modal init functions** — `dc-edit-submit-${id}`, `srv-edit-submit-${id}` etc. bind via `element.addEventListener('click', ...)` inside `initEditModal()` which is called from `htmx:afterSettle`, so the rebind happens automatically on every swap. Acceptable because the binding code captures local closure state (the JSONEditor instance, the modal id) cleanly.
  2. **Inline browser-API calls** — `onclick="document.getElementById('foo').classList.remove('is-active')"`, `onclick="event.stopPropagation()"`, `onclick="navigator.clipboard.writeText('...')"`. These call browser APIs, not module functions; no bridge entry needed.
  3. **Tab activation logic captured in init closures** — e.g. `tab.addEventListener('click', () => activatePanel(...))` inside `initDetailTabs`. The closure over `panelPairs` is the cleanest way to express the relationship; rebind happens automatically because `initDetailTabs` is called from afterSettle.
- **Never add new `window.X = X` bridge entries** — the existing `window.dcEditors`, `window.clusterEditors`, `window.srvEditors`, `window.reloadClusterFragment` are legitimate non-bridge uses (e2e exports + cross-module callables). New code uses delegation per the rule above.
- **`DOMContentLoaded` inside modules works correctly** — modules are deferred by default. Use delegated `document.addEventListener('click', e => { if (!e.target.closest('#id')) return; ... })` for button handlers. Never call `getElementById` at module top level.
- **Go template + HTMX is the primary rendering pattern** — server renders HTML fragments (including `<select>` options, lists, previews); JS fetches HTML and sets `innerHTML`. Reserve JS for things Go templates cannot do: polling loops, DataTables init, JSON editors, tab lifecycle management. Never write JS to fetch data and build DOM that a Go template handler could render directly.
- **All styles go in `web/sass/main.scss`** — never edit `web/shared/static/css/main.css` directly (generated). Rebuild: `make build-css` (one-time) or `make watch-css` (watch mode).
- `make run-orbital` uses version `v0.0.0-dev` — avoids noisy git-describe strings in local dev. `make push` still uses full `$(SERVER_VERSION)`.

## HTMX patterns

- **Never use `htmx.ajax()` for programmatic tab reloads** — it carries hidden request context (triggering element, OOB swap hints, lifecycle state) designed for declarative flows. Called imperatively from async handlers, it misroutes responses. Always use plain `fetch()`:
  ```js
  fetch(url, { headers: { 'HX-Request': 'true' } })
    .then(r => r.text())
    .then(html => { el.innerHTML = html; htmx.process(el); initXxx(...) })
  ```
  Always send `HX-Request: true` so Go handlers return fragments, not full pages.

- **HTMX does not re-execute `<script type="module">` in swapped content** — use the window bridge pattern: load the library once in `head.gohtml` as a module, assign to `window.MyLib`. Applied to JSONEditor: `head.gohtml` sets `window.JSONEditor = JSONEditor`; edit modals use `window.JSONEditor` directly.

- **JSONEditor must be initialized in a visible container** — initializing while the modal is hidden produces a blank editor. Always initialize lazily on the first Edit button click (after `modal.classList.add('is-active')`), not on HTMX swap.

- **HTMX declarative attributes (`hx-get`, `hx-post`) must include `{{.BasePath}}`** — rendered server-side, they do not go through the JS `BASE` variable.

- **Two separate `afterSwap` listeners** — DC tab init belongs in the global afterSwap listener, not the `addEventListeners()` one (which is server detail page only).

## URL construction (BASE path)

- `data-*` template attributes contain only the bare path: `data-url="/servers/{{.ID}}"`
- JS always prepends `BASE` (= `window.ORBITAL_BASE` = `{{.BasePath}}`): `BASE + el.dataset.url`
- **Never include `{{.BasePath}}` in `data-*` attributes** — JS would double-prefix → 404 on AKS (`/orbital/orbital/...`)
- Exception: HTMX declarative attributes must include `{{.BasePath}}` (rendered server-side, not via JS `BASE`)

## GraphQL responses

- **GraphQL always returns HTTP 200, even for errors** — check `resp.ok` first (transport failure), then `result.errors` in the body (GraphQL-layer errors). Both checks are required. DGraph returns errors in `{ "errors": [...] }` with HTTP 200.

## Recurring display patterns

These patterns are used in both orbital and orb. Always use them — never invent a one-off variant.

- **Digest display** — `digest.substring(0, 19) + '…'` (keeps `sha256:` prefix + 12 hex chars). Wrap in a flex div with a copy button:
  ```js
  `<div style="display:flex;align-items:center;gap:0.25rem;">
    <span class="is-family-monospace is-size-7">${digest.substring(0, 19)}…</span>
    <button class="button is-small is-white" title="Copy digest"
      onclick="navigator.clipboard.writeText('${digest}').then(...)">
      <span class="icon"><i class="fas fa-copy"></i></span>
    </button>
  </div>`
  ```

## Canonical button patterns

When adding a button that triggers an action, match the pattern below for its category. **Do not invent a new spinner / status mechanism.** Default to these unless you have a written reason not to.

### Refresh / reload button (data appears in-place)

User clicks → button shows spinner → page content swaps without navigation → spinner clears after a minimum visible duration.

- Pattern reference: `loadArtifactsTable()` (orbital.js), `loadOrbTags()` (orb.js), `refreshDivergenceReports()` (orbital.js).
- Helper: `fetchWithMinDelay(url, minMs = 500)` in `shared.js`.
- Server side: handler branches on `HX-Request: true` and returns just the fragment (a named `{{define}}` block) — see `UI.renderFragment` in `internal/handler/ui.go` and `DivergenceReports` for an example.
- Button id: `btn-refresh-*`. Spinner class: Bulma `is-loading` (added/removed by JS).
- Min display 500ms via `Promise.all([fetch, new Promise(r => setTimeout(r, 500))])`. The `.then` that does the swap must be on the `Promise.all`, **not** on the bare fetch — otherwise the swap fires when the fetch resolves and the skeleton flashes before disappearing. Mistake pattern to avoid:
  ```js
  // WRONG — skeleton flashes when fetch < 500ms because the swap fires inside the bare-fetch .then
  fetch(url).then(r => r.text()).then(html => { container.innerHTML = html })  // fires at t=fetch
    .finally(() => minDelay.then(() => btn.classList.remove('is-loading')))    // fires at t=max(fetch,500)
  ```
- **Show skeleton rows immediately** — inject a skeleton table whose `colgroup` mirrors the real table's column widths so the swap doesn't reflow. Use `<span class="is-skeleton" style="display:block">&nbsp;</span>` for cell placeholders. See `divergenceSkeletonHTML()` in orbital.js and `showDatacenterSkeleton()` in shared.js for the canonical shape.
- Always `htmx.process(container)` after swap so any HTMX attributes in the fragment get re-bound.
- Do NOT use `window.location.reload()`. Full reload doesn't match the established pattern and discards client state (open tab, scroll, expanded rows).

### Test connection / probe button (single status indicator)

User clicks → button spinner → server returns success / error HTML fragment → swapped into a result span next to the button.

- Pattern reference: backups page and divergence-reports page (`btn-test-backup-connection`, `btn-test-divergence-connection`).
- Declarative HTMX — no JS:
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
- Server: detect `HX-Request: true`, return HTML span (`<span class="has-text-success">… Connected</span>` or `<span class="has-text-danger">… <error></span>`). Always HTML-escape the error message — see `renderTestConnectionFragment` in `internal/handler/backup.go`.
- Spinner CSS hook: `.button.htmx-request` in `main.scss` mirrors `.button.is-loading`. HTMX adds `.htmx-request` automatically while the request is in flight.
- Do NOT add `hx-disabled-elt="this"` — it sets the `disabled` attribute which interacts badly with Bulma's disabled styling and was the source of a stuck-spinner bug. CSS `pointer-events: none` in the `.htmx-request` rule already blocks clicks during the request.

### Inline status — not toast

Action-result status (Publish succeeded, etc.) shows in an existing `<div id="*-status">` notification slot above or below the action. The slot stays visible until the user navigates away or triggers another action.

- Pattern reference: `showPublishStatus()` in `orb.js`; `#publish-status` on orb's divergence page.
- Do NOT use fixed-position toasts — they vanish before slow readers see them, and we don't have a toast convention anywhere else.

### Inside an action row, a notice is TEXT — not a `.notification` panel

A tinted `.notification` next to buttons is another coloured rectangle competing with them. The edit-modal footer reached four — an amber panel above a green *Propose change*, an amber *Save directly* and a white *Cancel* — and nothing read as primary any more.

**Colour in an action row belongs to the actions.** A notice sharing that row is `is-size-7` text with an `icon-text` glyph carrying the hue: `has-text-warning` + `fa-triangle-exclamation` for something to act on, `has-text-info` + `fa-circle-info` for context. One glyph carries the urgency a filled panel did.

A `.notification` panel is still right where it OWNS its space — a page-level status slot with nothing competing (`#publish-status` above). The rule is about crowding, not about the component. Reference: `applyGateState` in `configitem-editor.js`.

## Iterating on CSS / templates

- **Static assets are aggressively cached by browsers.** When a CSS or template change doesn't appear after server restart, **hard-refresh first** (Cmd+Shift+R). The `?v={{.Version}}` cache-bust on `head.gohtml` covers _real_ deploys, not iterative dev changes within the same `Version` value. Don't chase phantom JS / HTMX bugs before ruling out cache.
- **Templates hot-reload from disk; Go handlers do not.** Editing a `.gohtml` file in dev mode picks up on the next page request. Editing a `.go` file requires restarting orbital (`make run-orbital`).

## Horizontal scroll inside flex/grid layouts (`overflow` doesn't work by itself)

When a child scroll container (`<pre>`, `overflow-x:auto` wrapper, wide `<table>`) **clips instead of scrolling** inside our `.app-main-grid` → `.columns`/`.column` layout, `overflow:auto` on the child is NOT the fix — its ancestors are refusing to shrink and expanding to the widest content. **Every flex/grid ancestor from the scroll container up to the viewport must be allowed to shrink**, and there are two distinct knobs:

- **Flex item** (e.g. a Bulma `.column`) → `min-width: 0`. Flex items default to `min-width: auto` (won't shrink below content).
- **Grid track** (e.g. `.app-main-grid`'s content column) → `minmax(0, 1fr)`, **not** bare `1fr`. A bare `1fr` is `minmax(auto, 1fr)`, whose `auto` minimum blows the track out to content width. `min-width: 0` on the grid *item* does **not** override the *track's* minimum — only `minmax(0, …)` does.

Both are needed together in a grid-of-flex layout (`.app-main` grid item has `min-width:0`, `.app-main-grid` track is `minmax(0,1fr)`, `.tab-content .column` has `min-width:0`). Diagnosis shortcut: if the box is clipping at a bounded edge with no scrollbar, walk the **entire** ancestor chain and check every flex item and grid track at once — fixing one level at a time just moves the blowout up one node. `overflow-y:auto` already computes `overflow-x` to `auto`, so the child overflow is rarely the missing piece. (Fixed for `/schema`'s SDL `<pre>` 2026-07-24.)

## DataTables + Bulma

- **Page length `<select>` needs a Bulma wrapper** — DataTables renders a bare `<select>`; Bulma needs `<div class="select is-small">` for the custom arrow. Wrap after init: `initComplete: function() { dtWrapLengthSelect(this.api()) }`.
- Use **Bulma modifier classes** (e.g. `is-small`) in `initComplete`, not CSS overrides — Bulma sizes via CSS custom properties, so `font-size` overrides don't work.
- **`stateSave: true` on all main page tables** — persists length/search/sort/position in localStorage across navigations. Applied to: inventory, datacenter, server list, audit log tables. Exclude embedded per-tab tables (e.g. `dc-servers-table`) — they reinit on every tab load.
- `.field` adds `margin-bottom` that breaks flex alignment in DataTables toolbar — avoid in toolbar layouts. `vertical-align` on `dt-length` is also ignored in flex context.

## Dark mode

Dark mode is **`prefers-color-scheme`-driven through Bulma's `--bulma-*` scheme variables** — no manual toggle. Bulma flips its scheme vars at `:root` under `@media (prefers-color-scheme: dark)` and all its own components follow automatically.

- **Never hardcode a color literal in custom CSS / templates / inline styles.** A fixed `#fafafa` / `background:white` / `#666` does NOT flip — it's right in one mode and unreadable in the other (was the clusters child-row + login-modal bug). Derive from scheme vars: `var(--bulma-background)`, `var(--bulma-text)` / `-strong` / `-weak`, `var(--bulma-border)`, the `*-on-scheme` colors, or `hsl(var(--bulma-scheme-h) var(--bulma-scheme-s) calc(var(--bulma-scheme-main-l) ± N%))` for a scheme-relative tint. Target WCAG AA (≥4.5:1) in both modes.
- **DataTables' bundled CSS assumes a light page and does NOT honor `prefers-color-scheme`.** Two burns:
  - `responsive-*.bulma.min.css` ships a generic `div.modal-content{background:white}` that **collides with Bulma's modal** (shared class, out-specifies `.modal-content`) → white frame around dark modals. Neutralized by `.modal .modal-content{background:transparent;padding:0}` in `main.scss`. Do NOT edit the vendored min.css — override in `main.scss`.
  - DataTables gates its own dark theme on `html.dark` / `:root[data-bs-theme=dark]`, which we never set. `shared.js` syncs `prefers-color-scheme:dark` → `<html class="dark">` at module load so DataTables' shipped dark rules (sort arrows, responsive detail modal, child-row borders) actually fire. Do NOT remove that sync.

## Storage conventions

- **sessionStorage** → API response data (e.g. inventory rows) — clears on tab close, always fresh on new session. Data copies go here.
- **localStorage** → UI state (tab positions, filter selections, DataTables state) — persists across sessions. User preferences go here.
- **Logout clears both** — `localStorage.clear()` and `sessionStorage.clear()` called before POST. Next login starts with no tab state.
- **Inventory sessionStorage cache + `searchCols` pre-filter** — rows fed to DataTables at init time from cache, eliminating ajax flash on revisit. Saved type filter passed as `searchCols` so filtered state is the first and only draw. Reload button clears cache, empties table visually (`clear().draw()`), then refetches. `populateTypeDropdown()` called after data is available (not in `initComplete`).

## Tab state conventions

- **Detail-tab active panel persists under `localStorage.tab-active:{tabContainer.id}`** — e.g. `tab-active:dc-detail-tabs-{DomID}`, `tab-active:srv-detail-tabs-{DomID}`, `tab-active:cluster-detail-tabs-{DomID}`. Keys are wired automatically by `initDetailTabs` (in `shared.js`); do not write them by hand. On DC tab close (`tab-close-${domId}` handler) the matching key is cleared so reopening defaults to the first panel.
- Servers page tabs persist under `localStorage.serverTabs`; DC tabs under `localStorage.tabs` — separate keys, same `TabItem` class pattern.

## Detail tabs with audit log — canonical pattern

All detail pages (DC, Server, Cluster, future) share one JS function — `initDetailTabs(tabContainer, options?)` in `shared.js` — and one set of template conventions. **Adding a new detail page is one Go template + one line of JS.**

**Template conventions** (mirrored across `datacenter-tab.gohtml`, `server-tab.gohtml`, `cluster-tab.gohtml`):

- Tab container: `<div class="tabs is-boxed" id="{prefix}-detail-tabs-{{.DomID}}">` — `{prefix}` is page-specific (`dc`, `srv`, `cluster`, …). The DOM `id` is the storage key namespace; keep it unique per entity.
- Each tab: `<li data-panel="{prefix}-panel-X-{{.DomID}}">…</li>`. First tab carries `class="is-active"`.
- Audit tab: `<li data-panel="{prefix}-panel-audit-{{.DomID}}" data-orb-id="{{.OrbID}}" data-related-orb-ids="{{.RelatedOrbIDsCSV}}">` — **the `data-orb-id` attribute is how `initDetailTabs` detects the audit tab**, so do not put `data-orb-id` on any non-audit `<li>`. `data-related-orb-ids` is optional; when present it aggregates events across the parent + nested ConfigItems.
- Panel divs: `<div id="{prefix}-panel-X-{{.DomID}}">…</div>` as siblings of the tab container. Non-default panels carry `style="display:none"`.
- Audit panel placeholder: `<div id="{prefix}-panel-audit-{{.DomID}}" style="display:none"></div>` — empty; `initDetailTabs` fills it on first activation.

**JS call site:**
```js
const tabContainer = root.querySelector('[id^="newprefix-detail-tabs-"]')
if (tabContainer) initDetailTabs(tabContainer)
```

Options (all default to safe values):
- `scoped` (default `true`) — panel lookups scope to `tabContainer.parentElement` instead of `document`. Protects against the dual-rendering case where the same entity ID appears twice on the page (e.g. a server tab open both standalone and drilled in from a DC tab). Leave true unless you can prove only one rendering exists.
- `storage` (default `'local'`) — `'local'` persists active tab across browser sessions; `'session'` resets per browser tab. Default matches user expectation ("I had Audit open; reopen the page and it's still Audit").

**Audit panel row cap** — `AUDIT_PANEL_LIMIT` in `shared.js`, sourced from `layout.AuditPanelDefaultLimit` (Go) via `window.ORBITAL_CONFIG.auditPanelLimit`. Single source of truth.

**Outstanding cleanup (future phase):** the audit `<li>` declaration and audit `<div>` placeholder are still copy-pasted across `datacenter-tab.gohtml` / `server-tab.gohtml` / `cluster-tab.gohtml`. Extracting them into a shared `audit-tab.gohtml` partial requires either a `FuncMap` (`dict`) or per-handler `AuditPanelID` field. Worth doing if a 4th detail page lands.

## Template conventions

- **Page titles**: `{{.PageTitle}} | Orbital` — `head.gohtml` renders this. Home page where `PageTitle = "Orbital"` renders as just `Orbital`. Every handler must set `PageTitle` in the page data struct.
- **Never redeclare fields that already exist on embedded types** (`layout.Base`) — outer field shadows embedded one and template `{{.AppVersion}}` resolves to zero value.
- **Single-tab pages** (audit log, schema, divergence reports, signed artifacts) use `<p class="is-size-4">` + `<p class="has-text-grey">` heading, not `<nav class="tabs is-boxed">`. Keep `<div class="tab-content">` wrapper if page contains `.box` elements.
- **`ShowDCBack` / `dcCtx=1` pattern** — when a server tab is opened by drilling from a DC tab, URL includes `?dcCtx=1`. Handler sets `ShowDCBack: true`, renders back button (`is-warning` class — do not change to `is-link`), sets `data-reload-url`/`data-reload-target` on edit modal so post-save reload targets DC tab content.
- **`localStorage.serverTabs` is separate from `localStorage.tabs`** — DC tabs persist under `localStorage.tabs`; Servers page tabs persist under `localStorage.serverTabs`.
- **Edge delivery page** — route `/signed-artifacts`, template `signed-artifacts.gohtml`, template key `"signed-artifacts"`. No auto-poll — manual reload button only.
- **`updatedBy` and `updatedAt` excluded from audit log variable display** (`skipVars` in `orbital.js`) — system metadata, not user-supplied input. They remain in `details.variables` in the database.
- **REST-triggered audit events have no child row** — `renderPayload` returns `null` when `details.query` absent. Expand arrow also hidden via `createdRow`.
- **Startup log must use slog, not `log.Printf`** — `cmd/orbital/main.go` calls `slog.SetDefault` before anything else so startup line emits JSON consistent with all other output.
- **Standalone unauthenticated pages still include `head.gohtml`** — pages outside the main app shell (currently only `pages/device-code.gohtml`) must include `{{template "head.gohtml" .}}` so they pick up `?v={{.Version}}` cache-busting automatically. Page data struct needs `BasePath`, `Version`, `UI layout.UIConfig`, `PageTitle`.
- **Full-document templates live in `pages/`, not `partials/`** — `partials/` is for HTMX fragments swapped into an already-loaded page. Anything starting with `<!DOCTYPE html>` is a page.

## Timestamp rendering

- **`data-timestamp` must use RFC3339 format** — Go's `time.Time.String()` output (`2026-06-01 19:20:52.935848 +0000 UTC`) is NOT parseable by `new Date()` in JS. Always use `.Format "2006-01-02T15:04:05Z07:00"` for the `data-timestamp` attribute. Use a short human format (`.Format "2006-01-02 15:04"`) as the visible text fallback so columns stay narrow even before JS runs.
  ```gohtml
  <span data-timestamp="{{.SomeTime.Format "2006-01-02T15:04:05Z07:00"}}">{{.SomeTime.Format "2006-01-02 15:04"}}</span>
  ```
- **`renderTimestamps(document)` runs globally at DOMContentLoaded** — a single handler in `shared.js` calls `renderTimestamps(document)` on every page load. This covers full-page renders (e.g. import history). HTMX-swapped content calls `renderTimestamps(panel)` explicitly after swap — both paths are handled.
