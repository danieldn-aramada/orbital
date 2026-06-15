package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	orbmw "github.com/armada/orbital/internal/middleware"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

// captureHandler returns a slog.Handler that accumulates all records in buf as
// newline-delimited JSON, and a slog.Logger wrapping it.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// logLine parses the first complete JSON object from buf.
func logLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m); err != nil {
		t.Fatalf("parse log line: %v\nbuf=%s", err, buf)
	}
	return m
}

// runRequest registers the given cfg as AccessLog middleware, optionally adds
// RequestID middleware before it, fires one GET /, and returns the parsed log.
func runRequest(t *testing.T, cfg orbmw.AccessLogConfig, withRequestID bool, setup func(req *http.Request)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	cfg.Logger = captureLogger(&buf)

	e := echo.New()
	if withRequestID {
		e.Use(echomw.RequestID())
	}
	e.Use(orbmw.AccessLog(cfg))
	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if setup != nil {
		setup(req)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return logLine(t, &buf)
}

func TestAccessLog_BasicFields(t *testing.T) {
	m := runRequest(t, orbmw.AccessLogConfig{
		ActorFromContext: func(c echo.Context) string { return "test@example.com" },
	}, true, nil)

	for _, key := range []string{
		"http.request.method",
		"url.path",
		"http.response.status_code",
		"client.address",
		"duration_ms",
		"http.response.body.size",
		"request.id",
		"actor",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected key %q in log; got %v", key, m)
		}
	}
}

func TestAccessLog_NoUserAgent(t *testing.T) {
	m := runRequest(t, orbmw.AccessLogConfig{}, false, func(req *http.Request) {
		req.Header.Set("User-Agent", "test-browser/1.0")
	})
	if _, ok := m["user_agent.original"]; ok {
		t.Error("user_agent.original must NOT appear in access log")
	}
}

func TestAccessLog_GraphQLOperation_Present(t *testing.T) {
	var buf bytes.Buffer
	logger := captureLogger(&buf)

	e := echo.New()
	e.Use(orbmw.AccessLog(orbmw.AccessLogConfig{Logger: logger}))
	e.GET("/", func(c echo.Context) error {
		c.Set("graphql.operation.name", "ListDataCenters")
		c.Set("graphql.operation.type", "query")
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	e.ServeHTTP(httptest.NewRecorder(), req)

	m := logLine(t, &buf)
	if got, _ := m["graphql.operation.name"].(string); got != "ListDataCenters" {
		t.Errorf("graphql.operation.name = %q, want ListDataCenters", got)
	}
	if got, _ := m["graphql.operation.type"].(string); got != "query" {
		t.Errorf("graphql.operation.type = %q, want query", got)
	}
}

func TestAccessLog_GraphQLOperation_Absent(t *testing.T) {
	m := runRequest(t, orbmw.AccessLogConfig{}, false, nil)
	for _, key := range []string{"graphql.operation.name", "graphql.operation.type"} {
		if v, ok := m[key]; ok {
			t.Errorf("key %q must not appear when not set; got %v", key, v)
		}
	}
}

func TestAccessLog_LevelEscalation(t *testing.T) {
	cases := []struct {
		status    int
		wantLevel string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusNotFound, "WARN"},
		{http.StatusInternalServerError, "ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.wantLevel, func(t *testing.T) {
			var buf bytes.Buffer
			logger := captureLogger(&buf)

			e := echo.New()
			e.Use(orbmw.AccessLog(orbmw.AccessLogConfig{Logger: logger}))
			e.GET("/", func(c echo.Context) error {
				return c.String(tc.status, "body")
			})

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			e.ServeHTTP(httptest.NewRecorder(), req)

			m := logLine(t, &buf)
			if got, _ := m["level"].(string); !strings.EqualFold(got, tc.wantLevel) {
				t.Errorf("status %d: level = %q, want %s", tc.status, got, tc.wantLevel)
			}
		})
	}
}

func TestAccessLog_RequestIDFromContext(t *testing.T) {
	const knownID = "test-request-id-123"
	m := runRequest(t, orbmw.AccessLogConfig{}, true, func(req *http.Request) {
		req.Header.Set("X-Request-ID", knownID)
	})
	if got, _ := m["request.id"].(string); got != knownID {
		t.Errorf("request.id = %q, want %q", got, knownID)
	}
}
