package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// A client branches on `code`, so an unreadable schema must say UNAVAILABLE in
// the envelope rather than return an empty graph that looks like a schema with
// no types.
func TestSchemaGraphAPI_UnreadableSchemaIs503Unavailable(t *testing.T) {
	fields := NewSharedFieldSource("http://127.0.0.1:1/graphql", ViewsSource{Path: "../../config/views.yaml"}, slog.Default())
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/schema/graph", nil), rec)
	if err := NewSchemaGraph(fields, slog.Default()).Get(c); err != nil {
		t.Fatalf("Get: %v", err)
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusServiceUnavailable || body.Code != CodeUnavailable {
		t.Errorf("want 503 %s, got %d %+v", CodeUnavailable, rec.Code, body)
	}
}
