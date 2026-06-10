package handler

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/google/uuid"
)

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
