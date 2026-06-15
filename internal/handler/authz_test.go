package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

func TestRoleForEmail(t *testing.T) {
	adminEmails := map[string]struct{}{
		"admin@example.com": {},
		"boss@example.com":  {},
	}

	cases := []struct {
		email string
		want  user.Role
	}{
		{"admin@example.com", user.RoleAdmin},
		{"boss@example.com", user.RoleAdmin},
		{"user@example.com", user.RoleReadonly},
		{"", user.RoleReadonly},
		// Lookup is case-sensitive; caller (NewOIDC provisioning) lowercases email first.
		{"ADMIN@EXAMPLE.COM", user.RoleReadonly},
	}

	for _, tc := range cases {
		got := handler.RoleForEmail(tc.email, adminEmails)
		if got != tc.want {
			t.Errorf("RoleForEmail(%q) = %q, want %q", tc.email, got, tc.want)
		}
	}
}

func TestRoleForEmail_NilMap(t *testing.T) {
	got := handler.RoleForEmail("anyone@example.com", nil)
	if got != user.RoleReadonly {
		t.Errorf("expected readonly for nil adminEmails, got %q", got)
	}
}

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		actual  user.Role
		minimum user.Role
		want    bool
	}{
		{user.RoleAdmin, user.RoleAdmin, true},
		{user.RoleAdmin, user.RoleDev, true},
		{user.RoleAdmin, user.RoleReadonly, true},
		{user.RoleDev, user.RoleDev, true},
		{user.RoleDev, user.RoleReadonly, true},
		{user.RoleDev, user.RoleAdmin, false},
		{user.RoleReadonly, user.RoleReadonly, true},
		{user.RoleReadonly, user.RoleDev, false},
		{user.RoleReadonly, user.RoleAdmin, false},
	}

	for _, tc := range cases {
		got := handler.RoleAtLeast(tc.actual, tc.minimum)
		if got != tc.want {
			t.Errorf("RoleAtLeast(%q, %q) = %v, want %v", tc.actual, tc.minimum, got, tc.want)
		}
	}
}

// TestRequireAdmin_GetPassesThrough verifies GET requests are never blocked regardless of role.
func TestRequireAdmin_GetPassesThrough(t *testing.T) {
	e := echo.New()
	called := false
	mw := handler.RequireAdmin(nil) // nil db = dev pass-through
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/export/jobs", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler should have been called for GET")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// TestRequireAdmin_NilDB_PassesThrough verifies that when no DB is available (dev mode),
// all methods pass through without enforcement.
func TestRequireAdmin_NilDB_PassesThrough(t *testing.T) {
	e := echo.New()
	mw := handler.RequireAdmin(nil)
	called := false
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		called = false
		req := httptest.NewRequest(method, "/api/v1/export", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h(c); err != nil {
			t.Fatalf("%s: unexpected error: %v", method, err)
		}
		if !called {
			t.Errorf("%s: handler should have been called when db is nil", method)
		}
	}
}

// TestResolveUser_AppPrincipal_PassesThroughWithoutDB verifies the ADR 010
// MVP policy: an app-only caller (set by BearerVerifier with
// user_name="app:<appid>", user_email="") skips the users-table lookup. The
// app-principal branch must execute before any DB use, so a nil db is safe.
func TestResolveUser_AppPrincipal_PassesThroughWithoutDB(t *testing.T) {
	e := echo.New()
	mw := handler.ResolveUser(nil, nil)
	called := false
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/graphql", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// Mirror what BearerVerifier sets for an app-only token.
	c.Set("user_id", 0)
	c.Set("user_name", auth.AppPrincipalPrefix+"5fc832f6-843e-4207-93dd-b3c3a77c06f2")
	c.Set("user_email", "")

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler should have been called for app principal")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if userID, _ := c.Get("user_id").(int); userID != 0 {
		t.Errorf("app principal should not get a user_id; got %d", userID)
	}
}

// TestResolveUser_EmptyEmail_NoAppPrincipal_Unauthorized verifies that a non-app
// caller with no email and no session is rejected — the bug that motivated ADR 010
// only applies to the app-principal path.
func TestResolveUser_EmptyEmail_NoAppPrincipal_Unauthorized(t *testing.T) {
	e := echo.New()
	mw := handler.ResolveUser(nil, nil)
	called := false
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/graphql", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)
	c.Set("user_email", "")
	// no user_name → not an app principal

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("handler should NOT have been called for empty-email non-app caller")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// TestRequireRole_DevAllowedOnDevRoute verifies a dev user passes a RequireRole(dev) middleware.
func TestRequireRole_DevAllowedOnDevRoute(t *testing.T) {
	e := echo.New()
	mw := handler.RequireRole(nil, user.RoleDev) // nil db = pass-through
	called := false
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler should have been called")
	}
}
