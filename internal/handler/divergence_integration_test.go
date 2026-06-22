//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/divergenceresolution"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

// seedDivergenceEntry inserts one DivergenceEntry row with the given typeName
// (empty string is allowed — exercises the legacy-fallback path) and returns
// the entry's UUID as a string.
func seedDivergenceEntry(t *testing.T, dcID, orbID, field, typeName string, overrideValue any) string {
	t.Helper()
	ctx := context.Background()
	intended, _ := json.Marshal(false)
	override, _ := json.Marshal(overrideValue)
	e, err := testDB.DivergenceEntry.Create().
		SetDcOrbID(dcID).
		SetEntryOrbID(orbID).
		SetField(field).
		SetTypeName(typeName).
		SetIntendedValue(intended).
		SetOverrideValue(override).
		SetWho("local:admin").
		SetFirstSeenAt(time.Now().UTC().Add(-2 * time.Hour)).
		SetLastSeenAt(time.Now().UTC()).
		SetLastReportPublishedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed divergence entry: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.DivergenceResolution.Delete().
			Where(divergenceresolution.EntryOrbID(orbID), divergenceresolution.Field(field)).
			Exec(ctx)
		_ = testDB.DivergenceEntry.DeleteOneID(e.ID).Exec(ctx)
	})
	return e.ID.String()
}

// newPutResolutionRequest builds the echo.Context for
// PUT /api/v1/divergences/:id/resolution with {"action": action} as the body,
// authenticated as the given admin user.
func newPutResolutionRequest(t *testing.T, entryID, action string, adminID int, actor string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	body := strings.NewReader(`{"action":"` + action + `"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/divergences/"+entryID+"/resolution", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(entryID)
	c.Set("user_id", adminID)
	c.Set("user_email", actor)
	return c, rec
}

func TestAccept_EmptyTypeReturns422(t *testing.T) {
	adminID := createTestUser(t, "accept-empty-type@test.com", user.RoleAdmin)
	entryID := seedDivergenceEntry(t, "colo:colo-galleon", "colo:legacy-srv", "sshEnabled", "", true)

	// Mock DGraph — should never be called when type is empty.
	dgraphCalled := false
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dgraphCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{}}`)) //nolint:errcheck
	}))
	defer dgraph.Close()
	gql := handler.NewGraphQL(dgraph.URL, testDB, slog.Default())
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	c, _ := newPutResolutionRequest(t, entryID, "accept", adminID, "accept-empty-type@test.com")
	err := h.PutResolution(c)
	if err == nil {
		t.Fatal("expected error for empty type, got nil")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %v", err)
	}
	if dgraphCalled {
		t.Error("DGraph was called for a missing-type entry; it shouldn't have been")
	}
	// Resolution must NOT have been recorded.
	count := testDB.DivergenceResolution.Query().
		Where(divergenceresolution.EntryOrbID("colo:legacy-srv"), divergenceresolution.Field("sshEnabled")).
		CountX(context.Background())
	if count != 0 {
		t.Errorf("expected 0 resolutions, got %d", count)
	}
}

func TestAccept_DispatchesMutationAndRecordsResolution(t *testing.T) {
	adminID := createTestUser(t, "accept-success@test.com", user.RoleAdmin)
	entryID := seedDivergenceEntry(t, "colo:colo-galleon", "colo:srv-001", "sshEnabled", "Server", true)

	// Mock DGraph returning a successful updateServer response.
	var receivedBody struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"updateServer":{"numUids":1}}}`)) //nolint:errcheck
	}))
	defer dgraph.Close()
	gql := handler.NewGraphQL(dgraph.URL, testDB, slog.Default())
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	c, _ := newPutResolutionRequest(t, entryID, "accept", adminID, "accept-success@test.com")
	if err := h.PutResolution(c); err != nil {
		t.Fatalf("PutResolution(accept) failed: %v", err)
	}

	// Sanity-check the dispatched mutation hits the right type, declares
	// {Type}Filter/{Type}Patch, and carries the override value as a variable.
	if receivedBody.Query == "" {
		t.Fatal("expected DGraph to be called with a mutation, got nothing")
	}
	for _, want := range []string{"updateServer", "ServerFilter!", "ServerPatch!"} {
		if !strings.Contains(receivedBody.Query, want) {
			t.Errorf("mutation missing %q; got: %s", want, receivedBody.Query)
		}
	}
	filter, _ := receivedBody.Variables["filter"].(map[string]any)
	orbIDFilter, _ := filter["orbId"].(map[string]any)
	if got, _ := orbIDFilter["eq"].(string); got != "colo:srv-001" {
		t.Errorf("variables.filter.orbId.eq: got %q, want %q", got, "colo:srv-001")
	}
	set, _ := receivedBody.Variables["set"].(map[string]any)
	if got, _ := set["sshEnabled"].(bool); got != true {
		t.Errorf("variables.set.sshEnabled: got %v, want true", set["sshEnabled"])
	}

	// Resolution must be recorded with action=accept.
	res := testDB.DivergenceResolution.Query().
		Where(divergenceresolution.EntryOrbID("colo:srv-001"), divergenceresolution.Field("sshEnabled")).
		OnlyX(context.Background())
	if res.Action != divergenceresolution.ActionAccept {
		t.Errorf("resolution action: got %v, want accept", res.Action)
	}
	if res.Actor != "accept-success@test.com" {
		t.Errorf("resolution actor: got %q, want accept-success@test.com", res.Actor)
	}
}

func TestAccept_MutationFailureLeavesNoResolution(t *testing.T) {
	adminID := createTestUser(t, "accept-fail@test.com", user.RoleAdmin)
	entryID := seedDivergenceEntry(t, "colo:colo-galleon", "colo:srv-002", "sshEnabled", "Server", true)

	// DGraph returns an error in the GraphQL `errors` array.
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"errors":[{"message":"resolver error: server not found"}]}`)) //nolint:errcheck
	}))
	defer dgraph.Close()
	gql := handler.NewGraphQL(dgraph.URL, testDB, slog.Default())
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	c, _ := newPutResolutionRequest(t, entryID, "accept", adminID, "accept-fail@test.com")
	err := h.PutResolution(c)
	if err == nil {
		t.Fatal("expected error when DGraph returns gql errors, got nil")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %v", err)
	}
	// Resolution row must NOT exist.
	count := testDB.DivergenceResolution.Query().
		Where(divergenceresolution.EntryOrbID("colo:srv-002"), divergenceresolution.Field("sshEnabled")).
		CountX(context.Background())
	if count != 0 {
		t.Errorf("expected no resolution after mutation failure, got %d", count)
	}
	// The DivergenceEntry itself stays put (not deleted) so the admin can retry.
	if !testDB.DivergenceEntry.Query().Where(divergenceentry.EntryOrbID("colo:srv-002")).ExistX(context.Background()) {
		t.Error("expected entry to still exist after failed Accept")
	}
}

// TestList_ActionFilter_PartitionsIgnoreFromAcceptReject pins the contract that
// cb-bundler relies on for the local:admin-ownership semantic:
//
//   - GET /api/v1/divergences?action=accept&action=reject returns ONLY accept
//     and reject rows (these become spec.takeover[] → cb-controller releases
//     local:admin's claim).
//   - GET /api/v1/divergences?action=ignore returns ONLY ignore rows (these
//     become Omissions → bundler nils the field → cb-controller does not
//     re-claim → local:admin retains ownership).
//
// Regression class: if a refactor of the action-filter logic let an Ignore row
// leak into the accept|reject result, cb-controller would release local:admin's
// claim on an Ignored field — silently violating "Ignore retains ownership."
// Not caught by clicking; only visible after a full bundle/apply cycle.
func TestList_ActionFilter_PartitionsIgnoreFromAcceptReject(t *testing.T) {
	ctx := context.Background()
	dc := "colo:colo-list-filter"

	// Seed one entry per action with a resolution row attached.
	type seed struct {
		orbID, field string
		action       divergenceresolution.Action
	}
	seeds := []seed{
		{"colo:srv-list-accept", "sshEnabled", divergenceresolution.ActionAccept},
		{"colo:srv-list-reject", "ipmiEnabled", divergenceresolution.ActionReject},
		{"colo:srv-list-ignore", "dhcpEnabled", divergenceresolution.ActionIgnore},
	}
	for _, s := range seeds {
		seedDivergenceEntry(t, dc, s.orbID, s.field, "IdracSettings", true)
		_, err := testDB.DivergenceResolution.Create().
			SetEntryOrbID(s.orbID).
			SetField(s.field).
			SetAction(s.action).
			SetActor("list-filter@test.com").
			SetDecidedAt(time.Now().UTC()).
			Save(ctx)
		if err != nil {
			t.Fatalf("seed %s resolution: %v", s.action, err)
		}
	}

	gql := handler.NewGraphQL("http://unused", testDB, slog.Default())
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	listForActions := func(t *testing.T, query string) map[string]bool {
		t.Helper()
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/divergences?"+query+"&dc="+dc, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := h.List(c); err != nil {
			t.Fatalf("List(%s): %v", query, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("List(%s): status %d", query, rec.Code)
		}
		var items []struct {
			EntryOrbID string `json:"entryOrbId"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatalf("decode (%s): %v", query, err)
		}
		got := map[string]bool{}
		for _, it := range items {
			got[it.EntryOrbID] = true
		}
		return got
	}

	// action=accept|reject — must include accept+reject, must exclude ignore.
	got := listForActions(t, "action=accept&action=reject")
	for _, want := range []string{"colo:srv-list-accept", "colo:srv-list-reject"} {
		if !got[want] {
			t.Errorf("action=accept|reject: missing %s in response %v", want, got)
		}
	}
	if got["colo:srv-list-ignore"] {
		t.Errorf("action=accept|reject MUST NOT include the Ignore row — would silently strip local:admin ownership. got %v", got)
	}

	// action=ignore — must include ignore, must exclude accept+reject.
	got = listForActions(t, "action=ignore")
	if !got["colo:srv-list-ignore"] {
		t.Errorf("action=ignore: missing the Ignore row, got %v", got)
	}
	for _, mustNot := range []string{"colo:srv-list-accept", "colo:srv-list-reject"} {
		if got[mustNot] {
			t.Errorf("action=ignore MUST NOT include accept/reject row %s, got %v", mustNot, got)
		}
	}
}

// TestList_ActionFilter_BatchAcceptAndRejectOnSameConfigItem pins that the
// List handler returns batched decisions on sibling fields of the same
// ConfigItem correctly. Under ADR 012, resolutions are not subject to any
// post-decision staleness check — anything in the divergence_resolutions table
// is by construction current (the ingester wipes resolutions on supersede).
// The test exists to lock the action-filter contract: each row appears with
// its own decision; sibling rows don't shadow each other.
func TestList_ActionFilter_BatchAcceptAndRejectOnSameConfigItem(t *testing.T) {
	ctx := context.Background()
	dc := "colo:colo-batch-mvcc"
	orbID := "colo:srv-batch-mvcc-idrac"

	// Reject sshEnabled (intent=false stays); Accept ipmiEnabled (intent → true).
	falseV, _ := json.Marshal(false)
	trueV, _ := json.Marshal(true)
	for _, s := range []struct {
		field  string
		action divergenceresolution.Action
	}{
		{"sshEnabled", divergenceresolution.ActionReject},
		{"ipmiEnabled", divergenceresolution.ActionAccept},
	} {
		e2, err := testDB.DivergenceEntry.Create().
			SetDcOrbID(dc).
			SetEntryOrbID(orbID).
			SetField(s.field).
			SetTypeName("IdracSettings").
			SetIntendedValue(falseV).
			SetOverrideValue(trueV).
			SetWho("local:admin").
			SetFirstSeenAt(time.Now().UTC().Add(-1 * time.Hour)).
			SetLastSeenAt(time.Now().UTC()).
			SetLastReportPublishedAt(time.Now().UTC()).
			Save(ctx)
		if err != nil {
			t.Fatalf("seed %s entry: %v", s.field, err)
		}
		t.Cleanup(func() {
			_, _ = testDB.DivergenceResolution.Delete().
				Where(divergenceresolution.EntryOrbID(orbID), divergenceresolution.Field(s.field)).
				Exec(ctx)
			_ = testDB.DivergenceEntry.DeleteOneID(e2.ID).Exec(ctx)
		})
		if _, err := testDB.DivergenceResolution.Create().
			SetEntryOrbID(orbID).
			SetField(s.field).
			SetAction(s.action).
			SetActor("admin@test.com").
			SetDecidedAt(time.Now().UTC()).
			Save(ctx); err != nil {
			t.Fatalf("seed %s resolution: %v", s.field, err)
		}
	}

	// Mock DGraph: ipmiEnabled=true (Accept worked), sshEnabled=false (Reject's
	// expected intent unchanged). Both should pass the per-field check.
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "sshEnabled"):
			w.Write([]byte(`{"data":{"getIdracSettings":{"sshEnabled":false}}}`)) //nolint:errcheck
		case strings.Contains(string(body), "ipmiEnabled"):
			w.Write([]byte(`{"data":{"getIdracSettings":{"ipmiEnabled":true}}}`)) //nolint:errcheck
		default:
			w.Write([]byte(`{"data":{"getIdracSettings":{}}}`)) //nolint:errcheck
		}
	}))
	defer dgraph.Close()

	gql := handler.NewGraphQL(dgraph.URL, testDB, slog.Default())
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/divergences?action=accept&action=reject&dc="+dc, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	var items []struct {
		Field      string `json:"field"`
		Resolution *struct {
			Action string `json:"action"`
		} `json:"resolution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Accept + Reject on sibling fields must both survive; got %d items: %v",
			len(items), items)
	}
	gotFields := map[string]string{}
	for _, it := range items {
		if it.Resolution == nil {
			t.Fatalf("item %s has no resolution", it.Field)
		}
		gotFields[it.Field] = it.Resolution.Action
	}
	if gotFields["sshEnabled"] != "reject" {
		t.Errorf("sshEnabled should be reject, got %q (would re-introduce the user-reported bug — sshEnabled lost when batched with sibling Accept)", gotFields["sshEnabled"])
	}
	if gotFields["ipmiEnabled"] != "accept" {
		t.Errorf("ipmiEnabled should be accept, got %q", gotFields["ipmiEnabled"])
	}
}

