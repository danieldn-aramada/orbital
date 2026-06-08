# Migrate UI tab URLs from DGraph UID to orbId

**Status:** Not started
**Scope:** Both orbital and orb. Three resource types touched: DataCenter, Server (and Rack, transitively via the DC detail tab).
**Why:** DGraph UIDs (`0x973745`) are unstable across reseeds and restores; `orbId` (`colo:colo-galleon`) is the project's canonical stable identifier. The export, backup, restore, and audit APIs were already migrated. The UI tab/detail routes (`GET /datacenters/:id`, `GET /servers/:id`) are the remaining holdouts that still embed a DGraph UID in user-visible URLs. See `docs/decisions/004-namespace-id-scalar-migration.md` for context on the broader Namespace/orbId scalar work and the precedent set by the export API migration.

## Settled decisions (do not reopen)

- **`orbId` is the canonical identifier** in user-visible URLs and persisted client-side state (`localStorage`).
- **Both orbital and orb migrate together** in one PR — the JS that constructs these URLs lives in `shared.js` and is used by both, so a split migration would leave one app broken.
- **URL encoding**: `orbId` contains `:` (e.g. `colo:colo-galleon`). Always pass values through `encodeURIComponent` in JS and `url.PathEscape` in Go where appropriate. Echo's `c.Param("orbId")` returns the decoded value automatically.
- **No alias / no compat shim**: replace `:id` with `:orbId` outright. localStorage tab state is per-browser; users will lose one session of tab restoration on first deploy. Acceptable.
- **Route param name stays semantic**: prefer `:orbId` over keeping `:id`. Self-documenting and grep-friendly.

## File-by-file change list

### 1. Go handlers — DC detail tab

**`internal/orbserver/dc_handlers.go`** — `dcTab` handler currently does:
```go
id := c.Param("id")
body, _ := json.Marshal(map[string]any{
  "query":     orbGetDataCenterQuery,
  "variables": map[string]any{"id": id},
})
```

Change:
- Rename param: `c.Param("orbId")`.
- Switch the query from `getDataCenter(id: ID!)` to `queryDataCenter(filter: { orbId: { eq: $orbId } })` returning a single-element array. Update `orbGetDataCenterQuery` constant accordingly.
- Update the response struct decoder: `result.Data.QueryDataCenter []orbDCQueryResponse` (array shape). Take element `[0]` or 404 if empty.
- The `id` field in the struct stays — it's still returned by DGraph and the template uses `{{.ID}}` to construct per-DC DOM IDs (e.g. `dc-detail-tabs-{{.ID}}`). **Consider switching these to OrbID** for consistency — see "Inner DOM IDs" below.

Mirror equivalent change in:

**`internal/handler/datacenter.go`** — orbital's `Tab(c echo.Context)` handler. Same shape: param → orbId, query → queryDataCenter with orbId filter.

### 2. Go handlers — Server detail tab

**`internal/orbserver/server_handlers.go`** — `srvTab` handler.
**`internal/handler/server.go`** — orbital's `Tab` handler.

Same pattern: param `:id` → `:orbId`, query `getServer` → `queryServer(filter: { orbId: { eq: ... } })`.

### 3. Echo route registrations

**`internal/orbserver/server.go`** (~line 142, 144):
```go
e.GET("/datacenters/:id", s.dcTab)   →  e.GET("/datacenters/:orbId", s.dcTab)
e.GET("/servers/:id", s.srvTab)      →  e.GET("/servers/:orbId", s.srvTab)
```

**`internal/server/server.go`** — orbital's equivalents:
```go
root.GET("/datacenters/:id", dc.Tab)   →  root.GET("/datacenters/:orbId", dc.Tab)
root.GET("/servers/:id", srv.Tab)      →  root.GET("/servers/:orbId", srv.Tab)
```

### 4. JS — tab opening callsites

**`web/shared/static/shared.js`** — `loadDataCenterTab(displayName, id)` and `loadServerListTab(displayName, id)`:

Rename `id` parameter to `orbId` throughout (function signature, all internal references, DOM IDs like `tab-${orbId}`, `tab-content-${orbId}`). The HTMX URL becomes:

```js
htmx.ajax('GET', BASE + '/datacenters/' + encodeURIComponent(orbId), { target: '#tab-content-' + orbId, swap: 'innerHTML' })
```

**Important**: DOM IDs cannot contain `:`. Replace `:` with `-` (or another safe char) when building DOM IDs from orbId. Suggested helper:

```js
function tabDomId(prefix, orbId) {
  return prefix + orbId.replace(/:/g, '-')
}
```

Use `tabDomId('tab-', orbId)` and `tabDomId('tab-content-', orbId)`. Keep the **URL** as the raw `encodeURIComponent(orbId)`. This decouples DOM IDs from URLs.

`TabItem` in shared.js currently has `id` field — rename to `orbId`. Update `saveTab`, `deleteTab`, `saveServerTab`, `deleteServerTab` parameter names and JSON keys.

**`web/shared/static/orbital.js`** and **`web/shared/static/orb.js`** — both have `initDatacenterTable({ onRowOpen: (data) => { ... data.id ... } })`. Change to `data.orbId`. Same for the server table init.

### 5. localStorage migration

Existing users have `localStorage.datacenterTabs` and `localStorage.serverTabs` keyed by old DGraph UIDs. On the first page load after deploy:
- The restoration loop in `initDatacenterTabRestoration` will read entries with the old `id` field, try to load `BASE + '/datacenters/' + <DGraphUID>`, and 404 (because the route now expects orbId).
- Acceptable to clear once: in `initDatacenterTabRestoration` / `initServerListTabRestoration`, detect old entries (missing `orbId` field, or shaped like `0x...`) and wipe the key. Log nothing — silent.

```js
// At top of restoration functions
try {
  const raw = JSON.parse(localStorage.datacenterTabs || '[]')
  const looksLikeOldUID = raw.some(s => /^0x[0-9a-f]+$/.test(JSON.parse(s).id || ''))
  if (looksLikeOldUID) localStorage.removeItem('datacenterTabs')
} catch { localStorage.removeItem('datacenterTabs') }
```

### 6. Templates — `data-server-id` and similar

**`web/templates/shared/partials/datacenter-tab.gohtml`** has:
```
<tr data-server-id="{{.ID}}" data-display-name="...">
```

This drives the document-level dblclick handler in `orbital.js`:
```js
document.addEventListener('dblclick', (e) => {
  const row = e.target.closest('tr[data-server-id]')
  ...
  const id = row.dataset.serverId
  window.location.href = BASE + '/servers?open=' + encodeURIComponent(id) + '&label=' + ...
})
```

Change template attr to `data-server-orb-id="{{.OrbID}}"` and JS to `row.dataset.serverOrbId`. Same for any `data-dc-id`, `data-rack-id` attributes — check via `grep -rn "data-.*-id=" web/templates/`.

The query string `?open=<id>&label=<label>` consumed by `initServerListTabRestoration` likewise needs to switch to `?open=<orbId>`.

### 7. Inner DOM IDs (recommendation, not required)

Several templates construct per-resource DOM IDs from `{{.ID}}` (DGraph UID): `dc-detail-tabs-{{.ID}}`, `dc-panel-servers-{{.ID}}`, `dc-servers-table-{{$.ID}}`, etc. These IDs work fine with DGraph UIDs (no `:` to escape).

If you switch them to OrbID, you must escape the `:`. Mechanically possible but verbose in templates. **Recommendation**: leave inner DOM IDs as `.ID` (UID) for now. The user-visible URL is what matters; internal DOM IDs are implementation detail. If you change them later, do it as a separate cleanup.

### 8. e2e tests

**`e2e/datacenter.spec.ts`** uses:
```js
const tabLink = page.locator('[id^="tab-"][id$="colo-galleon"]')
```
That's OK as-is — it suffix-matches the display name, not the ID.

**Check `e2e/navigation.spec.ts`** — if it directly tests URL paths with `0x...` UIDs, update to orbId.

Run `grep -rn "0x[0-9a-f]" e2e/` and update any hardcoded UIDs.

**Add an e2e test for orb's dblclick → tab flow**. There currently is none; that's the gap that let my earlier refactor regression slip through (see session notes 2026-06-08). Test:
1. Visit `/datacenter`
2. Wait for `#datacenter-table tbody tr` with text `colo-galleon`
3. dblclick the row
4. Assert `#tablist li.tab` count goes from 1 → 2
5. Assert the new tab content panel loads (`[id^="tab-content-"]` with non-empty innerHTML)

### 9. Integration tests

**`internal/handler/dc_tab_integration_test.go`** (and orb's equivalent if it exists) — update URL paths and assertions.

`grep -rn 'datacenters/\|servers/' internal/handler/ internal/orbserver/ --include="*_test.go"` to find all callsites.

### 10. Generated swagger docs

Both `docs/orb/swagger.yaml` and the orbital docs will need re-gen. Run `make docs` and `make orb-docs` at the end.

## Suggested commit boundary

One commit covering all of the above. Split is unrealistic — the JS, Go routes, and templates are coupled. Keep the PR contained to URL/ID migration only; do not bundle unrelated cleanup.

## Verification checklist

Before declaring done, manually verify with `make up && make run-orbital && make run-orb`:

1. Orbital `/datacenters` — dblclick row → new tab opens with URL `/datacenters/colo%3Acolo-galleon` requested via HTMX → content loads
2. Orbital `/servers` — same flow for a server
3. Orb `/datacenter` — dblclick → tab opens, content loads
4. Orb `/servers` — same
5. Browser hard-refresh: localStorage migration runs once silently, no stale UID entries cause 404 loops
6. Existing e2e suite passes: `make test-e2e` and `make test-e2e-orb`
7. No remaining references: `grep -rn ':id\|"id"' internal/orbserver/dc_handlers.go internal/orbserver/server_handlers.go internal/handler/datacenter.go internal/handler/server.go web/shared/static/shared.js | grep -v "// "` — should be empty for the touched handlers

## What to do if blocked

- **DGraph schema doesn't allow `@id` on orbId for filtering**: unlikely — `orbId: String! @id @search(by: [hash])` is already in `schema/schema.graphql`. Verify before touching anything.
- **Restoration loop breaks for unrelated reasons**: don't paper over with try/catch. Diagnose, then decide whether to expand the localStorage purge condition.
- **Test fixtures use UIDs**: regenerate seed data via `make seed`. Don't hardcode UIDs in new tests — derive from the seeded fixtures.

## Out of scope for this migration

- Renaming UID fields in DGraph or ent schemas. The `:id` → `:orbId` change is **URL-layer only**.
- Touching the audit log's `orbId` extraction (already migrated, see `docs/decisions/001-mutation-audit-recording.md`).
- The export job / backup job UUID-based URLs (`:jobId`) — those are server-generated UUIDs, not DGraph UIDs, and stay as-is.
