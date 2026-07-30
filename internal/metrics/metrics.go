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
)

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration, graphqlOperationErrors, graphqlOpDuration, dgraphRequestDuration)
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
