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

// "Which change request produced this change?" was unanswerable from the audit
// log: a merge's writes went through DispatchMutation, which hardcoded an origin
// carrying nothing — no request id, no reference to the request that authorized
// them. Two writes by the same person, one reviewed through colo-58 and one made
// directly, were indistinguishable.
//
// The guarantee under test is DELIVERY, not storage. Storing it in `details`
// alone would leave a consumer parsing a blob whose shape varies by event type,
// so this reads back through GET /api/v1/audit-log and asserts the promoted
// top-level field — which is the thing a caller actually integrates against.
func TestAuditLog_AuthorizationIsReturnedTopLevel(t *testing.T) {
	const actor = "merger@authzby.test"
	ctx := context.Background()
	t.Cleanup(func() { testDB.AuditEvent.Delete().Where(auditevent.Actor(actor)).ExecX(ctx) })

	testDB.AuditEvent.Create().
		SetActor(actor).
		SetEventCategory("data").
		SetOperations([]string{"updateIdracSettings"}).
		SetEventSource("internal").
		SetRequestID("req-abc123").
		// Columns, not a key in details — the storage decision this test also
		// pins: the field must not appear in the payload a caller sent.
		SetAuthorizationType("changeRequest").
		SetAuthorizationID("colo-58").
		SetDetails(json.RawMessage(`{"operationName":"updateIdracSettings"}`)).
		SaveX(ctx)

	var found bool
	for _, it := range listAuthorizationEvents(t) {
		if it.Actor != actor {
			continue
		}
		found = true
		if it.Authorization == nil {
			t.Fatal("authorization absent from the response — a consumer would have to parse details")
		}
		if it.Authorization.Type != "changeRequest" || it.Authorization.ID != "colo-58" {
			t.Errorf("authorizedBy = %+v, want {changeRequest colo-58}", *it.Authorization)
		}
		// The request id is the other half: it groups every write one merge
		// produced, which a single change-request reference does not.
		if it.RequestID != "req-abc123" {
			t.Errorf("requestId = %q, want the merge's request id", it.RequestID)
		}
		// And it appears ONCE. It was briefly stored in details AND promoted,
		// so one response carried the same fact twice — incoherent, and unlike
		// every other field on this record.
		if strings.Contains(string(it.Details), "authorization") {
			t.Errorf("authorization is inside details as well as top-level — it is record metadata, not payload:\n%s", it.Details)
		}
	}
	if !found {
		t.Fatalf("event for %q not returned at all", actor)
	}
}

// The negative, and the reason the field is conditional: a direct write passed
// no control, and that is exactly what its absence means. If `authorizedBy`
// appeared on every row — empty, null, or otherwise — it would stop
// distinguishing reviewed changes from ungated ones, which is its only job.
func TestAuditLog_AuthorizationIsAbsentOnADirectWrite(t *testing.T) {
	const actor = "direct@authzby.test"
	ctx := context.Background()
	t.Cleanup(func() { testDB.AuditEvent.Delete().Where(auditevent.Actor(actor)).ExecX(ctx) })

	testDB.AuditEvent.Create().
		SetActor(actor).
		SetEventCategory("data").
		SetOperations([]string{"updateIdracSettings"}).
		SetDetails(json.RawMessage(`{"operationName":"updateIdracSettings"}`)).
		SaveX(ctx)

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"limit": "500"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, actor) {
		t.Fatalf("event for %q not in the response", actor)
	}
	// Asserted on the raw body: omitempty is the behaviour under test, and a
	// decoded struct cannot tell "absent" from "zero value".
	for _, chunk := range strings.Split(body, `{"id":`) {
		if strings.Contains(chunk, actor) && strings.Contains(chunk, `"authorization"`) {
			t.Errorf("authorization present on a direct write; want the key omitted:\n%s", chunk)
		}
	}
}

func listAuthorizationEvents(t *testing.T) []struct {
	Actor         string          `json:"actor"`
	RequestID     string          `json:"requestId"`
	Details       json.RawMessage `json:"details"`
	Authorization *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"authorization"`
} {
	t.Helper()
	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"limit": "500"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	var resp struct {
		Events []struct {
			Actor         string          `json:"actor"`
			RequestID     string          `json:"requestId"`
			Details       json.RawMessage `json:"details"`
			Authorization *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"authorization"`
		} `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Events
}
