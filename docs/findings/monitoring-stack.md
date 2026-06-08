# Cluster Monitoring Stack — Findings

> Research conducted June 2026 in preparation for instrumenting orbital with
> OpenTelemetry. This is reference material for the engineer implementing the
> observability spike — what already exists in the AKS cluster, what endpoints
> to send signals to, and what is still unknown.

---

## Summary

The AKS cluster already has a complete monitoring stack: traces flow to Azure
Monitor / Application Insights, metrics to Mimir via Prometheus Agent, logs to
Loki. Orbital should integrate as a producer using the OpenTelemetry SDK and
existing in-cluster gateway endpoints. **No new infrastructure needs to be
deployed.** What orbital must add is the SDK wiring and a small set of
spans/log attributes.

---

## Traces — Azure Monitor / Application Insights

### Topology

- **DaemonSet:** `otel-agent-observability` runs on every node in namespace
  `tracing-domain-observability`. Receives OTLP from workloads on the local
  node.
- **Gateway:** `otel-gateway-observability` (Deployment) in the same namespace.
  Receives from the DaemonSets, applies tail sampling, enriches with k8s
  attributes, then exports to Azure Monitor via the Application Insights
  connection string mounted from CSI secrets store.

### Endpoints (consumer-facing)

| Protocol | Endpoint |
|---|---|
| OTLP gRPC | `otel-gateway-observability.tracing-domain-observability.svc.cluster.local:4317` |
| OTLP HTTP | `otel-gateway-observability.tracing-domain-observability.svc.cluster.local:4318` |

Either works. gRPC is more efficient for batches; HTTP is easier to debug
with `curl`.

### Gateway processors (worth knowing)

- **Tail sampling.** The gateway samples after the full trace is assembled —
  do not implement client-side sampling that would prematurely drop signals.
- **k8sattributes processor.** Auto-populates `k8s.namespace.name`,
  `k8s.pod.name`, `k8s.deployment.name`, `k8s.container.name` based on the
  source IP. Orbital does **not** need to set these — they are added in the
  gateway. Orbital must set `service.name` and `service.version`; the cluster
  attributes ride along automatically.
- **Healthz/metrics drop.** The gateway drops spans whose `http.target` ends
  in `/healthz` or `/metrics` to keep the trace volume sane. This means
  orbital's `/metrics` endpoint will not show up in App Insights regardless of
  client-side filtering.

### App Insights export details

The gateway writes to a single App Insights workspace, identified by the
connection string mounted via CSI secrets store. Orbital does not need to
know the workspace — the gateway owns the credentials.

---

## Metrics — Prometheus Agent + Mimir

### Topology

- **Prometheus Agent** runs in the `monitoring` namespace in remote_write-only
  mode (no local storage, no querying — it scrapes and forwards).
- **Mimir** (`prometheus-mimir-monitoring`) is the likely remote_write target —
  it is the cluster's long-term metrics store. Grafana queries against Mimir,
  not against the Prometheus Agent.

### Service discovery

The Prometheus Agent scrapes targets discovered via `ServiceMonitor` CRDs
(prometheus-operator). **No `ServiceMonitor` exists for the netbox namespace
yet** — adding one is part of the orbital observability spike.

**Unknown:** the namespace selector on the Prometheus Agent's
`prometheusagent.spec.serviceMonitorNamespaceSelector`. We could not exec into
the agent pod to read its config. The spike should confirm this before
authoring the ServiceMonitor, or use `release: prometheus-monitoring` labels
in the ServiceMonitor metadata (the convention used elsewhere in the cluster)
and observe whether the agent picks it up.

### Orbital's metrics endpoint

`internal/metrics/metrics.go` already exposes `/metrics` in Prometheus text
format with the following series:

| Series | Labels |
|---|---|
| `http_requests_total` | `method`, `path`, `status` |
| `http_request_duration_seconds` | `method`, `path` |
| `graphql_operation_errors_total` | `operation` |

The orbital Deployment does **not** currently have annotations or a Service
exposing port 8001 with the appropriate `prometheus.io/scrape` labels for
auto-discovery. The ServiceMonitor approach (CRD-based) is preferred over
pod annotations — the cluster is already configured for it.

---

## Logs — Loki

### Topology

- **Loki** runs in `loki-observability`, multi-tenant (`auth_enabled: true`).
- **No DaemonSet log collector** — no Promtail, no Alloy, no Fluent Bit
  scraping stdout. Workloads push to Loki directly.

### Endpoints (consumer-facing)

| Protocol | Endpoint |
|---|---|
| Loki push API | `http://loki-gateway.loki-observability.svc.cluster.local:80/loki/api/v1/push` |
| OTLP logs | `http://loki-gateway.loki-observability.svc.cluster.local:80/otlp/v1/logs` |

The OTLP logs endpoint accepts the standard OpenTelemetry log protocol —
**the same OTel SDK used for traces can emit logs to this URL** without a
second client library. This is the recommended path: one SDK, two signal
types, one exporter pipeline.

### Multi-tenant header

Every request must include `X-Scope-OrgID: <tenant>`. **Unknown:** what
tenant ID is assigned to the netbox/orbital namespace. Ask the platform team
before implementation begins. Until known, use a placeholder value (e.g.
`netbox`) and treat the spike as blocked on real ingestion testing.

### Labels vs structured fields

Loki indexes a small set of *labels* (low-cardinality strings like
`namespace`, `pod`, `app`) and stores the rest of the log line as opaque
content. With the OTLP logs endpoint, OTel attributes map to Loki labels
following the OTLP → Loki promotion rules:

- `service.name` → `service_name` label
- `service.namespace` → `service_namespace` label
- Common k8s attributes are promoted by name

Avoid high-cardinality labels (e.g. `user_email`, `request_id`). Use log
attributes instead — they remain in the log body and are searchable via
LogQL but do not blow up the index.

---

## Grafana

`commander-grafana` in `commander-metrics` is the main cluster Grafana.
Already configured with Mimir and Loki as data sources. Orbital does not need
to provision dashboards as part of the integration spike — a single
"orbital overview" dashboard can be built in the UI after metrics start
flowing. Dashboards-as-code is post-MVP.

---

## What orbital must produce

Given the above, the integration work on orbital's side is:

1. **One OTel SDK initialization** in `cmd/orbital/main.go` that:
   - Sets `service.name=orbital` and `service.version` from
     `internal/version.Version`.
   - Creates a tracer provider with OTLP exporter pointing at
     `OTEL_EXPORTER_OTLP_ENDPOINT`.
   - Creates a log provider with OTLP exporter pointing at the same endpoint
     (the gateway routes logs to Loki via its OTLP receiver) — or directly at
     Loki's `/otlp/v1/logs` if the gateway does not forward logs.
   - Falls back to noop providers if the endpoint env var is unset (local dev).
2. **HTTP middleware** that creates a server span for every request, with
   `http.method`, `http.route`, `http.status_code`.
3. **Spans on the export pipeline** — one root span per export job, child
   spans for `dgraph.scratch.drop`, `dgraph.export`, `oci.publish`,
   `bundler.call`.
4. **Structured logs via OTel log bridge** — replace `slog.Default()` with a
   handler that forwards records to the OTel log provider. The existing
   `slog.Info(...)` call sites do not change.
5. **A ServiceMonitor manifest** in `deploy/base/` so Prometheus Agent picks
   up `/metrics`.

---

## What orbital should *not* do

- **Do not deploy a sidecar collector.** The DaemonSet already runs on every
  node — orbital pods reach it through the service DNS name.
- **Do not implement client-side trace sampling.** Tail sampling is owned by
  the gateway. Send every span; the gateway decides what survives.
- **Do not write directly to Azure Monitor with an Azure SDK.** The OTel
  gateway owns the workspace credentials. Orbital should not know which
  workspace it lands in.
- **Do not add `prometheus.io/scrape: "true"` pod annotations.** The cluster
  uses ServiceMonitor CRDs; the annotation path is unused and unreliable.
- **Do not add a custom log shipper to stdout.** stdout logs are not picked
  up anywhere — there is no DaemonSet collector. All logs must flow through
  the OTLP exporter to the OTel gateway (which forwards to Loki) or directly
  to Loki's `/otlp/v1/logs`.

---

## Open items (carry into the spike)

| Item | Action |
|---|---|
| Loki `X-Scope-OrgID` tenant ID for netbox/orbital namespace | Ask platform team |
| Prometheus Agent `serviceMonitorNamespaceSelector` configuration | Ask platform team or test empirically with a labelled ServiceMonitor |
| Whether the OTel gateway forwards OTLP logs to Loki, or whether orbital should target Loki directly | Confirm with platform team; default to gateway path |
| App Insights workspace name (for finding orbital traces in the portal) | Ask platform team — needed for the spike's "verify" step, not for the code |

---

## References

- OpenTelemetry collector — k8sattributes processor: <https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor/k8sattributesprocessor>
- OpenTelemetry collector — tail sampling processor: <https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor/tailsamplingprocessor>
- Loki OTLP ingestion: <https://grafana.com/docs/loki/latest/send-data/otel/>
- Prometheus Operator ServiceMonitor CRD: <https://prometheus-operator.dev/docs/operator/api/#monitoring.coreos.com/v1.ServiceMonitor>
- Application Insights via OTLP: <https://learn.microsoft.com/en-us/azure/azure-monitor/app/opentelemetry-overview>
