// Package orbmetrics is orb's Prometheus metrics surface, kept separate from
// orbital's internal/metrics because the two run as distinct binaries with
// distinct series. Metrics are `orb_`-prefixed and discoverable by that prefix.
package orbmetrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Hop labels for orb_artifact_propagation_seconds.
const (
	// HopPublishToEdge: orbital build/publish → edge Zot mirror
	// (PushTimestamp − build). Crosses cloud↔edge clocks — NTP-dependent.
	HopPublishToEdge = "publish_to_edge"
	// HopEdgeToImport: edge Zot mirror → orb import complete
	// (poll wait + pull + dgraph live).
	HopEdgeToImport = "edge_to_import"
	// HopImportToDispatch: orb import complete → consumer dispatch complete.
	HopImportToDispatch = "import_to_dispatch"
)

var (
	artifactPropagation = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "orb_artifact_propagation_seconds",
		Help:    "Config-artifact propagation latency by hop, observed once per OCI import (orbital→ACR→Zot→orb). Cross-machine wall-clock deltas — meaningful only under NTP sync; do not over-read sub-second values.",
		Buckets: []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 900},
	}, []string{"hop"})

	importsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "orb_imports_total",
		Help: "Total OCI imports by outcome status (done|partial|failed) and initiator (manual|auto).",
	}, []string{"status", "initiated_by"})

	// Generic HTTP RED — same metric names as orbital's internal/metrics so
	// dashboards/queries are portable across both apps (distinguished by the
	// target's namespace/pod labels). Continuous signal (probes, UI, API),
	// unlike the import-gated orb_* series above.
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by method, route, and status.",
	}, []string{"method", "path", "status"})

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration in seconds by method and route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// registry is orb's OWN registry, isolated from prometheus.DefaultRegisterer.
// orb transitively imports orbital's internal/metrics (via internal/handler),
// which registers http_requests_total etc. on the default registry — sharing it
// would duplicate-register and panic at boot. A private registry also means
// orb's /metrics exposes exactly orb's own series + Go/process runtime, not
// orbital's (empty) metric defs.
var registry = prometheus.NewRegistry()

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		artifactPropagation, importsTotal, httpRequests, httpDuration,
	)
}

// Middleware records HTTP request count + duration for every route. Uses the
// Echo route pattern (c.Path(), e.g. /datacenters/:orbId) — never the raw URI —
// to keep label cardinality bounded. Mirrors orbital's internal/metrics.Middleware.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			path := c.Path()

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
			httpRequests.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
			httpDuration.WithLabelValues(method, path).Observe(time.Since(start).Seconds())
			return err
		}
	}
}

// ObserveHop records one hop's propagation latency. A non-positive duration
// (missing anchor, or negative from clock skew) is dropped rather than recorded
// as a bogus observation.
func ObserveHop(hop string, d time.Duration) {
	if d <= 0 {
		return
	}
	artifactPropagation.WithLabelValues(hop).Observe(d.Seconds())
}

// IncImport counts one completed import by status and initiator.
func IncImport(status, initiatedBy string) {
	importsTotal.WithLabelValues(status, initiatedBy).Inc()
}

// Hops computes the per-hop durations from the four propagation anchors. A hop
// is zero when either endpoint is the zero time (missing anchor); ObserveHop
// drops zero/negative durations, so an absent timestamp simply yields no
// observation for the hops that touch it. Pure and side-effect-free.
func Hops(build, zotPush, importAt, dispatchAt time.Time) map[string]time.Duration {
	span := func(a, b time.Time) time.Duration {
		if a.IsZero() || b.IsZero() {
			return 0
		}
		return b.Sub(a)
	}
	return map[string]time.Duration{
		HopPublishToEdge:    span(build, zotPush),
		HopEdgeToImport:     span(zotPush, importAt),
		HopImportToDispatch: span(importAt, dispatchAt),
	}
}

// Handler returns the Prometheus metrics HTTP handler for mounting at /metrics.
// Serves orb's private registry (see the `registry` var) — not the default one.
func Handler() echo.HandlerFunc {
	h := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	return func(c echo.Context) error {
		h.ServeHTTP(c.Response(), c.Request())
		return nil
	}
}
