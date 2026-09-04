//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/labstack/echo/v4"
)

// The client-supplied `before` is gone: `ifVersion` answers the entity-level
// question with the same token /graphql uses, and the field-level question is
// answered at merge from the SERVER-recorded ancestor (`base_values`), which no
// client ever supplied.
//
// These three pin what must NOT have changed with it. Each names something that
// would keep passing its own tests while quietly losing a guarantee.

// ── 15. the subtree anchor survives ────────────────────────────────────────

// `base_hash` covers each declared orbId's OWNED SUBTREE, not just the entity.
// A reviewer approving a Server approved its IdracSettings too, so a third party
// editing the child has to invalidate that review. Nothing about `ifVersion`
// covers this — it is per declared item — so removing `before` must not have
// been read as "the item-level token is now the whole guard".
func TestNoBefore_ThirdPartyChildEditStillStalesTheParent(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "parent-edit"}})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if st := f.state(t, cr.ID); st.Stale || st.Valid != 1 {
		t.Fatalf("before the child edit: stale=%v valid=%d", st.Stale, st.Valid)
	}

	// The CHILD moves. The declared item is untouched.
	crGQL(t, `mutation($orbId: String!, $set: IdracSettingsPatch!) { updateIdracSettings(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": crIdracA, "set": map[string]any{
			"firmwareVersion": "9.9.9", "version": readIdracVersion(t, crIdracA) + 1,
		}})

	st := f.state(t, cr.ID)
	if !st.Stale {
		t.Error("editing an owned child no longer stales the parent's request — a reviewer's approval now covers a subtree they did not see")
	}
	if st.Valid != 0 {
		t.Errorf("valid approvals = %d, want 0", st.Valid)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); !errors.Is(err, errCRStale) {
		t.Errorf("merge err = %v, want stale", err)
	}
}

func readIdracVersion(t *testing.T, orbID string) int {
	t.Helper()
	out := crQuery(t, `query($orbId:String!){ n: getIdracSettings(orbId:$orbId){ version } }`,
		map[string]any{"orbId": orbID})
	node, _ := out["n"].(map[string]any)
	if node == nil {
		t.Fatalf("idrac %s not found", orbID)
	}
	v, _ := toFloat64(node["version"])
	return int(v)
}

// ── 16. satisfied[] and the three-way merge are unchanged ──────────────────

// They read `base_values`, the ancestor orbital records itself — a different
// field with a different source from the `before` that was removed. The two were
// routinely confused, which is exactly why this asserts rather than assumes.
func TestNoBefore_SatisfiedAndThreeWayMergeStillWork(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "agreed-value"}})

	// Someone else independently makes the same change. The proposal is now
	// already satisfied, and merging it must write nothing rather than bump a
	// version and emit an audit row for a change that changed nothing.
	setHostname(t, crServerA, "agreed-value")
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	versionBefore := readVersion(t, crServerA)
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merging an already-satisfied request failed: %v", err)
	}
	if got := readVersion(t, crServerA); got != versionBefore {
		t.Errorf("version %d → %d — a satisfied field was written anyway", versionBefore, got)
	}
	if got := readHostname(t, crServerA); got != "agreed-value" {
		t.Errorf("hostname = %q", got)
	}

	// And the three-way CONFLICT half: ancestor says X, someone moved it to a
	// third value, so merging would destroy their edit.
	// Same fixture — the policy is unique per namespace, so a second one here
	// would collide rather than isolate.
	cr2 := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "mine"}})
	if _, err := f.crh.Approve(ctx, cr2.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	crGQL(t, `mutation($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": crServerB, "set": map[string]any{"hostname": "theirs"}})

	_, err := f.crh.Merge(ctx, cr2.ID, author, user.RoleDev, false)
	var pf *preconditionFailed
	if !errors.As(err, &pf) {
		t.Fatalf("err = %v, want a field-level conflict from base_values", err)
	}
	if len(pf.Problems) == 0 || pf.Problems[0].Field == "" {
		t.Errorf("conflict does not name a field: %v", pf.Problems)
	}
	if got := readHostname(t, crServerB); got != "theirs" {
		t.Errorf("hostname = %q — the refused merge overwrote a third party's value", got)
	}
}

// ── 17. the 409 envelope is unchanged; only the producer moved ─────────────

// Clients already render `problems[]` from this envelope. The producer changed
// from a `before` mismatch to an `ifVersion` mismatch; `code`, the array shape
// and the hint semantics must not have.
func TestNoBefore_ConflictEnvelopeIsUnchanged(t *testing.T) {
	f := newCRFixture(t)

	saw := readVersion(t, crServerA)
	setHostname(t, crServerA, "moved")

	body, err := json.Marshal(map[string]any{
		"title": "envelope", "namespace": crNS,
		"changes": []map[string]any{{
			"orbId": crServerA, "op": "update",
			"set":       map[string]any{"hostname": "mine"},
			"ifVersion": saw,
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/api/v1/change-requests", strings.NewReader(string(body))), rec)
	c.Request().Header.Set("Content-Type", "application/json")
	c.Set("user_email", author)
	c.Set("role", string(user.RoleDev))
	if err := f.crh.CreateChangeRequest(c); err != nil {
		t.Fatalf("CreateChangeRequest: %v", err)
	}

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var out struct {
		Error      string `json:"error"`
		Code       string `json:"code"`
		HTTPStatus int    `json:"httpStatus"`
		Problems   []struct {
			Index int    `json:"index"`
			OrbID string `json:"orbId"`
			Field string `json:"field"`
			Msg   string `json:"message"`
			Hint  string `json:"hint"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if out.Code != CodeMVCCConflict {
		t.Errorf("code = %q, want %s", out.Code, CodeMVCCConflict)
	}
	if out.HTTPStatus != http.StatusConflict {
		t.Errorf("httpStatus = %d, want 409", out.HTTPStatus)
	}
	if out.Error == "" {
		t.Error("envelope has no top-level error string")
	}
	if len(out.Problems) != 1 {
		t.Fatalf("problems = %d, want 1", len(out.Problems))
	}
	p := out.Problems[0]
	if p.OrbID != crServerA || p.Msg == "" || p.Hint == "" {
		t.Errorf("problem lost part of its shape: %+v", p)
	}
}

// The negative for the whole phase: an item that sends NOTHING is still legal
// and still creatable. Removing a precondition must not have made one mandatory.
func TestNoBefore_UnconditionalItemStillWorks(t *testing.T) {
	f := newCRFixture(t)
	cr, problems, err := f.crh.Create(context.Background(), author, "unconditional", "",
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "plain"},
		}}})
	if err != nil || len(problems) > 0 || cr == nil {
		t.Fatalf("an unconditional item was refused: err=%v problems=%v", err, problems)
	}
}

// A client that has not caught up must be TOLD, not quietly downgraded.
//
// Echo's binder ignores unknown JSON fields, so removing `before` from the DTO
// would have left an existing caller believing it still had a field-level
// precondition while orbital silently applied none — the exact failure this
// whole area exists to eliminate. Loud beats convenient for a breaking change.
func TestNoBefore_AClientStillSendingBeforeIsToldNotIgnored(t *testing.T) {
	f := newCRFixture(t)

	body, err := json.Marshal(map[string]any{
		"title": "old client", "namespace": crNS,
		"changes": []map[string]any{{
			"orbId": crServerA, "op": "update",
			"set":    map[string]any{"hostname": "mine"},
			"before": map[string]any{"hostname": "a-original"},
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/api/v1/change-requests", strings.NewReader(string(body))), rec)
	c.Request().Header.Set("Content-Type", "application/json")
	c.Set("user_email", author)
	c.Set("role", string(user.RoleDev))
	if err := f.crh.CreateChangeRequest(c); err != nil {
		t.Fatalf("CreateChangeRequest: %v", err)
	}

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — `before` was accepted and silently ignored: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ifVersion") {
		t.Errorf("the refusal does not name the replacement: %s", rec.Body.String())
	}
}
