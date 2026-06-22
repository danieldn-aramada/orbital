# UI Reference

Read this before: Go template changes, HTMX interactions, JavaScript, CSS/SCSS, any frontend work.

## Settled Decisions

- **Inventory namespace filter is page-level, not a DataTable column filter** — lives in the page header (above the table), uses regex search on the orbId column (`^namespace:`). Do not move it into the DataTable toolbar — namespace is a scope selector, not a column filter.
- **Vendor libraries in `web/shared/static/` use named subdirectories** — `datatables/`, `font-awesome-6.6.0/`, `vanilla-jsoneditor/`. Do not flatten into a single `vendor/` bucket. Each subdir contains only browser-required files — strip docs, type defs, LESS/SASS sources, and package.json before committing a new vendor library.
- **Go template `range` does not propagate variable assignments outward** — `$x = true` inside `{{range}}` does not affect `$x` after the range ends. Compute aggregates server-side (method on the struct). Example: `ImportRecord.DispatchErrors()` instead of a `$hasError` flag inside range.
- **Field-value row ordering in detail views: identity/hierarchy field(s) first, then alphabetical.** "Identity fields" are the non-boolean scalars that describe what the row IS (Server: `hostname`, `model`, `manufacturer`; IdracSettings: `firmwareVersion`; KubernetesCluster: `name`, `kubernetesVersion`). Hierarchy fields (parent references) come before identity fields when both apply — e.g. Server's summary starts with Data Center, THEN identity fields, THEN alphabetical for the rest. Boolean state flags always sort alphabetically. Rationale: identity-first reflects how operators scan ("which thing am I looking at?" then "what's its state?"); alphabetical fallback eliminates per-field judgment calls and stays stable as fields are added. Reference: Server summary in `server-tab.gohtml`; IdracSettings in `server-tab.gohtml`. Schema declaration order is NOT the source of truth — schema order reflects when fields were added, not display intent.
- **Field-value labels in detail views use the schema field name in monospace, not human-readable labels.** Section headers and tab labels stay human ("iDRAC Settings", "Storage", "Network" — these are navigation). The leftmost cell of a `key: value` row is the *field identifier* and uses the GraphQL schema name verbatim (`sshEnabled`, not "SSH Enabled"), rendered with Bulma's `is-family-monospace`. Reasons: (1) audit log, API, seed data, tests already use schema names — one vocabulary across surfaces eliminates mental translation; (2) orbital's promise is graph-native truth, and visible truth requires visible field identifiers; (3) audience is technical even when role-mixed. Excludes: column headers in tabular lists (`<th>Manufacturer</th>` is navigational), and derived fields not present in the schema verbatim (e.g. a formatted "Last Seen" timestamp). Do NOT add tooltip-style human-label translations — that recreates the vocabulary fork. Do NOT smooth or abbreviate schema names — if a name like `osToIdracPassThroughEnabled` feels long, fix the schema or accept the length; do not translate in the UI.
- **User-visible UI strings use vendor-neutral terms for category-level resources, not protocol/vendor names.** Buttons, labels, page titles, error messages, and status text use "Object Store" / "object storage" — NOT "S3", "Azure Blob", "MinIO". Reason: orbital runs against Azure Blob in production (via the AWS SDK's S3-compatible client), MinIO locally, and any S3 vendor on other deploys. Writing "S3" in the UI misleads operators who'd otherwise look in the wrong cloud console. **Exclusions:** env var names (`ORBITAL_S3_BUCKET`), Go struct field names (`S3Endpoint`), and internal log keys (`s3Key`) stay as-is — they're internal vocabulary that signals "S3 protocol API" rather than "AWS S3 product," and renaming has deployment cost. Apply this rule to: user-clickable button labels, page subtitles, help text, configuration-warning messages, and error messages surfaced to the UI. Same rule applies to other vendor names that have a category equivalent (e.g., "PostgreSQL" in user UI → "database" unless the user choice of engine is the point).
- **Edit pattern — one JSON editor at the parent, no per-child Configure / Edit / Delete UI.** Parent ConfigItems (Server, DataCenter, KubernetesCluster) render a SINGLE Edit button that opens a JSON editor whose tree IS the parent + every owned-child ConfigItem nested inline. The page handler exports a `targets` JSON blob (one entry per editable entity, built by `configitems.BuildEditTargets`); the generic `configitem-editor.js` module owns snapshot/diff/dispatch — each changed subtree fires its own canonical `update{Kind}(orbId, set)` mutation in parallel, producing a clean colored diff per affected concrete type. **Do NOT add a per-child modal, "Configure" flow, or per-row Edit/Delete buttons for an owned child.** Read-only sub-tabs displaying current state are fine (e.g. cluster's Backups tab). The pattern's intentional trade-off: lower field discoverability vs forms; if it becomes unacceptable, propose a hybrid (form-with-advanced-JSON toggle) and apply it codebase-wide, never deviate for one feature. Reference implementations: Server → IdracSettings; Cluster → ClusterBackup → EtcdBackup/VeleroBackup/S3Sync. See `docs/playbooks/add-configitem.md` for the recipe.
- **Adding a new ConfigItem family is registry-driven; no JS or audit-pipeline edits needed.** The `internal/configitems.Types` list is the single source of truth — append one entry per type (Name, OwnerType, FormFields, BeforeFields, PayloadField, Implements). The audit pipeline's allowlist regex, before-fetch fields, and the page handlers' edit targets all derive from it. `configitem-editor.js` consumes the targets emitted by the page handler, so adding a new sub-kind to an existing parent doesn't touch JS at all. See the playbook for the canonical 3-step recipe; this used to be ~13 places.
- **Do NOT use native HTML `title=""` for user-facing tooltips.** Browser-controlled — ~1s delay before display, small system font, no theming, no smart positioning. For interactive UI use the existing `.tooltip` + `data-text=""` CSS class in `web/sass/main.scss:187` (instant on hover, themeable). Long-term: migrate to Tippy.js — tracked in ROADMAP technical debt. New code MUST NOT add `title=` attributes for user-facing tooltips; existing ones get migrated when touched.
- **Color convention — Bulma color classes are reserved for state classification.** `is-danger` (errors, failures, destructive actions), `is-warning` (needs attention, unverified, degraded), `is-success` (completed, healthy, verified), `is-info` (informational tags, neutral highlights). Do NOT apply these to ordinary value text — a cell showing a config value is not "danger" just because it differs from intent. `<code>` is rendered in default text color with a subtle background — its differentiation from prose is typographic (monospace), not chromatic. Bulma's default `--bulma-code` is overridden in `main.scss` to neutralize the hot-pink red, which would otherwise consume the danger-color channel for purely typographic styling. If you reach for `has-text-danger` on something that isn't an error, find a different signal.

## Core rules

- **JavaScript is split into ES modules — no bundler** — `web/shared/static/shared.js` (utilities used by both orbital and orb), `web/shared/static/orbital.js` (orbital-only features), `web/shared/static/orb.js` (orb-only features). `head.gohtml` conditionally loads `orbital.js` or `orb.js` based on `{{.UI.AppName}}`. Never inline `<script>` blocks in templates.
- **`window.*` bridge for `onclick` handlers** — ES modules don't expose functions to global scope. Functions called from template `onclick="fn()"` attributes must be explicitly assigned: `window.fn = fn` at the bottom of the relevant module. `DOMContentLoaded` listeners and delegated event handlers work fine without the bridge.
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

## Iterating on CSS / templates

- **Static assets are aggressively cached by browsers.** When a CSS or template change doesn't appear after server restart, **hard-refresh first** (Cmd+Shift+R). The `?v={{.Version}}` cache-bust on `head.gohtml` covers _real_ deploys, not iterative dev changes within the same `Version` value. Don't chase phantom JS / HTMX bugs before ruling out cache.
- **Templates hot-reload from disk; Go handlers do not.** Editing a `.gohtml` file in dev mode picks up on the next page request. Editing a `.go` file requires restarting orbital (`make run-orbital`).

## DataTables + Bulma

- **Page length `<select>` needs a Bulma wrapper** — DataTables renders a bare `<select>`; Bulma needs `<div class="select is-small">` for the custom arrow. Wrap after init: `initComplete: function() { dtWrapLengthSelect(this.api()) }`.
- Use **Bulma modifier classes** (e.g. `is-small`) in `initComplete`, not CSS overrides — Bulma sizes via CSS custom properties, so `font-size` overrides don't work.
- **`stateSave: true` on all main page tables** — persists length/search/sort/position in localStorage across navigations. Applied to: inventory, datacenter, server list, audit log tables. Exclude embedded per-tab tables (e.g. `dc-servers-table`) — they reinit on every tab load.
- `.field` adds `margin-bottom` that breaks flex alignment in DataTables toolbar — avoid in toolbar layouts. `vertical-align` on `dt-length` is also ignored in flex context.

## Storage conventions

- **sessionStorage** → API response data (e.g. inventory rows) — clears on tab close, always fresh on new session. Data copies go here.
- **localStorage** → UI state (tab positions, filter selections, DataTables state) — persists across sessions. User preferences go here.
- **Logout clears both** — `localStorage.clear()` and `sessionStorage.clear()` called before POST. Next login starts with no tab state.
- **Inventory sessionStorage cache + `searchCols` pre-filter** — rows fed to DataTables at init time from cache, eliminating ajax flash on revisit. Saved type filter passed as `searchCols` so filtered state is the first and only draw. Reload button clears cache, empties table visually (`clear().draw()`), then refetches. `populateTypeDropdown()` called after data is available (not in `initComplete`).

## Tab state conventions

- DC detail tab state (Servers/Racks/Divergence) persists per DC under `localStorage.dc-detail-tab-{id}` — **cleared on tab close** so reopening always defaults to Servers. Do not persist across tab close/reopen.
- Servers page tabs persist under `localStorage.serverTabs`; DC tabs under `localStorage.tabs` — separate keys, same `TabItem` class pattern.

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
