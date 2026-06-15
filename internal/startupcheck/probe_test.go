package startupcheck

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestProbeOrFatal_NoURLs_WarnsAndContinues(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	configured, err := ProbeOrFatal(context.Background(), "TEST_VAR", nil, true, logger)
	if err != nil {
		t.Fatalf("expected nil err on empty config, got %v", err)
	}
	if configured {
		t.Errorf("expected configured=false on empty config")
	}
	if !strings.Contains(buf.String(), "not configured") {
		t.Errorf("expected 'not configured' warning, got: %s", buf.String())
	}
}

func TestProbeOrFatal_AllReachable_ReturnsOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	configured, err := ProbeOrFatal(context.Background(), "TEST_VAR", []string{srv.URL}, true, discardLogger())
	if err != nil {
		t.Fatalf("expected nil err for reachable URL, got %v", err)
	}
	if !configured {
		t.Errorf("expected configured=true")
	}
}

func TestProbeOrFatal_405IsReachable(t *testing.T) {
	// POST-only endpoints return 405 to GET. Still alive — must NOT fail probe.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	configured, err := ProbeOrFatal(context.Background(), "TEST_VAR", []string{srv.URL}, true, discardLogger())
	if err != nil {
		t.Errorf("405 should not count as unreachable; got err=%v", err)
	}
	if !configured {
		t.Errorf("expected configured=true")
	}
}

func TestProbeOrFatal_5xxIsUnreachable_StrictFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := ProbeOrFatal(context.Background(), "TEST_VAR", []string{srv.URL}, true, discardLogger())
	if err == nil {
		t.Errorf("expected error on 5xx in strict mode, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected error to mention HTTP 500, got: %v", err)
	}
}

func TestProbeOrFatal_ConnectionRefused_StrictFails(t *testing.T) {
	_, err := ProbeOrFatal(context.Background(), "TEST_VAR", []string{"http://127.0.0.1:1/probe"}, true, discardLogger())
	if err == nil {
		t.Errorf("expected error on connection refused in strict mode, got nil")
	}
	if !strings.Contains(err.Error(), "configured but unreachable") {
		t.Errorf("expected 'configured but unreachable' in error, got: %v", err)
	}
}

func TestProbeOrFatal_ConnectionRefused_NonStrictWarns(t *testing.T) {
	// Non-strict mode (dev): unreachable URL should NOT fail startup;
	// instead the function returns (true, nil) and emits a WARN log.
	// Verifies the dev invariant — `make run-orbital` succeeds even when
	// cb-bundler isn't yet running.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	configured, err := ProbeOrFatal(context.Background(), "TEST_VAR", []string{"http://127.0.0.1:1/probe"}, false, logger)
	if err != nil {
		t.Errorf("non-strict should not return error on unreachable URL; got %v", err)
	}
	if !configured {
		t.Errorf("expected configured=true (URL was set, just unreachable)")
	}
	if !strings.Contains(buf.String(), "preflight failed (non-strict: continuing)") {
		t.Errorf("expected non-strict warning, got: %s", buf.String())
	}
}
