//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/auditevent"
)

// acting_client answers "a service did this on a human's behalf" — the column
// exists so that if a trusted upstream service's authorization has a bug, the
// audit log can tell a direct action apart from one taken through that service.
// It has been written on every applicable row since 2026-09 and was never
// returned by the API, so the question it exists to answer was unanswerable by
// any consumer. FedRAMP's AU-3(1) asks for exactly this under "individual
// identities of group account users".
//
// This reads back through GET /api/v1/audit-log rather than the table, because
// the guarantee is about delivery, not storage — the storage half already
// worked, and testing it again would have passed throughout the bug.
func TestAuditLog_ActingClientIsReturnedToConsumers(t *testing.T) {
	const actor = "human@actingclienttest.com"
	const client = "aep-fleet-commander"
	ctx := context.Background()
	t.Cleanup(func() { testDB.AuditEvent.Delete().Where(auditevent.Actor(actor)).ExecX(ctx) })

	testDB.AuditEvent.Create().
		SetActor(actor).
		SetEventCategory("data").
		SetOperations([]string{"updateServer"}).
		SetActingClient(client).
		SetDetails(json.RawMessage(`{"operationName":"updateServer"}`)).
		SaveX(ctx)

	items := listEvents(t, map[string]string{"limit": "500"})

	found := false
	for _, it := range items {
		if it.Actor != actor {
			continue
		}
		found = true
		if it.ActingClient != client {
			t.Errorf("actingClient = %q, want %q — the column is written but not delivered", it.ActingClient, client)
		}
	}
	if !found {
		t.Fatalf("event for %q not returned by the API at all", actor)
	}
}

// The negative, and it is the reason acting_client is conditional in the first
// place: a user signing in directly is their own actor. Stamping that on every
// row would make the field noise and train readers to ignore it — the same
// reasoning the `privileged` flag follows. Absent must stay absent, not become
// an empty string a consumer has to learn to filter.
func TestAuditLog_ActingClientIsOmittedWhenItIsTheSubject(t *testing.T) {
	const actor = "direct@actingclienttest.com"
	ctx := context.Background()
	t.Cleanup(func() { testDB.AuditEvent.Delete().Where(auditevent.Actor(actor)).ExecX(ctx) })

	testDB.AuditEvent.Create().
		SetActor(actor).
		SetEventCategory("data").
		SetOperations([]string{"updateServer"}).
		SetDetails(json.RawMessage(`{"operationName":"updateServer"}`)).
		SaveX(ctx)

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"limit": "500"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Asserted on the raw body: `omitempty` is the behaviour under test, and a
	// decoded struct cannot distinguish "absent" from "empty string".
	body := rec.Body.String()
	if !strings.Contains(body, actor) {
		t.Fatalf("event for %q not in the response", actor)
	}
	for _, line := range strings.Split(body, `{"id":`) {
		if strings.Contains(line, actor) && strings.Contains(line, `"actingClient"`) {
			t.Errorf("actingClient present on a direct action; want the key omitted entirely:\n%s", line)
		}
	}
}

// listEvents decodes the audit-log response into its items.
func listEvents(t *testing.T, query map[string]string) []struct {
	Actor        string `json:"actor"`
	ActingClient string `json:"actingClient"`
	Timestamp    string `json:"timestamp"`
} {
	t.Helper()
	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", query)
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Events []struct {
			Actor        string `json:"actor"`
			ActingClient string `json:"actingClient"`
			Timestamp    string `json:"timestamp"`
		} `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Events
}
