//go:build integration

package approval_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
)

// Round-trip coverage for the four engine tables. The regression class is
// silent serialization loss on the jsonb columns: base_present ([]string),
// payload and results (raw JSON carrying the adapter's shapes), and
// bypass_roles ([]string with a non-empty default). A field that fails to
// survive encode+decode is invisible at the call site — the write succeeds, the
// read returns a zero value, and the engine makes a wrong decision from it
// (an empty base_present turns a deleted target into a silent recreate).

func TestApprovalRequest_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	changeset := approval.Changeset{
		Namespace: "alaska-dot",
		Changes: []approval.ChangeItem{
			{
				OrbID: "alaska-dot:server-4FK8K44",
				Type:  "Server",
				Op:    approval.OpUpdate,
				Set:   map[string]any{"hostname": "edge-01", "rackUnit": float64(12)},
				Clear: []string{"oobMAC"},
			},
			{
				OrbID: "alaska-dot:idrac-settings-4FK8K44",
				Type:  "IdracSettings",
				Op:    approval.OpUpsert,
				Set:   map[string]any{"timezone": "UTC", "sshEnabled": true},
			},
			{
				// A guarded item. Version is a *int precisely so absent and 0
				// stay distinguishable, which is exactly the distinction a
				// round-trip through jsonb can quietly destroy.
				OrbID:   "alaska-dot:rack-01",
				Type:    "Rack",
				Op:      approval.OpUpdate,
				Set:     map[string]any{"name": "rack-01"},
				Version: ptrInt(0),
			},
		},
	}
	payload, err := json.Marshal(changeset)
	if err != nil {
		t.Fatalf("marshal changeset: %v", err)
	}

	present := []string{"alaska-dot:server-4FK8K44"}

	created, err := db.ApprovalRequest.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("alaska-dot").SetNumber(1).
		SetTitle("Rename edge-01 and enable SSH").
		SetDescription("multi\nline\tdescription").
		SetAuthor("proposer@test.com").
		SetBaseHash("sha256:abc123").
		SetBasePresent(present).
		SetPayload(payload).
		SetCreatedBy("proposer@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.ApprovalRequest.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ActionType != approval.ActionTypeConfigMutation {
		t.Errorf("action_type = %q, want %q", got.ActionType, approval.ActionTypeConfigMutation)
	}
	if got.Title != "Rename edge-01 and enable SSH" {
		t.Errorf("title = %q", got.Title)
	}
	if got.Description != "multi\nline\tdescription" {
		t.Errorf("description = %q — whitespace did not survive", got.Description)
	}
	if string(got.Status) != "open" {
		t.Errorf("status = %q, want default open", got.Status)
	}
	if got.Author != "proposer@test.com" {
		t.Errorf("author = %q", got.Author)
	}
	if got.BaseHash != "sha256:abc123" {
		t.Errorf("base_hash = %q", got.BaseHash)
	}
	if len(got.BasePresent) != 1 || got.BasePresent[0] != present[0] {
		t.Errorf("base_present = %v, want %v", got.BasePresent, present)
	}
	if got.ExecutedAt != nil {
		t.Errorf("executed_at = %v, want nil before merge", got.ExecutedAt)
	}

	// The payload has to come back as the same changeset, not merely as valid
	// JSON — nested maps and the typed Op are where a silent loss would hide.
	var back approval.Changeset
	if err := json.Unmarshal(got.Payload, &back); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if back.Namespace != changeset.Namespace {
		t.Errorf("namespace = %q", back.Namespace)
	}
	if len(back.Changes) != 3 {
		t.Fatalf("changes = %d, want 3", len(back.Changes))
	}
	if back.Changes[0].Op != approval.OpUpdate || back.Changes[1].Op != approval.OpUpsert {
		t.Errorf("ops = %q,%q", back.Changes[0].Op, back.Changes[1].Op)
	}
	if back.Changes[0].Set["hostname"] != "edge-01" || back.Changes[0].Set["rackUnit"] != float64(12) {
		t.Errorf("set = %#v", back.Changes[0].Set)
	}
	if len(back.Changes[0].Clear) != 1 || back.Changes[0].Clear[0] != "oobMAC" {
		t.Errorf("clear = %v", back.Changes[0].Clear)
	}
	if back.Changes[1].Set["sshEnabled"] != true {
		t.Errorf("bool field lost: %#v", back.Changes[1].Set["sshEnabled"])
	}

	// Version round-trip, both directions of the pointer.
	//
	// Version 0 is the case that matters: `omitempty` on a *int omits nil but
	// keeps a pointer to 0, so a value type here would marshal 0 and read back
	// as "the caller asserted version 0" for every unguarded item in the
	// changeset — turning "I did not check" into a precondition nobody wrote.
	if back.Changes[0].Version != nil {
		t.Errorf("changes[0].version = %v, want nil — an unguarded item came back guarded", *back.Changes[0].Version)
	}
	if back.Changes[1].Version != nil {
		t.Errorf("changes[1].version = %v, want nil", *back.Changes[1].Version)
	}
	if back.Changes[2].Version == nil {
		t.Fatal("changes[2].version came back nil — a guarded item lost its precondition in storage")
	}
	if *back.Changes[2].Version != 0 {
		t.Errorf("changes[2].version = %d, want 0", *back.Changes[2].Version)
	}

	// executed_at is Nillable — a zero time must not read back as "merged".
	when := time.Now().UTC().Truncate(time.Second)
	merged, err := db.ApprovalRequest.UpdateOneID(created.ID).
		SetStatus("merged").
		SetExecutedAt(when).
		SetExecutedBy("merger@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("update to merged: %v", err)
	}
	if merged.ExecutedAt == nil || !merged.ExecutedAt.UTC().Equal(when) {
		t.Errorf("executed_at = %v, want %v", merged.ExecutedAt, when)
	}
	if merged.ExecutedBy != "merger@test.com" {
		t.Errorf("executed_by = %q", merged.ExecutedBy)
	}
}

func TestApproval_RoundTripAndPerApproverUniqueness(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	req, err := db.ApprovalRequest.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("alaska-dot").SetNumber(1).
		SetTitle("t").SetAuthor("a@test.com").
		SetBaseHash("sha256:base").
		SetPayload(json.RawMessage(`{"namespace":"ns","changes":[]}`)).
		Save(ctx)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	if _, err := db.Approval.Create().
		SetApprovalRequestID(req.ID).
		SetApprover("reviewer@test.com").
		SetDecision("approved").
		SetComment("looks right").
		SetApprovedAtHash("sha256:base").
		Save(ctx); err != nil {
		t.Fatalf("create approval: %v", err)
	}

	got, err := req.QueryApprovals().Only(ctx)
	if err != nil {
		t.Fatalf("query approvals: %v", err)
	}
	if got.Approver != "reviewer@test.com" || string(got.Decision) != "approved" ||
		got.Comment != "looks right" || got.ApprovedAtHash != "sha256:base" {
		t.Errorf("approval round-trip lost a field: %+v", got)
	}

	// Two decisions by the same approver on the same request must collide —
	// N-of-M counts distinct approvers, so a duplicate row would let one person
	// satisfy a 2-approval policy alone.
	_, err = db.Approval.Create().
		SetApprovalRequestID(req.ID).
		SetApprover("reviewer@test.com").
		SetDecision("rejected").
		SetApprovedAtHash("sha256:base").
		Save(ctx)
	if err == nil {
		t.Fatal("duplicate (request, approver) was accepted — unique index missing")
	}
}

func TestMergeAttempt_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	req, err := db.ApprovalRequest.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("alaska-dot").SetNumber(1).
		SetTitle("t").SetAuthor("a@test.com").
		SetBaseHash("sha256:base").
		SetPayload(json.RawMessage(`{"namespace":"ns","changes":[]}`)).
		Save(ctx)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	// A partial attempt — the case the table exists for.
	results := []approval.ItemResult{
		{OrbID: "ns:server-A", Applied: true},
		{OrbID: "ns:server-B", Applied: false, Error: `field "bogus" not found`},
	}
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("marshal results: %v", err)
	}

	if _, err := db.MergeAttempt.Create().
		SetApprovalRequestID(req.ID).
		SetAttemptedBy("merger@test.com").
		SetResults(raw).
		SetError(`1 of 2 items failed`).
		Save(ctx); err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	got, err := req.QueryMergeAttempts().Only(ctx)
	if err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	var back []approval.ItemResult
	if err := json.Unmarshal(got.Results, &back); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	if len(back) != 2 {
		t.Fatalf("results = %d, want 2", len(back))
	}
	// Applied=false is the value most likely to be silently lost (omitempty on
	// a bool would drop it), and it is the one the retry path reads.
	if !back[0].Applied || back[1].Applied {
		t.Errorf("applied flags = %v,%v, want true,false", back[0].Applied, back[1].Applied)
	}
	if back[1].Error != `field "bogus" not found` {
		t.Errorf("item error = %q", back[1].Error)
	}
	if got.AttemptedAt.IsZero() {
		t.Error("attempted_at is zero — default did not fire")
	}
}

func TestApprovalPolicy_RoundTripAndBypassDefault(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	// Default path: bypass_roles must materialise as ["admin"], not null.
	// A null here would silently mean "nobody can bypass", locking admins out
	// of the frictionless path D15 depends on.
	defaulted, err := db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("alaska-dot").
		Save(ctx)
	if err != nil {
		t.Fatalf("create policy: %v", err)
	}
	got, err := db.ApprovalPolicy.Get(ctx, defaulted.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.BypassRoles) != 1 || got.BypassRoles[0] != "admin" {
		t.Errorf("bypass_roles = %v, want [admin]", got.BypassRoles)
	}
	if got.RequiredApprovals != 1 {
		t.Errorf("required_approvals = %d, want 1", got.RequiredApprovals)
	}
	if !got.Enabled {
		t.Error("enabled = false, want true by default")
	}
	if !got.AllTypes {
		t.Error("all_types = false, want true by default")
	}
	if len(got.Types) != 0 {
		t.Errorf("types = %v, want empty — all_types:true plus a type list say different things", got.Types)
	}

	// One policy per namespace: a second is refused by the unique index rather
	// than quietly coexisting, which is what makes "which policy gated this?"
	// answerable with one row instead of a resolution order nobody wrote down.
	if _, err := db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("alaska-dot").
		SetAllTypes(false).
		SetTypes([]string{"Server"}).
		Save(ctx); err == nil {
		t.Fatal("a second policy for the same namespace was accepted — unique index missing")
	}

	// The type list is a jsonb column, so the list-valued path needs its own
	// round trip: order and membership must both survive.
	typed, err := db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("colo").
		SetAllTypes(false).
		SetTypes([]string{"Server", "Rack"}).
		SetRequiredApprovals(2).
		SetBypassRoles([]string{"admin", "dev"}).
		SetEnabled(false).
		Save(ctx)
	if err != nil {
		t.Fatalf("create typed policy: %v", err)
	}
	got2, err := db.ApprovalPolicy.Get(ctx, typed.ID)
	if err != nil {
		t.Fatalf("get typed: %v", err)
	}
	if len(got2.Types) != 2 || got2.Types[0] != "Server" || got2.Types[1] != "Rack" {
		t.Errorf("types = %v, want [Server Rack]", got2.Types)
	}
	if got2.AllTypes {
		t.Error("all_types = true, want false")
	}
	if len(got2.BypassRoles) != 2 || got2.BypassRoles[0] != "admin" || got2.BypassRoles[1] != "dev" {
		t.Errorf("bypass_roles = %v", got2.BypassRoles)
	}
	if got2.RequiredApprovals != 2 || got2.Enabled {
		t.Errorf("required=%d enabled=%v", got2.RequiredApprovals, got2.Enabled)
	}
}

// all_types and types are an either/or, and the CHECK constraint is what makes
// that true of the DATA rather than of one code path. The API validates the
// same rule with a better message, but a policy written by a migration, a
// psql session, or a future handler that forgets goes straight to the table —
// and a row saying both "every type" and "these two types" has no answer to
// "what does this protect?".
func TestApprovalPolicy_ScopeConstraintRejectsContradictoryRows(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	if _, err := db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("contradictory").
		SetAllTypes(true).
		SetTypes([]string{"Server"}).
		Save(ctx); err == nil {
		t.Error("all_types:true with a type list was stored — the row claims two different scopes")
	}

	if _, err := db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace("empty-scope").
		SetAllTypes(false).
		SetTypes([]string{}).
		Save(ctx); err == nil {
		t.Error("all_types:false with no types was stored — the policy protects nothing while reporting itself active")
	}
}

func ptrInt(v int) *int { return &v }
