# Audit & Events Reference

Read this before: touching `internal/handler/graphql.go`, `internal/handler/event.go`, the `events` ent table, the HTTP request logger in `server.go`/`orbserver/server.go`, or audit log UI work.

## Two observability streams — different standards

| Stream | Standard | Storage | Retention | Configured in |
|---|---|---|---|---|
| **HTTP access log** | OpenTelemetry semantic conventions for HTTP server | stdout → OTel Collector → Loki / Azure Monitor | 30–90 days (TBD policy) | `internal/server/server.go` and `internal/orbserver/server.go` RequestLoggerWithConfig |
| **Security audit events** | OWASP Application Logging Cheat Sheet + NIST SP 800-92 | PostgreSQL `event` table | ≥ 12 months (TBD policy) | `internal/handler/event.go` `writeAuditEvent` |

They overlap (both capture `client_ip`, `actor`) but serve different audiences (ops vs. security/compliance) and have different retention. **Do not collapse the two streams** — operational log volume blows up audit storage; audit detail is wasted on stdout.

## HTTP access log — fields & convention

orbital and orb both emit a JSON log line per request with `msg: "request"`. Attribute names follow [OpenTelemetry HTTP server semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/http-spans/) so logs map cleanly onto Azure Monitor / App Insights via the OTel Collector pipeline (no transform-processor renames needed).

| Field | Source | Convention |
|---|---|---|
| `time` | slog default | slog envelope (maps to OTel `Timestamp` at the bridge) |
| `level` | slog default | slog envelope (maps to OTel `SeverityText`); escalates with status: 5xx → ERROR, 4xx → WARN, else INFO |
| `msg` | `"request"` | slog envelope (maps to OTel `Body`) |
| `http.request.method` | `v.Method` | OTel semconv |
| `url.path` | `v.URI` | OTel semconv |
| `http.response.status_code` | `v.Status` | OTel semconv |
| `http.response.body.size` | `v.ResponseSize` | OTel semconv |
| `client.address` | `v.RemoteIP` (Echo's `c.RealIP()`) | OTel semconv |
| `duration_ms` | `v.Latency.Milliseconds()` | **orbital-specific** — OTel reserves duration for spans/metrics; no log-record attribute exists |
| `request.id` | `v.RequestID` (from Echo `RequestID` middleware) | **orbital-specific** — correlates across log lines and into the audit event table |
| `graphql.operation.name` | `c.Get("graphql.operation.name")` set by `graphql.go` | OTel GraphQL semconv; only present on `/graphql` requests |
| `graphql.operation.type` | `c.Get("graphql.operation.type")` set by `graphql.go` | OTel GraphQL semconv; `"query"` or `"mutation"` |
| `actor` | `c.Get("user_email")` | **orbital-specific** — `enduser.id` was deprecated in OTel semconv with no stable replacement; orb omits this field (no auth) |

**User agent is NOT in access logs.** `user_agent.original` is captured in `loginSuccess` / `loginFailed` / `logout` audit events only (Stripe/Datadog convention: UA correlation belongs at session boundaries, not in high-volume per-request logs). Do not re-add it to access logs.

### Deliberately not emitted today

Add when the use case appears, not preemptively:

- `http.request.body.size` — not at scale where traffic analysis matters
- `referer` — blank for API calls; noise
- `url.full`, `url.scheme`, `server.address`, `server.port` — single-host service, scheme always known from context
- `network.peer.address`, `network.peer.port` — useful only when distinguishing peer (Istio sidecar) from client; `client.address` already resolves X-Forwarded-For
- `http.route` — Echo doesn't expose the matched route pattern out of the box; useful for high-cardinality URL grouping but we're not building dashboards needing it yet
- `trace_id`, `span_id` — not generating them yet; add when distributed tracing is wired (see ROADMAP observability spike)

### Skipper

Both apps skip access logs for `/static/*` and `/favicon.ico` (asset noise). Orbital additionally skips `/auth/device/poll` (long-poll endpoint, ~1/sec per active login flow — would dominate logs).

### X-Forwarded-For trust

Echo's `c.RealIP()` reads `X-Forwarded-For` and trusts it from **any** caller by default. Behind Istio this is fine — the threat of header spoofing is bounded by the cluster perimeter. If orbital ever gets exposed to public traffic without a stripping proxy, configure `e.IPExtractor` with a trusted-proxy list.

## Event model

The remaining sections describe the **security audit events** stream — structured rows written to PostgreSQL's `event` table, distinct from the HTTP access log above. Captures mutations, auth failures, administrative operations. Retention is long because regulatory frameworks (SOC 2, ISO 27001) expect 12+ months.

OWASP alignment: actor, timestamp, event category, action, resource, and reason are captured today. **Gap**: `client.address` and `user_agent.original` are not yet on the event row — adding them is the next iteration on this stream.

- **One event per HTTP request**, not per entity. A compound GraphQL mutation touching multiple entities produces one event row.
- `operations` (JSON array): DGraph operation names extracted from the query body — e.g. `["updateDataCenter","updateServer"]`.
- `resource_types` (JSON array): all DGraph types touched.
- `resource_ids` (JSON array): all orbIds touched — extracted from five sources (see below).
- `details` jsonb: full raw payload `{operationName, query, variables}`.
- **Events are always recorded** for mutations touching known types regardless of `ifVersion` presence — MVCC is opt-in and orthogonal to eventing.

## extractResourceIDs — five sources

1. `variables["orbId"]` — single string (single-entity mutations).
2. `variables["input"]` array walk — bulk add mutations (`addServer(input: [...])`).
3. `variables["filter"]["orbId"]` — `eq` (single) and `in` (list). Used by `update{Type}` / `delete{Type}` when the caller passes the filter as a `$filter` variable (e.g. `dispatchAcceptMutation` for divergence Accept). Missing this branch caused mutations dispatched through `DispatchMutation` to record empty `resource_ids` and become unfindable by orbId.
4. Inline `orbId: { eq: "..." }` matched in the query body string — for callers that inline the filter literal.
5. Recursive walk of the DGraph response JSON (`collectOrbIDs`) for every `"orbId"` value — covers nested creates and any entity the client selected orbId for.

**Known gap:** mutations filtered by a non-orbId field (e.g. `filter: { hostname: {...} }`) where the client selects only `{ numUids }` — these record empty `resource_ids`. The full query is still in `details`. Post-MVP fix: post-mutation DGraph UID lookup.

**Test:** `TestExtractResourceIDs` in `internal/handler/graphql_test.go` pins every source — add a new case there before adding any new source.

## Operation detection

- `knownMutationRe` regex matches `(add|update|delete)(DataCenter|Server|...)` in the raw query string.
- **Adding a new mutable type requires updating this regex** in `internal/handler/graphql.go`.
- Tech debt: `vektah/gqlparser` AST walking is the right long-term fix. Add when regex causes real problems. Tracked in ROADMAP.md technical debt.

## writeAuditEvent helper

- Package-level function in `internal/handler/event.go`. Shared by `GraphQL.writeEvent`, `Export.Trigger`, `BackupHandler.Trigger`.
- Arguments: `*ent.Client`, `*slog.Logger`, actor, opName, operations, resourceTypes, resourceIDs, details map.
- **Failures are logged and swallowed** — audit writes must never block or fail a request.

## event_category values

Three values (stored as string, not enum — adding a value does not require ent codegen):

| Value | Used for |
|---|---|
| `"data"` | GraphQL mutations on entities (GraphQL proxy handler) |
| `"management"` | System operations: backup, restore, export, schema apply, authorizationDenied |
| `"auth"` | Login/logout events: loginSuccess, loginFailed, logout |

The audit tab query (`GET /api/v1/events?orbId=...`) filters to `event_category IN ('data', 'management')`. Auth events are excluded structurally — they have no resource_ids and appear in every resource tab if included.

## ent conventions for events

- **Do not use `Immutable()` on ent schema fields** — immutability enforced at app layer (never call `.Update()` on event records). `Immutable()` causes migration pain: changing a field requires drop/recreate rather than ALTER.

## Settled Decisions

- **`event_category` values are fixed: `"data"` / `"management"` / `"auth"`** — see event_category section above. Do not add new categories without discussion. The audit tab filters to `data` and `management` — adding a fourth category requires auditing every query filter.
- **Management event operation names use `verbNoun` camelCase** — current names: `createBackup`, `restoreBackup`, `exportSubgraph`, `publishArtifact`, `applySchema`, `acceptDivergence`, `forceDivergence`, `ignoreDivergence`. Do not use dot-namespaced names (`dgraph.restore`), implementation-leaking prefixes (`trigger*`), or bare verbs (`accept`). The verb alone reads ambiguously in the global audit log — every action name should answer "what was done?" without the reader having to look up the resource. The `divergence_resolutions.action` column still stores the raw verb (`accept`/`force`/`ignore`) — that's internal state, not an audit operation name.
- **REST audit-log API is node-specific; nested-resource aggregation lives in the UI layer.** `GET /api/v1/audit-log?orbId=X&orbId=Y&orbId=Z` returns events whose resources contain ANY of the listed orbIds (strict EQ per id, OR'd together — no graph traversal in the handler). The page composer that knows the parent→child schema relationships is responsible for building the list. Capped at 32 ids. Do NOT add a `include=related` server-side join — knowledge of the edge belongs in the page composer that already pulled the nested orbIds via GraphQL, not the audit endpoint.
- **Audit allowlist + before-fields are derived from `internal/configitems.Types` — single source of truth.** `knownMutationRe` and `BeforeFields()` in graphql.go both come from the registry; never hand-maintain either. Adding a new ConfigItem type to the registry auto-extends both. Without the type in the registry: mutations succeed but write ZERO audit events (the regex doesn't match `add<Type>` / `update<Type>` / `delete<Type>` calls). Pinned by `internal/configitems/registry_test.go::TestParity_WithLegacyHandMaintainedValues`. See `docs/playbooks/add-configitem.md` for the recipe.
- **Add-mutation response selections MUST include `{ <payloadField> { orbId } }` so the audit extractor can link the new resource.** `extractResourceIDs` finds orbIds in three places: `variables["orbId"]`, `variables["input"][i].orbId`, and the response body. The configitem-editor JS module always includes the registry-declared `PayloadField` in its add-mutation response selection so this is automatic for editor-driven flows. If you write a custom mutation that uses a non-`input` variable name, you must include orbId in the response — otherwise the event lands with empty `event_resources` and is invisible to per-orbId panel queries.
- **For EDITS dispatch canonical `update{Kind}(input: { filter: { orbId: { eq: $orbId } }, set: $set })` — `add{Kind}.upsert=true` does NOT produce diffs.** The diff renderer reads `variables["set"]` as the after-state; the before-fetcher reads `variables["orbId"]`. Only this combination yields the green/red field-level diff. `add{Kind}` mutations have no before-state and render raw variables — correct for creates, unacceptable for edits. The `configitem-editor.js` module routes existing-row edits through `update{Kind}` and first-time creates through `add{Kind}` automatically based on the initial snapshot — no per-page edits needed.
- **Every parent ConfigItem with owned children MUST aggregate their audit events into the parent's tab.** Mirror the canonical pattern: handler exports `collect*RelatedOrbIDs(raw)`; tab data gets a `RelatedOrbIDsCSV`; audit `<li>` carries `data-related-orb-ids`; `shared.js initXDetailTabs.loadAuditPanel` reads it and OR-queries the audit endpoint. References: `server.go::collectRelatedOrbIDs` + `cluster.go::collectClusterRelatedOrbIDs`. (Future: derive this walker from `configitems.Children()` so adding a new owned child no longer requires touching the parent's collector — currently still hand-written.)
- **Audit diff is generic — excludelist, not allowlist.** `buildDiffHTML` in `event.go` diffs every field in `before ∩ after` (minus metadata: `id`, `version`, `orbId`, `namespace`, `createdAt/By`, `updatedAt/By`, `ifVersion`). Adding a new ConfigItem type produces diffs out of the box. Patch-style mutations (`update{Type}(input: {filter, set: $set})`) have after-values under `variables["set"]`; flat-shape edits keep them top-level. Both work. **Do NOT reintroduce per-type renderer blocks** — the previous nested-iDRAC block was removed 2026-06-20 once Server edit started dispatching parallel `updateServer`+`updateIdracSettings` mutations.
- **The JSON `changes` field is the client-facing diff; `computeChanges` is the single source of truth.** `computeChanges(before, variables)` returns `[]{field, before, after}` (raw typed values, metadata excluded); `buildDiffHTML` is a pure renderer over it, so the API's `changes` and the HTML panel can never disagree. `eventItem.Changes` is `omitempty` — **present ONLY for a clean single-entity update**, omitted for multi-op / bulk-add / create events. Clients branch on its presence, not on `operations`/`before`. **Do NOT add a second diff implementation** — extend `computeChanges`. Pinned by `TestComputeChanges`.
- **Never expose DGraph UIDs in audit output.** `stripDGraphIDs` (recursive) removes every `id` from `before` before it's persisted into `details` — UIDs are internal and reassigned on restore/reimport; clients key on `orbId`. It's a **write-time** strip: new events are clean, historical events keep their UIDs (the log is immutable — do not rewrite it). Pinned by `TestStripDGraphIDs`.
- **Internal dispatchers supply `before` themselves.** `GraphQL.DispatchMutation(ctx, actor, query, variables, before)` does NOT auto-fetch the before-state — that's a user-facing concern in `Handle`. Callers that want a diff in the audit row pass `before` directly. For divergence-accept, `dispatchAcceptMutation` builds `before` from `entry.IntendedValue` (already captured at ingest time — no extra DGraph round-trip). For other internal mutations, the caller picks the cheapest source of truth.
- **Resolved divergence freeze-vs-supersede behavior on re-ingest** is documented in [DIVERGENCE.md "Re-ingesting an already-resolved entry"](./DIVERGENCE.md#re-ingesting-an-already-resolved-entry). Audit history of the prior decision is preserved in the `events` table even when the resolution row is superseded; the `divergence_resolutions` table holds the CURRENT decision only.
- **`version`/`updatedAt`/`updatedBy` are server-stamped in the `/graphql` proxy — clients must NOT supply them.** `graphql.go Handle` injects `version` (auto-increment) + `updatedBy`=`actorFromContext(c)` + `updatedAt`=server clock into the mutation `set` (UPDATE), and `createdBy`/`createdAt`/`updatedBy`/`updatedAt` into the input (ADD) — from the authenticated identity, so they're consistent and unspoofable across the UI, orbctl, and direct API clients alike. Client-side stamping was removed from `configitem-editor.js` (2026-07-28); it is kept ONLY for the nested first-time-create subtree, which the proxy can't recurse into (there's a comment there). The audit log records the same actor+timestamp independently and remains the authoritative provenance.
- **Server metadata stamping runs ONLY on the variable-based mutation path — inline single-entity mutations silently skip it (Spike 31).** The before-fetch gate reads `req.Variables["orbId"]`/`["id"]`, so the selector AND `set` must be passed as GraphQL variables — `update{Kind}(input: { filter: { orbId: { eq: $orbId } }, set: $set })` with `variables { orbId, set }`. An **inline** `filter: { orbId: { eq: "…" } }` written in the query string is invisible to the gate, so `version` isn't bumped and `updatedAt`/`updatedBy` stay null (verified empirically 2026-07-28). **Spike 31 (implemented 2026-07-28)** rejects such mutations with a `400 VARIABLE_FORM_REQUIRED` (see `docs/reference/ERROR-RESPONSES.md`) whose `error`/`hint` name the caller's actual type. The guard (`h.rejectInlineSelectors` in `graphql.go`, default on, kill switch `ORBITAL_INLINE_SELECTOR_REJECT=false`) fires when an `update`-prefixed op on a known type lacks a variable selector OR a variable `set` map. Option D — reject, do NOT rewrite the load-bearing query string; Option B (extract-inline→variables) stays the documented upgrade path if external inline clients ever appear. Bulk `in:[…]`, non-orbId filters, compound multi-mutation documents, and inline `add(input:[…])` literals pass through unstamped by design. All orbital-authored clients (UI `configitem-editor.js`, orbctl `patch_dc.go`) already use the variable form.
