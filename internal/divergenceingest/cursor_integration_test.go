//go:build integration

package divergenceingest

// Acceptance 5 of the orbital-HA design: two replicas polling divergence must
// ingest a report exactly once, and an existing operator resolution must
// survive. This covers the mechanism that decides it — the cursor claim.
//
// Why the claim and not applyReport: re-ingesting a report fires the supersede
// branch, which silently drops operator resolutions. Nothing errors and nothing
// logs; the decision is simply gone. So the guard has to be at the point where
// a replica decides it owns the report, and that is what these tests pin.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/divergenceingestcursor"
	"github.com/armada/orbital/internal/testutil"
)

func newIngester(db *ent.Client) *Ingester {
	return &Ingester{db: db}
}

// The report is claimed by exactly one replica, whether or not a cursor row
// already exists — the two paths are different code (conditional UPDATE vs
// INSERT racing a unique index) and both have to hold.
func TestAdvanceCursor_ExactlyOneReplicaClaimsAReport(t *testing.T) {
	ctx := context.Background()
	published := time.Now().UTC().Truncate(time.Millisecond)

	for _, tc := range []struct {
		name       string
		seedCursor bool
	}{
		{"no cursor yet — racing INSERT", false},
		{"cursor exists — racing conditional UPDATE", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			const dc = "colo:dc-test"

			if tc.seedCursor {
				if _, err := db.DivergenceIngestCursor.Create().
					SetDcOrbID(dc).
					SetLastPublishedAt(published.Add(-time.Hour)).
					Save(ctx); err != nil {
					t.Fatalf("seed cursor: %v", err)
				}
			}

			const replicas = 8
			var wg sync.WaitGroup
			won := make([]bool, replicas)
			start := make(chan struct{})
			for i := range replicas {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					ok, err := newIngester(db).advanceCursor(ctx, dc, published)
					if err != nil {
						t.Errorf("replica %d: %v", i, err)
						return
					}
					won[i] = ok
				}(i)
			}
			close(start)
			wg.Wait()

			n := 0
			for _, w := range won {
				if w {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("expected exactly 1 replica to claim the report, got %d — "+
					"every extra claim is a re-ingest, which silently drops operator resolutions", n)
			}

			cur, err := db.DivergenceIngestCursor.Query().
				Where(divergenceingestcursor.DcOrbID(dc)).Only(ctx)
			if err != nil {
				t.Fatalf("read back cursor: %v", err)
			}
			if !cur.LastPublishedAt.Equal(published) {
				t.Errorf("cursor = %v, want %v", cur.LastPublishedAt, published)
			}
		})
	}
}

// A report at or behind the cursor must never be re-claimed. This is the
// restart case: a redeploy must not cause orbital to re-ingest a report it has
// already processed.
func TestAdvanceCursor_RefusesAReportAtOrBehindTheCursor(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	const dc = "colo:dc-test"
	now := time.Now().UTC().Truncate(time.Millisecond)

	won, err := newIngester(db).advanceCursor(ctx, dc, now)
	if err != nil || !won {
		t.Fatalf("first claim: won=%v err=%v", won, err)
	}

	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{"same publishedAt", now},
		{"older publishedAt", now.Add(-time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			won, err := newIngester(db).advanceCursor(ctx, dc, tc.at)
			if err != nil {
				t.Fatalf("advanceCursor: %v", err)
			}
			if won {
				t.Fatal("re-claimed an already-ingested report")
			}
		})
	}

	cur := db.DivergenceIngestCursor.Query().Where(divergenceingestcursor.DcOrbID(dc)).OnlyX(ctx)
	if !cur.LastPublishedAt.Equal(now) {
		t.Errorf("cursor moved backwards: %v, want %v", cur.LastPublishedAt, now)
	}
}
