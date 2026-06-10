package orbserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/armada/orbital/internal/orbconfig"
)

// testCfg builds a minimal config for route-level tests. Templates load from disk
// using paths relative to the repo root — callers must t.Chdir("../..") first.
func testCfg(t *testing.T) *orbconfig.Config {
	t.Helper()
	return &orbconfig.Config{
		Port:            "0",
		DGraphURL:       "http://localhost:8082/graphql",
		DGraphAdminURL:  "http://localhost:8082/admin",
		DGraphAlphaGRPC: "localhost:9082",
		DataDir:         t.TempDir(),
		PollInterval:    60 * time.Second,
		LogLevel:        "error",
	}
}

// routeStatus creates a server with the given config and returns the HTTP status
// for the given method + path.
func routeStatus(t *testing.T, cfg *orbconfig.Config, method, path string) int {
	t.Helper()
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)
	return rec.Code
}

func TestRoutes_ImportSubgraphAlwaysRegistered(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = false

	code := routeStatus(t, cfg, http.MethodPost, "/api/v1/import/subgraph")
	if code == http.StatusNotFound {
		t.Errorf("POST /api/v1/import/subgraph: got 404, expected route to be registered")
	}
}

func TestRoutes_ImportSubgraphRegisteredWithOCIEnabled(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = true

	code := routeStatus(t, cfg, http.MethodPost, "/api/v1/import/subgraph")
	if code == http.StatusNotFound {
		t.Errorf("POST /api/v1/import/subgraph: got 404 when OCI enabled, expected route to be registered")
	}
}

func TestRoutes_OldUploadEndpointRemoved(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = false

	code := routeStatus(t, cfg, http.MethodPost, "/api/v1/import/upload")
	if code != http.StatusNotFound {
		t.Errorf("POST /api/v1/import/upload: got %d, expected 404 (endpoint was renamed)", code)
	}
}

func TestRoutes_OCIRoutesAbsentWhenDisabled(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = false

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/import/tags"},
		{http.MethodPost, "/api/v1/import"},
	} {
		code := routeStatus(t, cfg, tc.method, tc.path)
		if code != http.StatusNotFound {
			t.Errorf("%s %s: got %d, expected 404 (OCI source disabled)", tc.method, tc.path, code)
		}
	}
}

func TestRoutes_OCIRoutesPresentWhenEnabled(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = true

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/import/tags"},
		{http.MethodPost, "/api/v1/import"},
	} {
		code := routeStatus(t, cfg, tc.method, tc.path)
		if code == http.StatusNotFound {
			t.Errorf("%s %s: got 404, expected route to be registered (OCI source enabled)", tc.method, tc.path)
		}
	}
}

func TestRoutes_ImportArtifactAlwaysRegistered(t *testing.T) {
	t.Chdir("../..")
	for _, ociEnabled := range []bool{false, true} {
		cfg := testCfg(t)
		cfg.EnableOCIRegistry = ociEnabled

		code := routeStatus(t, cfg, http.MethodPost, "/api/v1/import/artifact")
		if code == http.StatusNotFound {
			t.Errorf("POST /api/v1/import/artifact (OCI=%v): got 404, expected route to be registered", ociEnabled)
		}
	}
}

func TestRoutes_HistoryAndStatusAlwaysRegistered(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = false

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/import/history"},
		{http.MethodGet, "/api/v1/import/status"},
	} {
		code := routeStatus(t, cfg, tc.method, tc.path)
		if code == http.StatusNotFound {
			t.Errorf("%s %s: got 404, expected always-registered route", tc.method, tc.path)
		}
	}
}

// GraphQL must live at /graphql (not /api/v1/graphql) — GraphQL is not
// URL-versioned, per convention (GitHub, GitLab, NetBox, Apollo). /api/v1/
// is reserved for REST. See CLAUDE.md Settled Decisions.
func TestRoutes_GraphQLAtRoot(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = false

	if code := routeStatus(t, cfg, http.MethodPost, "/graphql"); code == http.StatusNotFound {
		t.Errorf("POST /graphql: got 404, expected route to be registered at root")
	}
	if code := routeStatus(t, cfg, http.MethodPost, "/api/v1/graphql"); code != http.StatusNotFound {
		t.Errorf("POST /api/v1/graphql: got %d, expected 404 (GraphQL must not live under /api/v1/)", code)
	}
}

// /healthz is the K8s readiness probe target. It must always return 200 and
// must NOT require auth — registered before any auth middleware. See CLAUDE.md
// Settled Decisions ("K8s probes: readiness only, always-200 /healthz").
func TestRoutes_HealthzAtRoot(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = false

	if code := routeStatus(t, cfg, http.MethodGet, "/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz: got %d, expected 200", code)
	}
}
