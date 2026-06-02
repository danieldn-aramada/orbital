//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

func newEventHandler(t *testing.T) *handler.EventHandler {
	t.Helper()
	t.Chdir("../..")
	return handler.NewEventHandler(testDB, slog.Default())
}

func eventCtx(method, url string, query map[string]string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, url, nil)
	if len(query) > 0 {
		q := req.URL.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestEventList_EmptyDB(t *testing.T) {
	ctx := context.Background()
	testDB.Event.Delete().ExecX(ctx)

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", nil)

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, _ := body["events"].([]any)
	if len(events) != 0 {
		t.Errorf("expected empty events, got %d", len(events))
	}
	if body["total"].(float64) != 0 {
		t.Errorf("expected total=0, got %v", body["total"])
	}
}

func TestEventList_ReturnsEventsWithRequiredFields(t *testing.T) {
	ctx := context.Background()
	testDB.Event.Delete().ExecX(ctx)

	e1 := testDB.Event.Create().
		SetActor("user-a").
		SetOperations([]string{"updateServer"}).
		SetResourceTypes([]string{"Server"}).
		SetResourceIds([]string{"test:srv-01"}).
		SaveX(ctx)
	e2 := testDB.Event.Create().
		SetActor("user-b").
		SetOperations([]string{"updateDataCenter"}).
		SetResourceTypes([]string{"DataCenter"}).
		SetResourceIds([]string{"test:dc-01"}).
		SaveX(ctx)
	t.Cleanup(func() {
		testDB.Event.DeleteOne(e1).ExecX(ctx)
		testDB.Event.DeleteOne(e2).ExecX(ctx)
	})

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", nil)

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	// Each event must have required fields.
	for _, item := range events {
		m := item.(map[string]any)
		for _, field := range []string{"id", "operations", "resourceTypes", "resourceIds", "actor", "timestamp"} {
			if _, ok := m[field]; !ok {
				t.Errorf("missing field %q in event response", field)
			}
		}
	}
	if body["total"].(float64) != 2 {
		t.Errorf("expected total=2, got %v", body["total"])
	}
}

func TestEventList_FilterByResourceType(t *testing.T) {
	ctx := context.Background()
	testDB.Event.Delete().ExecX(ctx)

	eServer := testDB.Event.Create().
		SetActor("filter-test").
		SetOperations([]string{"updateServer"}).
		SetResourceTypes([]string{"Server"}).
		SetResourceIds([]string{"test:srv-99"}).
		SaveX(ctx)
	eDC := testDB.Event.Create().
		SetActor("filter-test").
		SetOperations([]string{"updateDataCenter"}).
		SetResourceTypes([]string{"DataCenter"}).
		SetResourceIds([]string{"test:dc-99"}).
		SaveX(ctx)
	t.Cleanup(func() {
		testDB.Event.DeleteOne(eServer).ExecX(ctx)
		testDB.Event.DeleteOne(eDC).ExecX(ctx)
	})

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"resource_type": "Server"})

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, _ := body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("expected 1 Server event, got %d", len(events))
	}
	m := events[0].(map[string]any)
	types, _ := m["resourceTypes"].([]any)
	if len(types) == 0 || types[0].(string) != "Server" {
		t.Errorf("resourceTypes: got %v, want [Server]", types)
	}
}

func TestEventList_FilterByOrbId(t *testing.T) {
	ctx := context.Background()
	testDB.Event.Delete().ExecX(ctx)

	target := testDB.Event.Create().
		SetActor("orbid-test").
		SetOperations([]string{"updateServer"}).
		SetResourceTypes([]string{"Server"}).
		SetResourceIds([]string{"alaska:SRV-MATCH"}).
		SaveX(ctx)
	other := testDB.Event.Create().
		SetActor("orbid-test").
		SetOperations([]string{"updateServer"}).
		SetResourceTypes([]string{"Server"}).
		SetResourceIds([]string{"alaska:SRV-OTHER"}).
		SaveX(ctx)
	t.Cleanup(func() {
		testDB.Event.DeleteOne(target).ExecX(ctx)
		testDB.Event.DeleteOne(other).ExecX(ctx)
	})

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"orbId": "alaska:SRV-MATCH"})

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, _ := body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("expected 1 matching event, got %d", len(events))
	}
	m := events[0].(map[string]any)
	ids, _ := m["resourceIds"].([]any)
	if len(ids) == 0 || ids[0].(string) != "alaska:SRV-MATCH" {
		t.Errorf("resourceIds: got %v, want [alaska:SRV-MATCH]", ids)
	}
}

func TestEventList_LimitAndOffset(t *testing.T) {
	ctx := context.Background()
	testDB.Event.Delete().ExecX(ctx)

	for range 5 {
		testDB.Event.Create().
			SetActor("pagination-test").
			SetOperations([]string{"op"}).
			SetResourceTypes([]string{"Server"}).
			SetResourceIds([]string{"test:srv"}).
			ExecX(ctx)
	}
	t.Cleanup(func() {
		testDB.Event.Delete().ExecX(ctx)
	})

	h := newEventHandler(t)

	// Limit to 2.
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"limit": "2"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Errorf("limit=2: expected 2 events, got %d", len(events))
	}
	if body["total"].(float64) != 5 {
		t.Errorf("total: expected 5, got %v", body["total"])
	}

	// Offset beyond count returns empty.
	c2, rec2 := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"offset": "10"})
	if err := h.List(c2); err != nil {
		t.Fatalf("List offset: %v", err)
	}
	var body2 map[string]any
	json.Unmarshal(rec2.Body.Bytes(), &body2)
	events2, _ := body2["events"].([]any)
	if len(events2) != 0 {
		t.Errorf("offset=10: expected 0 events, got %d", len(events2))
	}
}

func TestEventList_OrderedByTimestampDesc(t *testing.T) {
	ctx := context.Background()
	testDB.Event.Delete().ExecX(ctx)

	// Create events with explicit timestamps so ordering is deterministic.
	e1 := testDB.Event.Create().
		SetActor("order-test").
		SetOperations([]string{"op"}).
		SetResourceTypes([]string{"Server"}).
		SetResourceIds([]string{"test:srv"}).
		SetTimestamp(time.Now().Add(-2 * time.Minute)).
		SaveX(ctx)
	e2 := testDB.Event.Create().
		SetActor("order-test").
		SetOperations([]string{"op"}).
		SetResourceTypes([]string{"Server"}).
		SetResourceIds([]string{"test:srv"}).
		SetTimestamp(time.Now()).
		SaveX(ctx)
	t.Cleanup(func() {
		testDB.Event.DeleteOne(e1).ExecX(ctx)
		testDB.Event.DeleteOne(e2).ExecX(ctx)
	})

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", nil)
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	events, _ := body["events"].([]any)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
	// First event should have a later timestamp than the second.
	t1 := events[0].(map[string]any)["timestamp"].(string)
	t2 := events[1].(map[string]any)["timestamp"].(string)
	if t1 < t2 {
		t.Errorf("events not ordered desc: first=%s second=%s", t1, t2)
	}
}
