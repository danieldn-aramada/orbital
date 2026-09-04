//go:build integration

package handler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
)

// Staleness has TWO signals with two owners.
//
//	stale          — a change object's `version` no longer matches its node.
//	                 The AUTHOR's to fix, by rebasing that object. Re-approving
//	                 cannot clear it: it is computed from the changeset, not from
//	                 the base anchor.
//	subtreeChanged — the reviewed scope moved without any change object going out
//	                 of date, typically an edit to an owned child. The REVIEWER's
//	                 to clear, by approving again.
//
// Both block merge. Before this split, approving cleared everything, so a
// reviewer could wave through a proposal written against a value that had moved.

// bump writes a field straight to DGraph, moving the node's version.
func bump(t *testing.T, orbID, hostname string) {
	t.Helper()
	setHostname(t, orbID, hostname)
}

func versionOf(t *testing.T, orbID string) int { return readVersion(t, orbID) }

// ── 1,2. staleness is per change object ────────────────────────────────────

func TestRebase_StaleIsPerChangeObjectNotScope(t *testing.T) {
	f := newCRFixture(t)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
	})
	if st := f.state(t, cr.ID); st.Stale {
		t.Fatal("fresh request reports stale")
	}
	bump(t, crServerA, "moved-by-someone")

	st := f.state(t, cr.ID)
	if !st.Stale {
		t.Error("a change object whose node moved did not make the request stale")
	}
	got := staleEntities(mustGet(t, f, cr.ID), st)
	if len(got) != 1 || got[0].OrbID != crServerA {
		t.Fatalf("staleEntities = %+v, want exactly %s", got, crServerA)
	}
	if got[0].Reviewed != 1 || got[0].Current == nil || *got[0].Current != 2 {
		t.Errorf("staleEntities = %+v, want reviewed 1 → current 2", got[0])
	}
}

// ── 3. an object with no version can never be item-stale ───────────────────

func TestRebase_ItemWithoutVersionIsNeverStale(t *testing.T) {
	f := newCRFixture(t)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "want"},
	})
	bump(t, crServerA, "moved")
	st := f.state(t, cr.ID)
	if st.Stale {
		t.Error("an unconditional change object reported stale — there is no version to compare")
	}
	if !st.SubtreeChanged {
		t.Error("the scope moved but subtreeChanged is false — such a request has no other guard")
	}
}

// ── 4,5,6. the subtree signal survives, separately ─────────────────────────

func TestRebase_ChildEditIsSubtreeChangedNotStale(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// The CHILD moves; the declared object does not.
	crGQL(t, `mutation($orbId: String!, $set: IdracSettingsPatch!) { updateIdracSettings(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": crIdracA, "set": map[string]any{
			"firmwareVersion": "9.9.9", "version": readIdracVersion(t, crIdracA) + 1,
		}})

	st := f.state(t, cr.ID)
	if st.Stale {
		t.Error("a child edit made the request item-stale — nothing the author proposed moved")
	}
	if !st.SubtreeChanged {
		t.Fatal("a child edit did not set subtreeChanged — the reviewer's guarantee is gone")
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); !errors.Is(err, errCRStale) {
		t.Errorf("merge err = %v, want blocked by subtreeChanged", err)
	}
	// The reviewer clears it.
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "re-reviewed"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if st := f.state(t, cr.ID); st.SubtreeChanged {
		t.Error("approving did not clear subtreeChanged")
	}
}

// ── 7,8,15. approved AND stale coexist; approving cannot clear stale ───────

func TestRebase_ApprovedAndStaleCoexistAndApprovingCannotClearIt(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
	})
	bump(t, crServerA, "moved")

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	st := f.state(t, cr.ID)
	if st.Status != approval.StatusApproved {
		t.Fatalf("status = %q, want approved — an approval cast against current state must count", st.Status)
	}
	if !st.Stale {
		t.Fatal("approving cleared item staleness — a reviewer waved through a moved value")
	}
	// 10: merge refuses, naming the entity and pointing at the author.
	_, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	var sw *staleWithEntities
	if !errors.As(err, &sw) {
		t.Fatalf("merge err = %v, want a named stale refusal", err)
	}
	if len(sw.Problems) != 1 || sw.Problems[0].OrbID != crServerA {
		t.Fatalf("refusal does not name the object: %v", sw.Problems)
	}
	if !strings.Contains(sw.Problems[0].Hint, "author") {
		t.Errorf("hint does not say who fixes it: %q", sw.Problems[0].Hint)
	}
	if got := readHostname(t, crServerA); got != "moved" {
		t.Error("the refused merge wrote anyway")
	}
}

// ── 9. availableActions offers edit, not merge ─────────────────────────────

func TestRebase_ApprovedAndStaleOffersEditNotMerge(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
	})
	bump(t, crServerA, "moved")
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	st := f.state(t, cr.ID)
	acts := availableActions(mustGet(t, f, cr.ID), st, author, user.RoleDev, false)
	if containsStr(acts, "merge") {
		t.Errorf("actions = %v, want no merge while stale", acts)
	}
	if !containsStr(acts, "edit") {
		t.Errorf("actions = %v, want edit offered to the author", acts)
	}
}

// ── 11,13. the author's rebase clears it and dismisses approvals ───────────

func TestRebase_AuthorRebaseClearsStaleAndReturnsToNeedsReview(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
	})
	bump(t, crServerA, "moved")
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if st := f.state(t, cr.ID); st.Valid != 1 {
		t.Fatalf("valid approvals = %d before rebase, want 1", st.Valid)
	}

	// The rebase moves NO graph state — only the version the object carries. The
	// hash is therefore unchanged, which is exactly why the revision exists.
	if _, problems, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
		}}}); err != nil || len(problems) > 0 {
		t.Fatalf("rebase: err=%v problems=%v", err, problems)
	}

	st := f.state(t, cr.ID)
	if st.Stale {
		t.Error("rebase did not clear stale")
	}
	if st.Valid != 0 {
		t.Errorf("valid approvals = %d after rebase, want 0 — the reviewer never saw this proposal", st.Valid)
	}
	if st.Status != approval.StatusOpen {
		t.Errorf("status = %q after rebase, want open", st.Status)
	}
}

// ── 12. removing one stale object clears it, leaving the rest ──────────────

func TestRebase_RemovingOneStaleObjectLeavesTheOthers(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	cr := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "a"}, Version: intp(versionOf(t, crServerA))},
		approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "b"}, Version: intp(versionOf(t, crServerB))},
	)
	bump(t, crServerA, "moved")
	if st := f.state(t, cr.ID); !st.Stale {
		t.Fatal("expected stale after the bump")
	}

	if _, problems, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerB, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "b"}, Version: intp(versionOf(t, crServerB)),
		}}}); err != nil || len(problems) > 0 {
		t.Fatalf("drop: err=%v problems=%v", err, problems)
	}
	st := f.state(t, cr.ID)
	if st.Stale {
		t.Error("dropping the stale object did not clear stale")
	}
	if len(st.Changeset.Changes) != 1 || st.Changeset.Changes[0].OrbID != crServerB {
		t.Errorf("changeset = %+v, want only %s left", st.Changeset.Changes, crServerB)
	}
}

// ── 14. a non-author cannot rebase ─────────────────────────────────────────

func TestRebase_NonAuthorCannotRebase(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
	})
	bump(t, crServerA, "moved")

	_, _, err := f.crh.Amend(ctx, cr.ID, reviewer, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
		}}})
	if !errors.Is(err, errCRForbidden) {
		t.Fatalf("err = %v, want forbidden — only the author rebases", err)
	}
	if st := f.state(t, cr.ID); !st.Stale {
		t.Error("the refused rebase cleared staleness anyway")
	}
}

// ── 16. the happy path still merges ────────────────────────────────────────

func TestRebase_CleanRequestStillMergesFirstTime(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "clean"}, Version: intp(versionOf(t, crServerA)),
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("a clean request did not merge: %v", err)
	}
	if got := readHostname(t, crServerA); got != "clean" {
		t.Errorf("hostname = %q, want clean", got)
	}
}

// ── 17. superseded approvals stay visible ──────────────────────────────────

func TestRebase_SupersededApprovalsRemainVisible(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "want"}, Version: intp(versionOf(t, crServerA)),
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, _, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "want2"}, Version: intp(versionOf(t, crServerA)),
		}}}); err != nil {
		t.Fatalf("amend: %v", err)
	}
	st := f.state(t, cr.ID)
	if st.Valid != 0 {
		t.Errorf("valid = %d, want 0 after an amend", st.Valid)
	}
	if len(st.Approvals) != 1 {
		t.Errorf("approvals recorded = %d, want the superseded one kept as history", len(st.Approvals))
	}
}

func intp(v int) *int { return &v }

// A merged request reports NEITHER staleness signal. Merging bumps the version
// vector by definition, so subtreeChanged is guaranteed to fire on every merged
// request if it is not cleared — the banner told people to re-approve something
// already applied. Both directions asserted: a flag that never fires is as
// wrong as one that always does.
func TestRebase_TerminalRequestReportsNeitherStaleSignal(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "merged-value"}, Version: intp(versionOf(t, crServerA)),
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// While OPEN and genuinely moved, it must still say so.
	bump(t, crServerA, "moved-while-open")
	if st := f.state(t, cr.ID); !st.SubtreeChanged {
		t.Fatal("an open request whose scope moved does not report subtreeChanged")
	}

	// Rebase onto the new version, re-approve, merge.
	if _, _, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "merged-value"}, Version: intp(versionOf(t, crServerA)),
		}}}); err != nil {
		t.Fatalf("rebase: %v", err)
	}
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge: %v", err)
	}

	st := f.state(t, cr.ID)
	if st.Status != approval.StatusMerged {
		t.Fatalf("status = %q, want merged", st.Status)
	}
	if st.SubtreeChanged {
		t.Error("a merged request reports subtreeChanged — the UI tells the reader to re-approve what is already applied")
	}
	if st.Stale {
		t.Error("a merged request reports stale")
	}
}
