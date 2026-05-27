package orb

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/orbconfig"
)

// mockBackend records RunLive calls and returns a configurable error.
type mockBackend struct {
	called   bool
	dataPath string
	err      error
}

func (m *mockBackend) RunLive(_ context.Context, dataPath string) (string, error) {
	m.called = true
	m.dataPath = dataPath
	return "", m.err
}

// fakeSchemaGZ returns a minimal valid gzip-compressed GraphQL schema.
func fakeSchemaGZ(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte("type Query { _dummy: String }"))
	gz.Close()
	return buf.Bytes()
}

// fakeDataGZ returns a minimal gzip payload (not valid RDF, but sufficient for unit tests
// that mock the backend and never run dgraph live).
func fakeDataGZ(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte(`{}`))
	gz.Close()
	return buf.Bytes()
}

// newTestDGraphServer returns an httptest server that handles /alter and /admin/schema,
// using the provided status codes.
func newTestDGraphServer(alterStatus, schemaStatus int) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/alter", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(alterStatus)
	})
	mux.HandleFunc("/admin/schema", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(schemaStatus)
	})
	return httptest.NewServer(mux)
}

func newTestImporter(t *testing.T, ts *httptest.Server, backend DGraphBackend) *Importer {
	t.Helper()
	cfg := orbconfig.Config{
		DGraphAdminURL: ts.URL + "/admin",
		DataDir:        t.TempDir(),
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewImporter(cfg, logger, backend)
}

func TestImporter_Import_Success(t *testing.T) {
	ts := newTestDGraphServer(http.StatusOK, http.StatusOK)
	defer ts.Close()

	backend := &mockBackend{}
	imp := newTestImporter(t, ts, backend)

	meta := ImportMeta{Tag: "v1", Digest: "sha256:abc", Verified: true}
	if err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), meta); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if !backend.called {
		t.Error("expected backend.RunLive to be called")
	}
	if !strings.HasSuffix(backend.dataPath, scratchFile) {
		t.Errorf("expected data path to end with %q, got %q", scratchFile, backend.dataPath)
	}

	records, err := LoadHistory(imp.cfg.DataDir)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(records))
	}
	r := records[0]
	if r.Tag != "v1" {
		t.Errorf("expected tag %q, got %q", "v1", r.Tag)
	}
	if r.Status != "done" {
		t.Errorf("expected status %q, got %q", "done", r.Status)
	}
	if !r.Verified {
		t.Error("expected Verified=true in history record")
	}
}

func TestImporter_Import_DropAllError(t *testing.T) {
	ts := newTestDGraphServer(http.StatusInternalServerError, http.StatusOK)
	defer ts.Close()

	backend := &mockBackend{}
	imp := newTestImporter(t, ts, backend)

	err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), ImportMeta{Tag: "v1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "drop_all") {
		t.Errorf("expected error to mention drop_all, got: %v", err)
	}
	if backend.called {
		t.Error("backend should not be called when drop_all fails")
	}
}

func TestImporter_Import_SchemaError(t *testing.T) {
	ts := newTestDGraphServer(http.StatusOK, http.StatusInternalServerError)
	defer ts.Close()

	backend := &mockBackend{}
	imp := newTestImporter(t, ts, backend)

	err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), ImportMeta{Tag: "v1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "apply schema") {
		t.Errorf("expected error to mention apply schema, got: %v", err)
	}
	if backend.called {
		t.Error("backend should not be called when schema apply fails")
	}
}

func TestImporter_Import_BackendError(t *testing.T) {
	ts := newTestDGraphServer(http.StatusOK, http.StatusOK)
	defer ts.Close()

	backend := &mockBackend{err: fmt.Errorf("dgraph live crashed")}
	imp := newTestImporter(t, ts, backend)

	err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), ImportMeta{Tag: "v1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "dgraph live") {
		t.Errorf("expected error to mention dgraph live, got: %v", err)
	}
}

func TestImporter_LoadHistory_VerifiedRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfg := orbconfig.Config{DataDir: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg.DGraphAdminURL = ts.URL + "/admin"

	imp := NewImporter(cfg, logger, &mockBackend{})

	meta := ImportMeta{Tag: "v2", Digest: "sha256:deadbeef", Verified: true}
	if err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), meta); err != nil {
		t.Fatalf("Import: %v", err)
	}

	records, err := LoadHistory(dir)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected at least one record")
	}
	if !records[0].Verified {
		t.Errorf("Verified field not persisted: got false, want true")
	}

	// Verify the file is valid JSON with the verified field.
	data, _ := os.ReadFile(filepath.Join(dir, importHistoryFile))
	if !strings.Contains(string(data), `"verified": true`) {
		t.Errorf("expected verified:true in history JSON, got: %s", data)
	}
}
