package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/armada/orbital/internal/orb/store"
	"github.com/armada/orbital/internal/orb/store/importrecord"
	"github.com/armada/orbital/internal/orb/store/publishedreport"
)

func newTestClient(t *testing.T) *store.Client {
	t.Helper()
	ctx := context.Background()
	c, err := store.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestImportRecord_RoundTrip(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	rec, err := c.ImportRecord.Create().
		SetTag("v1").
		SetDigest("sha256:deadbeef").
		SetDcOrbID("colo:colo-galleon").
		SetExportJobID("job-123").
		SetImportedAt(time.Now()).
		SetStatus(importrecord.StatusDone).
		SetVerification(importrecord.VerificationVerified).
		SetLayersJSON(`[{"mediaType":"application/vnd.dgraph+gz","role":"data"}]`).
		Save(ctx)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := c.ImportRecord.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tag != "v1" || got.Digest != "sha256:deadbeef" {
		t.Errorf("tag/digest mismatch: %+v", got)
	}
	if got.Status != importrecord.StatusDone {
		t.Errorf("status: got %q want %q", got.Status, importrecord.StatusDone)
	}
	if got.Verification != importrecord.VerificationVerified {
		t.Errorf("verification: got %q", got.Verification)
	}
	if got.DcOrbID != "colo:colo-galleon" {
		t.Errorf("dc_orb_id: got %q", got.DcOrbID)
	}
	if got.LayersJSON == "" {
		t.Errorf("layers_json empty")
	}
}

func TestPendingOverride_SnapshotReplace(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	for i, orbID := range []string{"colo:server-1", "colo:server-2", "colo:server-3"} {
		if _, err := c.PendingOverride.Create().
			SetTypeName("Server").
			SetEntryOrbID(orbID).
			SetField("iDRACSettings.rootPassword").
			SetIntendedValue("intended-" + orbID).
			SetOverrideValue("override-" + orbID).
			SetWho("cb-controller").
			SetFirstSeenAt(time.Now().Add(time.Duration(i) * time.Second)).
			Save(ctx); err != nil {
			t.Fatalf("create %s: %v", orbID, err)
		}
	}

	got, err := c.PendingOverride.Query().All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("initial: want 3 rows, got %d", len(got))
	}

	// Snapshot replace: delete all + insert new set in one transaction.
	tx, err := c.Tx(ctx)
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if _, err := tx.PendingOverride.Delete().Exec(ctx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("delete: %v", err)
	}
	if _, err := tx.PendingOverride.Create().
		SetEntryOrbID("colo:server-1").
		SetField("iDRACSettings.rootPassword").
		SetFirstSeenAt(time.Now()).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err = c.PendingOverride.Query().All(ctx)
	if err != nil {
		t.Fatalf("query after replace: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after replace: want 1 row, got %d", len(got))
	}
	if got[0].EntryOrbID != "colo:server-1" {
		t.Errorf("wrong row survived: %+v", got[0])
	}
}

func TestPublishedReport_AppendHistory(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := "colo:colo-galleon"
	base := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.PublishedReport.Create().
			SetDcOrbID(dc).
			SetPublishedAt(base.Add(time.Duration(i) * time.Minute)).
			SetS3Key("divergence/reports/orb-1/" + time.Now().Format("20060102-150405-") + string(rune('a'+i)) + ".json").
			SetEntryCount(i + 1).
			SetStatus(publishedreport.StatusPublished).
			Save(ctx); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	got, err := c.PublishedReport.Query().
		Where(publishedreport.DcOrbID(dc)).
		Order(store.Desc(publishedreport.FieldPublishedAt)).
		All(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 history rows, got %d", len(got))
	}
	// Newest first.
	if got[0].EntryCount != 3 || got[2].EntryCount != 1 {
		t.Errorf("wrong ordering: entry_counts=%d,%d,%d",
			got[0].EntryCount, got[1].EntryCount, got[2].EntryCount)
	}
}

func TestImportRecord_Indexes(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := "colo:colo-galleon"
	if _, err := c.ImportRecord.Create().
		SetTag("v1").SetDigest("sha256:aaa").
		SetDcOrbID(dc).
		SetStatus(importrecord.StatusDone).
		Save(ctx); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.ImportRecord.Create().
		SetTag("v2").SetDigest("sha256:bbb").
		SetDcOrbID("other-dc").
		SetStatus(importrecord.StatusFailed).
		Save(ctx); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := c.ImportRecord.Query().
		Where(importrecord.DcOrbID(dc)).
		All(ctx)
	if err != nil {
		t.Fatalf("query by dc_orb_id: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 filtered row, got %d", len(got))
	}
	if got[0].Tag != "v1" {
		t.Errorf("wrong row: %+v", got[0])
	}
}
