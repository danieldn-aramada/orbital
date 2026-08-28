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

## Metrics — AMW (ama-metrics) + self-owned Cortex

Verified in devcc (`aks-applz-devcc-westus3`, ns `netbox`) on 2026-08-03. The
original "Prometheus Agent + Mimir + ServiceMonitor" plan was **not** what
shipped: orbital is discovered by pod annotations (no `ServiceMonitor`), and
its `/metrics` (:8001) is consumed by **two independent scrape paths**.

### Path 1 — AMW (org-standard, but currently unreliable for orbital)

The AKS Managed-Prometheus addon `ama-metrics` (kube-system) scrapes any pod
with `prometheus.io/scrape=true` cluster-wide. Orbital is meshed, so Istio's
prometheus-merge rewrites the pod annotation to `:15020/stats/prometheus` and
merges orbital's app metrics with the envoy/istio metrics; ama-metrics scrapes
`:15020` → AMW `defaultazuremonitorworkspace-wus3` → the devcc **Azure Managed
Grafana** `grafana-2025100194459-w` (where the org's ~300 dashboards live).
The `prometheus.io/scrape|port|path` annotations must be on the pod template
at injection time — they are, in `deploy/base/deploy.yaml`.

> **KNOWN DEFECT (verified 2026-08-03): ama-metrics silently drops orbital's
> `:15020` target on redeploy and does not recover.** Reproduced — orbital
> flowed into AMW continuously until a redeploy, then stopped dead and stayed
> gone 95+ min while the live pod served valid metrics on `:15020`.
> ama-metrics kept scraping dgraph and the orbital-otel-collector (identical
> annotations/labels/namespace, same node pool) — only orbital's app pod is
> absent from its target set (no `up` series at all). Root cause is inside
> ama-metrics' discovery/sharding (observability-team infra, not an orbital
> misconfig); a candidate factor is orbital's unusually large `:15020` payload
> (~257 KB, 238 `envoy_cluster` series). **Consequence: after every redeploy,
> AMW loses orbital's app metrics AND `istio_requests_total{reporter=
> "destination"}`.** Do not treat AMW as orbital's metrics home until the
> observability team fixes this.

### Path 2 — self-owned Cortex (orbital's reliable path)

`orbital-otel-collector` (`deploy/overlays/dev-netbox/otel-collector.yaml`)
scrapes `orbital.netbox.svc:8001` **directly** via its own service-discovery
(independent of ama-metrics and istio-merge) and remote-writes to the
`commander-metrics` Cortex (`job=scrape-orbital`). Verified fresh (0-min
staleness) for the live pod; the collector runs clean (0 restarts over 3d+, no
remote-write errors). Same `otel-collector → Cortex` convention as
`scrape-armada-galleon-svc`, the starlink family, etc. **This is orbital's
reliable metrics path in devcc and must not be retired while the ama-metrics
defect stands.** Nothing downstream consumes it (`metrics-api` does not
reference orbital) — it exists to feed dashboards.

### Orbital's series (verified from live :8001)

| Series | Type | Labels |
|---|---|---|
| `http_requests_total` | counter | `method`, `path`, `status` |
| `http_request_duration_seconds` | histogram | `method`, `path` |
| `orbital_graphql_operation_duration_seconds` | histogram | `operation`, `type` |
| `orbital_dgraph_request_duration_seconds` | histogram | `kind` |

`orbital_graphql_*` gives per-operation GraphQL latency; `orbital_dgraph_*`
isolates the DGraph round-trip from orbital's own overhead (the gap between
`/graphql` `http_request_duration` and `orbital_dgraph_request_duration`).

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

## Grafana — orbital's own dashboard (Cortex)

Orbital's metrics are reliable only on Cortex (Path 2). The org's Azure Managed
Grafana has no Cortex data source and Cortex isn't externally exposed in devcc;
`commander-grafana` has the data source but ephemeral storage + no dashboard
sidecar (a UI import is lost on restart) and lives in a namespace orbital does
not own. So orbital runs its **own small Grafana** in the overlay — fully
as-code, durable, and reading Cortex without writing to anyone else's namespace.

**`orbital-grafana`** ([`deploy/overlays/dev-netbox/orbital-grafana.yaml`](../../deploy/overlays/dev-netbox/orbital-grafana.yaml))
— a 1-replica Grafana in `netbox` that provisions, on every boot:
- a **Cortex** data source pointed at the in-cluster query gateway
  (`http://commander-metrics-cortex-nginx.commander-metrics/prometheus` — query
  only; it writes nothing to `commander-metrics`), and
- the **orbital dashboard** ([`orbital-dashboard.json`](../../deploy/overlays/dev-netbox/orbital-dashboard.json))
  via a file provider.

Both come from ConfigMaps (kustomize generators), so a pod restart can never
lose them — no PVC needed. The admin password is a gitignored `secretGenerator`
env (`grafana-admin.env`); anonymous **Viewer** is on, so port-forward users see
dashboards without logging in. devcc/dev-netbox overlay only.

**Reach it (port-forward only, no ingress):**
```
kubectl -n netbox port-forward deploy/orbital-grafana 3000:3000   # → http://localhost:3000
```

**Dashboard** (9 panels): HTTP RED (rate / 5xx ratio / p50-p95-p99),
per-operation GraphQL latency, the orbital-overhead-vs-DGraph split, and Go
runtime. It uses a `datasource` template variable, so it's portable to AMW if
that path is ever fixed. Every query was validated against live Cortex.

This is orbital's durable interim home. The org-standard, **discoverable**
endgame is still ama-metrics → AMW → the Azure Grafana, once the observability
team fixes the drop (see the known defect above).

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
