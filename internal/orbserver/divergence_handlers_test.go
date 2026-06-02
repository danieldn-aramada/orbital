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

	entries := []divergence.OverrideEntry{
		{OrbID: "netbox:server-01", Field: "sshEnabled", Who: "local:admin", When: "2026-06-01T00:00:00Z"},
	}
	b, _ := json.Marshal(entries)
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
	// Verify persistence.
	stored, err := srv.divStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored) != 1 || stored[0].OrbID != "netbox:server-01" {
		t.Errorf("persisted entries mismatch: %+v", stored)
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

func TestReceiveDivergence_Replace(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	post := func(entries []divergence.OverrideEntry) {
		b, _ := json.Marshal(entries)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST: expected 200, got %d", rec.Code)
		}
	}

	post([]divergence.OverrideEntry{{OrbID: "netbox:server-01", Field: "sshEnabled"}})
	post([]divergence.OverrideEntry{{OrbID: "netbox:server-02", Field: "powerLimit"}, {OrbID: "netbox:server-03", Field: "pxeBoot"}})

	stored, err := srv.divStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 entries after replace, got %d", len(stored))
	}
	if stored[0].OrbID != "netbox:server-02" {
		t.Errorf("first entry after replace: got %q, want %q", stored[0].OrbID, "netbox:server-02")
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

	// Seed store directly.
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
	// No S3 config → divPublisher is nil.
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence/publish", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when S3 not configured, got %d", rec.Code)
	}
}
