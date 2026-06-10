# UI Reference

Read this before: Go template changes, HTMX interactions, JavaScript, CSS/SCSS, any frontend work.

## Settled Decisions

- **Inventory namespace filter is page-level, not a DataTable column filter** — lives in the page header (above the table), uses regex search on the orbId column (`^namespace:`). Do not move it into the DataTable toolbar — namespace is a scope selector, not a column filter.
- **Vendor libraries in `web/shared/static/` use named subdirectories** — `datatables/`, `font-awesome-6.6.0/`, `vanilla-jsoneditor/`. Do not flatten into a single `vendor/` bucket. Each subdir contains only browser-required files — strip docs, type defs, LESS/SASS sources, and package.json before committing a new vendor library.
- **Go template `range` does not propagate variable assignments outward** — `$x = true` inside `{{range}}` does not affect `$x` after the range ends. Compute aggregates server-side (method on the struct). Example: `ImportRecord.DispatchErrors()` instead of a `$hasError` flag inside range.

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
- **Skeleton + min-delay on refresh** — show skeleton rows immediately, enforce 500ms minimum display with `Promise.all([fetch(...), new Promise(r => setTimeout(r, 500))])`. Add `is-loading` to the refresh button for the same duration. See `loadOrbTags()` and `fetchWithMinDelay()` in `app.js`.
- **Refresh button loading state** — add `id="btn-refresh-*"` to the button; JS adds/removes `is-loading` class around the fetch.

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
- **`updatedBy` and `updatedAt` excluded from audit log variable display** (`skipVars` in `app.js`) — system metadata, not user-supplied input. They remain in `details.variables` in the database.
- **REST-triggered audit events have no child row** — `renderPayload` returns `null` when `details.query` absent. Expand arrow also hidden via `createdRow`.
- **Startup log must use slog, not `log.Printf`** — `cmd/orbital/main.go` calls `slog.SetDefault` before anything else so startup line emits JSON consistent with all other output.
- **Standalone unauthenticated pages still include `head.gohtml`** — pages outside the main app shell (currently only `pages/device-code.gohtml`) must include `{{template "head.gohtml" .}}` so they pick up `?v={{.Version}}` cache-busting automatically. Page data struct needs `BasePath`, `Version`, `UI layout.UIConfig`, `PageTitle`.
- **Full-document templates live in `pages/`, not `partials/`** — `partials/` is for HTMX fragments swapped into an already-loaded page. Anything starting with `<!DOCTYPE html>` is a page.

## Timestamp rendering

- **`data-timestamp` must use RFC3339 format** — Go's `time.Time.String()` output (`2026-06-01 19:20:52.935848 +0000 UTC`) is NOT parseable by `new Date()` in JS. Always use `.Format "2006-01-02T15:04:05Z07:00"` for the `data-timestamp` attribute. Use a short human format (`.Format "2006-01-02 15:04"`) as the visible text fallback so columns stay narrow even before JS runs.
  ```gohtml
  <span data-timestamp="{{.SomeTime.Format "2006-01-02T15:04:05Z07:00"}}">{{.SomeTime.Format "2006-01-02 15:04"}}</span>
  ```
- **`renderTimestamps(document)` runs globally at DOMContentLoaded** — a single handler in `app.js` calls `renderTimestamps(document)` on every page load. This covers full-page renders (e.g. import history). HTMX-swapped content calls `renderTimestamps(panel)` explicitly after swap — both paths are handled.
