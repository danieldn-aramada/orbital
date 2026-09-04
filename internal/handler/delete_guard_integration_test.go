//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// The cascade-delete endpoint is orbital's THIRD write path. It plans a cascade
// and POSTs a DQL delete, so it never passes through writeToDGraph — which is
// where both the approval gate and the MVCC check live for everything else.
//
// Two holes, both measured, both closed here:
//
//   - **The gate never ran.** Under a policy with no bypass roles,
//     `updateDataCenter` was refused 403 while `DELETE` of that same entity
//     returned 200 seconds later. A rename gated, a cascade delete not.
//   - **No version check**, so a delete could race a concurrent edit and destroy
//     it with nothing to diff back from.

func deleteFixture(t *testing.T) (*DeleteHandler, *crFixture) {
	t.Helper()
	// NewDeleteHandler parses its preview template from a repo-relative path at
	// construction. Tests run from the package directory, so without this the
	// constructor panics before any assertion runs. t.Chdir restores on cleanup.
	t.Chdir("../..")
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, slog.Default(), false)
	return NewDeleteHandler(testutil.DGraphURL(), f.db, slog.Default(), gql), f
}

func deleteReq(t *testing.T, h *DeleteHandler, typeName, orbID, query string, role user.Role) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	url := "/api/v1/config-items/" + typeName + "/" + orbID + query
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodDelete, url, nil), rec)
	c.SetParamNames("type", "id")
	c.SetParamValues(typeName, orbID)
	c.Set("user_email", "deleter@test.com")
	c.Set("role", string(role))
	if err := h.Execute(c); err != nil {
		// Echo would render this; the tests below read the recorder, so surface
		// anything that escaped as a plain error instead of failing opaquely.
		if he, ok := err.(*echo.HTTPError); ok {
			rec.Code = he.Code
			_ = json.NewEncoder(rec).Encode(map[string]any{"message": fmt.Sprint(he.Message)})
			return rec
		}
		t.Fatalf("Execute: %v", err)
	}
	return rec
}

// ── 14c. a stale delete is refused, and deletes nothing ────────────────────

func TestDeleteGuard_StaleIfVersionIsRefusedAndDeletesNothing(t *testing.T) {
	h, _ := deleteFixture(t)

	saw := readVersion(t, crServerB)
	setHostname(t, crServerB, "edited-after-the-dialog-opened")

	rec := deleteReq(t, h, "Server", crServerB, fmt.Sprintf("?version=%d", saw), user.RoleAdmin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Code string `json:"code"`
		Msg  string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Code != CodeMVCCConflict {
		t.Errorf("code = %q, want %s: %s", out.Code, CodeMVCCConflict, rec.Body.String())
	}
	if !exists(t, "Server", crServerB) {
		t.Fatal("the refused delete removed the entity anyway")
	}
}

// ── 14d. a current delete succeeds and cascades as before ──────────────────

func TestDeleteGuard_CurrentIfVersionDeletesAndCascades(t *testing.T) {
	h, _ := deleteFixture(t)

	if !exists(t, "IdracSettings", crIdracA) {
		t.Fatal("fixture is missing the owned child this asserts the cascade on")
	}
	rec := deleteReq(t, h, "Server", crServerA,
		fmt.Sprintf("?version=%d", readVersion(t, crServerA)), user.RoleAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if exists(t, "Server", crServerA) {
		t.Error("the server was not deleted")
	}
	if exists(t, "IdracSettings", crIdracA) {
		t.Error("the owned child survived — the cascade changed")
	}
}

// Omitting it stays legal, exactly as on every other path.
func TestDeleteGuard_NoIfVersionIsUnconditional(t *testing.T) {
	h, _ := deleteFixture(t)
	rec := deleteReq(t, h, "Server", crServerB, "", user.RoleAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — omitting version must not be an error: %s", rec.Code, rec.Body.String())
	}
	if exists(t, "Server", crServerB) {
		t.Error("the delete did not happen")
	}
}

// ── the gate bypass (debt.md Track A2) ─────────────────────────────────────

// The reproduction from the bug report, as a test: same entity, same policy,
// same caller. An update is refused; the delete must be refused too.
func TestDeleteGuard_ApprovalPolicyRefusesTheCascadeDelete(t *testing.T) {
	h, f := deleteFixture(t)
	f.requireApproval(t, 1) // allTypes, bypassRoles empty — nobody bypasses

	// The control: an ordinary mutation on this entity is refused.
	gql := NewGraphQL(testutil.DGraphURL(), f.db, slog.Default(), false)
	q, v := updateHostname(crServerA, "should-not-land")
	if err := mutate(t, gql, devCaller(), q, v); err == nil {
		t.Fatal("the control failed: updateServer was NOT gated, so this test proves nothing about delete")
	}

	rec := deleteReq(t, h, "Server", crServerA, "", user.RoleDev)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a cascade delete walked past the approval gate: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Code string `json:"code"`
		Hint string `json:"hint"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Code != CodeApprovalRequired {
		t.Errorf("code = %q, want %s — the delete refusal does not match the /graphql refusal", out.Code, CodeApprovalRequired)
	}
	if exists(t, "Server", crServerA) == false {
		t.Fatal("the refused delete removed the entity")
	}
}

// A policy protecting only an OWNED CHILD must still refuse the parent delete
// that would take the child with it. This is why the gate reads the types of
// the planned cascade rather than the declared type alone.
func TestDeleteGuard_PolicyOnAnOwnedChildRefusesTheParentDelete(t *testing.T) {
	h, f := deleteFixture(t)
	ctx := context.Background()
	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetRequiredApprovals(1).
		SetAllTypes(false).
		SetTypes([]string{"IdracSettings"}).
		SetBypassRoles([]string{}).
		Save(ctx); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	rec := deleteReq(t, h, "Server", crServerA, "", user.RoleDev)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — deleting a Server took a protected IdracSettings with it: %s",
			rec.Code, rec.Body.String())
	}
	if !exists(t, "IdracSettings", crIdracA) {
		t.Error("the protected child was deleted")
	}
}

// The negative, and it is the one that keeps the control usable: with no policy
// in play, deletes are untouched. A gate that refuses everything is switched off
// by whoever it inconveniences.
func TestDeleteGuard_UngovernedNamespaceIsUnaffected(t *testing.T) {
	h, _ := deleteFixture(t) // no policy
	rec := deleteReq(t, h, "Server", crServerA, "", user.RoleDev)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the gate refused a delete in an ungoverned namespace: %s",
			rec.Code, rec.Body.String())
	}
}

// ── the interface-type trap ────────────────────────────────────────────────

// `KubernetesCluster` is an INTERFACE, so DGraph generates no
// `getKubernetesCluster`. The version check originally built `get{Type}`, which
// 500s on exactly the delete it is meant to guard — and only for interface
// types, so DataCenter and Server deletes passed and hid it.
//
// Reproduces through the real endpoint rather than the helper, because the bug
// was in the query the handler builds from the PATH parameter.
func TestDeleteGuard_InterfaceTypedDeleteResolvesItsVersion(t *testing.T) {
	h, _ := deleteFixture(t)
	const dc = crNS + ":dc-iface"
	const cluster = crNS + ":cluster-iface"

	crGQL(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": dc, "name": "iface dc", "version": 1,
		}}})
	crGQL(t, `mutation($input:[AddEksaKubernetesClusterInput!]!){ addEksaKubernetesCluster(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": cluster, "name": "iface cluster", "version": 1,
			"dataCenter": map[string]any{"orbId": dc},
		}}})
	t.Cleanup(func() {
		deleteEntity(t, "EksaKubernetesCluster", cluster)
		deleteEntity(t, "DataCenter", dc)
	})

	// Deleted BY THE INTERFACE NAME, which is what the UI sends.
	rec := deleteReq(t, h, "KubernetesCluster", cluster, "?version=1", user.RoleAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an interface-typed delete could not resolve its own version: %s",
			rec.Code, rec.Body.String())
	}
	if exists(t, "EksaKubernetesCluster", cluster) {
		t.Error("the cluster was not deleted")
	}
}

// And the guard still fires on that path — the fix must not have turned the
// check into a no-op for interface types.
func TestDeleteGuard_InterfaceTypedDeleteStillRefusesAStaleVersion(t *testing.T) {
	h, _ := deleteFixture(t)
	const dc = crNS + ":dc-iface2"
	const cluster = crNS + ":cluster-iface2"

	crGQL(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": dc, "name": "iface dc2", "version": 1,
		}}})
	crGQL(t, `mutation($input:[AddEksaKubernetesClusterInput!]!){ addEksaKubernetesCluster(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": cluster, "name": "iface cluster2", "version": 5,
			"dataCenter": map[string]any{"orbId": dc},
		}}})
	t.Cleanup(func() {
		deleteEntity(t, "EksaKubernetesCluster", cluster)
		deleteEntity(t, "DataCenter", dc)
	})

	rec := deleteReq(t, h, "KubernetesCluster", cluster, "?version=1", user.RoleAdmin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a stale version: %s", rec.Code, rec.Body.String())
	}
	if !exists(t, "EksaKubernetesCluster", cluster) {
		t.Error("the refused delete removed the cluster anyway")
	}
}
