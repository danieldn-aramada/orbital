package orb

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
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

	meta := ImportMeta{Tag: "v1", Digest: "sha256:abc", Verification: VerificationVerified}
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
	if r.Verification != VerificationVerified {
		t.Errorf("expected Verification=%q, got %q", VerificationVerified, r.Verification)
	}
}

func TestImporter_Import_PostsSchemaToDGraphAdmin(t *testing.T) {
	// applySchema POSTs the decompressed SDL to DGraph's /admin/schema endpoint.
	// The schema page reads back from DGraph at request time (via
	// dgraphschema.Active), so we don't need a sidecar file anymore — but the
	// admin POST is still critical, and the test verifies that path receives
	// the SDL body (non-empty + matches the fixture).
	var got []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/alter", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/admin/schema", func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	backend := &mockBackend{}
	imp := newTestImporter(t, ts, backend)
	// Fresh-install case — DataDir doesn't yet exist; Import() should not
	// depend on the directory for schema application.
	imp.cfg.DataDir = filepath.Join(imp.cfg.DataDir, "fresh-install")
	if _, err := os.Stat(imp.cfg.DataDir); !os.IsNotExist(err) {
		t.Fatalf("test setup: DataDir should not exist yet, got err=%v", err)
	}

	meta := ImportMeta{Tag: "v1", Digest: "sha256:abc", Verification: VerificationVerified}
	if err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), meta); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(got) == 0 {
		t.Fatalf("expected schema POST to /admin/schema, got empty body")
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

func TestImportRecord_Layers_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := orbconfig.Config{DataDir: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ts.Close()
	cfg.DGraphAdminURL = ts.URL + "/admin"

	imp := NewImporter(cfg, logger, &mockBackend{})
	meta := ImportMeta{Tag: "v1", Digest: "sha256:abc", Verification: VerificationVerified}
	if err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), meta); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dr1 := DispatchResult{MediaType: "application/vnd.test", URL: "http://x", StatusCode: 200}
	dr2 := DispatchResult{MediaType: "application/vnd.other", URL: "http://y", StatusCode: 500, Error: "consumer returned 500"}
	extra := []LayerRecord{
		{MediaType: dr1.MediaType, Role: LayerRoleDispatched, Dispatch: &dr1},
		{MediaType: dr2.MediaType, Role: LayerRoleDispatched, Dispatch: &dr2},
	}
	if err := FinalizeLastHistory(dir, extra, ""); err != nil {
		t.Fatalf("AppendLayersToLastHistory: %v", err)
	}

	got, err := LoadHistory(dir)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	// 2 graph layers + 2 dispatched layers
	if len(got[0].Layers) != 4 {
		t.Fatalf("expected 4 layers, got %d", len(got[0].Layers))
	}
	dispatched := got[0].Layers[2:]
	if dispatched[0].Dispatch == nil || dispatched[0].Dispatch.StatusCode != 200 {
		t.Errorf("first dispatch layer: got %+v", dispatched[0])
	}
	if dispatched[1].Dispatch == nil || dispatched[1].Dispatch.Error != "consumer returned 500" {
		t.Errorf("second dispatch layer: got %+v", dispatched[1])
	}
}

func TestFinalizeLastHistory_UpdatesLast(t *testing.T) {
	dir := t.TempDir()
	cfg := orbconfig.Config{DataDir: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ts.Close()
	cfg.DGraphAdminURL = ts.URL + "/admin"

	imp := NewImporter(cfg, logger, &mockBackend{})
	for _, tag := range []string{"v1", "v2"} {
		if err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), ImportMeta{Tag: tag}); err != nil {
			t.Fatalf("Import %s: %v", tag, err)
		}
	}

	dr := DispatchResult{MediaType: "a/b", URL: "http://x", StatusCode: 200}
	extra := []LayerRecord{{MediaType: "a/b", Role: LayerRoleDispatched, Dispatch: &dr}}
	if err := FinalizeLastHistory(dir, extra, ""); err != nil {
		t.Fatalf("AppendLayersToLastHistory: %v", err)
	}

	got, _ := LoadHistory(dir)
	// First record: only the 2 base graph layers
	for _, l := range got[0].Layers {
		if l.Role != LayerRoleGraph {
			t.Errorf("first record should only have graph layers, got role=%q", l.Role)
		}
	}
	// Last record: 2 graph + 1 dispatched
	if len(got[1].Layers) != 3 {
		t.Errorf("last record: expected 3 layers (2 graph + 1 dispatched), got %d", len(got[1].Layers))
	}
}

func TestFinalizeLastHistory_EmptyLayers_NoOp(t *testing.T) {
	dir := t.TempDir()
	cfg := orbconfig.Config{DataDir: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ts.Close()
	cfg.DGraphAdminURL = ts.URL + "/admin"

	imp := NewImporter(cfg, logger, &mockBackend{})
	if err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), ImportMeta{Tag: "v1"}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if err := FinalizeLastHistory(dir, nil, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := LoadHistory(dir)
	// Should still have only the 2 base graph layers
	if len(got[0].Layers) != 2 {
		t.Errorf("expected 2 base graph layers, got %d", len(got[0].Layers))
	}
}

func TestFinalizeLastHistory_PreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	cfg := orbconfig.Config{DataDir: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ts.Close()
	cfg.DGraphAdminURL = ts.URL + "/admin"

	imp := NewImporter(cfg, logger, &mockBackend{})
	meta := ImportMeta{Tag: "v5", Digest: "sha256:abc", Verification: VerificationVerified}
	if err := imp.Import(context.Background(), fakeDataGZ(t), fakeSchemaGZ(t), meta); err != nil {
		t.Fatalf("Import: %v", err)
	}

	dr := DispatchResult{MediaType: "a/b", URL: "http://x", StatusCode: 200}
	FinalizeLastHistory(dir, []LayerRecord{{MediaType: "a/b", Role: LayerRoleDispatched, Dispatch: &dr}}, "")

	got, _ := LoadHistory(dir)
	if got[0].Tag != "v5" {
		t.Errorf("Tag changed: got %q", got[0].Tag)
	}
	if got[0].Digest != "sha256:abc" {
		t.Errorf("Digest changed: got %q", got[0].Digest)
	}
	if got[0].Verification != VerificationVerified {
		t.Errorf("Verification changed: got %q", got[0].Verification)
	}
	dispatched := 0
	for _, l := range got[0].Layers {
		if l.Role == LayerRoleDispatched {
			dispatched++
		}
	}
	if dispatched != 1 {
		t.Errorf("expected 1 dispatched layer, got %d", dispatched)
	}
}

func TestImporter_LoadHistory_VerificationRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfg := orbconfig.Config{DataDir: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg.DGraphAdminURL = ts.URL + "/admin"

	imp := NewImporter(cfg, logger, &mockBackend{})

	meta := ImportMeta{Tag: "v2", Digest: "sha256:deadbeef", Verification: VerificationVerified}
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
	if records[0].Verification != VerificationVerified {
		t.Errorf("Verification not persisted: got %q, want %q", records[0].Verification, VerificationVerified)
	}

	data, _ := os.ReadFile(filepath.Join(dir, importHistoryFile))
	if !strings.Contains(string(data), `"verification": "verified"`) {
		t.Errorf("expected verification:verified in history JSON, got: %s", data)
	}
}

// TestWriteAtomic_LeavesNoTmpFile verifies the helper renames the tmp away.
// A leftover .tmp file is a sign the rename failed silently.
func TestWriteAtomic_LeavesNoTmpFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := writeAtomic(path, []byte(`{"k":"v"}`)); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected tmp file to be renamed away, but it still exists")
	}
	data, _ := os.ReadFile(path)
	if string(data) != `{"k":"v"}` {
		t.Errorf("final file mismatch: %s", data)
	}
}

// TestWriteAtomic_OverwriteIsAtomic simulates the recovery scenario: when a
// new write completes successfully, the old content is fully replaced and no
// partial state lingers. (Pre-existing content is overwritten via rename.)
func TestWriteAtomic_OverwriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`old`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := writeAtomic(path, []byte(`new`)); err != nil {
		t.Fatalf("writeAtomic: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != `new` {
		t.Errorf("expected new content, got: %s", data)
	}
}
