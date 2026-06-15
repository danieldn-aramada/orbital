package orbserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/internal/divergence"
)

func TestReceiveDivergence_Valid(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	payload := map[string]any{
		"overrides": []map[string]any{
			{
				"orbId":         "netbox:server-01-idrac",
				"field":         "sshEnabled",
				"type":          "IdracSettings",
				"intendedValue": false,
				"overrideValue": true,
				"who":           "local:admin",
				"when":          "2026-06-01T00:00:00Z",
			},
		},
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["stored"] != 1 {
		t.Errorf("stored: got %d, want 1", resp["stored"])
	}
	stored, err := srv.divStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(stored))
	}
	got := stored[0]
	if got.OrbID != "netbox:server-01-idrac" || got.Field != "sshEnabled" || got.Type != "IdracSettings" {
		t.Errorf("entry stored incorrectly: %+v", got)
	}
	if got.Who != "local:admin" || got.When != "2026-06-01T00:00:00Z" {
		t.Errorf("who/when not preserved: %+v", got)
	}
}

func TestReceiveDivergence_MissingRequiredField(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	// Omit `type` — should reject.
	payload := map[string]any{
		"overrides": []map[string]any{
			{
				"orbId":         "netbox:server-01",
				"field":         "hostname",
				"intendedValue": "a",
				"overrideValue": "b",
				"who":           "local:admin",
				"when":          "2026-06-01T00:00:00Z",
			},
		},
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing type, got %d", rec.Code)
	}
}

func TestReceiveDivergence_BadTimestamp(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	payload := map[string]any{
		"overrides": []map[string]any{
			{
				"orbId":         "netbox:server-01",
				"field":         "hostname",
				"type":          "Server",
				"intendedValue": "a",
				"overrideValue": "b",
				"who":           "local:admin",
				"when":          "yesterday",
			},
		},
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unparseable timestamp, got %d", rec.Code)
	}
}

func TestReceiveDivergence_InvalidJSON(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestReceiveDivergence_EmptyArrayResolvesAll(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	// Pre-seed with two entries.
	seed := []divergence.OverrideEntry{
		{OrbID: "netbox:s1", Field: "f", Type: "Server", IntendedValue: "a", OverrideValue: "b", Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		{OrbID: "netbox:s2", Field: "f", Type: "Server", IntendedValue: "a", OverrideValue: "b", Who: "local:admin", When: "2026-06-01T00:00:00Z"},
	}
	if err := srv.divStore.Save(seed); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	// Post empty array — should fully replace, leaving nothing.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence",
		bytes.NewReader([]byte(`{"overrides":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty array, got %d", rec.Code)
	}
	stored, err := srv.divStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored) != 0 {
		t.Errorf("expected store cleared by empty array; got %d entries", len(stored))
	}
}

func TestReceiveDivergence_Replace(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	post := func(entries []divergence.OverrideEntry) {
		b, _ := json.Marshal(map[string]any{"overrides": entries})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST: expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	post([]divergence.OverrideEntry{
		{OrbID: "netbox:server-01-idrac", Field: "sshEnabled", Type: "IdracSettings",
			IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
	})
	post([]divergence.OverrideEntry{
		{OrbID: "netbox:server-02", Field: "oobIP", Type: "Server",
			IntendedValue: "10.0.0.1", OverrideValue: "10.0.0.99", Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		{OrbID: "netbox:server-01-idrac", Field: "ipmiEnabled", Type: "IdracSettings",
			IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
	})

	stored, err := srv.divStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 entries after replace, got %d", len(stored))
	}
	if stored[0].OrbID != "netbox:server-02" || stored[0].Field != "oobIP" {
		t.Errorf("first entry mismatch: %+v", stored[0])
	}
}

func TestGetDivergence_Empty(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/divergence", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var entries []divergence.OverrideEntry
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(entries))
	}
}

func TestGetDivergence_WithEntries(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	seed := []divergence.OverrideEntry{
		{OrbID: "netbox:server-01", Field: "sshEnabled", Who: "local:admin"},
		{OrbID: "netbox:server-02", Field: "powerLimit", Who: "local:ops"},
	}
	if err := srv.divStore.Save(seed); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/divergence", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var entries []divergence.OverrideEntry
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].OrbID != "netbox:server-01" || entries[1].OrbID != "netbox:server-02" {
		t.Errorf("entries mismatch: %+v", entries)
	}
}

func TestPublishDivergence_NoPublisher(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence/publish", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when S3 not configured, got %d", rec.Code)
	}
}
