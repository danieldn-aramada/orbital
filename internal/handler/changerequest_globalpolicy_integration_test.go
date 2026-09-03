//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// An approval policy can govern EVERY namespace, including ones onboarded after
// it was written — the same argument all_types makes, one axis up.
//
// Resolution is FALLBACK, not overlay: a namespace with its own policy uses that
// one instead, even when it is weaker, and a namespace whose policy is DISABLED
// is not gated at all. See docs/reference/CHANGE-CONTROL.md § All-namespaces.
//
// These are integration tests rather than UI checks because every one of them
// fails OPEN — a write that should have been refused goes straight through, the
// caller sees success, and nothing visible happens. There is no screen on which
// that looks wrong.

const gpNS2 = "cr-engine-second"

var gpServerC = gpNS2 + ":server-CCC"

// seedSecondNamespace creates an entity in a namespace that did not exist when
// the caller's global policy was written — which is the property being tested,
// not a convenience.
func seedSecondNamespace(t *testing.T) {
	t.Helper()
	crGQL(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": gpNS2, "orbId": gpNS2 + ":dc-1", "name": "second ns", "version": 1,
		}}})
	crGQL(t, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": gpNS2, "orbId": gpServerC, "version": 1, "hostname": "c-original",
			"dataCenter": map[string]any{"orbId": gpNS2 + ":dc-1"},
		}}})
	t.Cleanup(func() {
		deleteEntity(t, "Server", gpServerC)
		deleteEntity(t, "DataCenter", gpNS2+":dc-1")
	})
}

func newGlobalPolicy(t *testing.T, f *crFixture, required int) {
	t.Helper()
	_, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetAllNamespaces(true).
		SetAllTypes(true).
		SetRequiredApprovals(required).
		SetBypassRoles([]string{}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create global policy: %v", err)
	}
}

// AC 2 — with only a global policy, a mutation in ANY namespace is gated,
// including a namespace that did not exist when the policy was created.
func TestGlobalPolicy_GatesANamespaceCreatedAfterIt(t *testing.T) {
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, f.crh.logger, false)

	// Policy first, namespace second — the order is the point. Enumerating
	// today's namespaces could not express this.
	newGlobalPolicy(t, f, 1)
	seedSecondNamespace(t)

	q, v := updateHostname(gpServerC, "gated-by-global")
	if err := mutate(t, gql, adminCaller(), q, v); err == nil {
		t.Fatal("a namespace created after the global policy was not gated by it")
	}

	// And the namespace the fixture already had, to show it is not one-namespace
	// behaviour that happens to match.
	qa, va := updateHostname(crServerA, "gated-by-global-too")
	if err := mutate(t, gql, adminCaller(), qa, va); err == nil {
		t.Fatal("the pre-existing namespace was not gated by the global policy")
	}
}

// AC 3 — a namespace with its own ENABLED policy is governed by that one, not
// the global, INCLUDING when it is weaker.
//
// The weaker case is the whole test. A namespace policy that is stricter than
// the global produces the same refusal either way, so it cannot tell fallback
// from overlay; only a weaker one can, and a weaker one is what an overlay
// implementation would silently override.
func TestGlobalPolicy_NamespacePolicyWinsEvenWhenWeaker(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, f.crh.logger, false)

	newGlobalPolicy(t, f, 2) // strict: two approvals, nobody bypasses

	// Weaker: admin bypasses. Under overlay semantics (intersection of bypass
	// roles) this would still refuse; under fallback it writes through.
	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetAllTypes(true).
		SetRequiredApprovals(1).
		SetBypassRoles([]string{"admin"}).
		Save(ctx); err != nil {
		t.Fatalf("create namespace policy: %v", err)
	}

	q, v := updateHostname(crServerA, "weaker-wins")
	if err := mutate(t, gql, adminCaller(), q, v); err != nil {
		t.Fatalf("the weaker namespace policy did not override the global: %v", err)
	}
}

// AC 4 — a namespace whose own policy is DISABLED is not gated, even while an
// enabled global exists. Disabled SHADOWS the global; it does not fall through.
func TestGlobalPolicy_DisabledNamespacePolicyShadowsIt(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, f.crh.logger, false)

	newGlobalPolicy(t, f, 1)

	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetAllTypes(true).
		SetRequiredApprovals(1).
		SetBypassRoles([]string{}).
		SetEnabled(false).
		Save(ctx); err != nil {
		t.Fatalf("create disabled namespace policy: %v", err)
	}

	q, v := updateHostname(crServerA, "exempt")
	if err := mutate(t, gql, adminCaller(), q, v); err != nil {
		t.Fatalf("a disabled namespace policy did not exempt the namespace: %v", err)
	}

	// The global is still in force everywhere else — the exemption is scoped to
	// the namespace that asked for it, not a way to switch the global off.
	seedSecondNamespace(t)
	qc, vc := updateHostname(gpServerC, "still-gated")
	if err := mutate(t, gql, adminCaller(), qc, vc); err == nil {
		t.Fatal("one namespace's disabled policy switched off the global everywhere")
	}
}

// AC 1 — both, and neither, are refused. The API says which rule was broken;
// the CHECK constraint is the layer no future code path can skip.
func TestGlobalPolicy_BothOrNeitherIsRefused(t *testing.T) {
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{"allNamespaces": true, "namespace": crNS, "allTypes": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("allNamespaces + namespace: got %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}

	rec = postPolicy(t, f, map[string]any{"allTypes": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("neither allNamespaces nor namespace: got %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}

	// The database refuses it too, with the API bypassed entirely — the API is
	// one writer, and a migration or psql session is not.
	_, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetAllNamespaces(true).
		SetNamespace(crNS).
		SetAllTypes(true).
		Save(context.Background())
	if err == nil {
		t.Fatal("the CHECK constraint allowed a row that is both global and namespaced")
	}
	if !isNamespaceCheckViolation(err) {
		t.Fatalf("wrong constraint fired: %v", err)
	}
}

// AC 5 — a second global policy is refused, not silently created.
//
// This is the one the schema cannot get right by accident: `namespace` is
// nullable now, and Postgres treats NULLs as DISTINCT in a unique index, so
// UNIQUE(action_type, namespace) constrains nothing here. Without the partial
// index two rows both claiming every namespace insert happily.
func TestGlobalPolicy_SecondOneIsRefused(t *testing.T) {
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{"allNamespaces": true, "allTypes": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("first global policy: got %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	rec = postPolicy(t, f, map[string]any{"allNamespaces": true, "allTypes": true})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second global policy: got %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
}

// AC 6 — administering a global policy is readable back through the audit API,
// under a resource id that finds it. A row with no namespace would otherwise
// land with an empty resource id and be invisible to the query the audit tab
// uses — on the most consequential policy in the system.
func TestGlobalPolicyAudit_IsFindableByResourceID(t *testing.T) {
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{"allNamespaces": true, "allTypes": true, "requiredApprovals": 2})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// Read back through GET /api/v1/audit-log — the path an operator uses — not
	// the table.
	events := auditEventsByResource(t, f, allNamespacesResourceID)
	var found *auditEventView
	for i := range events {
		if slices.Contains(events[i].Operations, "createApprovalPolicy") {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no createApprovalPolicy event under resource_id %q", allNamespacesResourceID)
	}

	after, _ := found.Details["after"].(map[string]any)
	if after == nil {
		t.Fatalf("audit event carries no `after`: %+v", found.Details)
	}
	if all, _ := after["allNamespaces"].(bool); !all {
		t.Errorf("audit event does not record that the policy was global: %+v", after)
	}
	// Assert the negative too: a namespace policy must NOT claim to be global,
	// or the field trains whoever reads the record to ignore it.
	rec = postPolicy(t, f, map[string]any{"namespace": crNS, "allTypes": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create namespaced: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	for _, ev := range auditEventsByResource(t, f, crNS) {
		if !slices.Contains(ev.Operations, "createApprovalPolicy") {
			continue
		}
		a, _ := ev.Details["after"].(map[string]any)
		if all, _ := a["allNamespaces"].(bool); all {
			t.Errorf("a namespaced policy was audited as global: %+v", a)
		}
	}
}

// AC 7 — a refusal caused by a global policy names it in prose rather than
// leaving a blank where the namespace would be.
func TestGlobalPolicy_RefusalNamesIt(t *testing.T) {
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, f.crh.logger, false)

	newGlobalPolicy(t, f, 1)

	q, v := updateHostname(crServerA, "named")
	err := mutate(t, gql, adminCaller(), q, v)
	if err == nil {
		t.Fatal("precondition: the global policy did not gate the write")
	}
	got := err.Error()
	if !strings.Contains(got, "all namespaces") || !strings.Contains(got, "require approval") {
		t.Errorf("refusal does not name the policy: %q", got)
	}
	// The blank a missing label would leave, stated as its own assertion so the
	// test fails on the actual defect rather than on wording.
	if strings.Contains(got, "changes to  require") {
		t.Errorf("refusal left an empty policy label: %q", got)
	}
}

// auditEventsByResource reads GET /api/v1/audit-log?resource_id= — the query the
// audit tab issues, and the one a global policy would fall out of if its events
// landed with an empty resource id.
func auditEventsByResource(t *testing.T, f *crFixture, resourceID string) []auditEventView {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-log?resource_id="+resourceID, nil)
	c := e.NewContext(req, rec)
	c.Set("user_email", "admin@test.com")
	c.Set("role", string(user.RoleAdmin))
	if err := f.auditH.List(c); err != nil {
		t.Fatalf("audit list: %v", err)
	}
	var out struct {
		Events []auditEventView `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode audit log: %v (body: %s)", err, rec.Body.String())
	}
	return out.Events
}
