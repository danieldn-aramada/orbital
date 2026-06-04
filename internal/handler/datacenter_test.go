package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// newDGraphStub starts a server that always returns body as application/json.
func newDGraphStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDataCenterTab_NonHTMX_Redirects(t *testing.T) {
	t.Chdir("../..")
	h := NewDataCenter("http://localhost:8080/graphql", false, slog.Default(), "/app")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No HX-Request header — handler should redirect.
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/app/" {
		t.Errorf("Location: got %q, want /app/", loc)
	}
}

func TestDataCenterTab_DGraphUnreachable(t *testing.T) {
	t.Chdir("../..")
	// Start then immediately close — all requests will fail with connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close()

	h := NewDataCenter(srv.URL, false, slog.Default(), "/app")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	err := h.Tab(c)
	if err == nil {
		t.Error("expected error when DGraph is unreachable, got nil")
	}
}

func TestDataCenterTab_DGraphDecodeError(t *testing.T) {
	t.Chdir("../..")
	// DGraph returns non-JSON — Unmarshal must fail.
	dgraph := newDGraphStub(t, "not json")

	h := NewDataCenter(dgraph.URL, false, slog.Default(), "/app")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	err := h.Tab(c)
	if err == nil {
		t.Error("expected error on JSON decode failure, got nil")
	}
}

func TestDataCenterTab_Success(t *testing.T) {
	t.Chdir("../..")

	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"getDataCenter": map[string]any{
				"id":    "0x1",
				"name":  "Test DC",
				"orbId": "test:dc01",
				"namespace":        "test-ns",
				"racks":            []any{},
				"servers":          []any{},
				"serversAggregate": map[string]any{"count": 0},
			},
		},
	})
	dgraph := newDGraphStub(t, string(body))

	h := NewDataCenter(dgraph.URL, false, slog.Default(), "/app")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type: got %q, want text/html", ct)
	}
}
