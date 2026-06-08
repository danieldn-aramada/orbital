//go:build integration

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/armada/orbital/ent/event"
	"github.com/armada/orbital/ent/eventresource"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

// mockDGraphSrv starts an httptest server that returns a fixed JSON response.
func mockDGraphSrv(t *testing.T, response string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(response)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pollForEvent polls the events table until an event matching the actor appears
// or the deadline is exceeded. Returns the event or calls t.Fatal.
func pollForEvent(t *testing.T, actor string, deadline time.Duration) {
	t.Helper()
	ctx := context.Background()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		count := testDB.Event.Query().Where(event.Actor(actor)).CountX(ctx)
		if count > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no audit event written for actor %q within %s", actor, deadline)
}

func TestGraphQL_MutationWritesAuditEvent(t *testing.T) {
	const actor = "audit-test-actor"
	ctx := context.Background()

	// Create an admin user so the mutation role check passes.
	adminUser := createTestUser(t, "graphql-audit-admin@test.com", user.RoleAdmin)

	// Clean up any pre-existing events for this actor (from prior runs).
	clearEvents(ctx)
	t.Cleanup(func() { clearEvents(ctx) })

	// Mock DGraph returns a successful mutation response with an orbId.
	dgraph := mockDGraphSrv(t, `{"data":{"addServer":{"server":[{"orbId":"alaska:SRV999"}]}}}`)
	h := handler.NewGraphQL(dgraph.URL, testDB, slog.Default())

	e := echo.New()
	body, _ := json.Marshal(map[string]any{
		"query": `mutation { addServer(input:[]) { server { orbId } } }`,
		"variables": map[string]any{
			"updatedBy": actor,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", adminUser) // role enforcement requires an authenticated admin

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// The audit event is written in a goroutine — poll until it appears.
	pollForEvent(t, actor, 5*time.Second)

	// Verify the event fields.
	ev := testDB.Event.Query().Where(event.Actor(actor)).WithResources().WithResourceTypes().OnlyX(ctx)
	if len(ev.Operations) == 0 || ev.Operations[0] != "addServer" {
		t.Errorf("operations: got %v, want [addServer]", ev.Operations)
	}
	if len(ev.Edges.ResourceTypes) == 0 || ev.Edges.ResourceTypes[0].ResourceType != "Server" {
		t.Errorf("resourceTypes: got %v, want [Server]", ev.Edges.ResourceTypes)
	}
	resources := testDB.EventResource.Query().
		Where(eventresource.HasEventWith(event.Actor(actor))).
		AllX(ctx)
	if len(resources) == 0 || resources[0].OrbID != "alaska:SRV999" {
		t.Errorf("resources: got %v, want [alaska:SRV999]", resources)
	}
}

func TestGraphQL_GQLErrorsSuppressAuditEvent(t *testing.T) {
	const actor = "audit-error-actor"
	ctx := context.Background()

	clearEvents(ctx)

	// DGraph returns errors — audit must not be written.
	dgraph := mockDGraphSrv(t, `{"errors":[{"message":"something went wrong"}]}`)
	h := handler.NewGraphQL(dgraph.URL, testDB, slog.Default())

	e := echo.New()
	body, _ := json.Marshal(map[string]any{
		"query": `mutation { addServer(input:[]) { server { orbId } } }`,
		"variables": map[string]any{
			"updatedBy": actor,
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Give the goroutine time to run (it shouldn't write anything).
	time.Sleep(300 * time.Millisecond)

	count := testDB.Event.Query().Where(event.Actor(actor)).CountX(ctx)
	if count != 0 {
		t.Errorf("expected no audit event on GQL error, got %d", count)
	}
}
