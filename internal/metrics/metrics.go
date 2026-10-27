package metrics

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by method, path, and status.",
	}, []string{"method", "path", "status"})

	requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration in seconds by method and path.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	graphqlOperationErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "graphql_operation_errors_total",
		Help: "Total failed GraphQL requests by operation name.",
	}, []string{"operation"})

	// orbital_-prefixed, orbital-specific series — discoverable by the `orbital_`
	// prefix and distinct from the generic http_* series every service emits.
	graphqlOpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "orbital_graphql_operation_duration_seconds",
		Help:    "GraphQL request duration by operation name and type (query|mutation). Splits the single /graphql bucket in http_request_duration into per-operation series.",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation", "type"})

	dgraphRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "orbital_dgraph_request_duration_seconds",
		Help:    "Duration of orbital's HTTP round-trip to DGraph by call kind. The gap between http_request_duration and this attributes latency: orbital overhead vs the DGraph call.",
		Buckets: prometheus.DefBuckets,
	}, []string{"kind"})

	// A dropped audit event is the one failure in orbital that changes nothing
	// observable at the time: the mutation succeeded, the caller got its 200, and
	// the only trace is a log line. It surfaces much later, as a question that can
	// no longer be answered. This counter exists so it is alertable instead.
	// A gauge, not a counter: the question is "are the audit tables protected
	// RIGHT NOW", and the answer must be able to go back down when someone
	// reinstalls the guard. Alert on > 0, not on increase.
	auditTablesUnprotected = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "orbital_audit_tables_unprotected",
		Help: "Audit tables lacking an enforced append-only guard. ANY non-zero value means audit records can be edited or deleted without trace — alert on > 0.",
	})

	auditWriteFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "orbital_audit_write_failures_total",
		Help: "Audit events that could not be persisted, by the stage that failed. ANY non-zero value means provenance was lost for a write that succeeded — alert on increase, not on a threshold.",
	}, []string{"stage"})

	// Async job outcomes.
	//
	// These exist because an async job's failure is INVISIBLE to every HTTP
	// metric. The trigger returns 202 and the work fails later in a goroutine,
	// so http_requests_total never records a 5xx and the dashboard stays green
	// while every publish fails. Observed 2026-10-09: a publish broken by an
	// expired Keycloak client ran ~40 minutes against a clean dashboard.
	jobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "orbital_jobs_total",
		Help: "Async jobs that reached a terminal state, by operation and outcome. Use with job failures for a success ratio.",
	}, []string{"operation", "outcome"})

	jobFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "orbital_job_failures_total",
		Help: "Async jobs that failed, by operation and classified reason. `reason` says WHERE to look — dependency_auth means a credential, not a bug. Alert on increase, not on a threshold.",
	}, []string{"operation", "reason"})

	jobLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "orbital_job_last_success_timestamp_seconds",
		Help: "Unix time of the last SUCCESSFUL job per operation. A failure counter only moves when someone tries; this catches an operation that quietly stopped being attempted at all. Alert on time() - this exceeding your republish interval.",
	}, []string{"operation"})
)

// Job operations. A closed set: these are the async operations orbital runs,
// and the label must never be caller-derived.
const (
	JobExport   = "export"   // build the artifact zip
	JobPublish  = "publish"  // export, then push to OCI
	JobDownload = "download" // export, then hand the zip to the caller
	JobRestore  = "restore"  // restore a backup into DGraph
)

// Job failure reasons. A closed set, chosen AT THE CALL SITE — never derived
// from an error string. Error text drifts, and a reason that silently
// reclassifies itself is worse than no reason at all: whoever reads the panel
// cannot tell that the meaning moved underneath them.
const (
	ReasonDependencyAuth        = "dependency_auth"        // a credential to another service was refused
	ReasonDependencyUnreachable = "dependency_unreachable" // could not connect to another service
	ReasonDependencyRejected    = "dependency_rejected"    // another service answered, with an error
	ReasonDGraph                = "dgraph"                 // the graph could not be read or written
	ReasonStorage               = "storage"                // disk, S3 or the OCI registry
	ReasonSigning               = "signing"                // cosign could not sign the artifact
	ReasonLeaseLost             = "lease_lost"             // the runner stopped heartbeating; the reaper failed it
	ReasonInternal              = "internal"               // anything orbital itself got wrong
)

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration, graphqlOperationErrors, graphqlOpDuration, dgraphRequestDuration, auditWriteFailures, auditTablesUnprotected,
		jobsTotal, jobFailures, jobLastSuccess)
}

// SetAuditTablesUnprotected records how many append-only tables (the audit
// tables and approval_decisions — db.AppendOnlyTables) lack the guard. Called at startup after the guard is installed and verified; passed as
// a callback so internal/db needs no dependency on this package.
func SetAuditTablesUnprotected(n int) { auditTablesUnprotected.Set(float64(n)) }

// RecordAuditWriteFailure counts one audit event that could not be persisted.
// stage must be a small fixed set ("begin_tx", "event", "resources",
// "resource_types", "commit") — never caller-controlled, or the series
// cardinality is unbounded.
func RecordAuditWriteFailure(stage string) {
	auditWriteFailures.WithLabelValues(stage).Inc()
}

// RecordJobSuccess records one job that completed. Advances the freshness gauge
// for that operation, which is the only signal that survives nobody trying.
//
// operation must be one of the Job* constants.
func RecordJobSuccess(operation string, at time.Time) {
	jobsTotal.WithLabelValues(operation, "succeeded").Inc()
	jobLastSuccess.WithLabelValues(operation).Set(float64(at.Unix()))
}

// RecordJobFailure records one job that failed. Deliberately does NOT touch the
// freshness gauge: a failure is not a success, and letting it advance the clock
// would hide exactly the outage this is here to show.
//
// operation must be one of the Job* constants and reason one of the Reason*
// constants — both closed sets, never caller-derived.
func RecordJobFailure(operation, reason string) {
	jobsTotal.WithLabelValues(operation, "failed").Inc()
	jobFailures.WithLabelValues(operation, reason).Inc()
}

// ObserveDGraphCall records one orbital→DGraph HTTP round-trip. kind must be a
// small fixed set ("query", "mutation", "before_fetch") — never caller-controlled.
func ObserveDGraphCall(kind string, d time.Duration) {
	dgraphRequestDuration.WithLabelValues(kind).Observe(d.Seconds())
}

// Handler returns the Prometheus metrics HTTP handler for mounting at /metrics.
func Handler() echo.HandlerFunc {
	h := promhttp.Handler()
	return func(c echo.Context) error {
		h.ServeHTTP(c.Response(), c.Request())
		return nil
	}
}

// Middleware records HTTP request metrics for every endpoint.
// For requests to /graphql it additionally buffers the body to extract the
// operation name, which is recorded on error responses.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			path := c.Path() // Echo route pattern e.g. /datacenters/:id

			var operation string
			if isGraphQLPath(path) {
				operation = extractOperation(c.Request())
			}

			err := next(c)

			status := c.Response().Status
			if err != nil {
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				} else {
					status = http.StatusInternalServerError
				}
			}

			method := c.Request().Method
			statusStr := strconv.Itoa(status)

			requestsTotal.WithLabelValues(method, path, statusStr).Inc()
			requestDuration.WithLabelValues(method, path).Observe(time.Since(start).Seconds())

			if isGraphQLPath(path) {
				// Per-operation latency — http_request_duration lumps all of
				// /graphql into one series; this splits it by op name + type.
				// Prefer the resolved name/type the handler set on the context.
				opName := operation
				if n, _ := c.Get("graphql.operation.name").(string); n != "" {
					opName = n
				}
				if opName == "" {
					opName = "anonymous"
				}
				opType, _ := c.Get("graphql.operation.type").(string)
				if opType == "" {
					opType = "unknown"
				}
				graphqlOpDuration.WithLabelValues(opName, opType).Observe(time.Since(start).Seconds())

				if status >= 400 {
					graphqlOperationErrors.WithLabelValues(operation).Inc()
				}
			}

			return err
		}
	}
}

// extractOperation reads the request body to pull the GraphQL operationName,
// then restores the body so the downstream handler can read it normally.
func extractOperation(r *http.Request) string {
	if r.Body == nil {
		return "anonymous"
	}

	body, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body)) // restore
	if err != nil {
		return "anonymous"
	}

	var payload struct {
		OperationName string `json:"operationName"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.OperationName == "" {
		return "anonymous"
	}
	return payload.OperationName
}

// isGraphQLPath reports whether the matched Echo route is the GraphQL endpoint.
// Matches on suffix, not equality, because the route is mounted under the
// configurable base path (ORBITAL_BASE_PATH) — "/orbital/graphql" in AKS,
// "/graphql" locally. A plain path == "/graphql" check silently misses every
// request when a base path is set, so per-operation metrics never record.
func isGraphQLPath(path string) bool {
	return strings.HasSuffix(path, "/graphql")
}
