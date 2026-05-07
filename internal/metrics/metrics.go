package metrics

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
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
)

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration, graphqlOperationErrors)
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
			if path == "/graphql" {
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

			if path == "/graphql" && status >= 400 {
				graphqlOperationErrors.WithLabelValues(operation).Inc()
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
