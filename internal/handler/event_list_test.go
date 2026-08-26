//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

func newEventHandler(t *testing.T) *handler.EventHandler {
	t.Helper()
	t.Chdir("../..")
	return handler.NewEventHandler(testDB, slog.Default(), "")
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
// clearEvents wipes the audit tables, children first.
//
// Retries because this races background audit writers: an export goroutine's
// emitExportEvent can insert an Event AND its resource-type children in the
// window between our child delete and our parent delete, which trips
// event_resource_types' FK onto events. The window is tiny but real — it made
// TestGraphQL_MutationWritesAuditEvent fail roughly 1 run in 4 once other tests
// started completing their exports.
func clearEvents(ctx context.Context) {
	for attempt := 0; attempt < 5; attempt++ {
		_, _ = testDB.EventResourceType.Delete().Exec(ctx)
		_, _ = testDB.EventResource.Delete().Exec(ctx)
		if _, err := testDB.Event.Delete().Exec(ctx); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
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

// TestEventList_OperationNameFilter pins the operation_name filter (JSONB
// array-membership). The regression class is the containment predicate: it must
// match an event whose `operations` array *contains* the value — including
// compound multi-op events — and exclude events that don't. A broken predicate
// 500s or returns the wrong set; neither is visible without a real Postgres.
func TestEventList_OperationNameFilter(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "a@x.com", []string{"updateVeleroBackup"}, []string{"VeleroBackup"}, []string{"colo:dev-main-velero-backup"}, "data")
	createEvent(t, "a@x.com", []string{"updateEtcdBackup"}, []string{"EtcdBackup"}, []string{"colo:dev-main-etcd-backup"}, "data")
	// Compound event: array-membership must still match, not just single-element arrays.
	createEvent(t, "a@x.com", []string{"updateServer", "updateVeleroBackup"}, []string{"Server", "VeleroBackup"}, []string{"colo:srv-1"}, "data")
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"operation_name": "updateVeleroBackup"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("operation_name=updateVeleroBackup should match the pure + compound events (2), got %d: %s", len(events), rec.Body.String())
	}
	for _, item := range events {
		ops, _ := item.(map[string]any)["operations"].([]any)
		found := false
		for _, o := range ops {
			if o == "updateVeleroBackup" {
				found = true
			}
		}
		if !found {
			t.Errorf("returned event lacks updateVeleroBackup in operations: %v", ops)
		}
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

// TestEventList_FilterByOrbId_Multiple verifies repeatable ?orbId= params
// return events whose resources include ANY of the listed orbIds.
// This is the shape the server tab uses to pull events for a server PLUS
// its nested ConfigItems (IdracSettings, ServerConfigurationProfile, etc.)
// in one fetch.
func TestEventList_FilterByOrbId_Multiple(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "multi-test", []string{"updateServer"}, []string{"Server"}, []string{"colo:CWJHDX3"}, "")
	createEvent(t, "multi-test", []string{"updateIdracSettings"}, []string{"IdracSettings"}, []string{"colo:CWJHDX3-idrac"}, "")
	createEvent(t, "multi-test", []string{"updateServer"}, []string{"Server"}, []string{"colo:UNRELATED"}, "")
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/audit-log?orbId=colo:CWJHDX3&orbId=colo:CWJHDX3-idrac", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("expected 2 events (server + idrac), got %d", len(events))
	}
	seen := map[string]bool{}
	for _, ev := range events {
		m := ev.(map[string]any)
		ids, _ := m["resourceIds"].([]any)
		for _, id := range ids {
			seen[id.(string)] = true
		}
	}
	for _, want := range []string{"colo:CWJHDX3", "colo:CWJHDX3-idrac"} {
		if !seen[want] {
			t.Errorf("missing expected orbId %q in returned events", want)
		}
	}
	if seen["colo:UNRELATED"] {
		t.Errorf("returned event for unlisted orbId colo:UNRELATED")
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

// TestEventList_OrbIdFilterExcludesUnrelatedManagementEvents verifies that
// resource-scoped audit panels (DC, Server) only show events whose resources
// include the queried orbId. Management events with no resource link
// (createBackup, authorizationDenied, updateUserRole) are system-wide and
// belong only on the global audit log. Management events that DO attach a
// resource (restoreBackup with per-DC resourceIDs, exportSubgraph, resolveDivergence)
// match via HasResourcesWith — see _IncludesResource subtest.
func TestEventList_OrbIdFilterExcludesUnrelatedManagementEvents(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "mgmt-filter-test", []string{"updateServer"}, []string{"Server"}, []string{"alaska:SRV-MGMT"}, "data")
	createEvent(t, "mgmt-filter-test", []string{"createBackup"}, nil, nil, "management")                                             // system-wide, no resources
	createEvent(t, "mgmt-filter-test", []string{"authorizationDenied"}, nil, nil, "management")                                      // system-wide, no resources
	createEvent(t, "mgmt-filter-test", []string{"restoreBackup"}, []string{"DataCenter"}, []string{"alaska:DC-OTHER"}, "management") // affects different DC
	createEvent(t, "mgmt-filter-test", []string{"updateServer"}, []string{"Server"}, []string{"alaska:SRV-OTHER"}, "data")
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
	if len(events) != 1 {
		t.Fatalf("expected 1 event (data event for alaska:SRV-MGMT only), got %d: %v", len(events), body)
	}
	m := events[0].(map[string]any)
	ids, _ := m["resourceIds"].([]any)
	if len(ids) == 0 || ids[0].(string) != "alaska:SRV-MGMT" {
		t.Errorf("resourceIds: got %v, want [alaska:SRV-MGMT]", ids)
	}
}

// TestEventList_OrbIdFilterIncludesResourcedManagementEvents verifies that
// management events that DO attach a resourceID (restoreBackup with per-DC
// resourceIDs, exportSubgraph, resolveDivergence) DO appear on that resource's
// audit panel. The HasResourcesWith match is the only inclusion criterion.
func TestEventList_OrbIdFilterIncludesResourcedManagementEvents(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "mgmt-resourced-test", []string{"restoreBackup"}, []string{"DataCenter"}, []string{"alaska:DC-A", "alaska:DC-B"}, "management")
	createEvent(t, "mgmt-resourced-test", []string{"exportSubgraph"}, []string{"DataCenter"}, []string{"alaska:DC-A"}, "management")
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"orbId": "alaska:DC-A"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("expected 2 management events (restoreBackup + exportSubgraph) for alaska:DC-A, got %d", len(events))
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

// TestEventList_FilterByNamespace verifies orb_id LIKE '<ns>:%' prefix
// matching. Underlying use case: publish-changes panel needs "every event
// under DC X" without enumerating every server/idrac/cluster orbId.
func TestEventList_FilterByNamespace(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	createEvent(t, "ns-test", []string{"updateServer"}, []string{"Server"}, []string{"colo:srv-01"}, "data")
	createEvent(t, "ns-test", []string{"updateIdrac"}, []string{"IdracSettings"}, []string{"colo:srv-01-idrac"}, "data")
	createEvent(t, "ns-test", []string{"updateServer"}, []string{"Server"}, []string{"aws:srv-99"}, "data")
	createEvent(t, "ns-test", []string{"loginSuccess"}, nil, nil, "auth")
	t.Cleanup(func() { clearEvents(ctx) })

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"namespace": "colo"})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("expected 2 colo events (server + idrac); got %d: %+v", len(events), events)
	}
	for _, ev := range events {
		m := ev.(map[string]any)
		ids, _ := m["resourceIds"].([]any)
		if len(ids) == 0 {
			t.Errorf("event missing resourceIds: %v", m)
			continue
		}
		if !strings.HasPrefix(ids[0].(string), "colo:") {
			t.Errorf("non-colo event leaked into namespace filter: %v", ids)
		}
	}
}

// TestEventList_FilterByTimestampWindow verifies `since` (exclusive) and
// `until` (inclusive). Consecutive windows [t0..t1] and (t1..t2] must not
// double-count the event at t1.
func TestEventList_FilterByTimestampWindow(t *testing.T) {
	ctx := context.Background()
	clearEvents(ctx)

	base := time.Now().UTC().Truncate(time.Second)
	for i, off := range []time.Duration{-3 * time.Minute, -2 * time.Minute, -1 * time.Minute, 0} {
		ev := testDB.Event.Create().
			SetActor("ts-test").
			SetOperations([]string{"op"}).
			SetTimestamp(base.Add(off)).
			SetEventCategory("data").
			SaveX(ctx)
		testDB.EventResource.Create().SetOrbID("ns:ts-" + strconv.Itoa(i)).SetEventID(ev.ID).ExecX(ctx)
	}
	t.Cleanup(func() { clearEvents(ctx) })

	// Window: (-2m, -1m] → only the event at -1m is included.
	since := base.Add(-2 * time.Minute).Format(time.RFC3339)
	until := base.Add(-1 * time.Minute).Format(time.RFC3339)

	h := newEventHandler(t)
	c, rec := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{
		"since": since,
		"until": until,
	})
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	events, _ := body["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("expected 1 event in (%s, %s], got %d", since, until, len(events))
	}
	m := events[0].(map[string]any)
	ids, _ := m["resourceIds"].([]any)
	if len(ids) == 0 || ids[0].(string) != "ns:ts-2" {
		t.Errorf("wrong event in window; got %v, want ns:ts-2", ids)
	}
}

func TestEventList_FilterByTimestamp_MalformedReturns400(t *testing.T) {
	h := newEventHandler(t)
	c, _ := eventCtx(http.MethodGet, "/api/v1/audit-log", map[string]string{"since": "not-a-timestamp"})
	err := h.List(c)
	if err == nil {
		t.Fatal("expected 400 for malformed since; got nil")
	}
	if he, ok := err.(*echo.HTTPError); !ok || he.Code != http.StatusBadRequest {
		t.Errorf("expected *echo.HTTPError 400, got %T %v", err, err)
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
