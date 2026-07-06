package divergence

import (
	"context"
	"testing"
	"time"

	"github.com/armada/orbital/internal/orb/store"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	c, err := store.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return NewStore(c)
}

func TestStore_SaveLoad_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	entries := []OverrideEntry{
		{OrbID: "netbox:server-01", Type: "Server", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		{OrbID: "netbox:server-02", Type: "Server", Field: "powerLimit", IntendedValue: "500w", OverrideValue: "400w", Who: "local:ops", When: "2026-06-02T00:00:00Z"},
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
		if got[i].When != e.When {
			t.Errorf("entry %d when: got %q, want %q", i, got[i].When, e.When)
		}
	}
	// Round-trip the intendedValue types — bool and string must survive.
	if got[0].IntendedValue != false || got[0].OverrideValue != true {
		t.Errorf("bool round-trip failed: %+v", got[0])
	}
	if got[1].IntendedValue != "500w" || got[1].OverrideValue != "400w" {
		t.Errorf("string round-trip failed: %+v", got[1])
	}
}

func TestStore_Load_Empty(t *testing.T) {
	s := newTestStore(t)
	entries, err := s.Load()
	if err != nil {
		t.Fatalf("Load on empty store: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(entries))
	}
}

func TestStore_Save_Replace(t *testing.T) {
	s := newTestStore(t)
	first := []OverrideEntry{{OrbID: "netbox:server-01", Type: "Server", Field: "sshEnabled"}}
	second := []OverrideEntry{
		{OrbID: "netbox:server-02", Type: "Server", Field: "powerLimit"},
		{OrbID: "netbox:server-03", Type: "Server", Field: "pxeBoot"},
	}

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
	s := newTestStore(t)
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
	s := newTestStore(t)
	got, err := s.LoadPublishRecord()
	if err != nil {
		t.Fatalf("LoadPublishRecord on empty store: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestStore_PublishHistory_Append pins the new history feature — successive
// publishes accumulate, LoadPublishRecord returns newest, LoadPublishHistory
// returns all rows newest-first.
func TestStore_PublishHistory_Append(t *testing.T) {
	s := newTestStore(t)
	dc := "colo:colo-galleon"
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		rec := PublishRecord{
			PublishedAt: base.Add(time.Duration(i) * time.Minute),
			S3Key:       "divergence/reports/orbital/colo-galleon/" + string(rune('a'+i)) + ".json",
		}
		entries := make([]OverrideEntry, i+1)
		for j := range entries {
			entries[j] = OverrideEntry{OrbID: "colo:s-" + string(rune('a'+j)), Type: "Server", Field: "hostname"}
		}
		if err := s.SavePublishRow(rec, dc, entries); err != nil {
			t.Fatalf("SavePublishRow[%d]: %v", i, err)
		}
	}

	// LoadPublishRecord returns newest.
	last, err := s.LoadPublishRecord()
	if err != nil || last == nil {
		t.Fatalf("LoadPublishRecord: %v, %+v", err, last)
	}
	if !last.PublishedAt.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("newest published_at wrong: got %v", last.PublishedAt)
	}

	// LoadPublishHistory returns all rows newest-first.
	rows, total, err := s.LoadPublishHistory("", 0, 0)
	if err != nil {
		t.Fatalf("LoadPublishHistory: %v", err)
	}
	if total != 3 {
		t.Errorf("total: got %d, want 3", total)
	}
	if len(rows) != 3 {
		t.Fatalf("rows: got %d, want 3", len(rows))
	}
	if rows[0].EntryCount != 3 || rows[2].EntryCount != 1 {
		t.Errorf("wrong ordering: counts=%d,%d,%d", rows[0].EntryCount, rows[1].EntryCount, rows[2].EntryCount)
	}

	// Filter by DC.
	filtered, _, err := s.LoadPublishHistory(dc, 0, 0)
	if err != nil {
		t.Fatalf("LoadPublishHistory(dc): %v", err)
	}
	if len(filtered) != 3 {
		t.Errorf("dc filter: got %d rows, want 3", len(filtered))
	}
	other, _, err := s.LoadPublishHistory("other-dc", 0, 0)
	if err != nil {
		t.Fatalf("LoadPublishHistory(other): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("other dc filter: got %d rows, want 0", len(other))
	}

	// Pagination.
	page1, _, err := s.LoadPublishHistory("", 2, 0)
	if err != nil {
		t.Fatalf("LoadPublishHistory(limit=2): %v", err)
	}
	if len(page1) != 2 || page1[0].EntryCount != 3 {
		t.Errorf("page 1: got %d rows starting %d", len(page1), page1[0].EntryCount)
	}
	page2, _, err := s.LoadPublishHistory("", 2, 2)
	if err != nil {
		t.Fatalf("LoadPublishHistory(limit=2,offset=2): %v", err)
	}
	if len(page2) != 1 || page2[0].EntryCount != 1 {
		t.Errorf("page 2: got %d rows starting %d", len(page2), page2[0].EntryCount)
	}
}
