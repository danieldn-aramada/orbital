package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// The view list is derived from the deployed schema, so when that cannot be read
// the endpoint must say so — and say it is retryable. Returning 200 with an
// empty list would tell every client "this deployment has no pages", which is a
// different and misleading claim.
func TestViews_UnavailableWhenSchemaUnreadable(t *testing.T) {
	// A server that answers, but not with a schema — so introspection fails
	// rather than the connection.
	stub := newDGraphStub(t, `{"data":{"getServer":{"id":"0x1"}}}`)
	h := NewViewsHandler(NewSharedFieldSource(stub.URL, slog.Default()), slog.Default())

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/views", nil), rec)
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (retryable, not a client error)", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "UNAVAILABLE" {
		t.Errorf("code = %v, want UNAVAILABLE — clients switch on this", body["code"])
	}
	if body["hint"] == nil || body["hint"] == "" {
		t.Error("the envelope must carry a hint saying it recovers on its own")
	}
	if body["error"] == nil {
		t.Error("the envelope must carry an error message")
	}
}
