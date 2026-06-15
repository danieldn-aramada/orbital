// Package middleware holds shared Echo middleware used by both orbital and orb.
package middleware

import (
	"log/slog"
	"slices"
	"strings"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

// AccessLogConfig parameterizes the shared HTTP access log middleware. Both
// orbital and orb use the same OpenTelemetry semantic conventions for HTTP
// server attributes (`http.request.method`, `url.path`, etc.) plus orbital-
// specific extensions (`duration_ms`, `actor`). See docs/reference/AUDIT.md.
type AccessLogConfig struct {
	Logger *slog.Logger

	// SkipExactPaths matches the request path exactly. Use for fixed paths
	// like "/favicon.ico" or "/healthz" that should never produce log lines.
	SkipExactPaths []string

	// SkipPrefixes matches request paths by prefix. Use for static-asset
	// directories like "/static/".
	SkipPrefixes []string

	// SkipSuffixes matches request paths by suffix. Use for noisy polling
	// endpoints like "/auth/device/poll" that hit any handler tree.
	SkipSuffixes []string

	// ActorFromContext, if non-nil, is called per request to extract a string
	// identifying the authenticated user. The result is logged as `actor`.
	// Orb passes nil (no auth); orbital passes a function that reads
	// `user_email` (or similar) out of the Echo context.
	ActorFromContext func(c echo.Context) string
}

// AccessLog returns an Echo middleware that writes one structured log line per
// request, following OTel HTTP server conventions. Static / probe / poll paths
// can be filtered via the config's Skip* fields.
func AccessLog(cfg AccessLogConfig) echo.MiddlewareFunc {
	return echomw.RequestLoggerWithConfig(echomw.RequestLoggerConfig{
		Skipper: func(c echo.Context) bool {
			p := c.Request().URL.Path
			for _, prefix := range cfg.SkipPrefixes {
				if strings.HasPrefix(p, prefix) {
					return true
				}
			}
			for _, suffix := range cfg.SkipSuffixes {
				if strings.HasSuffix(p, suffix) {
					return true
				}
			}
			return slices.Contains(cfg.SkipExactPaths, p)
		},
		LogMethod:        true,
		LogURI:           true,
		LogStatus:        true,
		LogLatency:       true,
		LogRemoteIP:      true,
		LogResponseSize:  true,
		LogRequestID:     true,
		LogValuesFunc: func(c echo.Context, v echomw.RequestLoggerValues) error {
			attrs := []any{
				"http.request.method", v.Method,
				"url.path", v.URI,
				"http.response.status_code", v.Status,
				"client.address", v.RemoteIP,
				"duration_ms", v.Latency.Milliseconds(),
				"http.response.body.size", v.ResponseSize,
				"request.id", v.RequestID,
			}
			if name, _ := c.Get("graphql.operation.name").(string); name != "" {
				attrs = append(attrs, "graphql.operation.name", name)
			}
			if t, _ := c.Get("graphql.operation.type").(string); t != "" {
				attrs = append(attrs, "graphql.operation.type", t)
			}
			if cfg.ActorFromContext != nil {
				attrs = append(attrs, "actor", cfg.ActorFromContext(c))
			}
			switch {
			case v.Status >= 500:
				cfg.Logger.Error("request", attrs...)
			case v.Status >= 400:
				cfg.Logger.Warn("request", attrs...)
			default:
				cfg.Logger.Info("request", attrs...)
			}
			return nil
		},
	})
}
