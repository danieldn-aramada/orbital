//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
)

// setCreatedAt pins a request's created_at. The column is immutable through
// ent, and a window test that relies on wall-clock gaps between inserts is a
// flake waiting for a fast machine.
func setCreatedAt(t *testing.T, cr *ent.ApprovalRequest, at time.Time) {
	t.Helper()
	if _, err := testutil.RawTestDB(t).ExecContext(context.Background(),
		`UPDATE approval_requests SET created_at = $1 WHERE id = $2`, at, cr.ID); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
}

func sortedIDs(t *testing.T, f *crFixture, query string) string {
	t.Helper()
	ids := keysOf(listIDs(t, f, query))
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func idsOf(crs ...*ent.ApprovalRequest) string {
	ids := make([]string, 0, len(crs))
	for _, cr := range crs {
		ids = append(ids, crHumanID(cr))
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// AC 1 + 5 — the window keeps only requests inside it, each operator honours
// its own boundary, and `total` counts what survived the filter (listIDs
// asserts total == len(items)).
func TestListFilter_CreatedAtWindowReturnsOnlyRequestsInside(t *testing.T) {
	f := newCRFixture(t)
	day := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	older := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "older"}})
	middle := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "middle"}})
	newer := f.open(t, approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate, Set: map[string]any{"firmwareVersion": "2.0.0"}})
	setCreatedAt(t, older, day.Add(-24*time.Hour))
	setCreatedAt(t, middle, day)
	setCreatedAt(t, newer, day.Add(24*time.Hour))

	at := url.QueryEscape(day.Format(time.RFC3339))
	for query, want := range map[string]string{
		"?createdAt_gte=" + at:                          idsOf(middle, newer),
		"?createdAt_gt=" + at:                           idsOf(newer),
		"?createdAt_lte=" + at:                          idsOf(older, middle),
		"?createdAt_lt=" + at:                           idsOf(older),
		"?createdAt_gte=" + at + "&createdAt_lte=" + at: idsOf(middle),
	} {
		if got := sortedIDs(t, f, query); got != want {
			t.Errorf("%s returned %s, want %s", query, got, want)
		}
	}
}

// AC 2 + 3 at the endpoint: refused with the envelope, never a silent full list.
func TestListFilter_BadTimeFilterIsA400(t *testing.T) {
	f := newCRFixture(t)
	f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "x"}})
	for _, query := range []string{"?createdAt_gte=yesterday", "?createAt_gte=2026-10-01T00:00:00Z"} {
		rec := callList(t, f.crh, query)
		var body errorResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusBadRequest || body.Code != CodeBadUserInput {
			t.Errorf("%s: got %d %+v, want 400 BAD_USER_INPUT", query, rec.Code, body)
		}
	}
}

// AC 8 + 9 — a request matches on ANY item's type, not only the first;
// repeated types are OR-ed; an unknown type is an empty list, not an error.
func TestListFilter_TypeMatchesAnyItemNotJustTheFirst(t *testing.T) {
	f := newCRFixture(t)
	serverThenIdrac := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "s"}},
		approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate, Set: map[string]any{"firmwareVersion": "3.0.0"}},
	)
	serverOnly := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "b"}})

	for query, want := range map[string]string{
		"?type=IdracSettings":             idsOf(serverThenIdrac),
		"?type=Server":                    idsOf(serverThenIdrac, serverOnly),
		"?type=IdracSettings&type=Server": idsOf(serverThenIdrac, serverOnly),
		"?type=NoSuchType":                "",
	} {
		if got := sortedIDs(t, f, query); got != want {
			t.Errorf("%s returned %q, want %q", query, got, want)
		}
	}
}

// AC 10 — every status returns exactly its own requests, and status ANDs with
// type and time. The derived split (open vs approved) is what SQL alone cannot
// answer, so a regression there passes every stored-status check.
func TestListFilter_EveryStatusReturnsOnlyItsOwn(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	open := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "open"}})

	approved := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "approved"}})
	if _, err := f.crh.Approve(ctx, approved.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	rejected := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"rackPosition": 7}})
	if _, err := f.crh.Reject(ctx, rejected.ID, reviewer, user.RoleDev, "no"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	closed := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"rackPosition": 8}})
	if _, err := f.crh.Close(ctx, closed.ID, author, user.RoleDev); err != nil {
		t.Fatalf("close: %v", err)
	}

	merged := f.open(t, approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate, Set: map[string]any{"firmwareVersion": "4.0.0"}})
	if _, err := f.crh.Approve(ctx, merged.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve merged: %v", err)
	}
	if _, err := f.crh.Merge(ctx, merged.ID, author, user.RoleDev, false, ""); err != nil {
		t.Fatalf("merge: %v", err)
	}

	future := url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	for query, want := range map[string]string{
		"?status=open":                            idsOf(open),
		"?status=approved":                        idsOf(approved),
		"?status=active":                          idsOf(open, approved),
		"?status=rejected":                        idsOf(rejected),
		"?status=closed":                          idsOf(closed),
		"?status=merged":                          idsOf(merged),
		"?status=rejected&status=closed":          idsOf(rejected, closed),
		"?status=merged&type=IdracSettings":       idsOf(merged),
		"?status=active&type=IdracSettings":       "",
		"?status=active&createdAt_gte=" + future:  "",
		"?status=merged&executedAt_lte=" + future: idsOf(merged),
	} {
		if got := sortedIDs(t, f, query); got != want {
			t.Errorf("%s returned %q, want %q", query, got, want)
		}
	}

	if rec := callList(t, f.crh, "?status=Merged"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown status returned %d, want 400", rec.Code)
	}
}
