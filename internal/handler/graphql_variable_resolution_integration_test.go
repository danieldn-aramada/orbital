//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

// ── the write path ───────────────────────────────────────────────────────────

// This test was the tripwire for a KNOWN GAP and has been FLIPPED: a
// renamed-variable update used to be refused 400 VARIABLE_FORM_REQUIRED, and
// that refusal was correct at the time — `fetchCurrentState` resolved its
// target by literal variable name, so the write could not be stamped, guarded
// or diffed, and an unstamped record would have been worse than a 400.
//
// Now the selector and the patch are resolved by REFERENCE, so the assertion is
// the stronger one: the two spellings must be INDISTINGUISHABLE downstream.
// Asserting "the renamed one succeeds" would pass while silently dropping the
// version bump or the diff — the exact half-fix that shipped last time — so
// every observable the canonical spelling produces is checked on both.
func TestVariableResolution_RenamedUpdateIsStampedAndDiffedLikeTheCanonicalOne(t *testing.T) {
	cases := []struct {
		name, query, orbIDVar, setVar string
	}{
		{
			name:     "canonical $orbId / $set",
			query:    `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
			orbIDVar: "orbId", setVar: "set",
		},
		{
			name:     "renamed $serverOrbId / $patch",
			query:    `mutation UpdateServer($serverOrbId: String!, $patch: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $serverOrbId}}, set: $patch}) { numUids } }`,
			orbIDVar: "serverOrbId", setVar: "patch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actor := "varres-" + tc.orbIDVar
			f := newCRFixture(t)
			dgraph, forwarded := mockDGraphWithBefore(t,
				`{"data":{"queryServer":[{"id":"1","orbId":"`+crServerA+`","hostname":"old-host","version":7}]}}`,
				`{"data":{"updateServer":{"numUids":1}}}`)

			runMutation(t, f, actor, dgraph.URL, tc.query, map[string]any{
				tc.orbIDVar: crServerA,
				tc.setVar:   map[string]any{"hostname": "new-host"},
			})

			// 1. Accepted — runMutation fails the test on a non-2xx, so reaching
			//    here already means no VARIABLE_FORM_REQUIRED.
			// 2. Stamped, in the caller's own patch variable.
			var fwd gqlRequest
			if err := json.Unmarshal(*forwarded, &fwd); err != nil {
				t.Fatalf("decode forwarded body: %v", err)
			}
			set, _ := fwd.Variables[tc.setVar].(map[string]any)
			if n, _ := toFloat64(set["version"]); n != 8 {
				t.Errorf("%s.version = %v, want 8 (current 7 + 1) — the write was not stamped", tc.setVar, set["version"])
			}
			if set["updatedBy"] != actor {
				t.Errorf("%s.updatedBy = %v, want %q", tc.setVar, set["updatedBy"], actor)
			}
			if set["updatedAt"] == nil {
				t.Errorf("%s.updatedAt was not stamped: %#v", tc.setVar, set)
			}

			// 3 + 4. The audit row carries a real before-state AND a rendered
			//        field diff, read back through GET /api/v1/audit-log.
			//
			//        `Changes` is the assertion that matters: the differ finds
			//        after-values in the patch variable, and its old fallback —
			//        "no key called set, so diff against the WHOLE variables map"
			//        — intersects {serverOrbId, patch} with the before-state,
			//        matches nothing, and renders an empty diff on a write that
			//        landed normally. No error, nothing in the log.
			events := auditEventsByResource(t, f, crServerA)
			var ev *auditEventView
			for i := range events {
				if events[i].Actor == actor {
					ev = &events[i]
					break
				}
			}
			if ev == nil {
				t.Fatalf("no audit event for actor %q at resource %s", actor, crServerA)
			}
			if ev.Details["before"] == nil {
				t.Fatalf("audit event carries no before-state: %#v", ev.Details)
			}
			if len(ev.Changes) != 1 {
				t.Fatalf("changes = %#v, want exactly 1 (hostname) — an empty diff is how this fails silently", ev.Changes)
			}
			if ev.Changes[0].Field != "hostname" ||
				ev.Changes[0].Before != "old-host" || ev.Changes[0].After != "new-host" {
				t.Errorf("diff = %+v, want hostname old-host → new-host", ev.Changes[0])
			}
		})
	}
}

// A shape the write path cannot pin to exactly one row must STILL be refused,
// and must not reach DGraph. Resolution was only ever allowed to turn a refusal
// into a fully stamped write — never into an unstamped one.
func TestVariableResolution_UnresolvableUpdateIsStillRefusedAndNeverSent(t *testing.T) {
	cases := []struct{ name, query string }{
		{
			name:  "inline literal selector and inline set",
			query: `mutation UpdateServer { updateServer(input: {filter: {orbId: {eq: "` + crServerA + `"}}, set: {hostname: "x"}}) { numUids } }`,
		},
		{
			name:  "in-list names more than one row",
			query: `mutation UpdateServer($a: String!, $patch: ServerPatch!) { updateServer(input: {filter: {orbId: {in: [$a, "ns:server-B"]}}, set: $patch}) { numUids } }`,
		},
		{
			name:  "whole filter behind a variable — readable by the gate, not stampable",
			query: `mutation UpdateServer($f: ServerFilter!, $patch: ServerPatch!) { updateServer(input: {filter: $f, set: $patch}) { numUids } }`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCRFixture(t)
			var reached bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				if bytes.Contains(b, []byte("BeforeFetch")) {
					w.Write([]byte(`{"data":{"queryServer":[{"id":"1","version":7}]}}`)) //nolint:errcheck
					return
				}
				reached = true
				w.Write([]byte(`{"data":{"updateServer":{"numUids":1}}}`)) //nolint:errcheck
			}))
			t.Cleanup(srv.Close)

			gql := NewGraphQL(srv.URL, f.db, slog.Default(), true)
			body, _ := json.Marshal(gqlRequest{Query: tc.query, Variables: map[string]any{
				"a":     crServerA,
				"f":     map[string]any{"orbId": map[string]any{"eq": crServerA}},
				"patch": map[string]any{"hostname": "x"},
			}})
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.Set("user_email", "unresolvable-actor")
			c.Set("role", string(user.RoleAdmin))

			if err := gql.Handle(c); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if reached {
				t.Fatal("an unstampable update was sent to DGraph")
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			var got struct{ Code string }
			_ = json.Unmarshal(rec.Body.Bytes(), &got)
			if got.Code != CodeVariableFormRequired {
				t.Errorf("code = %q, want %s", got.Code, CodeVariableFormRequired)
			}
		})
	}
}

// mockDGraphWithBefore answers the before-fetch and the mutation differently,
// and hands back the body the mutation actually forwarded — the only place the
// server-side stamp is observable before it reaches the store.
func mockDGraphWithBefore(t *testing.T, beforeResp, mutationResp string) (*httptest.Server, *[]byte) {
	t.Helper()
	var forwarded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(b, []byte("BeforeFetch")) {
			w.Write([]byte(beforeResp)) //nolint:errcheck
			return
		}
		forwarded = b
		w.Write([]byte(mutationResp)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv, &forwarded
}
