//go:build integration

package handler

// Acceptance tests for the append-only approval decision history. Names were
// transcribed from the acceptance list before any body was written:
//
//	1 every decision is recorded                  TestApprovalDecisions_EveryDecisionIsRecorded
//	2+8 re-deciding appends; read back via GET    TestApprovalDecisions_ReDecidingAppends_ReadBackThroughGET
//	3 written atomically with the current row     TestApprovalDecisions_WrittenAtomicallyWithTheCurrentDecision
//	4 nothing but a decision writes a row         TestApprovalDecisions_OnlyADecisionWritesARow
//	UI: each entry says whether it was replaced   TestApprovalDecisions_HistoryMarksSupersededAndCurrent
//
// Items 5 and 6 are in internal/db. Item 7 is the existing change-request
// suite, which runs unmodified.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/approvaldecision"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

const secondReviewer = "reviewer2@test.com"

type crReadBack struct {
	Reviews []struct {
		Approver string `json:"approver"`
		Decision string `json:"decision"`
		Comment  string `json:"comment"`
		Current  bool   `json:"current"`
	} `json:"reviews"`
	ReviewHistory []struct {
		Approver   string `json:"approver"`
		Decision   string `json:"decision"`
		Comment    string `json:"comment"`
		Revision   int    `json:"revision"`
		Superseded bool   `json:"superseded"`
		Current    bool   `json:"current"`
	} `json:"reviewHistory"`
}

// getThroughAPI reads a request the way an auditor would: GET /api/v1/change-requests/:id.
func getThroughAPI(t *testing.T, f *crFixture, cr *ent.ApprovalRequest) crReadBack {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/change-requests/"+crHumanID(cr), nil), rec)
	c.SetParamNames("id")
	c.SetParamValues(crHumanID(cr))
	c.Set("user_email", reviewer)
	c.Set("role", string(user.RoleDev))
	if err := f.crh.GetChangeRequest(c); err != nil {
		t.Fatalf("GetChangeRequest: %v", err)
	}
	var out crReadBack
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	return out
}

func historyCount(t *testing.T, f *crFixture, cr *ent.ApprovalRequest) int {
	t.Helper()
	return f.db.ApprovalDecision.Query().Where(approvaldecision.ApprovalRequestID(cr.ID)).CountX(context.Background())
}

func TestApprovalDecisions_EveryDecisionIsRecorded(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 2)
	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "x"}})

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "fine by me"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.crh.Reject(ctx, cr.ID, secondReviewer, user.RoleDev, "wrong host"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	got := getThroughAPI(t, f, cr).ReviewHistory
	if len(got) != 2 {
		t.Fatalf("reviewHistory has %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Approver != reviewer || got[0].Decision != "approved" || got[0].Comment != "fine by me" {
		t.Errorf("first entry = %+v, want the approval", got[0])
	}
	if got[1].Approver != secondReviewer || got[1].Decision != "rejected" || got[1].Comment != "wrong host" {
		t.Errorf("second entry = %+v, want the rejection", got[1])
	}
}

func TestApprovalDecisions_ReDecidingAppends_ReadBackThroughGET(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 2) // one approval leaves the request open, so it can be re-decided
	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "v1"}})

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "first look"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, problems, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{
			{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "v2"}},
		}}); err != nil || len(problems) > 0 {
		t.Fatalf("amend: err=%v problems=%v", err, problems)
	}
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "after the amend"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}

	view := getThroughAPI(t, f, cr)
	h := view.ReviewHistory
	if len(h) != 2 {
		t.Fatalf("reviewHistory has %d entries, want both decisions: %+v", len(h), h)
	}
	if h[0].Comment != "first look" || h[1].Comment != "after the amend" {
		t.Errorf("history comments = %q, %q — want oldest first, both kept", h[0].Comment, h[1].Comment)
	}
	if h[1].Revision <= h[0].Revision {
		t.Errorf("revisions = %d then %d, want the re-approval stamped at the amended revision",
			h[0].Revision, h[1].Revision)
	}
	// `reviews` keeps its meaning: the current decision per approver.
	if len(view.Reviews) != 1 || view.Reviews[0].Comment != "after the amend" {
		t.Errorf("reviews = %+v, want only the current decision", view.Reviews)
	}
}

func TestApprovalDecisions_WrittenAtomicallyWithTheCurrentDecision(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 2)
	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "x"}})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "first"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Make the history write fail, as a full disk or a lost connection would.
	raw := testutil.RawTestDB(t)
	for _, stmt := range []string{
		`CREATE OR REPLACE FUNCTION test_fail_decision_insert() RETURNS trigger AS $$
		 BEGIN RAISE EXCEPTION 'simulated history write failure'; END; $$ LANGUAGE plpgsql`,
		`CREATE TRIGGER test_fail_decision_insert BEFORE INSERT ON approval_decisions
		 FOR EACH ROW EXECUTE FUNCTION test_fail_decision_insert()`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("install failure trigger: %v", err)
		}
	}
	t.Cleanup(func() {
		raw.Exec(`DROP TRIGGER IF EXISTS test_fail_decision_insert ON approval_decisions`) //nolint:errcheck
		raw.Exec(`DROP FUNCTION IF EXISTS test_fail_decision_insert()`)                    //nolint:errcheck
	})

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "second"); err == nil {
		t.Fatal("re-approve succeeded although the history write failed")
	}

	// Neither half landed: the current decision is still the first one, and the
	// history still has exactly the first one.
	cur := getThroughAPI(t, f, cr).Reviews
	if len(cur) != 1 || cur[0].Comment != "first" {
		t.Errorf("reviews = %+v — the current decision changed although its history write failed", cur)
	}
	if n := historyCount(t, f, cr); n != 1 {
		t.Errorf("history rows = %d, want 1", n)
	}
}

func TestApprovalDecisions_OnlyADecisionWritesARow(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "x"}})

	if _, problems, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{
			{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "y"}},
		}}); err != nil || len(problems) > 0 {
		t.Fatalf("amend: err=%v problems=%v", err, problems)
	}
	if _, err := f.crh.Approve(ctx, cr.ID, author, user.RoleDev, "my own"); err == nil {
		t.Fatal("precondition: self-approval was accepted")
	}
	if _, err := f.crh.Close(ctx, cr.ID, author, user.RoleDev); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "too late"); err == nil {
		t.Fatal("precondition: deciding a closed request was accepted")
	}

	if n := historyCount(t, f, cr); n != 0 {
		t.Errorf("history rows = %d after amend, refused self-approval, close and a refused late decision — want 0", n)
	}
	if h := getThroughAPI(t, f, cr).ReviewHistory; len(h) != 0 {
		t.Errorf("reviewHistory = %+v, want empty", h)
	}
}

// The timeline renders straight from these flags, so they must be right in
// every combination it shows: a replaced decision, a latest decision cast
// against an older revision, and a latest decision that still counts.
func TestApprovalDecisions_HistoryMarksSupersededAndCurrent(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "v1"}})

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "a1"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.crh.Approve(ctx, cr.ID, secondReviewer, user.RoleDev, "b1"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, problems, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{
			{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "v2"}},
		}}); err != nil || len(problems) > 0 {
		t.Fatalf("amend: err=%v problems=%v", err, problems)
	}
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "a2"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}

	view := getThroughAPI(t, f, cr)
	// `reviews` must agree with the gate too. It used to compare the hash only,
	// so an amend that kept the same entities left b1 marked current while the
	// gate had stopped counting it.
	for _, r := range view.Reviews {
		if r.Comment == "b1" && r.Current {
			t.Error("reviews: b1 is current after the amend, but the gate no longer counts it")
		}
	}
	h := view.ReviewHistory
	if len(h) != 3 {
		t.Fatalf("reviewHistory has %d entries, want 3: %+v", len(h), h)
	}
	want := map[string]struct{ superseded, current bool }{
		"a1": {true, false},  // replaced by a2
		"b1": {false, false}, // still b's decision, but cast against the pre-amend version
		"a2": {false, true},  // counts now
	}
	for _, e := range h {
		w := want[e.Comment]
		if e.Superseded != w.superseded || e.Current != w.current {
			t.Errorf("%s: superseded=%v current=%v, want superseded=%v current=%v",
				e.Comment, e.Superseded, e.Current, w.superseded, w.current)
		}
	}
}
