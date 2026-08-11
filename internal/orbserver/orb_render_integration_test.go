//go:build integration

package orbserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/orbconfig"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

// newOrbServer creates a Server for render tests. OCIEnabled controls whether
// the OCI tags section renders on the import page.
func newOrbServer(t *testing.T, ociEnabled bool) *Server {
	t.Helper()
	t.Chdir("../..")

	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(dgraph.Close)

	cfg := &orbconfig.Config{
		Port:              "0",
		DGraphURL:         dgraph.URL + "/graphql",
		DGraphAdminURL:    dgraph.URL + "/admin",
		DGraphAlphaGRPC:   "localhost:9082",
		DataDir:           t.TempDir(),
		EnableOCIRegistry: ociEnabled,
		OCIRegistry:       "registry.test",
		OCIRepo:           "test/repo",
		LogLevel:          "error",
	}

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// callHandler calls a Server method as if it were an Echo handler.
func callHandler(t *testing.T, e *echo.Echo, method, path string, handlerFn func(echo.Context) error) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := handlerFn(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return rec
}

// ── Page render tests ─────────────────────────────────────────────────────────

func TestOrbPages_AllPathsReturn200(t *testing.T) {
	srv := newOrbServer(t, false)
	e := echo.New()

	pages := []struct {
		path    string
		handler func(echo.Context) error
		wantIn  string
	}{
		{"/", srv.statusPage, `data-testid="page-heading"`},
		{"/status", srv.statusPage, `data-testid="page-heading"`},
		{"/inventory", srv.inventoryPage, `id="inventory-table"`},
		{"/schema", srv.schemaPage, `data-testid="page-heading"`},
		{"/datacenter", srv.dcPage, `id="datacenter-table"`},
		{"/servers", srv.serversPage, `id="server-list-table"`},
		{"/clusters", srv.clustersPage, `id="cluster-table"`},
		{"/import", srv.importPage, `data-testid="page-heading"`},
		{"/import-history", srv.importHistoryPage, `Import History`},
		{"/divergence", srv.divergencePage, `Publish Report`},
		{"/publish-history", srv.publishHistoryPage, `Publish History`},
	}

	for _, p := range pages {
		t.Run(p.path, func(t *testing.T) {
			rec := callHandler(t, e, http.MethodGet, p.path, p.handler)
			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), p.wantIn) {
				t.Errorf("expected HTML to contain %q", p.wantIn)
			}
		})
	}
}

func TestOrbSidebar_ShowsOrbSectionsNotOrbitalSections(t *testing.T) {
	srv := newOrbServer(t, false)
	e := echo.New()
	rec := callHandler(t, e, http.MethodGet, "/", srv.statusPage)

	html := rec.Body.String()
	for _, want := range []string{
		`Orb`,
		`Sync`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected sidebar to contain %q", want)
		}
	}
	for _, notWant := range []string{
		`>Edge<`,
		`>Operations<`,
	} {
		if strings.Contains(html, notWant) {
			t.Errorf("expected sidebar NOT to contain %q", notWant)
		}
	}
}

func TestOrbNavbar_ShowsOrbBrand(t *testing.T) {
	srv := newOrbServer(t, false)
	e := echo.New()
	rec := callHandler(t, e, http.MethodGet, "/", srv.statusPage)

	if !strings.Contains(rec.Body.String(), "navbar-brand") {
		t.Error("expected navbar-brand element")
	}
	// AppName for orb pages is "Orb" (set in orbBase)
	if !strings.Contains(rec.Body.String(), "Orb") {
		t.Error("expected navbar to contain 'Orb'")
	}
}

func TestOrbPages_NoEditOrDeleteButtons(t *testing.T) {
	srv := newOrbServer(t, false)
	e := echo.New()
	rec := callHandler(t, e, http.MethodGet, "/datacenter", srv.dcPage)

	html := rec.Body.String()
	if strings.Contains(html, `>Edit<`) || strings.Contains(html, `>Edit </`) {
		t.Error("orb pages must not contain Edit buttons")
	}
	if strings.Contains(html, `>Delete<`) || strings.Contains(html, `>Delete </`) {
		t.Error("orb pages must not contain Delete buttons")
	}
}

func TestOrbAppVersionBadge_ContainsOrb(t *testing.T) {
	srv := newOrbServer(t, false)
	e := echo.New()
	rec := callHandler(t, e, http.MethodGet, "/", srv.statusPage)

	html := rec.Body.String()
	if !strings.Contains(html, `data-testid="app-version"`) {
		t.Error("expected app-version badge to be present")
	}
	if !strings.Contains(html, "Orb") {
		t.Error("expected app-version badge to contain 'Orb'")
	}
}

// ── Import page tests ─────────────────────────────────────────────────────────

func TestOrbImportPage_CourierSection(t *testing.T) {
	srv := newOrbServer(t, false)
	e := echo.New()
	rec := callHandler(t, e, http.MethodGet, "/import", srv.importPage)

	html := rec.Body.String()
	for _, want := range []string{
		`id="orb-courier-file"`,
		`id="orb-courier-upload-btn"`,
		`disabled`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected import page to contain %q", want)
		}
	}
}

func TestOrbImportPage_WithOCI_HasTagsAndButtons(t *testing.T) {
	srv := newOrbServer(t, true) // OCIEnabled=true
	e := echo.New()
	rec := callHandler(t, e, http.MethodGet, "/import", srv.importPage)

	html := rec.Body.String()
	for _, want := range []string{
		`id="btn-refresh-tags"`,
		`id="btn-import-latest"`,
		"Tag",
		"Signature",
		"Digest",
		"Size",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected import page (OCI enabled) to contain %q", want)
		}
	}
}

// ── Fragment tests (canned DGraph responses) ──────────────────────────────────

// newDCFragmentDGraph creates a mock DGraph that returns a minimal DC response.
func newDCFragmentDGraph(t *testing.T) *httptest.Server {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"getDataCenter": map[string]any{
				"id":               "0x1",
				"name":             "Test DC",
				"orbId":            "test-dc",
				"namespace":        "test-ns",
				"racks":            []any{},
				"servers":          []any{},
				"serversAggregate": map[string]any{"count": 0},
			},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newClusterFragmentDGraph creates a mock DGraph returning a minimal KubernetesCluster.
func newClusterFragmentDGraph(t *testing.T) *httptest.Server {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"queryConfigItem": []any{
				map[string]any{
					"__typename":           "EksaKubernetesCluster",
					"id":                   "0x2",
					"orbId":                "test-cluster",
					"name":                 "Test Cluster",
					"namespace":            "test-ns",
					"provider":             "eksa",
					"environment":          "dev",
					"kubernetesVersion":    "1.29",
					"clusterType":          "management",
					"controlPlaneEndpoint": map[string]any{"address": "10.0.0.1"},
					"cni":                  "cilium",
					"nodes":                []any{},
					"dataCenter":           map[string]any{"id": "0x1", "orbId": "test-dc", "name": "Test DC"},
					"workloadClusters":     []any{},
				},
			},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newServerFragmentDGraph creates a mock DGraph returning a minimal Server.
func newServerFragmentDGraph(t *testing.T) *httptest.Server {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"getServer": map[string]any{
				"id":         "0x3",
				"orbId":      "test-server",
				"name":       "Test Server",
				"hostname":   "test-server-01",
				"serviceTag": "ABC123",
				"model":      "PowerEdge R750",
				"dataCenter": map[string]any{"id": "0x1", "orbId": "test-dc", "name": "Test DC"},
			},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOrbDataCenterTab_FragmentRendersData(t *testing.T) {
	t.Chdir("../..")
	dgraph := newDCFragmentDGraph(t)

	h := handler.NewDataCenter(dgraph.URL+"/graphql", false, slog.Default(), "", func(echo.Context) layout.PageActions { return layout.OrbActions })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/datacenters/test-dc", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("orbId")
	c.SetParamValues("test-dc")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	html := rec.Body.String()
	for _, want := range []string{"Data Center Summary", "Test DC"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected HTML to contain %q", want)
		}
	}
}

func TestOrbClusterTab_FragmentRendersData(t *testing.T) {
	t.Chdir("../..")
	dgraph := newClusterFragmentDGraph(t)

	h := handler.NewClusterHandler(dgraph.URL+"/graphql", false, slog.Default(), "", func(echo.Context) layout.PageActions { return layout.OrbActions })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/clusters/test-cluster", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("orbId")
	c.SetParamValues("test-cluster")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	html := rec.Body.String()
	for _, want := range []string{"Cluster Summary", "Test Cluster"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected HTML to contain %q", want)
		}
	}
}

func TestOrbClusterTab_NoEditDeleteControls(t *testing.T) {
	t.Chdir("../..")
	dgraph := newClusterFragmentDGraph(t)

	h := handler.NewClusterHandler(dgraph.URL+"/graphql", false, slog.Default(), "", func(echo.Context) layout.PageActions { return layout.OrbActions })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/clusters/test-cluster", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("orbId")
	c.SetParamValues("test-cluster")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}

	html := rec.Body.String()
	if strings.Contains(html, "data-cluster-edit-id") {
		t.Error("orb cluster tab must not contain data-cluster-edit-id (edit control)")
	}
	if strings.Contains(html, "data-cfg-delete-id") {
		t.Error("orb cluster tab must not contain data-cfg-delete-id (delete control)")
	}
}

func TestOrbServerTab_FragmentRendersData(t *testing.T) {
	t.Chdir("../..")
	dgraph := newServerFragmentDGraph(t)

	h := handler.NewServerHandler(dgraph.URL+"/graphql", false, slog.Default(), "", func(echo.Context) layout.PageActions { return layout.OrbActions })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/servers/test-server", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("orbId")
	c.SetParamValues("test-server")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	html := rec.Body.String()
	for _, want := range []string{"Server Summary", "test-server-01"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected HTML to contain %q", want)
		}
	}
}
