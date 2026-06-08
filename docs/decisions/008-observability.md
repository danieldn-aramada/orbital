# 008 — Observability Approach

**Date:** 2026-06-08
**Status:** Decided — implementation tracked as Spike 21
**Context:** AKS cluster already has a complete monitoring stack (OTel gateway
→ Azure Monitor, Prometheus Agent → Mimir, Loki for logs). Orbital is not
producing signals to any of them. This ADR records the approach for closing
that gap.

---

## Decision

Adopt the **OpenTelemetry SDK** as the single instrumentation surface for
orbital. Emit traces and logs over OTLP to the cluster's OTel gateway;
expose Prometheus metrics on `/metrics` and register a `ServiceMonitor` so
the Prometheus Agent scrapes them.

**One SDK, three signals, no per-signal client library.**

---

## Why OTel SDK (not direct Prometheus client + Loki push + custom traces)

The cluster's monitoring stack is already standardized on OTLP at the
ingestion edge:

- The OTel gateway accepts OTLP gRPC and HTTP, applies tail sampling and k8s
  attribute enrichment, and forwards to Azure Monitor.
- Loki accepts OTLP logs directly on `/otlp/v1/logs` — the same wire
  protocol as traces.
- Only metrics are still scraped pull-style by Prometheus Agent — and
  orbital already has a `/metrics` endpoint via the prometheus client_golang
  library.

Alternative considered: ship traces via a vendor SDK
(`microsoft/ApplicationInsights-Go`), logs via a Loki HTTP client, keep
Prometheus as-is. Rejected because:

1. **Three libraries to maintain instead of one.**
2. **Coupling to Azure Monitor at the code level.** Switching backends later
   (Tempo, Jaeger) becomes a code change instead of a gateway config change.
3. **No context propagation across signal types.** OTel correlates traces
   and logs via `trace_id` automatically; ad-hoc libraries do not.
4. **No alignment with the gateway's processors.** Tail sampling expects to
   see complete OTLP traces; vendor SDKs may format spans differently and
   subtly miscategorize.

The OTel SDK is the path of least friction for both today and the next time
the cluster adds a backend.

---

## What gets instrumented (MVP)

Three layers, in priority order:

### 1. HTTP request tracing (middleware)

A trace middleware on the Echo router wraps every request in a server span.
Attributes:

- `http.method`, `http.route` (Echo path pattern, not raw URI — keeps
  cardinality bounded), `http.status_code`
- `orbital.actor` — `actorFromContext(c)` when authenticated; absent for
  anonymous routes
- `orbital.orb_id` — when the handler extracts an orbId from path or body
  (set inside the handler, not the middleware)

The existing `metrics.Middleware()` stays — it owns the `/metrics` export.
The new trace middleware is additive and runs alongside it.

### 2. Export pipeline spans

`POST /api/v1/export` and its async job are the most operationally
interesting code path: they touch DGraph scratch, Blue, S3, and OCI. One
root span per job (`orbital.export_job`) with child spans for:

- `dgraph.scratch.drop` (drop_all on scratch)
- `dgraph.export` (the actual export mutation)
- `oci.bundle` (per-bundler HTTP call — one span each)
- `oci.publish` (sign + push)

This gives the operator a single Application Insights trace per job that
explains where time was spent — useful for triaging slow publishes.

### 3. Structured logs via OTel log bridge

Replace the bare `slog.NewJSONHandler` in `cmd/orbital/main.go` and
`internal/server/server.go` with a multi-handler that fans out to:

- stdout JSON (preserved for local dev, kubectl logs, debugging)
- OTel log provider (forwarded to Loki via the gateway)

Existing `slog.Info(...)`/`slog.Error(...)` call sites do not change. The
multi-handler ensures the log records carry the active `trace_id` and
`span_id` automatically, so a log line in Loki links back to its trace in
Application Insights.

---

## What is skipped for MVP

- **DGraph client spans.** The handlers use raw `http.Post` to DGraph today
  (tech debt — see `docs/findings/maintainability.md` 2.1). Instrumenting
  raw `http.Post` requires a wrapper around every call site. Defer until the
  DGraph client abstraction lands. Until then, the export pipeline spans
  capture the *total* DGraph time per phase — good enough for MVP triage.
- **Backup scheduler spans.** The scheduler ticks every 60s; tracing each
  tick is noise. Trace only the actual backup job creation path (`createBackup`
  audit operation already records the trigger). Add later if scheduler
  behavior becomes a debug target.
- **Outbound HTTP propagation.** Bundlers and OCI registries are external
  systems. Span propagation across the wire requires both ends to speak
  OTel; orbital cannot control bundler implementations. Emit the client-side
  span and stop.
- **Custom metrics beyond what exists.** `internal/metrics/metrics.go`
  already exposes `http_requests_total`, `http_request_duration_seconds`,
  `graphql_operation_errors_total`. Adding export job duration, backup job
  duration, etc. is a follow-up — the trace data covers it for MVP.
- **Dashboards-as-code.** Build one "orbital overview" dashboard in
  Grafana's UI after metrics flow. Export to JSON and commit later if it
  proves stable.

---

## Service identity

| Resource attribute | Value | Source |
|---|---|---|
| `service.name` | `orbital` | constant |
| `service.version` | `v0.x.y` | `internal/version.Version` (ldflags) |
| `service.namespace` | (k8s namespace) | injected by gateway k8sattributes processor |
| `k8s.deployment.name` | `orbital` | injected by gateway k8sattributes processor |
| `k8s.pod.name` | (pod name) | injected by gateway k8sattributes processor |

Orbital sets only `service.name` and `service.version`. The OTel gateway's
k8sattributes processor adds the cluster topology fields based on the source
pod IP. Do not duplicate them in the SDK — they would override the
authoritative values.

---

## Graceful degradation

**Orbital must run correctly without the cluster stack.** Local development
does not have the OTel gateway available.

The OTel SDK initialization:

1. If `OTEL_EXPORTER_OTLP_ENDPOINT` is unset → register noop tracer and
   logger providers. All `tracer.Start(...)` calls succeed and produce
   non-recording spans; all log forwards are dropped. Code paths are
   unchanged.
2. If the endpoint is set but unreachable at startup → log a warning, fall
   back to noop. Do not block startup. The exporter library has its own
   retry; we should not duplicate it.
3. Stdout JSON logging is **always on**, regardless of OTel state. Local
   dev and `kubectl logs` still work.

This mirrors the pattern used elsewhere in orbital (S3 not configured →
backup disabled with a warning; OCI not configured → publish disabled with a
warning). Observability is best-effort infrastructure, never a hard
dependency.

---

## Environment variables (new)

Adopt the standard OTel env var names — no orbital-specific prefix.

| Variable | Default | Purpose |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (unset, noop) | Gateway endpoint. Cluster value: `http://otel-gateway-observability.tracing-domain-observability.svc.cluster.local:4317`. |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | `grpc` or `http/protobuf`. Default to gRPC. |
| `OTEL_SERVICE_NAME` | `orbital` | Overrideable for sidecar/standalone testing. |
| `OTEL_TRACES_SAMPLER` | `parentbased_always_on` | Defer sampling to the gateway. |
| `OTEL_LOG_LEVEL` | `info` | SDK's own log verbosity (separate from `ORBITAL_LOG_LEVEL`). |
| `LOKI_TENANT_ID` | (unset) | Sent as `X-Scope-OrgID` if logs go directly to Loki (skip if the gateway forwards). |

`ORBITAL_*` prefix is intentionally not used — these are OTel-standard names
and tooling expects them. Keep orbital's own conventions for orbital's own
configuration; do not relabel community standards.

---

## Why not just stdout + a sidecar later?

The instinct to "log to stdout and let infrastructure collect it" works if
the infrastructure actually collects stdout. **It does not.** There is no
Promtail, no Alloy, no Fluent Bit DaemonSet in this cluster. Stdout goes to
`kubectl logs` and node disk; nothing aggregates it. Sending logs requires
an in-process exporter regardless of whether we use OTel or a direct Loki
client. Given we need an SDK either way, OTel costs the same and gives us
traces for free.

---

## Decision summary

1. **OTel SDK is the instrumentation surface** — traces, logs, future
   metrics over OTLP.
2. **HTTP middleware + export pipeline spans + slog bridge** are the MVP
   instrumentation set.
3. **Graceful degradation to noop** when `OTEL_EXPORTER_OTLP_ENDPOINT` is
   unset — local dev unchanged.
4. **Standard OTel env vars, not `ORBITAL_*`** — these are community
   conventions, not orbital configuration.
5. **No client-side sampling, no cardinality-heavy labels, no per-call-site
   DGraph spans for MVP.**
6. **ServiceMonitor in `deploy/base/`** for Prometheus Agent scraping of
   `/metrics`.

---

## Sources

- Findings: `docs/findings/monitoring-stack.md`
- OpenTelemetry SDK (Go): <https://opentelemetry.io/docs/languages/go/>
- OTLP HTTP/gRPC exporter docs: <https://opentelemetry.io/docs/specs/otlp/>
- Loki OTLP ingestion: <https://grafana.com/docs/loki/latest/send-data/otel/>
- Application Insights + OpenTelemetry (Go): <https://learn.microsoft.com/en-us/azure/azure-monitor/app/opentelemetry-enable?tabs=go>
- OTel semantic conventions for HTTP: <https://opentelemetry.io/docs/specs/semconv/http/http-spans/>
