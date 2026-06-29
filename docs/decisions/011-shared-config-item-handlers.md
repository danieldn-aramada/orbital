# 011 — Shared ConfigItem handlers across orbital and orb

**Status:** Implemented
**Date:** 2026-06-20 (cluster); 2026-06-25 (DC + Server + JS extraction)

## Context

Orbital (cloud) and orb (edge) both render UI surfaces for the same ConfigItem types (DataCenter, Server, Cluster). They share the same DGraph schema and the same fragment templates in `web/templates/shared/partials/*-tab.gohtml`. The only behavioral differences between the two apps are:

| Aspect | Orbital | Orb |
|---|---|---|
| Edit modal | Yes (role-gated) | No |
| Delete control | Yes (role-gated) | No |
| Audit Log tab | Yes | No |
| Reload control | Yes | Yes |
| `BasePath` prefix | Configurable | `""` |
| DGraph URL | Cloud DGraph | Edge DGraph |
| `actorFromContext` | Returns real identity | Empty |

Today, each app maintains its own parallel handler, struct, and template-data builder:

| Concern | Orbital path | Orb path |
|---|---|---|
| DC tab handler | `internal/handler/datacenter.go::DataCenterHandler.Tab` | `internal/orbserver/dc_handlers.go::Server.dcTab` |
| DC tab struct | `dcTabData` (orbital, ~150 lines, ~30 fields) | `dcTabData` (orb, ~14 fields — drifted) |
| Server tab handler | `internal/handler/server.go::ServerHandler.Tab` | `internal/orbserver/server_handlers.go::Server.srvTab` |
| Server tab struct | `serverTabDetailData` (~40 fields) | `orbSrvTabData` (~25 fields — drifted) |
| Cluster tab handler | `internal/handler/cluster.go::ClusterHandler.Tab` | (was missing entirely — added 2026-06-20 by reusing orbital's handler with injected actions) |
| GraphQL query | `getDataCenterQuery`, `getServerQuery`, `getClusterQuery` | Re-defined inline per orb handler |

### The drift class

Because both apps render the same template but each maintains its own struct, every template change in orbital silently breaks orb if orb's struct doesn't get the same field added.

Recent burns (2026-06-20):

1. Orbital added `.DomID`, `.DataCenterDomID`, `.DataCenterOrbID` references to `web/templates/shared/partials/server-tab.gohtml`. orb's `orbSrvTabData` lacked all three. Go template execution aborted at the first `{{.DomID}}` reference — orb returned **HTTP 200 with a 281-byte body** (template wrote out everything before the first missing field, then errored silently). The orb Server tab rendered with the reload button only — no Hostname, no Service Tag, no Rack, no nothing.
2. Same shape on the DC tab: 5 separate `{{.DomID}}` references in `datacenter-tab.gohtml` caused orb to render the summary + metadata sections, then abort before any of the Racks/Audit/Servers inner-panel content. The user saw a Data Center Summary with the right counts but no way to drill in.
3. `/clusters` existed in orbital but was never ported to orb at all. The user expected parity and didn't get it.

None of these failures surfaced as 500s, panics, or log errors. The bug class is: **template execution that aborts mid-render returns 200 OK with a partial body**, which the page-load smoke tests ("does `/datacenter` return < 500?") trivially pass.

## Decision

### Today (2026-06-20, shipped)

1. **Inject `actions` resolver into `ClusterHandler`** (`internal/handler/cluster.go`). Signature change:

   ```go
   func NewClusterHandler(..., actions func(echo.Context) layout.PageActions) *ClusterHandler
   ```

   - Orbital wires the existing closure that reads `can_mutate` from the context.
   - Orb wires `func(echo.Context) layout.PageActions { return layout.OrbActions }`.
   - Both apps now share one DGraph query, one struct, one renderer — the only difference is the actions value baked into the response data, which the shared template already consumes via `{{if .Actions.Edit}}` / `{{if .Actions.Delete}}` / `{{if .Actions.ShowAuditTab}}`.

2. **Add missing fields to orb's `dcTabData` and `orbSrvTabData`** (`.DomID`, `.DataCenterOrbID`, `.DataCenterDomID`). The DGraph query for the orb server tab also gained `dataCenter.orbId`. Restores parity end-to-end.

3. **Add orb `/clusters` + `/clusters/:orbId` routes**, reusing orbital's `ClusterHandler`. orb's `clustersPage` is a thin renderer for the list page; the tab handler is orbital's — unchanged — with orb's actions resolver injected.

4. **e2e regression coverage** (`e2e/orb.spec.ts`) — three new tests guard the partial-render bug class:
   - `datacenter tab fragment renders populated data` (dbl-click row, assert summary + count)
   - `cluster tab fragment renders populated data` (dbl-click row, assert summary + provider)
   - `orb cluster tab has no Edit / Delete controls` (asserts the actions-injection seam: `data-cluster-edit-id` and `data-cfg-delete-id` have count zero)

### Next (post-MVP spike)

The cluster handler is now correct by construction. The DC and Server handlers still maintain parallel structs. Collapse them on the same model:

1. **DataCenter:** Add `actions func(echo.Context) layout.PageActions` to `DataCenterHandler`. orb's `dcTab` deleted; orb registers `cluster.Tab`-style with the orbital handler.
2. **Server:** Same for `ServerHandler`.
3. **Shared query module:** Move `getServerQuery`, `getDataCenterQuery`, `getClusterQuery` to `internal/handler/queries.go` (they're already identical between apps in spirit; orb's slightly slimmer queries become the orbital query — orbital's edit-modal data fields are populated only when `Actions.Edit` is true).

After (1)-(3), orb's `internal/orbserver/dc_handlers.go` and `server_handlers.go` collapse to ~20 lines each: just the page handler + route registration. The DGraph query, struct, and template render all live in `internal/handler/` and are exercised by both apps' e2e suites.

## Principle

> **One template ↔ one builder ↔ one navigation script.** The view layer (shared `*.gohtml` fragments) is already unified. The data layer (struct + builder) must be too. The JS interaction layer must be too. Per-app variance is injected through:
>
> 1. **`layout.PageActions`** — declarative capability gating, already consumed by the template.
> 2. **`BasePath`** — URL prefix string, already threaded through.
> 3. **`actorFromContext(c)`** — identity helper, already centralized; returns empty for unauthenticated callers (the orb case).
> 4. **DGraph URL** — constructor parameter.
>
> Anything more is duplication waiting to drift.

## JS layer drift (extension, 2026-06-20)

The struct/template duplication has a mirror at the JS layer. `web/shared/static/orbital.js` and `web/shared/static/orb.js` are parallel app entry points that both `import` from `shared.js`. But the page-level event handlers — `document.addEventListener('dblclick'/'click', ...)` blocks that wire row dblclicks, link clicks, and reload buttons — live inline in `orbital.js` and have to be hand-mirrored into `orb.js`.

### The bug that surfaced this

After today's struct/template fix, the user dblclicked a **workload child row** in orb's `/clusters` list and nothing happened. Root cause: `orbital.js:153-157` has a `document.addEventListener('dblclick', ...)` against `tr[data-cluster-orb-id]` that navigates to `/clusters?open=<orbId>`. The main DataTable dblclick handler in `shared.js::initClusterTable` explicitly excludes `cluster-child-row` (it expects orbital.js to handle them). orb.js never had the corresponding navigation handler. So the workload-row dblclick was a no-op in orb — a silent dead click.

Same class as the struct drift: orbital owns the handler, orb is a partial copy, and the failure mode is silent (an event fires, nothing happens, no console error).

### Handler inventory

Cross-app navigation / reload handlers in `orbital.js` that **must** also work in orb (orb is a read-only mirror of orbital data — every navigation that makes sense in orbital makes sense in orb):

| orbital.js line | Selector | Action |
|---|---|---|
| 48 | `tr[data-server-id]` (dblclick) | Open server tab in DC detail panel |
| 130 | `tr[data-server-orb-id]` (dblclick) | Navigate `/servers?open=<orbId>` |
| 146 | `.js-cluster-link[data-cluster-orb-id]` (click) | Navigate `/clusters?open=<orbId>` |
| 153 | `tr[data-cluster-orb-id]` (dblclick) | Navigate `/clusters?open=<orbId>` ← **today's drift bug** |
| 160 | `tr[data-server-id]` (dblclick, DC panel) | Navigate `/servers?open=<orbId>` |
| 731 | `.js-cluster-reload` (click) | Reload cluster tab via HTMX |
| 741 | `.js-dc-reload` (click) | Reload DC tab via HTMX |
| 760 | `.js-srv-reload` (click) | Reload server tab via HTMX |

Orbital-only handlers (must NOT move — they only apply when `Actions.Edit == true`):

| orbital.js line | Selector | Action |
|---|---|---|
| 535 | `[data-dc-edit-id]` (click) | Open DC edit modal |
| 651 | `[data-cluster-edit-id]` (click) | Open cluster edit modal |
| 818 | `[data-srv-edit-id]` (click) | Open server edit modal |

### Extraction plan

Add three exported initializers to `shared.js`:

```js
export function initRowNavigation()      // 5 dblclick handlers (server/cluster row navigation)
export function initLinkNavigation()     // 1 click handler (.js-cluster-link)
export function initReloadButtons()      // 3 click handlers (.js-{dc,srv,cluster}-reload)
```

Both `orbital.js` and `orb.js` call all three. Edit-modal handlers stay in `orbital.js` because they're guarded by `Actions.Edit` and would no-op anyway in orb — but keeping them out of shared.js makes the read-only contract explicit at the file level.

After extraction, the relevant blocks in `orbital.js` (~30 lines) and `orb.js` (~20 lines) collapse to three lines each:

```js
initRowNavigation()
initLinkNavigation()
initReloadButtons()
```

A new navigation pattern (e.g. dblclick on a new resource type) is added in **one place** and both apps get it automatically.

### Bundled with the Spike 011 follow-up

The same spike that collapses `DataCenterHandler` and `ServerHandler` (today's pattern for `ClusterHandler`) should do this JS extraction. Same drift class, same fix shape, same review boundary. Doing it as one spike means the principle is enforceable from CI in one pass: e.g. a lint that forbids `document.addEventListener('(click|dblclick)'` outside `shared.js` unless the handler is gated on an `Actions.*` flag.

## Consequences

**Won today (verified by e2e):**
- One handler for clusters across both apps. Adding a field to the cluster template requires updating one struct, not two.
- orb gains read-only Clusters parity with orbital.
- New regression tests pin the partial-render bug class.

**Also shipped (2026-06-25):**
- `DataCenterHandler` and `ServerHandler` collapsed — same `actions` injection pattern as Cluster. orb's `dcTab`/`srvTab` and all parallel structs/queries deleted from `internal/orbserver/`.
- `initRowNavigation()`, `initLinkNavigation()`, `initReloadButtons()` extracted to `shared.js` and exported. Both `orbital.js` and `orb.js` call all three. orb.js dead navigation handlers removed. DataTables reload buttons get `dt.one('error.dt', ...)` guard so spinner clears on AJAX error.

## Alternatives considered

1. **Keep parallel structs, just add a doc convention requiring orb developers to mirror every orbital template change.** Rejected: this is exactly what we had, and the burn class repeats. Conventions don't survive contact with template changes that look local from the orbital side.
2. **One mega-struct with all orbital fields, orb just doesn't populate them.** This is effectively what we're doing — `Actions` flips the template branches; unused fields are zero-valued. The "shared handler" approach formalizes it.
3. **Templatize the orb-specific behavior with separate `_orb.gohtml` files.** Rejected: doubles the template-maintenance surface, doesn't solve drift — it inverts which template drifts behind the other.

## References

- `internal/web/data/layout/actions.go` — `PageActions`, `OrbitalActions(canMutate)`, `OrbActions`.
- `internal/handler/cluster.go` — first handler refactored with injected actions resolver.
- `e2e/orb.spec.ts` — regression coverage for the partial-render class.
