# Observability Reference

Read this before: adding new spans, changing the OTel SDK init, touching
`internal/observability/`, adding instrumentation to a new handler or job,
or modifying `/metrics`.

Stack survey: `docs/reference/monitoring-stack.md`.

---

## Settled Decisions

- **OTel SDK is the single instrumentation surface** — traces, logs, and
  future metrics flow over OTLP. Do not add direct Loki HTTP clients,
  Azure Monitor SDKs, or vendor-specific shippers. One SDK end-to-end.
- **Graceful degradation to noop when `OTEL_EXPORTER_OTLP_ENDPOINT` is
  unset** — local dev must run with no observability infrastructure.
  `tracer.Start(...)` returns a non-recording span; log forwards drop.
  Stdout JSON logging is always on regardless.
- **Standard OTel env vars, not `ORBITAL_*`** — `OTEL_EXPORTER_OTLP_ENDPOINT`,
  `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_SERVICE_NAME`, `OTEL_TRACES_SAMPLER`.
  These are community conventions and shared tooling expects them.
- **No client-side sampling** — the cluster's OTel gateway does tail
  sampling. Emit every span. `OTEL_TRACES_SAMPLER=parentbased_always_on` is
  the default.
- **k8s topology attributes come from the gateway, not the SDK** —
  `k8s.namespace.name`, `k8s.pod.name`, `k8s.deployment.name` are populated
  by the gateway's `k8sattributes` processor based on the source IP. Do
  **not** set them in code; the SDK values would override the authoritative
  ones.
- **No high-cardinality span/log attributes** — never use `user_email`,
  `request_id`, raw query strings, or anything unbounded as a span
  attribute or Loki label. Use Echo route patterns (`http.route`), not raw
  URIs. Orbital's `actorFromContext` returns email — for traces, treat that
  as a *content* attribute (`orbital.actor`), not a Loki label.
- **No DGraph client spans for MVP** — handlers still use raw `http.Post`.
  Per-call-site instrumentation is tracked in tech debt under "DGraph
  client abstraction." Until that lands, the export pipeline's coarse phase
  spans (`dgraph.scratch.drop`, `dgraph.export`) are the granularity.

---

## SDK setup pattern

Single init function in `internal/observability/observability.go` called
from `cmd/orbital/main.go` before any handler is constructed:

```go
shutdown, err := observability.Init(ctx, observability.Config{
    ServiceName:    "orbital",
    ServiceVersion: version.Version,
    Endpoint:       os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
    Logger:         slog.Default(),
})
if err != nil {
    slog.Warn("observability init failed", "err", err)
}
defer shutdown(context.Background())
```

`Init` returns a no-op `shutdown` when `Endpoint` is empty. Never returns a
fatal error — degraded telemetry must not stop the server.

The function:
1. Builds a `resource.Resource` with `service.name`, `service.version`.
2. Creates an OTLP trace exporter (gRPC by default, HTTP if
   `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`).
3. Wires a `BatchSpanProcessor` and sets the global `TracerProvider`.
4. Creates an OTLP log exporter to the same endpoint.
5. Returns a `shutdown` func that flushes both providers.

When the endpoint is unset, all of step 2–4 become noop and `shutdown` is a
nil function.

---

## What is instrumented today

| Layer | Instrumentation | Lives in |
|---|---|---|
| HTTP server | Echo middleware creating a span per request | `internal/observability/middleware.go` |
| Export job | `orbital.export_job` root span + `dgraph.scratch.drop`, `dgraph.export`, `oci.bundle`, `oci.publish` children | `internal/handler/export.go`, `internal/handler/oci.go` |
| Structured logs | `slog` handler that forwards records to the OTel log provider (preserving stdout JSON) | `internal/observability/slog_handler.go` |
| Prometheus metrics | `/metrics` endpoint (preserved from before this spike) | `internal/metrics/metrics.go` |
| ServiceMonitor | Prometheus Agent scrape config for `/metrics` | `deploy/base/servicemonitor.yaml` |

---

## Edge metrics scraping (orb + Zot) — colo clusters

The edge clusters (e.g. colo-dev-main) run **kube-prometheus-stack** (Prometheus
Operator), helm release **`kube-prometheus-stack`** in ns **`monitoring`**; Grafana is
`grafana.dev-main.dev.armadaapps.io`. **The scrape convention is `ServiceMonitor`,
not pod annotations** — matching how the cluster's other apps (velero, rook-ceph)
are scraped. Do NOT use `prometheus.io/scrape` annotations for edge components.

- orb + Zot `ServiceMonitor`s live in `deploy/edge/overlays/colo-galleon/servicemonitor.yaml`;
  the Services (`deploy/edge/base/{orb,zot}.yaml`) carry an `app:` metadata label
  and a **named** `http` port (a ServiceMonitor selects Services by label and
  references the port by name — an unnamed port can't be targeted).
- orb serves `/metrics` on **:8010**, Zot on **:5000** — both on the main HTTP
  port, unauthenticated (like `/healthz`). Zot needs `extensions.metrics.enable:
  true` in its config (added to the colo overlay's `zot-config.json`).
- **Footgun — the `release: prometheus-stack` label is load-bearing.** A
  ServiceMonitor whose labels don't match the Prometheus's `serviceMonitorSelector`
  is **ignored with no error, no metrics**. kube-prometheus-stack's default
  selector matches `release: <helm-release>`; on colo-dev-main the live selector
  is **`release: kube-prometheus-stack`** (verified 2026-08-03 — NOT the
  `prometheus-stack` that a stale `application_set.yaml` suggested), so every edge
  ServiceMonitor MUST carry `release: kube-prometheus-stack`. Always confirm the
  LIVE selector, don't trust the deploy repo:
  `kubectl -n monitoring get prometheus -o jsonpath='{.items[*].spec.serviceMonitorSelector}'`.
- SMs live in the app namespace (`orb`), not `monitoring` — the Operator watches
  all namespaces (proven by velero's SM living in ns `velero` and working).
- Verify after apply (allow ~30–60s): in Grafana, `up{namespace="orb"}` should
  show the orb + Zot targets (=1 each); then the **real "new artifact landed"
  signals**: `orb_imports_total` and `orb_artifact_propagation_seconds` — these
  fire exactly once per genuine import.
- **Do NOT read `zot_repo_uploads_total` as "artifacts landed."** It is a
  cumulative counter of Zot manifest *writes*, and Zot re-registers every matching
  manifest on each pollInterval reconcile — so it climbs continuously (~tags per
  reconcile ÷ interval, e.g. ~2/sec here) even with zero new publishes. Its raw
  total means nothing; only `rate(...)` is meaningful, and even then it measures
  **reconcile churn** (a proxy for reconcile load / the latency-tuning concern),
  not new arrivals. Confirmed 2026-08-04: 1530 in ~13 min of uptime, no publishes.

---

## How to add a new span

1. Get the tracer:
   ```go
   tracer := otel.Tracer("github.com/armada/orbital/internal/handler")
   ```
   Use the package import path as the tracer name. Do not invent custom
   namespaces.
2. Start the span at the boundary of the operation:
   ```go
   ctx, span := tracer.Start(ctx, "operation.name")
   defer span.End()
   ```
3. Use **`verb.noun`** dotted names — `dgraph.export`, `oci.publish`,
   `bundler.call`. Not `ExportDGraph` or `do_export`.
4. Attach attributes for filter-worthy fields only:
   ```go
   span.SetAttributes(
       attribute.String("orbital.orb_id", orbId),
       attribute.Int("orbital.layer_count", len(layers)),
   )
   ```
   Prefix with `orbital.` for orbital-specific attributes. Use OTel
   semantic conventions (`http.*`, `db.*`) when one applies.
5. Record errors:
   ```go
   if err != nil {
       span.RecordError(err)
       span.SetStatus(codes.Error, err.Error())
       return err
   }
   ```
6. **Do not span trivial helpers.** Span boundaries should correspond to
   things a human operator would care about timing — phase boundaries of a
   job, external calls, expensive transforms. A 3-line helper does not need
   one.

---

## How to add an attribute to existing logs

`slog.Info("export started", "orbId", orbId)` already works — the OTel slog
handler reads structured key/value pairs and propagates them as log
attributes. Active trace context (`trace_id`, `span_id`) is attached
automatically.

For attributes that should appear on the span as well, set them explicitly:

```go
span.SetAttributes(attribute.String("orbital.orb_id", orbId))
slog.InfoContext(ctx, "export started", "orbId", orbId)
```

`slog.InfoContext(ctx, ...)` is the canonical form — it carries the trace
context. Prefer it over `slog.Info(...)` inside handlers.

---

## Env vars

| Variable | Default | Purpose |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (unset → noop) | Cluster value: `http://otel-gateway-observability.tracing-domain-observability.svc.cluster.local:4317` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | `grpc` or `http/protobuf` |
| `OTEL_SERVICE_NAME` | `orbital` | Override for sidecar/standalone testing |
| `OTEL_TRACES_SAMPLER` | `parentbased_always_on` | Sampling deferred to gateway — do not change |
| `OTEL_LOG_LEVEL` | `info` | SDK's own verbosity (separate from `ORBITAL_LOG_LEVEL`) |
| `LOKI_TENANT_ID` | (unset) | `X-Scope-OrgID` if logs go directly to Loki |

`ORBITAL_LOG_LEVEL` still controls the stdout slog filter — unchanged.

---

## Local dev workflow

1. `make up` — local stack does **not** include an OTel collector. This is
   intentional.
2. `make run-orbital` — `OTEL_EXPORTER_OTLP_ENDPOINT` is unset → noop
   providers → no warnings, no failed connections.
3. To exercise the OTLP pipeline locally, run a single-binary collector and
   point orbital at it:
   ```bash
   docker run --rm -p 4317:4317 -p 4318:4318 \
     otel/opentelemetry-collector-contrib:0.108.0 \
     --config=/etc/otelcol-contrib/config.yaml
   OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317 make run-orbital
   ```
4. Stdout JSON logs include `trace_id` and `span_id` when a request has an
   active span — useful for correlating across logs even without a
   collector.

---

## Production verification

After deploy, validate the pipeline with:

1. **Traces** — Application Insights workspace → Transaction Search → filter
   to `cloud_RoleName == "orbital"`. A recent export job should produce a
   trace tree with `orbital.export_job` at the root.
2. **Logs** — Grafana → Loki data source →
   `{service_name="orbital"} |= "export"`. Should return slog lines with
   `trace_id` attributes that match the App Insights trace IDs.
3. **Metrics** — in devcc, orbital's reliable metrics path is the self-owned
   `orbital-otel-collector` → Cortex; view in `commander-grafana` on the Cortex
   data source (e.g. `sum by (path) (rate(http_requests_total{job="scrape-orbital"}[5m]))`).
   The AMW/ama-metrics path silently drops orbital's target after each redeploy —
   see `docs/reference/monitoring-stack.md` (Metrics + Grafana) for the verified
   topology, the known ama-metrics defect, and the dashboard JSON.

---

## Gotchas

- **Echo `c.Request().Context()` propagation.** Middleware-set spans are
  attached via the request context. Handlers must use `c.Request().Context()`
  when calling downstream (DGraph, S3) — not `context.Background()` — or
  spans will detach.
- **Goroutines lose context unless explicitly passed.** `go h.runBackup(...)`
  must capture `ctx` if the work should appear under the trigger's trace.
  Background work spawned by the scheduler has no upstream trace — start a
  new root span (`tracer.Start(context.Background(), "scheduler.backup")`).
- **`span.End()` must run.** Defer it at the call site, never inside an
  inner function — otherwise a goroutine panic above the defer leaks the
  span (and the BatchSpanProcessor's buffer slot).
- **Do not call `span.SetStatus(codes.Ok, ...)` for normal completion.**
  OTel's default status is "unset," which Application Insights treats as
  success. Explicit `Ok` is reserved for cases where the SDK cannot tell.
- **`/healthz` and `/metrics` are filtered at the gateway.** Spans for
  these endpoints are dropped — do not waste time wondering why they don't
  appear in App Insights.
- **Loki labels vs attributes.** Set high-value low-cardinality fields as
  resource attributes (promoted to Loki labels): `service.name`,
  `service.version`, `deployment.environment`. Everything else is a log
  attribute — searchable via LogQL `|= "..."`, not indexed.

---

## When to add metrics vs traces vs logs

| Question | Signal |
|---|---|
| How long did X take? | trace span |
| How often does X happen? | metric counter |
| What was the exact value/payload when X happened? | log record |
| Did X fail this minute? | metric + alert |
| Why did this specific X fail? | trace + log |

Traces and metrics are about *patterns* — they are sampled and aggregated.
Logs are about *individual occurrences*. If a question is "which request
broke," it's logs. If it's "is the error rate up," it's metrics. If it's
"where does the time go in a typical request," it's traces.
