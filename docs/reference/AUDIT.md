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
| `level` | slog default | slog envelope (maps to OTel `SeverityText`) |
| `msg` | `"request"` | slog envelope (maps to OTel `Body`) |
| `http.request.method` | `v.Method` | OTel semconv |
| `url.path` | `v.URI` | OTel semconv |
| `http.response.status_code` | `v.Status` | OTel semconv |
| `client.address` | `v.RemoteIP` (Echo's `c.RealIP()`) | OTel semconv |
| `user_agent.original` | `v.UserAgent` | OTel semconv |
| `duration_ms` | `v.Latency.Milliseconds()` | **orbital-specific** — OTel reserves duration for spans/metrics; no log-record attribute exists |
| `actor` | `c.Get("user_email")` | **orbital-specific** — `enduser.id` was deprecated in OTel semconv with no stable replacement |

### Deliberately not emitted today

Add when the use case appears, not preemptively:

- `http.response.body.size`, `http.request.body.size` — not at scale where traffic analysis matters
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
- `resource_ids` (JSON array): all orbIds touched — extracted from three sources (see below).
- `details` jsonb: full raw payload `{operationName, query, variables}`.
- **Events are always recorded** for mutations touching known types regardless of `ifVersion` presence — MVCC is opt-in and orthogonal to eventing.

## extractResourceIDs — three sources

1. `variables["orbId"]` — single string (single-entity mutations)
2. `variables["input"]` array walk — bulk add mutations (`addServer(input: [...])`)
3. Recursive walk of the DGraph response JSON (`collectOrbIDs`) for every `"orbId"` value — covers nested creates and any entity the client selected orbId for

**Known gap:** mutations filtered by a non-orbId field (e.g. `filter: { hostname: {...} }`) where the client selects only `{ numUids }` — these record empty `resource_ids`. The full query is still in `details`. Post-MVP fix: post-mutation DGraph UID lookup.

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
- **Management event operation names use `verbNoun` camelCase** — current names: `createBackup`, `restoreBackup`, `exportSubgraph`, `publishArtifact`, `applySchema`. Do not use dot-namespaced names (`dgraph.restore`) or implementation-leaking prefixes (`trigger*`). "trigger" is an implementation detail; name what happened from the user's perspective.
