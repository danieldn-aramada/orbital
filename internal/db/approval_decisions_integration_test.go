//go:build integration

package db_test

// Acceptance tests for the append-only approval decision history (items 5 and
// 6). Names were transcribed from the acceptance list before any body was
// written. Items 1–4 and 8 live with the change-request handler tests, where
// decisions are actually cast.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	entapproval "github.com/armada/orbital/ent/approval"
	"github.com/armada/orbital/ent/approvaldecision"
	"github.com/armada/orbital/internal/approval"
	orbitaldb "github.com/armada/orbital/internal/db"
)

func migrated(t *testing.T, name string) (string, *ent.Client) {
	t.Helper()
	dsn := freshDatabase(t, name)
	raw, client := openClient(t, dsn)
	if err := orbitaldb.Migrate(context.Background(), raw, client, time.Minute, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return dsn, client
}

func seedRequest(t *testing.T, client *ent.Client) *ent.ApprovalRequest {
	t.Helper()
	req, err := client.ApprovalRequest.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("ns").SetNumber(1).
		SetTitle("t").SetAuthor("author@test.com").
		SetBaseHash("sha256:base").
		SetPayload(json.RawMessage(`{"namespace":"ns","changes":[]}`)).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	return req
}

// Item 5: the database refuses UPDATE and DELETE on a recorded decision, and
// startup verification would notice if that protection were removed.
func TestAuditGuard_ApprovalDecisionsCannotBeUpdatedOrDeleted(t *testing.T) {
	ctx := context.Background()
	dsn, client := migrated(t, "orbital_guard_decisions")
	raw, _ := openClient(t, dsn)
	if err := orbitaldb.InstallAuditGuard(ctx, raw); err != nil {
		t.Fatalf("install guard: %v", err)
	}
	req := seedRequest(t, client)
	a := client.Approval.Create().SetApprovalRequestID(req.ID).SetApprover("r@test.com").
		SetDecision("approved").SetComment("original").SetApprovedAtHash("sha256:base").SaveX(ctx)
	client.ApprovalDecision.Create().SetID(a.ID).SetApprovalRequestID(req.ID).SetApprover("r@test.com").
		SetDecision(approvaldecision.DecisionApproved).SetComment("original").SetApprovedAtHash("sha256:base").ExecX(ctx)

	if _, err := raw.Exec(`UPDATE approval_decisions SET comment = 'rewritten' WHERE id = $1`, a.ID); err == nil {
		t.Error("UPDATE on approval_decisions succeeded — the review record is editable")
	}
	if _, err := raw.Exec(`DELETE FROM approval_decisions WHERE id = $1`, a.ID); err == nil {
		t.Error("DELETE on approval_decisions succeeded — the review record is erasable")
	}
	got := client.ApprovalDecision.GetX(ctx, a.ID)
	if got.Comment != "original" {
		t.Errorf("comment = %q after a refused UPDATE, want %q", got.Comment, "original")
	}

	// Detection: verification must cover this table, or dropping its trigger
	// would go unnoticed.
	if unprotected, err := orbitaldb.VerifyAuditGuard(ctx, raw); err != nil || len(unprotected) != 0 {
		t.Fatalf("with the guard installed: unprotected=%v err=%v, want none", unprotected, err)
	}
	if _, err := raw.Exec(`DROP TRIGGER approval_decisions_append_only ON approval_decisions`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	unprotected, err := orbitaldb.VerifyAuditGuard(ctx, raw)
	if err != nil || len(unprotected) != 1 || unprotected[0] != "approval_decisions" {
		t.Errorf("after dropping its trigger: unprotected=%v err=%v, want [approval_decisions]", unprotected, err)
	}
}

// Item 6: approvals that existed before the history are copied into it once,
// field for field, and a restart copies nothing more.
func TestBackfillApprovalDecisions_CopiesExistingDecisionsOnce(t *testing.T) {
	ctx := context.Background()
	dsn, client := migrated(t, "orbital_backfill_decisions")
	req := seedRequest(t, client)

	// Pre-upgrade state: current decisions with no history behind them.
	for _, s := range []struct{ who, decision, comment string }{
		{"one@test.com", "approved", "lgtm"},
		{"two@test.com", "rejected", "wrong rack"},
	} {
		client.Approval.Create().SetApprovalRequestID(req.ID).SetApprover(s.who).
			SetDecision(entapproval.Decision(s.decision)).
			SetComment(s.comment).SetApprovedAtHash("sha256:base").SetApprovedAtRevision(2).SaveX(ctx)
	}
	if n := client.ApprovalDecision.Query().CountX(ctx); n != 0 {
		t.Fatalf("precondition: %d history rows before the backfill, want 0", n)
	}

	raw, _ := openClient(t, dsn)
	if err := orbitaldb.Migrate(ctx, raw, client, time.Minute, quietLogger()); err != nil {
		t.Fatalf("migrate (upgrade): %v", err)
	}
	current := client.Approval.Query().AllX(ctx)
	history := client.ApprovalDecision.Query().AllX(ctx)
	if len(history) != len(current) {
		t.Fatalf("history rows = %d, want one per current approval (%d)", len(history), len(current))
	}
	byID := map[string]*ent.ApprovalDecision{}
	for _, d := range history {
		byID[d.ID.String()] = d
	}
	for _, a := range current {
		d, ok := byID[a.ID.String()]
		if !ok {
			t.Errorf("approval %s by %s has no history row", a.ID, a.Approver)
			continue
		}
		if d.Approver != a.Approver || string(d.Decision) != string(a.Decision) || d.Comment != a.Comment ||
			d.ApprovedAtHash != a.ApprovedAtHash || d.ApprovedAtRevision != a.ApprovedAtRevision ||
			!d.CreatedAt.Equal(a.CreatedAt) {
			t.Errorf("history row does not match its approval:\n  approval %+v\n  history  %+v", a, d)
		}
	}

	// A restart is a no-op.
	if err := orbitaldb.Migrate(ctx, raw, client, time.Minute, quietLogger()); err != nil {
		t.Fatalf("migrate (restart): %v", err)
	}
	if n := client.ApprovalDecision.Query().CountX(ctx); n != len(current) {
		t.Errorf("history rows after a restart = %d, want %d", n, len(current))
	}
}
