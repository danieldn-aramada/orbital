//go:build integration

package handler

import (
	"context"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
)

// Scope is PINNED at base capture rather than re-derived on read.
//
// baseScope walks containment, and containment is view configuration — it is
// editable. Re-deriving on every read therefore lets a later view change
// retroactively alter what a past review is deemed to have covered. "Which
// entities did this reviewer look at" is a fact about the moment of review.
//
// Scope drives STALENESS only. Merge correctness rests on base_values plus the
// version pre-flight, neither of which consults containment.

const (
	pinNS     = "cr-pin"
	pinDC     = "cr-pin:datacenter-1"
	pinServer = "cr-pin:server-PIN1"
	pinIdrac  = "cr-pin:idrac-PIN1"
)

// seedPinFixture builds an isolated server + owned iDRAC. Isolated on purpose:
// one test deletes the child to prove the pin holds, which must not disturb any
// other suite's fixture.
func seedPinFixture(t *testing.T) {
	t.Helper()
	gqlMutate(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": pinNS, "orbId": pinDC, "name": "cr pin fixture", "version": 1,
		}}})
	gqlMutate(t, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": pinNS, "orbId": pinServer, "version": 1, "hostname": "pin-01",
			"dataCenter": map[string]any{"orbId": pinDC},
		}}})
	gqlMutate(t, `mutation($input:[AddIdracSettingsInput!]!){ addIdracSettings(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": pinNS, "orbId": pinIdrac, "version": 1, "firmwareVersion": "1.0.0",
			"server": map[string]any{"orbId": pinServer},
		}}})
	t.Cleanup(func() {
		deleteByOrbID(t, "IdracSettings", pinIdrac)
		deleteByOrbID(t, "Server", pinServer)
		deleteByOrbID(t, "DataCenter", pinDC)
	})
}

func openPinCR(t *testing.T, f *crFixture, orbID, hostname string) int64 {
	t.Helper()
	cr, problems, err := f.crh.Create(context.Background(), author, "pin test", "",
		&approval.Changeset{Namespace: pinNS, Changes: []approval.ChangeItem{{
			OrbID: orbID, Type: "Server", Op: approval.OpUpdate,
			Set: map[string]any{"hostname": hostname},
		}}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("validation problems: %v", problems)
	}
	return cr.ID
}

// Acceptance 1: resolved once at creation and stored on the row.
func TestChangeRequest_ScopeResolvedOnceAtCreation(t *testing.T) {
	f := newCRFixture(t)
	seedPinFixture(t)

	id := openPinCR(t, f, pinServer, "pinned-01")

	cr, err := f.crh.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(cr.BaseScope) == 0 {
		t.Fatal("base_scope is empty — the scope was not pinned at creation")
	}
	if !contains(cr.BaseScope, pinServer) {
		t.Errorf("base_scope %v must contain the declared entity %s", cr.BaseScope, pinServer)
	}
	if !contains(cr.BaseScope, pinIdrac) {
		t.Errorf("base_scope %v must contain the owned child %s that containment pulled in", cr.BaseScope, pinIdrac)
	}
}

// Acceptance 2 (CORRECTED from the approved list): Amend RE-PINS rather than
// reading the stored value. An amend re-proposes against a newly captured base
// — payload, base_hash, base_versions, base_effect and base_values all move —
// so a scope carried forward would describe entities the request no longer
// proposes against.
func TestChangeRequest_AmendRePinsScope(t *testing.T) {
	f := newCRFixture(t)
	seedPinFixture(t)

	id := openPinCR(t, f, pinServer, "pinned-01")
	before, err := f.crh.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !contains(before.BaseScope, pinIdrac) {
		t.Fatalf("precondition: base_scope %v should contain the child", before.BaseScope)
	}

	// Amend to target the DataCenter instead, which owns a different subtree.
	_, problems, err := f.crh.Amend(context.Background(), id, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: pinNS, Changes: []approval.ChangeItem{{
			OrbID: pinDC, Type: "DataCenter", Op: approval.OpUpdate,
			Set: map[string]any{"name": "renamed"},
		}}})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("amend validation problems: %v", problems)
	}

	after, err := f.crh.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get after amend: %v", err)
	}
	if !contains(after.BaseScope, pinDC) {
		t.Errorf("after amend, base_scope %v must contain the newly declared %s", after.BaseScope, pinDC)
	}
	if contains(after.BaseScope, pinServer) {
		t.Errorf("after amend, base_scope %v still names %s — the old changeset's scope was carried forward",
			after.BaseScope, pinServer)
	}
}

// Acceptance 3: the staleness hash is still computed, over the STORED scope.
func TestChangeRequest_StalenessHashComputedOverStoredScope(t *testing.T) {
	f := newCRFixture(t)
	seedPinFixture(t)

	id := openPinCR(t, f, pinServer, "pinned-01")
	st := f.state(t, id)

	if len(st.Scope) == 0 {
		t.Fatal("state carries no scope")
	}
	for _, orbID := range st.Scope {
		if _, ok := st.Versions[orbID]; !ok {
			t.Errorf("scope names %s but no version was read for it — the hash does not cover the stored scope", orbID)
		}
	}
	// Editing the owned child must still move staleness: the pin must not turn
	// the mechanism off, only stop it re-deriving WHICH entities to watch.
	firstHash := versionHash(st.Versions)
	setPinIdracFirmware(t, "2.0.0", 2)
	after := f.state(t, id)
	if versionHash(after.Versions) == firstHash {
		t.Error("editing an entity in the pinned scope did not move the hash — staleness is no longer detected")
	}
}

// Acceptance 4: the load-bearing one. A change to what containment WOULD return
// must not move an already-pinned scope.
func TestChangeRequest_ContainmentChangeDoesNotMovePinnedScope(t *testing.T) {
	f := newCRFixture(t)
	seedPinFixture(t)

	id := openPinCR(t, f, pinServer, "pinned-01")
	cr, err := f.crh.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !contains(cr.BaseScope, pinIdrac) {
		t.Fatalf("precondition: %s must be in the pinned scope, got %v", pinIdrac, cr.BaseScope)
	}

	// Remove the owned child, so re-deriving would now produce a SMALLER scope.
	deleteByOrbID(t, "IdracSettings", pinIdrac)

	// Prove the test is meaningful: a fresh derivation really does drop it.
	existing, err := approval.NewDGraphSchemaSource(f.crh.dgraphURL).ResolveEntities(context.Background(), []string{pinServer})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	fresh := baseScope(context.Background(), f.crh.dgraphURL, liveViewSet(t, f.crh.dgraphURL), []string{pinServer}, existing)
	if contains(fresh, pinIdrac) {
		t.Fatalf("test is vacuous: re-derivation still returns %s, so pinning cannot be distinguished", pinIdrac)
	}

	// The pinned scope is unmoved, through the consumer path.
	st := f.state(t, id)
	if !contains(st.Scope, pinIdrac) {
		t.Errorf("scope %v dropped %s — it was re-derived instead of read, so what this review covered changed retroactively",
			st.Scope, pinIdrac)
	}
}

// Acceptance 5: the stored value round-trips through the path a consumer uses.
// State() is that path: every queue render, the diff view and every decision go
// through it.
func TestChangeRequest_StoredScopeRoundTripsThroughAPI(t *testing.T) {
	f := newCRFixture(t)
	seedPinFixture(t)

	id := openPinCR(t, f, pinServer, "pinned-01")
	cr, err := f.crh.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	st := f.state(t, id)
	if len(st.Scope) != len(cr.BaseScope) {
		t.Fatalf("State scope %v does not match the stored base_scope %v", st.Scope, cr.BaseScope)
	}
	for _, orbID := range cr.BaseScope {
		if !contains(st.Scope, orbID) {
			t.Errorf("stored %s did not survive the round trip; State returned %v", orbID, st.Scope)
		}
	}
}

// Acceptance 6: a row written before base_scope existed still renders. Those
// rows are deliberately NOT backfilled — pinning an old row with today's
// containment would assert a review covered something nobody can show it did.
func TestChangeRequest_PreExistingRowWithoutStoredScopeStillWorks(t *testing.T) {
	f := newCRFixture(t)
	seedPinFixture(t)

	id := openPinCR(t, f, pinServer, "pinned-01")

	// Simulate a row that predates the field.
	if err := f.db.ApprovalRequest.UpdateOneID(id).ClearBaseScope().Exec(context.Background()); err != nil {
		t.Fatalf("clear base_scope: %v", err)
	}
	cr, err := f.crh.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(cr.BaseScope) != 0 {
		t.Fatalf("precondition: base_scope should be empty, got %v", cr.BaseScope)
	}

	st := f.state(t, id)
	if !contains(st.Scope, pinServer) || !contains(st.Scope, pinIdrac) {
		t.Errorf("a row without a pinned scope must fall back to deriving it; got %v", st.Scope)
	}
}

// setPinIdracFirmware writes straight to DGraph, so it must bump `version`
// itself: staleness is computed from the OCC counter, and it is orbital's proxy
// (writeToDGraph) that normally increments it. A raw mutation that only changed
// firmwareVersion would leave the counter at 1 and the hash unmoved — which
// says nothing about staleness detection.
func setPinIdracFirmware(t *testing.T, v string, version int) {
	t.Helper()
	gqlMutate(t, `mutation($orbId:String!,$set:IdracSettingsPatch!){ updateIdracSettings(input:{filter:{orbId:{eq:$orbId}}, set:$set}){ numUids } }`,
		map[string]any{"orbId": pinIdrac, "set": map[string]any{"firmwareVersion": v, "version": version}})
}
