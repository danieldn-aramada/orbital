# Audit & Events Reference

Read this before: touching `internal/handler/graphql.go`, `internal/handler/audit.go`, the `audit_events` ent table, the HTTP request logger in `server.go`/`orbserver/server.go`, or audit log UI work.

## Two observability streams — different standards

| Stream | Standard | Storage | Retention | Configured in |
|---|---|---|---|---|
| **HTTP access log** | OpenTelemetry semantic conventions for HTTP server | stdout → OTel Collector → Loki / Azure Monitor | 30–90 days (TBD policy) | `internal/server/server.go` and `internal/orbserver/server.go` RequestLoggerWithConfig |
| **Security audit events** | AWS CloudTrail (structure) + OWASP Application Logging Cheat Sheet + NIST SP 800-92 | PostgreSQL `audit_events` table | **operator-configured** — see § Retention | `internal/handler/audit.go` `writeAuditEvent` |

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

## What the audit log is for

Orbital is an intent-only CMDB, never in the reconciliation path, so the only thing it can be authoritative about is what happened **at its own API boundary**:

> **Provenance for control-plane actions — every act that changed orbital's state, plus every authn/authz failure, and what each carried.**

Intent changes (`event_category: data`) are **one of three categories**, not the whole stream; `management` and `auth` change no intent.

### Row admission — two criteria, and only two

A row exists if and only if one of these holds. Everything else belongs in the app log.

1. **State changed** — a mutation committed, a policy was created/updated/deleted, a backup ran. This is the changelog half, and it is what makes `?orbId=` per-node lineage.
2. **A security event occurred** — an authn/authz failure (`loginFailed`, `authorizationDenied`). No state changed; these are an intrusion signal, expected by OWASP ASVS and ISO 27001 A.8.15, and GitHub records failed SSO/2FA the same way.

**Business-logic refusals and technical failures are NOT audited.** A `403 APPROVAL_REQUIRED` (the caller *has* the role — the workflow says "not yet") and a mutation rejected by DGraph both leave no row, only a `WARN` in the app log. The caller is present and holds the error; the durable record is the write that eventually lands. This is NetBox's model (`ObjectChange` rows only on commit) and GitHub's (a rejected push is a git error, not an audit entry), and it is what `!hasGQLErrors(respBytes)` in `graphql.go` already does — **ratified 2026-09-02, not newly built.**

**The one earned exception is advisory enforcement** (`backlog.md`): a would-have-been-blocked write must be recorded, because unlike a refusal there is **no caller reading the result** — the point is a report an operator reads later to decide whether to enforce. That asymmetry, not the success or failure of the write, is what decides admission; Kyverno writes a PolicyReport for the same reason.

**Because the app log is the answer, it has to be able to answer.** *(Added 2026-09-15.)* Routing refusals there makes that `WARN` the durable record, not a debugging aid — so it is written at the CHOKEPOINT (`checkPolicyFor` in `approval_gate.go`), where it covers `/graphql`, `DispatchMutation` and cascade-delete from one place, and it carries the same fields as the privileged-write line ten lines above it: policy, actor, role, types, **orb_ids**. It did not, until this was noticed: the bypass branch named what it touched, the refusal branch logged nothing at all, and two of the three entry points had no line anywhere — so the log could say someone was blocked without saying from changing what, while this section pointed readers at it. **Do not "simplify" it to policy + actor**, and do not add a second line at a call site: one refusal, one line. Pinned by `TestGate_RefusalIsLoggedOnceAndNamesTheEntity`, `…FromTheInternalDispatchPathToo`, and `TestGate_AllowedAndBypassedWritesLogNoRefusal` (the negative — a refusal signal that fires when nothing was refused trains people to ignore it).

Two consequences worth stating, because both are easy to get backwards. **A refusal is not evidence of nothing happening** — "how many writes were blocked pending review" is answerable from the app log, not this table, and that is deliberate. And **do not re-adopt CloudTrail's failed-call records** without reversing this section first (§ CloudTrail field parity explains what is being declined) — a half-adoption yields a table where some refusals appear and others do not, which is worse than either rule applied consistently.

**The rule:**

> The audit log answers *who did what, in one act, and when* — **per event, attributed**. It never answers *what is the state now* or *what is the net difference between two points*. Those are the Topology API and `internal/graphdiff`.

Both the audit log and `graphdiff` carry before/after values. The difference is **grain and attribution**, not field-level detail:

| | audit log | `graphdiff` |
|---|---|---|
| Grain | one declared act | two states, any distance apart |
| Attribution | yes — `actor` | none |
| `hostname: a → b → a` | **2 records** | **0 changes** |

That last row is why both surfaces can be correct while disagreeing — so **never label a `graphdiff` result and an audit result with the same noun in the same view**. The content diff owns "Changes"/"Diff"; the audit stream owns "Activity"/"Edits".

This is the split AWS makes between Config (*"what did my resource look like?"*) and CloudTrail (*"who made an API call to modify this resource?"*). CloudTrail has no before-state field at all; orbital's `details.before` is a deliberate improvement on it, and is why orbital needs no second store to render a diff.

## `acting_client` — who acted, when that is not who it is attributed to

A trusted upstream service (AEP's BFF) authenticates its own users and calls
orbital on their behalf. The token names a human, so `actor` is that human — but
the request was made by a service, and without recording that, the row says only
"daniel did X" and cannot distinguish a direct action from one taken through the
service. If that service's authorization has a bug, the audit log is the thing
that should be able to tell the difference, and it could not.

`acting_client` holds the OAuth client from the token's verified `azp`, and is
**set only when it differs from the subject**. A user signing in directly is their
own actor; stamping that on every row would make the column noise and train
readers to ignore it — the same reasoning as the `privileged` flag.

RFC 8693 calls this the actor and carries it in the `act` claim, which the IdP
signs. Orbital infers it from `azp` because it does not yet consume exchanged
tokens. **The inferred version is weaker in one specific way** — `azp` names the
client that requested the token, so under a chain of services it reflects the
last one rather than the whole path, and `act` nests where `azp` does not. Worth
replacing when token exchange lands, not worth waiting for.

## Naming — `audit_events` (storage) / `audit-log` (API)

**Settled 2026-08-26.** Both layers share the root noun `audit`:

| Layer | Name |
|---|---|
| Postgres tables | `audit_events`, `audit_event_resources`, `audit_event_resource_types` |
| ent types | `AuditEvent`, `AuditEventResource`, `AuditEventResourceType` |
| Go handler | `AuditHandler` in `internal/handler/audit.go` |
| REST API | `/api/v1/audit-log` |
| UI / Swagger tag | "Audit Log" / `audit` |

**Do NOT reintroduce a bare `Event` noun for audit data.** `event` is already contested in this codebase — OTel log records, divergence reports, edge Kubernetes events. Kubernetes hit the identical collision and had to mint an `audit.k8s.io` API group because core `v1 Event` (1-hour TTL, "best-effort, supplemental") was already taken; its audit events default to 366-day retention. GitLab compounds the same way and kept `audit_events` in all six of its sharded tables.

The API path stays `/api/v1/audit-log` — collection vs record, matching GitHub (`GET /orgs/{org}/audit-log`, records called "audit log events"). Renaming it to `/audit-events` would break AEP for no gain.

`writeAuditEvent` (package-level, persists a row) and `GraphQL.auditMutation` (builds mutation-shaped `details`, then delegates) are **two acts, not two names for one** — keep them distinct.

## CloudTrail field parity

CloudTrail is the structural anchor (see § Naming), so this is the checklist for what an audit row should carry. Verified against AWS's record-contents reference (`eventVersion` 1.11).

| CloudTrail field | Orbital | Status |
|---|---|---|
| `eventID` | `id` (uuid) | ✅ |
| `eventTime` | `timestamp` | ✅ |
| `eventCategory` | `event_category` | ✅ borrowed directly |
| `requestParameters` | `details.variables` | ✅ |
| `resources[]` | `audit_event_resources` + `_resource_types` | ✅ richer than CloudTrail |
| `eventName` | `operations[]` (array) | ~ **deliberate** — one row per HTTP request, so compound mutations produce an array |
| `userIdentity` (nested union) | `actor` (flat string) | ~ flattened; adequate at current scale. CloudTrail's is a discriminated union with only `type` required — do not half-adopt it |
| `responseElements` | — (we store `details.before` instead) | ~ **deliberate and better for a CMDB** — see below |
| `eventSource` | `event_source` | ✅ *(2026-09-15)* — `graphql` / `rest` / `internal` |
| `sourceIPAddress` | `source_ip_address` | ✅ *(2026-09-15)* — `c.RealIP()`; absent for internal writers |
| `userAgent` | `details.user_agent` on auth events only | ~ **deliberate** — CLAUDE.md settles UA at session boundaries (`loginSuccess`/`loginFailed`/`logout`), following Stripe/Datadog. Putting it on every data event would reverse that; do not "complete" this row without reversing it first |
| `errorCode` / `errorMessage` | — | ~ **deliberately not adopted** — a rejected write records no row at all; see below |
| *(no CloudTrail equivalent)* | `request_id` | ✅ *(2026-09-15)* — Echo's `X-Request-Id`, ties every event from one request together. CloudTrail has no such field; one request yields one record there, while orbital can emit several |
| `readOnly` | — | n/a — mutations only |

**An async job records `event_source: internal`, not the surface that triggered it.** *(Added 2026-09-15.)* Export, restore and scheduled backup write their audit row from a background worker, long after the HTTP request returned — so there is no IP and no request id to record, and `internal` is the truthful answer for all three. `actor` still carries who asked. The alternative (carrying the triggering request's origin into the job) needs the origin persisted on the job row and is deliberately not done: it buys correlation for three endpoints at the cost of a schema change per job type. If it is ever wanted, do it for all of them at once.

**Capture origin at the HTTP boundary and pass it BY VALUE — never hold the `echo.Context`.** The audit write runs in a goroutine (`go h.auditMutation(...)`) and Echo pools and reuses Context objects, so reading `c` after the handler returns yields whatever request recycled it next. That failure is invisible in testing and produces an audit row attributing a write to another caller's address — a wrong IP reads as fact, which is worse than a missing one. `originFromContext(c, source)` exists to be called on the handler's own goroutine; `auditOrigin` is a plain struct so it can safely outlive the request. Pinned by `TestWritePath_AuditEventRecordsWhereTheWriteCameFrom`.

**Deliberately NOT adopted: CloudTrail's failed-call records.** These three behaviours were listed here as "copy deliberately" until 2026-09-02, when they were reversed in favour of the NetBox/GitHub model — see § Row admission. Recorded so the decision is checkable rather than merely absent, and because each is a genuine CloudTrail behaviour someone will rediscover:

1. **Failed calls are recorded, and not only authorization failures.** AWS's worked example is a `TrailNotFoundException` — a resource-not-found error. `errorMessage` is documented as *"includes messages for authorization failures"*, i.e. authz failures are a **subset** of the scope, not the scope.
2. **The success/failure signal is the presence of `errorCode`**, not a status field. A failed call still carries full `requestParameters`.
3. **The failure reason outranks the payload.** In CloudTrail's truncation order for oversized events, `errorMessage` is dropped **last** — after `requestParameters` and `responseElements`.

Orbital declines all three. `authorizationDenied` and `loginFailed` still write rows, but under admission criterion 2 (security event), not because they failed — which is why they carry their reason in `details` and need no `errorCode` field.

If a future requirement does force failed-call records, **one CloudTrail weakness not to inherit:** AWS admits some services put `errorCode`/`errorMessage` at top level and others bury them inside `responseElements`. Make them mandatory top-level fields.

**Where orbital is deliberately ahead:** CloudTrail's `requestParameters` carries only what the caller sent, so a CloudTrail-shaped record **cannot render a field-level diff on its own** — that is precisely why AWS needs Config alongside it. Orbital's `details.before` gives us the diff without a second store. Keep it; it is a considered divergence, not drift.

## Retention

**Current state: nothing is pruned. There is no retention mechanism yet** — rows accumulate indefinitely. The decision below is settled design, not shipped behaviour.

**Decided: orbital is not prescriptive.** `ORBITAL_AUDIT_RETENTION_DAYS` (*not yet implemented*), **default `0` = retain indefinitely**; the operator sets it. Orbital cannot know an adopter's obligations, and four of six common frameworks leave the period to the organization (ISO 27001 A.8.15, NIST 800-53 AU-11, NIST 800-171 3.3.1/03.03.03, SOC 2 TSC). Only PCI DSS names a number (Req 10.5.1: 12 months, ≥3 immediately available).

**Do NOT restore a "≥ 12 months" claim here** — it was never ratified, and it asserts a compliance posture on the adopter's behalf.

⚠️ **The append-only trigger refuses `DELETE` unconditionally, so a pruner cannot simply delete.**
*(Added 2026-10-07.)* It needs a deliberate path — a privileged role, or a session flag the trigger
honours — designed as part of the pruner rather than pre-built. See § Append-only above.

Three rules for whoever implements pruning:
- **Ship the pruner in-process**, not as an optional external job. NetBox's 90-day default sat inert behind a cron many operators never installed; when 4.4 moved housekeeping into a built-in scheduler, the dormant default fired and deleted everything older than 90 days on upgrade.
- **Surface growth** (row count / oldest-record age) since the default is unbounded.


**Framework guidance for adopters** (orbital ships none of these as a default — see above). Two widely repeated figures are wrong or misattributed:

| Framework | Requirement | Retention |
|---|---|---|
| **PCI DSS v4.x** | Req 10.5.1 (v3.2.1: Req 10.7) | **12 months**, ≥3 immediately available |
| **SOC 2** | Trust Services Criteria | **No numeric retention in the TSC.** The practical ~12 months derives from the **Type 2 observation window** the auditor samples across — not from any criterion |
| **ISO 27001:2022** | A.8.15 Logging (consolidates 2013 A.12.4.1/.2/.3) | Organization-determined; ISO 27002 §8.15 supplies no number |
| **NIST 800-171 / CMMC** | Rev 2 §3.3.1 / Rev 3 §03.03.03 | ❌ **No 90-day minimum exists.** That figure comes from **DFARS 252.204-7012(e)** incident media preservation — a clock running *forward from an incident report*, not a retention floor |
| **NIST 800-53** | AU-11 | Organization-defined. AU-11(1) additionally requires proving long-term records can be *retrieved* |
| **HIPAA** | 45 CFR §164.316(b)(2)(i) | 6 years, for Security Rule *documentation* — never names audit logs. **N/A** for a CMDB holding no ePHI |
| **NetBox** (closest peer) | `CHANGELOG_RETENTION` | 90 days default; `0` = forever |


## Event model

The remaining sections describe the **security audit events** stream — structured rows written to PostgreSQL's `audit_events` table, distinct from the HTTP access log above. Captures mutations, auth failures, administrative operations.

OWASP alignment: actor, timestamp, event category, action, resource, and reason are captured today. **Gap**: `client.address` and `user_agent.original` are not yet on the event row — adding them is the next iteration on this stream.

- **One event per HTTP request**, not per entity. A compound GraphQL mutation touching multiple entities produces one event row.
- `operations` (JSON array): DGraph operation names extracted from the query body — e.g. `["updateDataCenter","updateServer"]`.
- `resource_types` (JSON array): all DGraph types touched.
- `resource_ids` (JSON array): all orbIds touched — extracted from five sources (see below).
- `details` jsonb: full raw payload `{operationName, query, variables}`, plus `before` when a single-entity mutation resolved one.
- **`authorization_type` / `authorization_id` — COLUMNS, not a key in `details`.** *(Added 2026-10-07.)* They name the act that authorized the write — `changeRequest`/`colo-58`, or `divergenceResolution`/`<entry uuid>`. Written by `DispatchMutation` from the `auditOrigin` its caller passes, present ONLY when an authority exists, and served as a top-level `authorization` object on the audit-log response (`{type, id}`) — `type` because that is the discriminator orbital already uses elsewhere and CloudTrail's `userIdentity.type`; `authorization` because Kubernetes labels this same concept `authorization.k8s.io/*` on its own audit events. **Columns because this is record metadata, not payload** — the same line the CloudTrail parity table above draws between `eventSource`/`sourceIPAddress` (columns) and `requestParameters` (nested in `details`). It was briefly stored in `details` AND promoted, which returned the same fact twice in one response; nothing else on this record does that. Pinned by `TestAuditLog_AuthorizationIsReturnedTopLevel`, which asserts the absence from `details` as well as the presence top-level. It is the positive counterpart to `bypassedPolicy` below: reviewed / break-glass / ungated. ⚠️ **Merge and divergence-resolve now carry a `request_id` despite `event_source: internal`** — that is NOT a violation of the async-job rule below, because both run inside the HTTP request that triggered them; export, restore and the scheduled backup are the async cases and still pass `auditInternal()`. Do not "fix" it.
- **`details.privileged` + `details.bypassedPolicy`** (Spike 36) — present ONLY when the write skipped an approval policy because the caller's role was in that policy's `bypass_roles`. Absent (not `false`) otherwise, so a query for `details ? 'privileged'` finds exactly the break-glass writes. The policy LABEL is carried (`<namespace>` or `<namespace>/<type>`) rather than a bare boolean, because the useful question is *which control was skipped*. **The audit row is the durable record, not the log line** — `approval_gate.go` also emits a `WARN`, but that is for an operator watching in real time; "who bypassed review last quarter" is asked from this table, by someone with no prior suspicion. A bypass that produced only a log line would satisfy the letter of "audited break-glass" and none of its purpose.
- **Events are always recorded** for mutations touching known types regardless of `version` presence — MVCC is opt-in and orthogonal to eventing.
- **Attribution ("who changed this") is a client-side join on `orbId`, not a new API.** A client pairs a content diff (e.g. the export preview) with `GET /api/v1/audit-log?orbId=<node>` to answer *who/when* per changed entity. Division of labour: **the diff answers *what* changed; the audit log answers *who/when*** — never use the audit log to compute *what* (see `OCI.md` § "Export preview" for why: it's an event stream, records mutation input rather than before→after, and a `dropAll` restore writes zero rows). **Node-level attribution is exact; field-level is best-effort** — the event stores the mutation *input* (`details.variables`), not a per-field owner, so "who set *this field*" is inferred from the most recent event whose variables include it. Do NOT add a field-history table to make it exact; that's the rejected antipattern.

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

- Package-level function in `internal/handler/audit.go`. Shared by `GraphQL.auditMutation`, `Export.Trigger`, `BackupHandler.Trigger`.
- Arguments: `*ent.Client`, `*slog.Logger`, actor, opName, operations, resourceTypes, resourceIDs, details map.
- **Failures are logged and swallowed** — audit writes must never block or fail a request.

## event_category values

Three values (stored as string, not enum — adding a value does not require ent codegen):

| Value | Used for |
|---|---|
| `"data"` | GraphQL mutations on entities (GraphQL proxy handler) |
| `"management"` | System operations: backup, restore, export, schema apply, authorizationDenied, approval-policy administration |
| `"auth"` | Login/logout events: loginSuccess, loginFailed, logout |

The audit tab query (`GET /api/v1/audit-log?orbId=...`) filters to `event_category IN ('data', 'management')`. Auth events are excluded structurally — they have no resource_ids and appear in every resource tab if included.

## Append-only is enforced by a PostgreSQL TRIGGER, not by grants

- **Do NOT "fix" this by revoking `UPDATE`/`DELETE` instead.** Orbital owns its schema — it runs
  ent's auto-migration, so it must — and a grant does not bind a table's owner. A trigger fires
  regardless of privilege, which is why this needs no role split and no deployment change.
- **All three tables are guarded.** A record's orbIds and resource types live in the children;
  guarding only the parent would let someone make an event unfindable without touching the event.
- **Do NOT add a bypass flag or session variable "for the pruner".** An escape hatch nobody uses is
  attack surface. The retention pruner needs a defined path designed with it, not a pre-built one.
- Installed and verified on every boot by `db.EnsureAuditGuard`; a missing *or disabled* trigger logs
  at ERROR and sets `orbital_audit_tables_unprotected`. **A disabled trigger counts as absent** — it
  reads as present to anything checking only for existence, and fires never.
- Does **not** stop a superuser (`session_replication_role = replica`) or filesystem access. Pinned by
  `TestAuditGuard_*`.

## ent conventions for events

- **Do not use `Immutable()` on ent schema fields** — immutability enforced at app layer (never call `.Update()` on event records). `Immutable()` causes migration pain: changing a field requires drop/recreate rather than ALTER.

## Settled Decisions

- **ONE table serves both provenance and lineage — do NOT split into two engines.** The question *"is `audit_events` a request log or a changelog?"* was asked 2026-09-02. The answer: it is CloudTrail's **structure** (`operations`, `actor`, `event_category`, resource links) carrying CloudTrail's **payload** (`details.variables` — what the caller sent) **plus a partial before-image** (`details.before`) that CloudTrail has no equivalent of. That before-image alone is what makes a field diff possible, and it is why orbital needs one store where AWS needs two: `requestParameters` cannot render a diff on its own, which is precisely why AWS Config exists beside CloudTrail. **Do not split.** AWS split because CloudTrail spans ~200 services with incompatible resource models; orbital has ONE resource model (`ConfigItem`) and ONE write chokepoint, so the reason does not transfer. NetBox likewise keeps a single `ObjectChange` table and no separate provenance store. **The real second engine already exists and is `internal/graphdiff`** — "net difference between two snapshots" is deliberately not the audit log (a `dropAll` restore writes zero rows). The correct three-way split: `audit_events` = who/when/what changed per act; `graphdiff` = net delta between two states; Topology API = current state.

- **Orbital is NetBox-shaped on row admission and NOT on payload — do not claim otherwise.** NetBox's `ObjectChange` stores a **full object snapshot on both sides** (`prechange_data` / `postchange_data`) and diffs at render time, so its changelog is complete for creates, deletes, and every field. Orbital stores the mutation **input** as the after-state and the type's **derived field set** as the before (`configitems.BeforeSelection`), then intersects them in `computeChanges`. Two consequences remain live: a create has no `before`, so `changes` is absent entirely, and a multi-entity event cannot be attributed per node. *(The third — a scalar missing from a hand-declared per-type `BeforeFields` producing no diff — was closed when the registry was replaced by schema-derived fields: the selection is now every field the type has. Closing the remaining two is `backlog.md` § "One audit event per entity" and Spike 35.)*

- **A `DispatchMutation` caller no longer has to supply `before` — the fetch happens for every write.** *(Changed 2026-09-03; this bullet used to say the opposite.)* The before-fetch, the `version`/`updatedAt`/`updatedBy` stamping and the `version` check all live in `writeToDGraph`, the one function both entry points pass through. They used to live in `Handle`, so `DispatchMutation` got none of them and left each to the caller — and both callers forgot a different one: **merge** passed a `before` holding only `version` plus the `clear` fields, so its audit row rendered raw variables where a diff belongs; **divergence-Accept** passed a good `before` but wrote intent with no version bump, which made an Accept invisible to change-request staleness. Neither failed anything at the time. **What to know when adding a call site:** dispatch the canonical `update{Kind}($orbId, $set)` shape — the row to stamp is resolved from the `orbId` VARIABLE, so an inline selector or a `$filter` object resolves to nothing and you silently get neither the stamp nor the diff. Pass `before` ONLY when you hold a field `BeforeSelection` does not read (divergence-Accept does: it names its field at runtime); it is merged UNDER the fetch, field by field, never instead of it.

- **ent auto-migration does NOT rename tables — the `events` → `audit_events` rename orphans history in every already-deployed database.** `cmd/orbital/main.go` runs `db.Schema.Create(ctx, migrate.WithDropColumn(true))`, which reconciles *columns* within a table it can match by name. A renamed table is simply an unknown table: ent creates `audit_events` / `audit_event_resources` / `audit_event_resource_types` **empty** and leaves `events` / `event_resources` / `event_resource_types` sitting beside them. Nothing is deleted, but every historical row becomes invisible to `GET /api/v1/audit-log` and the audit tab. Column shapes are otherwise identical — the only difference is the FK column `event_id` → `audit_event_id` — so a recovery copy is mechanically simple if one is ever needed.
  **Decision for AKS dev, 2026-08-31: accept the reset.** The old tables stay in place as the recovery path. **Do NOT hand-massage table, index, and FK constraint names on a live database to simulate the rename** — the constraint names travel with the old tables and reconciling them by hand is exactly the class of work Spike 27 (Atlas migrations) exists to remove. Any environment whose audit history is load-bearing must wait for a real migration.
- **`event_category` values are fixed: `"data"` / `"management"` / `"auth"`** — see event_category section above. Do not add new categories without discussion. The audit tab filters to `data` and `management` — adding a fourth category requires auditing every query filter.
- **Management event operation names use `verbNoun` camelCase** — current names: `createBackup`, `deleteBackup`, `restoreBackup`, `exportSubgraph`, `publishArtifact`, `deleteArtifact`, `applySchema`, `acceptDivergence`, `forceDivergence`, `ignoreDivergence`, `createApprovalPolicy`, `updateApprovalPolicy`, `deleteApprovalPolicy`. Destructive operations (`deleteBackup`, `deleteArtifact`) MUST emit an event — a delete with no audit trail is a compliance gap (audit S.10). Do not use dot-namespaced names (`dgraph.restore`), implementation-leaking prefixes (`trigger*`), or bare verbs (`accept`). The verb alone reads ambiguously in the global audit log — every action name should answer "what was done?" without the reader having to look up the resource. The `divergence_resolutions.action` column still stores the raw verb (`accept`/`force`/`ignore`) — that's internal state, not an audit operation name.
- **A control that decides what gets audited must itself be audited.** Approval-policy create/update/delete write `management` events carrying `before`/`after` (`policyFields`), the namespace as the resource id, and an explicit `enforcementStopped` / `enforcementStarted` flag. Shipped 2026-08-30 after the gap was found the hard way: a write that BYPASSED a policy was recorded, while deleting the policy that would have gated it left nothing — so "who turned the gate off" was unanswerable, and a policy deleted by accident was unrecoverable. Three rules fall out and apply to any future control of this kind. **Read the row before changing it** — "required_approvals is 1" does not answer "was the bar lowered". **A delete must carry the whole object**, because after it the event is the only record the thing existed. **A refused write records nothing** — a trail entry for a change that never took effect is worse than none. (Since 2026-09-02 this is the general rule, not a policy-admin quirk: see § Row admission.) Pinned by `TestApprovalPolicyAudit_*` (all reading back through `GET /api/v1/audit-log`, not the table) and `e2e/policy-admin-audit.spec.ts` (reading the page a person actually opens).
- **REST audit-log API is node-specific; nested-resource aggregation lives in the UI layer.** `GET /api/v1/audit-log?orbId=X&orbId=Y&orbId=Z` returns events whose resources contain ANY of the listed orbIds (strict EQ per id, OR'd together — no graph traversal in the handler). The page composer that knows the parent→child schema relationships is responsible for building the list. Capped at 32 ids. Do NOT add a `include=related` server-side join — knowledge of the edge belongs in the page composer that already pulled the nested orbIds via GraphQL, not the audit endpoint.
- **Audit allowlist and before-fields are DERIVED, never hand-maintained.** The mutation regex comes from the resolved view set and `BeforeSelection` from the schema-derived field set — a type that resolves a view auto-extends both. A type absent from the view set: mutations succeed but write ZERO audit events (the regex does not match `add<Type>` / `update<Type>` / `delete<Type>`). See `docs/playbooks/add-configitem.md` for the recipe.
- **Add-mutation response selections MUST include `{ <payloadField> { orbId } }` so the audit extractor can link the new resource.** `extractResourceIDs` finds orbIds in three places: `variables["orbId"]`, `variables["input"][i].orbId`, and the response body. The configitem-editor JS module always includes the registry-declared `PayloadField` in its add-mutation response selection so this is automatic for editor-driven flows. If you write a custom mutation that uses a non-`input` variable name, you must include orbId in the response — otherwise the event lands with empty `event_resources` and is invisible to per-orbId panel queries.
- **The audit before-fetch is FLAT — it must never reach into an owned child.** *(Settled 2026-10-06.)* `changes` is `keys(before) ∩ keys(set)` (`computeChanges`), so the MUTATION decides the shape and a before field the mutation never set can only be dropped. One mutation also only ever reaches one type's scalars: a nested object in `set` links by `@id` and its values are discarded (see DGRAPH.md), which is why the editor dispatches one mutation per concrete type. A child sub-selection therefore widens the stored `before` snapshot with fields no diff can reach. The parent still sees its children's events — that subgraph is **read-side, by orbId** (§ below), and that is the only place ownership belongs in the audit pipeline. Pinned by `TestBeforeSelection_DoesNotReachIntoOwnedChildren`. *(Removed after the claim in the code — "the audit `changes` must cover whatever one mutation through this page could have altered" — was checked against `computeChanges` and did not hold. A comment asserting a property is not evidence of it.)*
- **For EDITS dispatch canonical `update{Kind}(input: { filter: { orbId: { eq: $orbId } }, set: $set })` — `add{Kind}.upsert=true` does NOT produce diffs.** The diff renderer reads `variables["set"]` as the after-state; the before-fetcher reads `variables["orbId"]`. Only this combination yields the green/red field-level diff. `add{Kind}` mutations have no before-state and render raw variables — correct for creates, unacceptable for edits. The `configitem-editor.js` module routes existing-row edits through `update{Kind}` and first-time creates through `add{Kind}` automatically based on the initial snapshot — no per-page edits needed.
- **A page's audit tab covers exactly its declared subgraph (`subgraphOrbIDs`)** — the same set its delete removes and the change-request scope pin uses. *(2026-10-07; replaces the ownership roll-up and page boundary.)* One walker only. Over 128 orbIds the audit API 400s; narrowing is the page's job.
- **An interface-typed owned member needs an inline fragment — `orbId` lives on `ConfigItem`, not on a sub-interface.** *(Fixed 2026-10-06.)* `OwnedOrbIDSelection` emitted bare `orbId` on `DataCenter.kubernetesClusters`, which DGraph rejects at VALIDATION. The scope-pin expander batches every root into ONE aliased query and reads a failed query as "owns nothing", so a single interface member returned an **empty subgraph for every root in the batch** — silently, for every change request touching a data centre. The same lesson is already written down twice (`delete_cascade.go:idSel`, `generic.go:idNameSel`); `viewresolve.go` was the copy that missed it. Pinned by `TestOwnedOrbIDSelection_SurvivesAnInterfaceTypedMember`.
- **Audit diff is generic — excludelist, not allowlist.** `buildDiffHTML` in `audit.go` diffs every field in `before ∩ after` (minus metadata: `id`, `version`, `orbId`, `namespace`, `createdAt/By`, `updatedAt/By`, `version`). Adding a new ConfigItem type produces diffs out of the box. Patch-style mutations (`update{Type}(input: {filter, set: $set})`) have after-values under `variables["set"]`; flat-shape edits keep them top-level. Both work. **Do NOT reintroduce per-type renderer blocks** — the previous nested-iDRAC block was removed 2026-06-20 once Server edit started dispatching parallel `updateServer`+`updateIdracSettings` mutations.
- **The JSON `changes` field is the client-facing diff; `computeChanges` is the single source of truth.** `computeChanges(before, variables)` returns `[]{field, before, after}` (raw typed values, metadata excluded); `buildDiffHTML` is a pure renderer over it, so the API's `changes` and the HTML panel can never disagree. `eventItem.Changes` is `omitempty` — **present ONLY for a clean single-entity update**, omitted for multi-op / bulk-add / create events. Clients branch on its presence, not on `operations`/`before`. **Do NOT add a second diff implementation** — extend `computeChanges`. Pinned by `TestComputeChanges`.
- **Never expose DGraph UIDs in audit output.** `stripDGraphIDs` (recursive) removes every `id` from `before` before it's persisted into `details` — UIDs are internal and reassigned on restore/reimport; clients key on `orbId`. It's a **write-time** strip: new events are clean, historical events keep their UIDs (the log is immutable — do not rewrite it). Pinned by `TestStripDGraphIDs`.
- **`before` on `DispatchMutation` is a FALLBACK, not a substitute for the fetch.** *(Corrected 2026-09-03. This bullet previously said internal dispatchers supply `before` themselves and that divergence-Accept thereby avoided a DGraph round-trip. That optimisation is gone, deliberately: the next version is one more than the current one, so a write that stamps `version` must read it — "no extra read" and "Accept bumps the version" cannot both hold, and the version bump is what makes an Accept visible to change-request staleness.)* `writeToDGraph` reads current state on every write and overlays it on whatever the caller passed, **field by field, fetched winning**. The overlay is not ceremony: `dispatchAcceptMutation` resolves its field name at runtime, so for a field outside the selection the caller's value is the only before-state there is. (It is far less likely to bite now that `BeforeSelection` derives every field from the schema rather than a hand-declared per-type list.) Cost: one extra read per Accept, and one per merged item that merge had already read. Both are human-driven and low-frequency; optimise later with an explicit skip hint, not by making the guarantee optional again.
- **Resolved divergence freeze-vs-supersede behavior on re-ingest** is documented in [DIVERGENCE.md "Re-ingesting an already-resolved entry"](./DIVERGENCE.md#re-ingesting-an-already-resolved-entry). Audit history of the prior decision is preserved in the `events` table even when the resolution row is superseded; the `divergence_resolutions` table holds the CURRENT decision only.
- **The stamper and the audit differ must resolve the patch variable through ONE helper (`resolveSetMap`) — never by reading `variables["set"]` separately.** *(Added 2026-09-14.)* The patch need not be named `set`: `update{Kind}($serverOrbId, $patch)` is legal GraphQL and orbital accepts it. The differ used to read `variables["set"]` and, failing that, fall back to diffing against the **whole variables map** — which for `set: $patch` intersects `{serverOrbId, patch}` with the before-state, matches nothing, and renders an audit event with **no changes at all**. The write lands normally; there is no error, no warning, and nothing in the log. **This is the failure mode to watch for in this file generally:** a stamping bug 400s or leaves a null column, but a diffing bug produces a well-formed row that is simply empty, and nobody notices until someone asks what changed. Pinned by `TestVariableResolution_RenamedUpdateIsStampedAndDiffedLikeTheCanonicalOne`, which asserts the two spellings are indistinguishable downstream rather than merely that the renamed one succeeds — "it returned 200" would have passed throughout the bug. `computeChanges`/`buildDiffHTML` therefore take the mutation **query** as well as the variables; `details.query` has always been persisted, so historical rows resolve too.
- **`version`/`updatedAt`/`updatedBy` are server-stamped on the single write path — clients must NOT supply them.** `graphql.go stampMutation`, called from `writeToDGraph` (moved out of `Handle` 2026-09-03, so internal dispatchers get it too), injects `version` (auto-increment) + `updatedBy`=the authenticated actor + `updatedAt`=server clock into the mutation `set` (UPDATE), and `createdBy`/`createdAt`/`updatedBy`/`updatedAt` into the input (ADD) — from the authenticated identity, so they're consistent and unspoofable across the UI, orbctl, and direct API clients alike. Client-side stamping was removed from `configitem-editor.js` (2026-07-28); it is kept ONLY for the nested first-time-create subgraph, which the proxy can't recurse into (there's a comment there). The audit log records the same actor+timestamp independently and remains the authoritative provenance.
- **Server metadata stamping runs ONLY on the variable-based mutation path — inline single-entity mutations silently skip it (Spike 31).** `fetchCurrentState` reads `req.Variables["orbId"]`/`["id"]`, falling back to the single `orbId: { eq: $anyName }` reference in the query (`resolveWriteSelector`, 2026-09-14 — the variable's NAME is irrelevant, only that exactly one row is named), so the selector AND the patch must be passed as GraphQL variables — `update{Kind}(input: { filter: { orbId: { eq: $orbId } }, set: $set })` with `variables { orbId, set }`. An **inline** `filter: { orbId: { eq: "…" } }` written in the query string is invisible to the gate, so `version` isn't bumped and `updatedAt`/`updatedBy` stay null (verified empirically 2026-07-28). **Spike 31 (implemented 2026-07-28)** rejects such mutations with a `400 VARIABLE_FORM_REQUIRED` (see `docs/reference/ERROR-RESPONSES.md`) whose `error`/`hint` name the caller's actual type. The guard (`h.rejectInlineSelectors` in `graphql.go`, default on, kill switch `ORBITAL_INLINE_SELECTOR_REJECT=false`) fires when an `update`-prefixed op on a known type lacks a variable selector OR a variable `set` map. Option D — reject, do NOT rewrite the load-bearing query string; Option B (extract-inline→variables) stays the documented upgrade path if external inline clients ever appear. Bulk `in:[…]`, non-orbId filters, compound multi-mutation documents, and inline `add(input:[…])` literals pass through unstamped by design. All orbital-authored clients (UI `configitem-editor.js`, orbctl `patch_dc.go`) already use the variable form.
