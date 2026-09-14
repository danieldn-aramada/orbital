//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/armada/orbital/ent/auditevent"
	"github.com/armada/orbital/ent/user"
	"github.com/labstack/echo/v4"
)

// extractResourceIDs feeds BOTH the approval gate and the audit log, so
// teaching it to resolve variable references changes what lands in
// `resource_ids` as well as what the gate can see.
//
// That is persistence, not logging: `GET /api/v1/audit-log?resource_id=` is the
// query behind every ConfigItem audit tab, and an orbId that never resolved was
// an entity whose changelog silently had a hole in it. These read back through
// that endpoint — the consumer API — rather than the table the writer used.

// runMutation drives a mutation through Handle exactly as an API caller would,
// against a mock DGraph, and waits for the async audit write.
func runMutation(t *testing.T, f *crFixture, actor, dgraphURL, query string, vars map[string]any) {
	t.Helper()
	// true = production's ORBITAL_INLINE_SELECTOR_REJECT. This DOES matter here:
	// runMutation drives Handle, where the inline-selector guard lives.
	gql := NewGraphQL(dgraphURL, f.db, slog.Default(), true)

	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_email", actor)
	c.Set("role", string(user.RoleAdmin))

	if err := gql.Handle(c); err != nil {
		t.Fatalf("Handle: %v (body: %s)", err, rec.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.db.AuditEvent.Query().Where(auditevent.Actor(actor)).CountX(context.Background()) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no audit event written for actor %q", actor)
}

// Acceptance 6: an orbId named only through a differently-named variable is
// findable through the audit-log API afterwards.
func TestAuditLog_VariableFormOrbIdIsFindableThroughTheAuditLogAPI(t *testing.T) {
	const actor = "resourceid-audit-actor"
	f := newCRFixture(t)

	// The response deliberately carries NO orbId, so the id can only come from
	// resolving $serverOrbId. Otherwise the response-body walk would mask the bug.
	dgraph := mockDGraph(t, `{"data":{"deleteServer":{"numUids":1}}}`)
	// A DELETE, not an update: deletes carry no stamping, so they pass the
	// inline-selector guard and actually reach the gate and the audit writer.
	// A renamed-variable UPDATE is still refused upstream — pinned separately.
	q := `mutation DeleteServer($serverOrbId: String!) { deleteServer(filter: {orbId: {eq: $serverOrbId}}) { numUids } }`
	runMutation(t, f, actor, dgraph.URL, q, map[string]any{"serverOrbId": crServerA})

	events := auditEventsByResource(t, f, crServerA)
	found := slices.ContainsFunc(events, func(e auditEventView) bool {
		return e.Actor == actor && slices.Contains(e.Operations, "deleteServer")
	})
	if !found {
		t.Fatalf("the write is invisible to GET /api/v1/audit-log?resource_id=%s — got %d events: %+v",
			crServerA, len(events), events)
	}
}

// Acceptance 7: assert the negative. A mutation naming no resolvable orbId must
// record NO resource id rather than a guessed one — a wrong id reads as fact.
func TestAuditLog_UnresolvableMutationRecordsNoResourceIDs(t *testing.T) {
	const actor = "resourceid-negative-actor"
	f := newCRFixture(t)

	dgraph := mockDGraph(t, `{"data":{"deleteServer":{"numUids":1}}}`)
	// Filters on hostname, not orbId, and the response carries no orbId either.
	q := `mutation ByName($f: ServerFilter!) { deleteServer(filter: $f) { numUids } }`
	runMutation(t, f, actor, dgraph.URL, q, map[string]any{
		"f": map[string]any{"hostname": map[string]any{"eq": "no-orbid-anywhere"}},
	})

	ev := f.db.AuditEvent.Query().
		Where(auditevent.Actor(actor)).
		WithResources().
		OnlyX(context.Background())
	if n := len(ev.Edges.Resources); n != 0 {
		var got []string
		for _, r := range ev.Edges.Resources {
			got = append(got, r.OrbID)
		}
		t.Fatalf("recorded %d resource id(s) for a mutation that named none: %v", n, got)
	}

	// And it must not have been mis-attributed to a real entity either.
	for _, e := range auditEventsByResource(t, f, crServerA) {
		if e.Actor == actor {
			t.Fatalf("a mutation naming no orbId was attributed to %s", crServerA)
		}
	}
}

// KNOWN GAP, pinned deliberately: a renamed-variable UPDATE is still refused.
//
// Resolving variable references fixed the approval gate's namespace lookup, but
// `update` mutations never reach it. `fetchCurrentState` resolves its target
// from Variables["id"]/["orbId"] BY NAME, and it feeds three things at once —
// the version check, the version bump, and the audit before-state. So an update
// behind `$serverOrbId` genuinely cannot be stamped, and the inline-selector
// guard in Handle refuses it first. That refusal is CORRECT: writing an
// unstamped, unguarded, undiffable record would be worse.
//
// This test exists so the gap is a recorded fact rather than a surprise. When
// the write path learns to resolve the variable too, this test should FLIP to
// expecting the mutation to succeed — it is the tripwire for that work, tracked
// in docs/planning/debt.md.
func TestHandle_RenamedVariableUpdateIsStillRefusedByTheInlineSelectorGuard(t *testing.T) {
	f := newCRFixture(t)
	dgraph := mockDGraph(t, `{"data":{"updateServer":{"numUids":1}}}`)
	gql := NewGraphQL(dgraph.URL, f.db, slog.Default(), true) // production's value

	body, err := json.Marshal(gqlRequest{
		Query: `mutation UpdateServer($serverOrbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $serverOrbId}}, set: $set}) { numUids } }`,
		Variables: map[string]any{
			"serverOrbId": crServerA,
			"set":         map[string]any{"hostname": "should-not-land"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_email", "gap-actor")
	c.Set("role", string(user.RoleAdmin))

	if err := gql.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — if this now succeeds, the write path "+
			"learned to resolve renamed variables and this test should be flipped", rec.Code)
	}
	var body2 struct{ Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body2)
	if body2.Code != CodeVariableFormRequired {
		t.Errorf("code = %q, want %s", body2.Code, CodeVariableFormRequired)
	}
}
