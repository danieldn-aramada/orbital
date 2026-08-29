//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/armada/orbital/ent"
	entapproval "github.com/armada/orbital/ent/approval"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
	"github.com/google/uuid"
)

const (
	crNS      = "cr-engine"
	crDC      = "cr-engine:datacenter-1"
	crRack    = "cr-engine:rack-1"
	crServerA = "cr-engine:server-AAA"
	crServerB = "cr-engine:server-BBB"
	crIdracA  = "cr-engine:idrac-AAA"

	author   = "proposer@test.com"
	reviewer = "reviewer@test.com"
)

type crFixture struct {
	db  *ent.Client
	crh *ChangeRequest
}

func newCRFixture(t *testing.T) *crFixture {
	t.Helper()
	db := testutil.NewTestDB(t)
	gql := NewGraphQL(testutil.DGraphURL(), db, slog.Default(), false)
	crh := NewChangeRequest(db, gql, testutil.DGraphURL(), slog.Default())
	seedCREngineFixture(t)
	return &crFixture{db: db, crh: crh}
}

// requireApproval installs a policy so the namespace is actually governed.
// Without one, required is 0 and every request reads as approved immediately —
// which is the correct opt-in default, and useless for testing the gate.
func (f *crFixture) requireApproval(t *testing.T, n int) {
	t.Helper()
	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetRequiredApprovals(n).
		Save(context.Background()); err != nil {
		t.Fatalf("create policy: %v", err)
	}
}

func (f *crFixture) open(t *testing.T, items ...approval.ChangeItem) *ent.ApprovalRequest {
	t.Helper()
	cr, problems, err := f.crh.Create(context.Background(), author, "test change", "",
		&approval.Changeset{Namespace: crNS, Changes: items})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("unexpected validation problems: %v", problems)
	}
	return cr
}

func (f *crFixture) state(t *testing.T, id interface{ String() string }) crState {
	t.Helper()
	cr, err := f.crh.Get(context.Background(), mustUUID(t, id.String()))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	st, err := f.crh.State(context.Background(), cr)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	return st
}

// ── 1. The happy path ───────────────────────────────────────────────────────

func TestCR_CreateApproveMerge_LandsInDGraph(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "approved-name"},
	})

	if st := f.state(t, cr.ID); st.Status != approval.StatusOpen {
		t.Fatalf("status = %q, want open before approval", st.Status)
	}

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if st := f.state(t, cr.ID); st.Status != approval.StatusApproved {
		t.Fatalf("status = %q, want approved after 1 of 1", st.Status)
	}

	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if got := readHostname(t, crServerA); got != "approved-name" {
		t.Errorf("hostname in DGraph = %q, want approved-name — the merge did not land", got)
	}
	merged, err := f.crh.Get(ctx, cr.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if merged.Status != approvalrequest.StatusMerged {
		t.Errorf("status = %q, want merged", merged.Status)
	}
	if merged.ExecutedAt == nil || merged.ExecutedBy != author {
		t.Errorf("executed_at/by not recorded: %v %q", merged.ExecutedAt, merged.ExecutedBy)
	}
	// The OCC counter must advance like any other orbital write — a merge is an
	// ordinary mutation with a change request behind it, not a side channel.
	if v := readVersion(t, crServerA); v < 2 {
		t.Errorf("version = %d, want the merge to have bumped it", v)
	}
}

// A merge moves the graph, so a request that has just merged would report
// itself stale with zero approvals if staleness were derived for terminal
// requests too — the record of who approved it reading as "approved an earlier
// version" of a change that already shipped.
func TestCR_MergedRequestDoesNotReportItselfStale(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "shipped"},
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge: %v", err)
	}

	st := f.state(t, cr.ID)
	if st.Stale {
		t.Error("a merged request reports itself stale")
	}
	if st.Valid != 1 {
		t.Errorf("valid approvals = %d, want 1 — a merged request's approvals are history, not a claim", st.Valid)
	}
	if st.Status != approval.StatusMerged {
		t.Errorf("status = %q, want merged", st.Status)
	}
	if len(st.Missing) != 0 {
		t.Errorf("missing = %v, want none on a terminal request", st.Missing)
	}
	if got := availableActions(mustGet(t, f, cr.ID), st, author, user.RoleDev, false); len(got) != 0 {
		t.Errorf("actions on a merged request = %v, want none", got)
	}
}

func mustGet(t *testing.T, f *crFixture, id uuid.UUID) *ent.ApprovalRequest {
	t.Helper()
	cr, err := f.crh.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return cr
}

// ── 2 & 8. Staleness is derived, and it blocks the merge ────────────────────

func TestCR_StaleBlocksMergeAndIsDerivedWithNoHook(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "proposed"},
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// A write straight to DGraph. Nothing tells orbital about it — no hook, no
	// event, no job. The very next read must still notice.
	setHostname(t, crServerA, "someone-else-was-here")

	st := f.state(t, cr.ID)
	if !st.Stale {
		t.Fatal("request is not stale after a direct DGraph write — staleness is not being derived")
	}
	// The approval was cast against the old hash, so it stops counting on its
	// own. No dismissal step ran.
	if st.Valid != 0 {
		t.Errorf("valid approvals = %d, want 0 — the approval predates the change", st.Valid)
	}
	if st.Status != approval.StatusOpen {
		t.Errorf("status = %q, want open — approved must revert when its approvals stop counting", st.Status)
	}
	if len(st.Approvals) != 1 {
		t.Errorf("the approval row should still exist so the UI can say 'approved an earlier version'")
	}

	_, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	if err == nil {
		t.Fatal("merge succeeded against a stale base")
	}
	// Not-approved fires first here, which is correct: with no counting
	// approvals it is not mergeable regardless of staleness.
	if !strings.Contains(err.Error(), errCRNotApproved.Error()) && !strings.Contains(err.Error(), errCRStale.Error()) {
		t.Errorf("merge error = %v, want not-approved or stale", err)
	}

	// Re-approving against the CURRENT state is the intended way forward, and
	// it must work — otherwise a request that goes stale is unrecoverable.
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "re-reviewed"); err != nil {
		t.Fatalf("re-approve after stale: %v", err)
	}
	if st := f.state(t, cr.ID); st.Valid != 1 || st.Status != approval.StatusApproved {
		t.Fatalf("after re-approval: valid=%d status=%q", st.Valid, st.Status)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge after re-approval: %v", err)
	}
	if got := readHostname(t, crServerA); got != "proposed" {
		t.Errorf("hostname = %q, want proposed", got)
	}
}

// A stale request that is still approved (2-of-2 policy, one approval re-cast)
// must hit the MVCC guard specifically, not a generic conflict.
func TestCR_StaleWithValidApprovals_IsMVCCConflict(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "proposed"},
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Move the base, then re-approve so the approval counts again — but do NOT
	// let State() re-read before the merge... it will, so instead move the base
	// again AFTER re-approving is impossible deterministically. Assert the guard
	// directly on a request whose base_hash we corrupt to simulate drift.
	if _, err := cr.Update().SetBaseHash("sha256:not-the-current-state").Save(ctx); err != nil {
		t.Fatalf("force drift: %v", err)
	}
	st := f.state(t, cr.ID)
	if !st.Stale {
		t.Fatal("expected stale")
	}
	// Re-stamp the approval to the current hash so the approval count is
	// satisfied while the base is still moved — the exact state the MVCC guard
	// exists for.
	if _, err := f.db.Approval.Update().
		Where(entapproval.ApprovalRequestID(cr.ID)).
		SetApprovedAtHash(st.CurrentHash).Save(ctx); err != nil {
		t.Fatalf("restamp: %v", err)
	}

	_, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	if err == nil {
		t.Fatal("merge succeeded with a moved base")
	}
	if !strings.Contains(err.Error(), errCRStale.Error()) {
		t.Fatalf("merge error = %v, want the stale guard", err)
	}
}

// ── 3. Partial merge is self-correcting and approvals survive ───────────────

func TestCR_PartialMerge_ApprovalsSurviveAndRemainderReMerges(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	// Item 2 points at a rack. Deleting the rack before the merge makes item 2
	// fail at DGraph (an edge to a missing orbId is read as a nested create),
	// while item 1 applies cleanly. The rack is not in scope — it is neither
	// declared nor owned — so its deletion does NOT make the request stale.
	// That is the shape this test needs: a genuine mid-merge failure with an
	// intact base.
	cr := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "first-item-applied"}},
		approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
			Set: map[string]any{"rack": map[string]any{"orbId": crRack}}},
	)
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	deleteEntity(t, "Rack", crRack)

	_, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	if err == nil {
		t.Fatal("merge reported success despite a failing item")
	}
	if !strings.Contains(err.Error(), "applied 1 of 2") {
		t.Errorf("merge error = %v, want it to name what applied", err)
	}

	// Item 1 landed and stays landed. Nothing is rolled back — there is no
	// transaction to roll back, which is why partial merge is a first-class
	// outcome rather than an error state.
	if got := readHostname(t, crServerA); got != "first-item-applied" {
		t.Errorf("item 1 did not land: hostname = %q", got)
	}

	after, err := f.crh.Get(ctx, cr.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// No merge_failed status. The request is still open.
	if after.Status != approvalrequest.StatusOpen {
		t.Errorf("status = %q, want open — a partial merge is not a new state", after.Status)
	}
	if len(after.Edges.MergeAttempts) != 1 {
		t.Fatalf("merge attempts = %d, want 1", len(after.Edges.MergeAttempts))
	}
	var results []approval.ItemResult
	if err := json.Unmarshal(after.Edges.MergeAttempts[0].Results, &results); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	if len(results) != 2 || !results[0].Applied || results[1].Applied {
		t.Fatalf("per-item results = %+v, want [applied, not applied]", results)
	}
	if results[1].Error == "" {
		t.Error("the failing item recorded no error")
	}

	// The base was rebased onto what we ourselves applied, so the approval
	// still counts and the request is still mergeable — a transient failure
	// costs a retry, not a re-review.
	st := f.state(t, cr.ID)
	if st.Stale {
		t.Error("request is stale after a merge whose only drift we caused")
	}
	if st.Valid != 1 || st.Status != approval.StatusApproved {
		t.Fatalf("approvals did not survive: valid=%d status=%q", st.Valid, st.Status)
	}

	// Retrying is safe: item 1 re-applies as a no-op, and item 2 now succeeds.
	addRack(t)
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("re-merge after fixing the cause: %v", err)
	}
	final, err := f.crh.Get(ctx, cr.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if final.Status != approvalrequest.StatusMerged {
		t.Errorf("status = %q, want merged", final.Status)
	}
	if len(final.Edges.MergeAttempts) != 2 {
		t.Errorf("merge attempts = %d, want both recorded", len(final.Edges.MergeAttempts))
	}
}

// ── 4. Partial merge + a third-party write does NOT carry approvals ─────────

func TestCR_PartialMerge_ThirdPartyWriteBlocksRebase(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "ours"}},
		approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "also-ours"}},
	)
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	st := f.state(t, cr.ID)
	before := st.Snapshot
	oldHash := cr.BaseHash

	// Simulate the interleaving a real partial merge produces: we applied item
	// 1, and while we were doing so someone else wrote to item 2's target.
	setHostname(t, crServerA, "ours")
	setHostname(t, crServerB, "someone-else")

	f.crh.rebaseOrStale(ctx, cr, st, before, map[string]bool{crServerA: true})

	reloaded, err := f.crh.Get(ctx, cr.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if reloaded.BaseHash != oldHash {
		t.Fatal("base was rebased even though an entity we did not touch changed")
	}
	final := f.state(t, cr.ID)
	if !final.Stale {
		t.Error("request is not stale after a third-party write")
	}
	if final.Valid != 0 {
		t.Errorf("valid approvals = %d, want 0 — a third-party write must force re-review", final.Valid)
	}
}

// ── 5. A bad changeset is refused at CREATE, not at merge ──────────────────

func TestCR_InvalidChangesetRejectedAtCreate(t *testing.T) {
	f := newCRFixture(t)

	_, problems, err := f.crh.Create(context.Background(), author, "bad", "",
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{
			{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostnmae": "typo"}},
		}})
	if err != nil {
		t.Fatalf("create returned a transport error: %v", err)
	}
	if len(problems) == 0 {
		t.Fatal("a changeset naming a non-existent field was accepted")
	}
	if !strings.Contains(problems[0].Msg, "no such field") {
		t.Errorf("problem = %v", problems[0])
	}
	n, err := f.db.ApprovalRequest.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("an invalid request was persisted (%d rows) — a reviewer could have been asked to look at it", n)
	}
}

// ── 6. Type resolution ──────────────────────────────────────────────────────

func TestCR_TypeResolvedFromOrbIDAndPersisted(t *testing.T) {
	f := newCRFixture(t)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "x"},
	})

	// Optional on input, always present on output: what is stored names its
	// types even though the caller did not.
	var cs approval.Changeset
	if err := json.Unmarshal(cr.Payload, &cs); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if cs.Changes[0].Type != "Server" {
		t.Errorf("persisted type = %q, want Server", cs.Changes[0].Type)
	}
}

// ── 7. Creates ──────────────────────────────────────────────────────────────

func TestCR_CreateNewEntity(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	const newServer = "cr-engine:server-CREATED"
	t.Cleanup(func() { deleteEntity(t, "Server", newServer) })

	cr := f.open(t, approval.ChangeItem{
		OrbID: newServer, Type: "Server", Op: approval.OpUpsert,
		Set: map[string]any{
			"hostname":   "brand-new",
			"dataCenter": map[string]any{"orbId": crDC},
		},
	})

	// base_present must NOT list it — that is what tells merge this is a create
	// rather than a target someone deleted.
	if len(cr.BasePresent) != 0 {
		t.Errorf("base_present = %v, want empty for a create", cr.BasePresent)
	}

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := readHostname(t, newServer); got != "brand-new" {
		t.Errorf("created server hostname = %q, want brand-new", got)
	}
}

// ── 9. A target deleted during review is a hard failure ────────────────────

func TestCR_TargetDeletedDuringReview_IsHardFailure(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerB, Op: approval.OpUpsert,
		Set: map[string]any{"hostname": "would-be-recreated"},
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	deleteEntity(t, "Server", crServerB)

	st := f.state(t, cr.ID)
	if len(st.Missing) != 1 || st.Missing[0] != crServerB {
		t.Fatalf("missing = %v, want [%s]", st.Missing, crServerB)
	}

	// Re-approve so the ONLY thing standing between this request and the graph
	// is the missing-target guard.
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	_, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	if err == nil {
		t.Fatal("merge recreated a deleted entity from a field delta")
	}
	if !strings.Contains(err.Error(), errCRTargetMissing.Error()) {
		t.Fatalf("merge error = %v, want target-missing", err)
	}
	if !strings.Contains(err.Error(), crServerB) {
		t.Errorf("error does not name the missing orbId: %v", err)
	}
	// Nothing was written — not even a partial object.
	if exists(t, "Server", crServerB) {
		t.Error("the deleted entity was recreated")
	}
}

// ── 10. Peer review, and the bypass waiver ─────────────────────────────────

func TestCR_ProposerCannotApproveUnlessTheyCouldBypass(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "self-approved"},
	})

	if _, err := f.crh.Approve(ctx, cr.ID, author, user.RoleDev, ""); err == nil {
		t.Fatal("the author approved their own change request")
	} else if !errors.Is(err, errCRSelfApproval) {
		t.Fatalf("approve error = %v, want the self-approval guard", err)
	}

	// An admin is in the policy's bypass_roles, so demanding a second pair of
	// eyes from them is friction with no control value — they could have
	// written directly.
	if _, err := f.crh.Approve(ctx, cr.ID, author, user.RoleAdmin, "privileged"); err != nil {
		t.Fatalf("admin self-approval should be waived: %v", err)
	}
	if st := f.state(t, cr.ID); st.Status != approval.StatusApproved {
		t.Errorf("status = %q, want approved", st.Status)
	}
}

// N-of-M counts distinct people, not distinct clicks.
func TestCR_NofM_CountsDistinctApprovers(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 2)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "needs-two"},
	})

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "first"); err != nil {
		t.Fatalf("approve 1: %v", err)
	}
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "same person again"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if st := f.state(t, cr.ID); st.Valid != 1 || st.Status != approval.StatusOpen {
		t.Fatalf("one person clicking twice satisfied a 2-of-M policy: valid=%d status=%q", st.Valid, st.Status)
	}

	if _, err := f.crh.Approve(ctx, cr.ID, "second@test.com", user.RoleDev, "second"); err != nil {
		t.Fatalf("approve 2: %v", err)
	}
	if st := f.state(t, cr.ID); st.Valid != 2 || st.Status != approval.StatusApproved {
		t.Fatalf("valid=%d status=%q, want 2/approved", st.Valid, st.Status)
	}
}

// With no policy the namespace is ungoverned: a voluntarily-opened request
// needs no review and merges on its own. That is the opt-in property —
// installing the engine must not make anything harder than it was, and a
// request nobody is required to review must not be a request nobody CAN merge.
//
// Its only guard is staleness, which is the same guarantee guarded Apply gives:
// what you merge is what you looked at.
func TestCR_NoPolicy_IsUngovernedButStillMVCCGuarded(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "ungoverned"},
	})
	st := f.state(t, cr.ID)
	if st.Required != 0 {
		t.Fatalf("required = %d, want 0 with no policy", st.Required)
	}
	if st.Status != approval.StatusApproved {
		t.Fatalf("status = %q, want approved — nothing governs this change", st.Status)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := readHostname(t, crServerA); got != "ungoverned" {
		t.Errorf("hostname = %q, want ungoverned", got)
	}

	// A second request whose base moves before merge must still be refused —
	// ungoverned is not unguarded.
	cr2 := f.open(t, approval.ChangeItem{
		OrbID: crServerB, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "second"},
	})
	setHostname(t, crServerB, "someone-else")
	if _, err := f.crh.Merge(ctx, cr2.ID, author, user.RoleDev, false); err == nil {
		t.Fatal("an ungoverned request merged against a moved base")
	} else if !errors.Is(err, errCRStale) {
		t.Fatalf("merge error = %v, want stale", err)
	}
}

// ── Lifecycle guards ────────────────────────────────────────────────────────

func TestCR_RejectAndCloseAreTerminal(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	rejected := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "nope"},
	})
	if _, err := f.crh.Reject(ctx, rejected.ID, reviewer, user.RoleDev, "no"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := f.crh.Approve(ctx, rejected.ID, reviewer, user.RoleDev, ""); err == nil {
		t.Error("a rejected request accepted an approval")
	}
	if _, err := f.crh.Merge(ctx, rejected.ID, author, user.RoleDev, false); err == nil {
		t.Error("a rejected request merged")
	}

	closed := f.open(t, approval.ChangeItem{
		OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "withdrawn"},
	})
	if _, err := f.crh.Close(ctx, closed.ID, "someone-else@test.com", user.RoleDev); err == nil {
		t.Error("a non-author closed someone else's request")
	}
	if _, err := f.crh.Close(ctx, closed.ID, author, user.RoleDev); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := f.crh.Merge(ctx, closed.ID, author, user.RoleDev, false); err == nil {
		t.Error("a closed request merged")
	}
}

// Amending the changeset must invalidate the approvals cast against the old
// one — otherwise a reviewer's approval of one proposal silently carries to a
// different one.
func TestCR_AmendInvalidatesApprovals(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "v1"},
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if st := f.state(t, cr.ID); st.Status != approval.StatusApproved {
		t.Fatalf("precondition: status = %q", st.Status)
	}

	// Amend to target a DIFFERENT entity, which changes the scope and therefore
	// the base hash.
	_, problems, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{
			{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "v2"}},
		}})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("amend rejected: %v", problems)
	}

	st := f.state(t, cr.ID)
	if st.Valid != 0 || st.Status != approval.StatusApproved && st.Status != approval.StatusOpen {
		t.Fatalf("valid=%d status=%q", st.Valid, st.Status)
	}
	if st.Valid != 0 {
		t.Errorf("valid approvals = %d after amend, want 0", st.Valid)
	}
}

// ── available_actions ───────────────────────────────────────────────────────

func TestCR_AvailableActions_AreCallerRelative(t *testing.T) {
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "x"},
	})
	st := f.state(t, cr.ID)

	authorActions := availableActions(cr, st, author, user.RoleDev, false)
	if containsStr(authorActions, "approve") {
		t.Errorf("author was offered approve: %v", authorActions)
	}
	if !containsStr(authorActions, "edit") || !containsStr(authorActions, "close") {
		t.Errorf("author actions = %v, want edit and close", authorActions)
	}

	reviewerActions := availableActions(cr, st, reviewer, user.RoleDev, false)
	if !containsStr(reviewerActions, "approve") || !containsStr(reviewerActions, "reject") {
		t.Errorf("reviewer actions = %v, want approve and reject", reviewerActions)
	}
	if containsStr(reviewerActions, "edit") {
		t.Errorf("a non-author was offered edit: %v", reviewerActions)
	}

	if got := availableActions(cr, st, reviewer, user.RoleReadonly, false); len(got) != 0 {
		t.Errorf("readonly actions = %v, want none", got)
	}
}

// ── Diff ────────────────────────────────────────────────────────────────────

func TestCR_Diff_ShowsProposedChangeAndShrinksAfterPartialMerge(t *testing.T) {
	f := newCRFixture(t)

	cr := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "new-a"}},
		approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "new-b"}},
	)
	st := f.state(t, cr.ID)
	res := compareChangeset(st)
	if res.Summary.Modified != 2 {
		t.Fatalf("diff shows %d modified, want 2", res.Summary.Modified)
	}

	// Apply one of them out of band. The diff is recomputed against live
	// intent, so it must shrink to exactly the remainder — that is what makes a
	// retry after a partial merge readable.
	setHostname(t, crServerA, "new-a")
	st = f.state(t, cr.ID)
	res = compareChangeset(st)
	if res.Summary.Modified != 1 {
		t.Fatalf("diff shows %d modified after one item landed, want 1", res.Summary.Modified)
	}
	if res.Changes[0].OrbID != crServerB {
		t.Errorf("remaining change = %s, want %s", res.Changes[0].OrbID, crServerB)
	}
}

// ── Error mapping ───────────────────────────────────────────────────────────

func TestCR_ErrorCodesAreDistinct(t *testing.T) {
	// MVCC_CONFLICT and TARGET_MISSING are both 409 and mean different things:
	// one says "re-review your diff", the other says "the entity is gone".
	// A client that cannot tell them apart cannot offer the right next step.
	if CodeMVCCConflict == CodeTargetMissing {
		t.Fatal("stale and missing-target share an error code")
	}
}

// A policy that is recorded but not enforced must say so on every surface an
// admin could read, and must STOP saying so the moment enforcement lands.
//
// This is the test that matters when session 2 flips approvalGateInstalled: it
// fails if the flag moves and the "not enforced" wording is left behind, which
// would be the same false assurance in reverse — a control that is on while
// every response claims it is off. It asserts the RELATIONSHIP, not the current
// value, so it is correct in both states and needs no edit when the flag flips.
func TestApprovalPolicy_NoticeMatchesEnforcement(t *testing.T) {
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	pol, err := f.db.ApprovalPolicy.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("query policy: %v", err)
	}
	view := renderPolicy(pol)

	if view.Enforced != approvalGateInstalled {
		t.Errorf("enforced = %v, want %v (the gate's actual state)", view.Enforced, approvalGateInstalled)
	}
	if approvalGateInstalled && view.Notice != "" {
		t.Errorf("enforcement is live but the response still says %q", view.Notice)
	}
	if !approvalGateInstalled && view.Notice == "" {
		t.Error("an enabled policy is not enforced and the response does not say so")
	}

	// A disabled policy is not enforced either, but for a reason the admin
	// chose — it must not carry the "orbital has not implemented this yet"
	// notice, which would misattribute their own switch to a missing feature.
	off, err := pol.Update().SetEnabled(false).Save(context.Background())
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	offView := renderPolicy(off)
	if offView.Enforced {
		t.Error("a disabled policy reports itself enforced")
	}
	if offView.Notice != "" {
		t.Errorf("a deliberately disabled policy carries the not-implemented notice: %q", offView.Notice)
	}
}
