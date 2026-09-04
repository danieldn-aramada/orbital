//go:build integration

package handler

import (
	"bytes"
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

// `ifVersion` on a change-request item is the same concurrency token /graphql
// mutations accept, meaning the same thing: the version I read.
//
// Entity-level, where `before` is field-level. base_hash already anchors the
// whole scope, but it is an aggregate — it answers "did anything move" and can
// never say WHICH thing. These tests pin the part that names the offender.
//
// Every one of them fails OPEN when it breaks: the request is created, the
// caller gets a 201, and the proposal looks exactly like a guarded one.

func ifVersionPtr(v int) *int { return &v }

// ── 5. a matching precondition is accepted ──────────────────────────────────

func TestCRIfVersion_MatchingIsAccepted(t *testing.T) {
	f := newCRFixture(t)

	cur := readVersion(t, crServerA)
	cr, problems, err := f.crh.Create(context.Background(), author, "matching", "",
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set:       map[string]any{"hostname": "guarded"},
			IfVersion: ifVersionPtr(cur),
		}}})
	if err != nil {
		t.Fatalf("create with a matching ifVersion was refused: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("validation problems: %v", problems)
	}
	if cr == nil {
		t.Fatal("no change request returned")
	}
}

// ── 6. a stale precondition is refused, naming the item and both versions ───

func TestCRIfVersion_StaleIsRefusedNamingBothVersions(t *testing.T) {
	f := newCRFixture(t)

	cur := readVersion(t, crServerA)
	setHostname(t, crServerA, "moved-by-someone-else") // a third party writes
	moved := readVersion(t, crServerA)
	if moved == cur {
		t.Fatalf("fixture did not move the version (%d)", cur)
	}

	_, _, err := f.crh.Create(context.Background(), author, "stale", "",
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set:       map[string]any{"hostname": "based-on-stale-read"},
			IfVersion: ifVersionPtr(cur),
		}}})

	var pf *preconditionFailed
	if !errors.As(err, &pf) {
		t.Fatalf("err = %v, want a preconditionFailed — a proposal written against a version that has moved was accepted", err)
	}
	if len(pf.Problems) != 1 {
		t.Fatalf("problems = %d, want 1: %v", len(pf.Problems), pf.Problems)
	}
	p := pf.Problems[0]
	if p.OrbID != crServerA {
		t.Errorf("problem does not name the entity: %+v", p)
	}
	// Both versions, because "it moved" without saying from what to what leaves
	// the author unable to tell a stale read from a wrong orbId.
	for _, want := range []string{"version 1", "it is now 2"} {
		if !strings.Contains(p.Msg, want) {
			t.Errorf("message %q does not carry %q", p.Msg, want)
		}
	}
	// Entity-level refusals carry no Field. That is the signal a client uses to
	// tell "the entity moved" from "one of its values moved".
	if p.Field != "" {
		t.Errorf("entity-level problem carries Field=%q — indistinguishable from a field-level refusal", p.Field)
	}
}

// ── 7. omission is not an error ─────────────────────────────────────────────

// The opt-in promise. `ifVersion` is a guard a caller may ask for; a caller that
// does not ask must be no worse off than before the feature existed.
func TestCRIfVersion_OmittedIsAcceptedAndStillScopeGuarded(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)

	cr, problems, err := f.crh.Create(ctx, author, "unconditional", "",
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "unconditional"},
		}}})
	if err != nil || len(problems) > 0 {
		t.Fatalf("an item with no ifVersion was refused: err=%v problems=%v", err, problems)
	}

	// The scope anchor still applies — omitting ifVersion drops the item-level
	// guard, not every guard.
	setHostname(t, crServerA, "third-party")
	if st := f.state(t, cr.ID); !st.Stale {
		t.Error("an unconditional request did not go stale when its target moved — the scope anchor is not covering it")
	}
}

// ── 8. honoured on delete ───────────────────────────────────────────────────

// A delete is where a stale precondition costs most: it destroys work with no
// diff to recover it from. `before` deliberately skips deletes (there are no
// fields to assert); `ifVersion` deliberately does not.
func TestCRIfVersion_HonouredOnDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("stale delete is refused", func(t *testing.T) {
		f := newCRFixture(t)
		cur := readVersion(t, crServerB)
		setHostname(t, crServerB, "edited-after-you-read-it")

		_, _, err := f.crh.Create(ctx, author, "stale delete", "",
			&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
				OrbID: crServerB, Op: approval.OpDelete, IfVersion: ifVersionPtr(cur),
			}}})

		var pf *preconditionFailed
		if !errors.As(err, &pf) {
			t.Fatalf("err = %v, want a preconditionFailed — a delete proposed against a version that has moved was accepted", err)
		}
		if len(pf.Problems) == 0 || pf.Problems[0].OrbID != crServerB {
			t.Errorf("refusal does not name the entity: %v", pf.Problems)
		}
		if !exists(t, "Server", crServerB) {
			t.Error("the refused delete removed the entity anyway")
		}
	})

	t.Run("current delete is accepted and still deletes", func(t *testing.T) {
		f := newCRFixture(t)
		f.requireApproval(t, 1)

		cr, problems, err := f.crh.Create(ctx, author, "current delete", "",
			&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
				OrbID: crServerB, Op: approval.OpDelete, IfVersion: ifVersionPtr(readVersion(t, crServerB)),
			}}})
		if err != nil || len(problems) > 0 {
			t.Fatalf("a delete with a current ifVersion was refused: err=%v problems=%v", err, problems)
		}
		if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if exists(t, "Server", crServerB) {
			t.Error("the approved delete did not remove the entity")
		}
	})
}

// ── 6, at the wire ──────────────────────────────────────────────────────────

// The domain-level test above proves the refusal happens; this proves what an
// API client actually receives. They are different claims — a refusal that
// surfaces as a 500 is still a refusal, and still useless to the caller.
func TestCRIfVersion_StaleCreateIs409OverHTTP(t *testing.T) {
	f := newCRFixture(t)

	cur := readVersion(t, crServerA)
	setHostname(t, crServerA, "moved-by-someone-else")

	body, err := json.Marshal(map[string]any{
		"title":     "stale over http",
		"namespace": crNS,
		"changes": []map[string]any{{
			"orbId": crServerA, "op": "update",
			"set":       map[string]any{"hostname": "based-on-stale-read"},
			"ifVersion": cur,
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/change-requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_email", author)
	c.Set("role", string(user.RoleDev))
	if err := f.crh.CreateChangeRequest(c); err != nil {
		t.Fatalf("CreateChangeRequest returned a transport error rather than a 409: %v", err)
	}

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Code     string `json:"code"`
		Problems []struct {
			OrbID string `json:"orbId"`
			Field string `json:"field"`
			Msg   string `json:"message"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if out.Code != CodeMVCCConflict {
		t.Errorf("code = %q, want %s", out.Code, CodeMVCCConflict)
	}
	if len(out.Problems) != 1 {
		t.Fatalf("problems = %d, want 1: %s", len(out.Problems), rec.Body.String())
	}
	if out.Problems[0].OrbID != crServerA {
		t.Errorf("problem does not name the entity: %+v", out.Problems[0])
	}
	if out.Problems[0].Field != "" {
		t.Errorf("entity-level problem carries field=%q over the wire", out.Problems[0].Field)
	}
	// The `message` key specifically: the JSON tag is `message`, and a test that
	// reads the wrong key asserts nothing while looking like it asserts a lot.
	if !strings.Contains(out.Problems[0].Msg, "version 1") {
		t.Errorf("problems[0].message = %q, want it to name the version the caller read", out.Problems[0].Msg)
	}
}
