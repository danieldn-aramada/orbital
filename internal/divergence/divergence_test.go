package divergence

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_SaveLoad_RoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	entries := []OverrideEntry{
		{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		{OrbID: "netbox:server-02", Field: "powerLimit", IntendedValue: "500w", OverrideValue: "400w", Who: "local:ops", When: "2026-06-02T00:00:00Z"},
	}
	if err := s.Save(entries); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("len: got %d, want %d", len(got), len(entries))
	}
	for i, e := range entries {
		if got[i].OrbID != e.OrbID || got[i].Field != e.Field || got[i].Who != e.Who {
			t.Errorf("entry %d mismatch: got %+v, want %+v", i, got[i], e)
		}
	}
}

func TestStore_Load_Empty(t *testing.T) {
	s := NewStore(t.TempDir())
	entries, err := s.Load()
	if err != nil {
		t.Fatalf("Load on empty store: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(entries))
	}
}

func TestStore_Save_Replace(t *testing.T) {
	s := NewStore(t.TempDir())
	first := []OverrideEntry{{OrbID: "netbox:server-01", Field: "sshEnabled"}}
	second := []OverrideEntry{{OrbID: "netbox:server-02", Field: "powerLimit"}, {OrbID: "netbox:server-03", Field: "pxeBoot"}}

	if err := s.Save(first); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	if err := s.Save(second); err != nil {
		t.Fatalf("Save second: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after replace, got %d", len(got))
	}
	if got[0].OrbID != "netbox:server-02" {
		t.Errorf("first entry: got %q, want %q", got[0].OrbID, "netbox:server-02")
	}
}

func TestStore_PublishRecord_RoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	rec := PublishRecord{
		PublishedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		S3Key:       "divergence/orbital/colo-galleon/2026-06-01T12-00-00Z.json",
	}
	if err := s.SavePublishRecord(rec); err != nil {
		t.Fatalf("SavePublishRecord: %v", err)
	}
	got, err := s.LoadPublishRecord()
	if err != nil {
		t.Fatalf("LoadPublishRecord: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil record")
	}
	if got.S3Key != rec.S3Key {
		t.Errorf("S3Key: got %q, want %q", got.S3Key, rec.S3Key)
	}
	if !got.PublishedAt.Equal(rec.PublishedAt) {
		t.Errorf("PublishedAt: got %v, want %v", got.PublishedAt, rec.PublishedAt)
	}
}

func TestStore_LoadPublishRecord_Empty(t *testing.T) {
	s := NewStore(t.TempDir())
	got, err := s.LoadPublishRecord()
	if err != nil {
		t.Fatalf("LoadPublishRecord on empty store: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestWriteAtomic_LeavesNoTmpFile guards the divergence-package copy of the
// atomic write helper (mirrors the orb-package test).
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
