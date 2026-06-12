//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
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
		SetLastSnapshotPublishedAt(time.Now().UTC()).
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

// newAcceptRequest builds the echo.Context for POST /api/v1/divergence/:id/accept
// authenticated as the given admin user.
func newAcceptRequest(t *testing.T, entryID string, adminID int, actor string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence/"+entryID+"/accept", nil)
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

	c, _ := newAcceptRequest(t, entryID, adminID, "accept-empty-type@test.com")
	err := h.Accept(c)
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

	c, _ := newAcceptRequest(t, entryID, adminID, "accept-success@test.com")
	if err := h.Accept(c); err != nil {
		t.Fatalf("Accept failed: %v", err)
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

	c, _ := newAcceptRequest(t, entryID, adminID, "accept-fail@test.com")
	err := h.Accept(c)
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

