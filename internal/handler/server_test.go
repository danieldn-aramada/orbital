package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestServerTab_NonHTMX_Redirects(t *testing.T) {
	t.Chdir("../..")
	h := NewServerHandler("http://localhost:8080/graphql", false, slog.Default(), "/app")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
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

func TestServerTab_DGraphUnreachable(t *testing.T) {
	t.Chdir("../..")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close()

	h := NewServerHandler(srv.URL, false, slog.Default(), "/app")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	if err := h.Tab(c); err == nil {
		t.Error("expected error when DGraph is unreachable, got nil")
	}
}

func TestServerTab_DGraphDecodeError(t *testing.T) {
	t.Chdir("../..")
	dgraph := newDGraphStub(t, "not json")

	h := NewServerHandler(dgraph.URL, false, slog.Default(), "/app")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	if err := h.Tab(c); err == nil {
		t.Error("expected error on JSON decode failure, got nil")
	}
}

func TestServerTab_Success(t *testing.T) {
	t.Chdir("../..")

	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"getServer": map[string]any{
				"id":           "0x2",
				"name":         "srv-01",
				"orbId":        "test:srv-01",
				"hostname":     "srv-01.example.com",
				"rackPosition": 1,
				"namespace":    map[string]any{"name": "test-ns"},
				"rack":         map[string]any{"id": "0x3", "name": "rack-a"},
				"dataCenter":   map[string]any{"id": "0x1", "name": "Test DC"},
				"oobIP":        map[string]any{"address": "10.0.0.1", "role": "oob"},
				"storageControllers": []any{},
			},
		},
	})
	dgraph := newDGraphStub(t, string(body))

	h := NewServerHandler(dgraph.URL, false, slog.Default(), "/app")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x2")

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
