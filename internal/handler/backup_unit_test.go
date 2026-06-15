package handler

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// stubBlobstore is a minimal blobstore.Store stub for the test-connection
// handler tests. Only Ping carries real behavior; the rest exist to satisfy
// the interface.
type stubBlobstore struct {
	pingErr error
}

func (s *stubBlobstore) Put(context.Context, string, io.Reader, string) error { return nil }
func (s *stubBlobstore) Get(context.Context, string) ([]byte, error)          { return nil, nil }
func (s *stubBlobstore) List(context.Context, string) ([]string, error)       { return nil, nil }
func (s *stubBlobstore) Delete(context.Context, string) error                 { return nil }
func (s *stubBlobstore) PresignURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (s *stubBlobstore) Ping(context.Context) error { return s.pingErr }

// ── TestConnection HX-Request negotiation ─────────────────────────────────────
//
// The same endpoint serves backups.gohtml and divergence-reports.gohtml. Both
// pages target their own result span via hx-target; the server-rendered
// fragment is identical, so one handler covers both UIs.

func TestTestConnection_HX_Success(t *testing.T) {
	h := &BackupHandler{storage: &stubBlobstore{pingErr: nil}}
	c, rec := newBackupEchoCtx(http.MethodPost, "/api/v1/backup/test-connection")
	c.Request().Header.Set("HX-Request", "true")

	if err := h.TestConnection(c); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected HTML content-type for HX-Request, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Connected") || !strings.Contains(body, "has-text-success") {
		t.Errorf("expected success fragment, got: %s", body)
	}
}

func TestTestConnection_HX_Failure_EscapesErrorMessage(t *testing.T) {
	// The error string is interpolated into HTML — must be escaped to prevent
	// a backend that returns crafted text from poisoning the fragment.
	h := &BackupHandler{storage: &stubBlobstore{pingErr: errors.New("denied <script>x</script>")}}
	c, rec := newBackupEchoCtx(http.MethodPost, "/api/v1/backup/test-connection")
	c.Request().Header.Set("HX-Request", "true")

	if err := h.TestConnection(c); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "has-text-danger") {
		t.Errorf("expected danger styling in fragment, got: %s", body)
	}
	if strings.Contains(body, "<script>x</script>") {
		t.Errorf("error message must be HTML-escaped; got raw script tag in body: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected escaped script tag in fragment, got: %s", body)
	}
}

func TestTestConnection_JSON_FallbackForNonHXCallers(t *testing.T) {
	h := &BackupHandler{storage: &stubBlobstore{pingErr: nil}}
	c, rec := newBackupEchoCtx(http.MethodPost, "/api/v1/backup/test-connection")

	if err := h.TestConnection(c); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("non-HX caller must get JSON; got content-type %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if got["ok"] != true {
		t.Errorf("expected ok=true in JSON body, got: %v", got)
	}
}

func newBackupEchoCtx(method, path string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// ── toBackupResponse ──────────────────────────────────────────────────────────

func TestToBackupResponse_IncludesTrigger(t *testing.T) {
	j := &ent.Backup{
		ID:        uuid.New(),
		Status:    "completed",
		Trigger:   backup.TriggerScheduled,
		CreatedAt: time.Now(),
	}
	r := toBackupResponse(j)
	if r.Trigger != "scheduled" {
		t.Errorf("expected trigger=scheduled, got %q", r.Trigger)
	}
}

func TestToBackupResponse_ManualTrigger(t *testing.T) {
	j := &ent.Backup{
		ID:        uuid.New(),
		Status:    "pending",
		Trigger:   backup.TriggerManual,
		CreatedAt: time.Now(),
	}
	r := toBackupResponse(j)
	if r.Trigger != "manual" {
		t.Errorf("expected trigger=manual, got %q", r.Trigger)
	}
}

// ── readSchemaVersion ─────────────────────────────────────────────────────────

func TestReadSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.graphql")
	if err := os.WriteFile(schemaPath, []byte("type Foo { id: ID! }"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readSchemaVersion(schemaPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "v1" {
		t.Errorf("want v1, got %q", got)
	}
}

func TestReadSchemaVersion_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := readSchemaVersion(filepath.Join(dir, "schema.graphql"))
	if err == nil {
		t.Error("expected error for missing VERSION file, got nil")
	}
}

// ── writeZip manifest ─────────────────────────────────────────────────────────

func TestWriteZipIncludesManifest(t *testing.T) {
	manifest := backupManifest{
		ManifestVersion: 1,
		CreatedAt:       "2026-06-09T00:00:00Z",
		OrbitalVersion:  "v1.0.0",
		SchemaVersion:   "v1",
	}
	manifestJSON, _ := json.Marshal(manifest)

	path := filepath.Join(t.TempDir(), "test.zip")
	if err := writeZip(path, []byte("data"), []byte("schema"), []byte("gql"), manifestJSON); err != nil {
		t.Fatalf("writeZip: %v", err)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()

	names := make(map[string]bool)
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"manifest.json", "data.json.gz", "schema.gz", "gql_schema.gz"} {
		if !names[want] {
			t.Errorf("missing zip entry %q; got %v", want, names)
		}
	}

	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, _ := f.Open()
		defer rc.Close()
		var got backupManifest
		if err := json.NewDecoder(rc).Decode(&got); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		if got.SchemaVersion != "v1" || got.OrbitalVersion != "v1.0.0" {
			t.Errorf("unexpected manifest: %+v", got)
		}
	}
}

// ── formatDuration ────────────────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{24 * time.Hour, "1d"},
		{7 * 24 * time.Hour, "7d"},
		{time.Hour, "1h"},
		{12 * time.Hour, "12h"},
		{30 * time.Minute, "30m"},
		{0, "0s"},
		{90 * time.Second, "1m30s"},
	}
	for _, c := range cases {
		got := formatDuration(c.d)
		if got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
