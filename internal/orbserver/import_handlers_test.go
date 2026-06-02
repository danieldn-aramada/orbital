package orbserver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// buildZip creates an in-memory zip archive from the given filename → bytes map.
func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		f.Write(data)
	}
	w.Close()
	return buf.Bytes()
}

// validArtifactZip returns a minimal zip with data.json.gz and schema.gz.
func validArtifactZip(t *testing.T) []byte {
	return buildZip(t, map[string][]byte{
		"data.json.gz": []byte("fake-data"),
		"schema.gz":    []byte("fake-schema"),
	})
}

// makeZipForm wraps zip bytes in a multipart form with field name "bundle".
func makeZipForm(t *testing.T, zipBytes []byte) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("bundle", "artifact.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	fw.Write(zipBytes)
	mw.Close()
	return &body, mw.FormDataContentType()
}

func TestImportArtifact_AlreadyRunning(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)
	srv.state.setRunning()

	body, ct := makeZipForm(t, validArtifactZip(t))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", rec.Code)
	}
}

func TestImportArtifact_MissingBundle(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", nil)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestImportArtifact_InvalidZip(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	body, ct := makeZipForm(t, []byte("not a zip"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestImportArtifact_MissingDataLayer(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	z := buildZip(t, map[string][]byte{"schema.gz": []byte("schema")})
	body, ct := makeZipForm(t, z)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestImportArtifact_MissingSchemaLayer(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	z := buildZip(t, map[string][]byte{"data.json.gz": []byte("data")})
	body, ct := makeZipForm(t, z)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestImportArtifact_InvalidLayersJSON(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	z := buildZip(t, map[string][]byte{
		"data.json.gz": []byte("data"),
		"schema.gz":    []byte("schema"),
		"layers.json":  []byte("not json"),
	})
	body, ct := makeZipForm(t, z)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestImportArtifact_ValidZipReturns202(t *testing.T) {
	// Asserts the handler accepts a valid zip and returns 202.
	// The async goroutine will fail (no real DGraph) — acceptable at unit level.
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	body, ct := makeZipForm(t, validArtifactZip(t))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "started" {
		t.Errorf("status: got %q, want %q", resp["status"], "started")
	}
	if resp["importId"] == "" {
		t.Error("importId should be set in response")
	}
	if resp["tag"] == "" {
		t.Error("tag should be set in response")
	}
}

func TestTriggerImport_MissingTag(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = true
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTriggerImport_AlreadyRunning(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = true
	srv, _ := New(cfg)
	srv.state.setRunning()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import", bytes.NewReader([]byte(`{"tag":"v1"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", rec.Code)
	}
}

func TestTriggerImport_AcceptsTag(t *testing.T) {
	// Asserts the synchronous part returns 202. The goroutine will fail (no real
	// registry) but the HTTP response is sent before the goroutine completes.
	t.Chdir("../..")
	cfg := testCfg(t)
	cfg.EnableOCIRegistry = true
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import", bytes.NewReader([]byte(`{"tag":"v1"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportStatus_Shape(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/import/status", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["status"]; !ok {
		t.Error("response missing 'status' key")
	}
}

func TestImportHistory_Empty(t *testing.T) {
	t.Chdir("../..")
	cfg := testCfg(t)
	srv, _ := New(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/import/history", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Must decode as array, not null.
	var records []json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if records == nil {
		t.Error("body should decode as empty array, not null")
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}
