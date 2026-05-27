# UI Reference

Read this before: Go template changes, HTMX interactions, JavaScript in `app.js`, CSS/SCSS, any frontend work.

## Core rules

- **All JavaScript goes in `web/static/app.js`** — never inline `<script>` blocks in templates.
- **All styles go in `web/sass/main.scss`** — never edit `web/static/css/main.css` directly (generated). Rebuild: `make build-css` (one-time) or `make watch-css` (watch mode).
- `make run-orbital` uses version `dev` — avoids noisy git-describe strings in local dev. `make build-orbital` and `make push` still use full `$(VERSION)`.

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
