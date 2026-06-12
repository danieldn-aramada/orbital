package orbserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/internal/divergence"
)

// seedTestMapping writes a small canonical mapping for tests to use. Returns
// the bundle digest so callers can reference it in their intake payload.
func seedTestMapping(t *testing.T, srv *Server) string {
	t.Helper()
	const digest = "sha256:test-digest"
	payload := []byte(`{
		"bundleDigest": "sha256:test-digest",
		"items": [
			{"path": "spec", "orbId": "netbox:dc-1", "type": "DataCenter"},
			{"path": "spec.servers[serviceTag=ABC123]", "orbId": "netbox:server-01", "type": "Server"},
			{"path": "spec.servers[serviceTag=ABC123].idrac", "orbId": "netbox:server-01-idrac", "type": "IdracSettings"},
			{"path": "spec.servers[serviceTag=DEF456]", "orbId": "netbox:server-02", "type": "Server"}
		]
	}`)
	if err := srv.mappingStore.Save(digest, payload); err != nil {
		t.Fatalf("seed mapping: %v", err)
	}
	return digest
}

func TestReceiveDivergence_Valid(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)
	digest := seedTestMapping(t, srv)

	payload := map[string]any{
		"bundleDigest": digest,
		"overrides": []map[string]any{
			{
				"path":          "spec.servers[serviceTag=ABC123].idrac.sshEnabled",
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
	// Verify the path got translated to canonical orbId+field on disk.
	stored, err := srv.divStore.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(stored))
	}
	if stored[0].OrbID != "netbox:server-01-idrac" {
		t.Errorf("orbId after translation: got %q, want %q", stored[0].OrbID, "netbox:server-01-idrac")
	}
	if stored[0].Field != "sshEnabled" {
		t.Errorf("field after translation: got %q, want %q", stored[0].Field, "sshEnabled")
	}
	if stored[0].Type != "IdracSettings" {
		t.Errorf("type after translation: got %q, want %q", stored[0].Type, "IdracSettings")
	}
	if stored[0].Who != "local:admin" || stored[0].When != "2026-06-01T00:00:00Z" {
		t.Errorf("who/when not preserved: %+v", stored[0])
	}
}

func TestReceiveDivergence_UnknownBundleDigest(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	payload := map[string]any{
		"bundleDigest": "sha256:never-imported",
		"overrides":    []map[string]any{},
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for unknown digest, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReceiveDivergence_MissingDigest(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence",
		bytes.NewReader([]byte(`{"overrides":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing digest, got %d", rec.Code)
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
	digest := seedTestMapping(t, srv)

	post := func(paths []string) {
		overrides := make([]map[string]any, 0, len(paths))
		for _, p := range paths {
			overrides = append(overrides, map[string]any{"path": p, "who": "local:admin"})
		}
		payload := map[string]any{"bundleDigest": digest, "overrides": overrides}
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST: expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	post([]string{"spec.servers[serviceTag=ABC123].idrac.sshEnabled"})
	post([]string{
		"spec.servers[serviceTag=DEF456].oobIP",
		"spec.servers[serviceTag=ABC123].idrac.ipmiEnabled",
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

	// Seed store directly with canonical entries (bypasses intake translation).
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
