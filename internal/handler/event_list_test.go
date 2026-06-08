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

// clearEvents deletes all event child records then events to satisfy FK constraints.
func clearEvents(ctx context.Context) {
	testDB.EventResourceType.Delete().ExecX(ctx)
	testDB.EventResource.Delete().ExecX(ctx)
	testDB.Event.Delete().ExecX(ctx)
}

// createEvent creates an event and associates orbId resources and resource types with it.
func createEvent(t *testing.T, actor string, operations, resourceTypes, resourceIDs []string, category string) {
	t.Helper()
	ctx := context.Background()
	c := testDB.Event.Create().SetActor(actor).SetOperations(operations)
	if category != "" {
		c = c.SetEventCategory(category)
	}
	ev := c.SaveX(ctx)
	for _, rid := range resourceIDs {
		testDB.EventResource.Create().SetOrbID(rid).SetEventID(ev.ID).ExecX(ctx)
	}
	for _, rt := range resourceTypes {
		testDB.EventResourceType.Create().SetResourceType(rt).SetEventID(ev.ID).ExecX(ctx)
	}
}

func TestEventList_EmptyDB(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

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
	clearEvents(ctx)

	createEvent(t, "user-a", []string{"updateServer"}, []string{"Server"}, []string{"test:srv-01"}, "")
	createEvent(t, "user-b", []string{"updateDataCenter"}, []string{"DataCenter"}, []string{"test:dc-01"}, "")
	t.Cleanup(func() { clearEvents(ctx) })

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


func TestEventList_FilterByOrbId(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "orbid-test", []string{"updateServer"}, []string{"Server"}, []string{"alaska:SRV-MATCH"}, "")
	createEvent(t, "orbid-test", []string{"updateServer"}, []string{"Server"}, []string{"alaska:SRV-OTHER"}, "")
	t.Cleanup(func() { clearEvents(ctx) })

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

// TestEventList_FilterByOrbId_ExactMatch verifies that prefix orbIds don't collide.
// e.g. filtering for "ns:server:a" must not return events for "ns:server:ab".
func TestEventList_FilterByOrbId_ExactMatch(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "exact-test", []string{"updateServer"}, []string{"Server"}, []string{"ns:server:a"}, "")
	createEvent(t, "exact-test", []string{"updateServer"}, []string{"Server"}, []string{"ns:server:ab"}, "")
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"orbId": "ns:server:a"})

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	events, _ := body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("exact match: expected 1 event for ns:server:a, got %d", len(events))
	}
}

func TestEventList_FilterByResourceType(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "filter-test", []string{"updateServer"}, []string{"Server"}, []string{"test:srv-99"}, "")
	createEvent(t, "filter-test", []string{"updateDataCenter"}, []string{"DataCenter"}, []string{"test:dc-99"}, "")
	t.Cleanup(func() { clearEvents(ctx) })

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

func TestEventList_LimitAndOffset(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	for range 5 {
		createEvent(t, "pagination-test", []string{"op"}, []string{"Server"}, []string{"test:srv"}, "")
	}
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)

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

func TestEventList_EventCategoryInResponse(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "cat-test", []string{"updateServer"}, []string{"Server"}, []string{"test:srv-cat"}, "data")
	createEvent(t, "cat-test", []string{"restoreBackup"}, nil, nil, "management")
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", nil)
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	cats := map[string]bool{}
	for _, ev := range events {
		m := ev.(map[string]any)
		cat, ok := m["eventCategory"].(string)
		if !ok || cat == "" {
			t.Errorf("eventCategory missing or empty in event: %v", m)
		}
		cats[cat] = true
	}
	if !cats["data"] {
		t.Error("expected at least one data event")
	}
	if !cats["management"] {
		t.Error("expected at least one management event")
	}
}

func TestEventList_OrbIdFilterIncludesManagementEvents(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "mgmt-filter-test", []string{"updateServer"}, []string{"Server"}, []string{"alaska:SRV-MGMT"}, "data")
	createEvent(t, "mgmt-filter-test", []string{"restoreBackup"}, nil, nil, "management")
	createEvent(t, "mgmt-filter-test", []string{"updateServer"}, nil, []string{"alaska:SRV-OTHER"}, "data")
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"orbId": "alaska:SRV-MGMT"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 data + 1 management), got %d: %v", len(events), body)
	}
	cats := map[string]bool{}
	for _, ev := range events {
		m := ev.(map[string]any)
		cats[m["eventCategory"].(string)] = true
	}
	if !cats["data"] {
		t.Error("expected data event in results")
	}
	if !cats["management"] {
		t.Error("expected management event in results")
	}
}

func TestEventList_OrbIdFilterExcludesAuthEvents(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "auth-filter-test", []string{"updateServer"}, []string{"Server"}, []string{"ns:srv-01"}, "data")
	createEvent(t, "auth-filter-test", []string{"loginSuccess"}, nil, nil, "auth")
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"orbId": "ns:srv-01"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	events, _ := body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("auth events must be excluded from orbId filter; expected 1 event, got %d: %v", len(events), body)
	}
	m := events[0].(map[string]any)
	if m["eventCategory"].(string) != "data" {
		t.Errorf("expected data event, got %q", m["eventCategory"])
	}
}

func TestEventList_OrderedByTimestampDesc(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	e1 := testDB.Event.Create().
		SetActor("order-test").
		SetOperations([]string{"op"}).
		SetTimestamp(time.Now().Add(-2 * time.Minute)).
		SaveX(ctx)
	testDB.EventResource.Create().SetOrbID("test:srv").SetEventID(e1.ID).ExecX(ctx)
	testDB.EventResourceType.Create().SetResourceType("Server").SetEventID(e1.ID).ExecX(ctx)

	e2 := testDB.Event.Create().
		SetActor("order-test").
		SetOperations([]string{"op"}).
		SetTimestamp(time.Now()).
		SaveX(ctx)
	testDB.EventResource.Create().SetOrbID("test:srv").SetEventID(e2.ID).ExecX(ctx)
	testDB.EventResourceType.Create().SetResourceType("Server").SetEventID(e2.ID).ExecX(ctx)

	t.Cleanup(func() { clearEvents(ctx) })

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
	t1 := events[0].(map[string]any)["timestamp"].(string)
	t2 := events[1].(map[string]any)["timestamp"].(string)
	if t1 < t2 {
		t.Errorf("events not ordered desc: first=%s second=%s", t1, t2)
	}
}
