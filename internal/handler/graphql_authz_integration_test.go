//go:build integration

package handler_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

// These tests cover the handler-level authorization boundary on the GraphQL
// endpoint. server.go exposes a single /graphql with no route-level
// RequireRole — mutation authz is enforced inside graphql.go via isMutation +
// RoleAtLeast(role, RoleDev).
//
// They invoke Handle directly with a pre-populated user_id context, bypassing
// the route middleware. Route-level RequireAuth is exercised separately in
// authz_integration_test.go.

func newAuthzGQLContext(t *testing.T, userID int, body string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("user_id", userID)
	return c, rec
}

func mockDGraphSuccess(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"queryDataCenter":[{"id":"dc1","name":"alaska"}]}}`))
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGraphQL_ReadonlyCanQuery(t *testing.T) {
	userID := createTestUser(t, "readonly-gql@authztest.com", user.RoleReadonly)
	srv := mockDGraphSuccess(t)
	h := handler.NewGraphQL(srv.URL, testDB, slog.Default(), false)

	c, rec := newAuthzGQLContext(t, userID, `{"query":"{ queryDataCenter { id name } }"}`)
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("readonly query should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGraphQL_ReadonlyMutationBlocked(t *testing.T) {
	userID := createTestUser(t, "readonly-mut@authztest.com", user.RoleReadonly)
	srv := mockDGraphSuccess(t)
	h := handler.NewGraphQL(srv.URL, testDB, slog.Default(), false)

	c, rec := newAuthzGQLContext(t, userID, `{"query":"mutation { addServer(input:[]) { server { id } } }"}`)
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("readonly mutation should be 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "dev or admin") {
		t.Errorf("403 body should explain role requirement, got: %s", rec.Body.String())
	}
	// The machine code is the client contract — a typo in CodeForbidden would
	// break callers that branch on it while the human message still reads fine.
	if !strings.Contains(rec.Body.String(), `"code":"FORBIDDEN"`) {
		t.Errorf("403 body should carry code FORBIDDEN, got: %s", rec.Body.String())
	}
}

func TestGraphQL_ReadonlyMutationBypassAttemptBlocked(t *testing.T) {
	// Regression: this is the comment-smuggling bypass the old isMutation
	// (prefix check only) would have missed. After the strengthening, the
	// readonly user is correctly blocked.
	userID := createTestUser(t, "readonly-bypass@authztest.com", user.RoleReadonly)
	srv := mockDGraphSuccess(t)
	h := handler.NewGraphQL(srv.URL, testDB, slog.Default(), false)

	body := `{"query":"# looks like a query\nmutation Bar { addServer(input:[]) { server { id } } }"}`
	c, rec := newAuthzGQLContext(t, userID, body)
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("comment-smuggled mutation should be 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGraphQL_DevCanMutate(t *testing.T) {
	userID := createTestUser(t, "dev-gql@authztest.com", user.RoleDev)
	// Mock DGraph to accept mutations.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"addServer":{"server":[{"id":"s1"}]}}}`))
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	t.Cleanup(srv.Close)

	h := handler.NewGraphQL(srv.URL, testDB, slog.Default(), false)
	c, rec := newAuthzGQLContext(t, userID, `{"query":"mutation { addServer(input:[]) { server { id } } }"}`)
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("dev mutation should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGraphQL_AdminCanMutate(t *testing.T) {
	userID := createTestUser(t, "admin-gql@authztest.com", user.RoleAdmin)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"updateDataCenter":{"dataCenter":[{"id":"dc1"}]}}}`))
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	t.Cleanup(srv.Close)

	h := handler.NewGraphQL(srv.URL, testDB, slog.Default(), false)
	c, rec := newAuthzGQLContext(t, userID, `{"query":"mutation { updateDataCenter(input:{}) { dataCenter { id } } }"}`)
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("admin mutation should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGraphQL_UnauthenticatedMutationBlocked(t *testing.T) {
	// user_id == 0 simulates a request that bypassed the route middleware
	// (or pre-resolution). The handler's defensive check at graphql.go:93-96
	// should still reject mutations.
	srv := mockDGraphSuccess(t)
	h := handler.NewGraphQL(srv.URL, testDB, slog.Default(), false)

	c, rec := newAuthzGQLContext(t, 0, `{"query":"mutation { addServer(input:[]) { server { id } } }"}`)
	if err := h.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("unauthenticated mutation should be 403, got %d: %s", rec.Code, rec.Body.String())
	}
}
