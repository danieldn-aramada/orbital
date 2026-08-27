//go:build integration

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/auditevent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

func createTestUser(t *testing.T, email string, role user.Role) int {
	t.Helper()
	u, err := testDB.User.Create().
		SetEmail(email).
		SetName(email).
		SetPreferredUsername(email).
		SetVerified(true).
		SetRole(role).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	t.Cleanup(func() { testDB.User.DeleteOneID(u.ID).Exec(context.Background()) }) //nolint:errcheck
	return u.ID
}

func TestRequireAdmin_ReadonlyUser_Forbidden(t *testing.T) {
	userID := createTestUser(t, "readonly@authztest.com", user.RoleReadonly)

	e := echo.New()
	mw := handler.RequireAdmin(testDB)
	h := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "reached")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", userID)

	err := h(c)
	if err == nil {
		t.Fatal("expected error for readonly user on POST, got nil")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusForbidden {
		t.Errorf("expected 403 HTTPError, got: %v", err)
	}
}

func TestRequireAdmin_AdminUser_Allowed(t *testing.T) {
	userID := createTestUser(t, "admin-allowed@authztest.com", user.RoleAdmin)

	e := echo.New()
	called := false
	mw := handler.RequireAdmin(testDB)
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "reached")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", userID)

	if err := h(c); err != nil {
		t.Fatalf("unexpected error for admin user: %v", err)
	}
	if !called {
		t.Error("handler should have been called for admin user")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequireAdmin_UnknownUserID_Forbidden(t *testing.T) {
	e := echo.New()
	mw := handler.RequireAdmin(testDB)
	h := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "should not reach")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 99999999) // non-existent ID

	err := h(c)
	if err == nil {
		t.Fatal("expected error for unknown user ID, got nil")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusForbidden {
		t.Errorf("expected 403 HTTPError, got: %v", err)
	}
}

// TestRequireRole_Denial_WritesAuditEvent verifies that a role denial is recorded
// as an authorizationDenied audit event in the database.
func TestRequireRole_Denial_WritesAuditEvent(t *testing.T) {
	const email = "readonly-denied@authztest.com"
	userID := createTestUser(t, email, user.RoleReadonly)
	ctx := context.Background()
	t.Cleanup(func() { testDB.AuditEvent.Delete().Where(auditevent.Actor(email)).ExecX(ctx) })

	e := echo.New()
	mw := handler.RequireAdmin(testDB)
	h := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "reached")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/1/role", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", userID)

	err := h(c)
	if err == nil {
		t.Fatal("expected 403 error")
	}

	time.Sleep(50 * time.Millisecond)

	ev, dbErr := testDB.AuditEvent.Query().Where(auditevent.Actor(email)).Only(ctx)
	if dbErr != nil {
		t.Fatalf("audit event not found: %v", dbErr)
	}
	if len(ev.Operations) == 0 || ev.Operations[0] != "authorizationDenied" {
		t.Errorf("expected operation authorizationDenied, got %v", ev.Operations)
	}
	if ev.EventCategory != "management" {
		t.Errorf("expected category management, got %q", ev.EventCategory)
	}
}

// ── ResolveUser ───────────────────────────────────────────────────────────────

// TestResolveUser_SkipsWhenUserIDAlreadySet verifies that when user_id is already
// set (session auth path), ResolveUser is a no-op and does not query the DB.
func TestResolveUser_SkipsWhenUserIDAlreadySet(t *testing.T) {
	userID := createTestUser(t, "session-user@resolvetest.com", user.RoleReadonly)

	e := echo.New()
	called := false
	mw := handler.ResolveUser(testDB, nil)
	h := mw(func(c echo.Context) error {
		called = true
		// user_id must remain unchanged
		if id, _ := c.Get("user_id").(int); id != userID {
			t.Errorf("expected user_id %d unchanged, got %d", userID, id)
		}
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", userID)

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler should have been called")
	}
}

// TestResolveUser_ExistingUser_SetsUserID verifies that a known email (set by bearer
// token auth) is resolved to an existing user row and user_id is set on context.
func TestResolveUser_ExistingUser_SetsUserID(t *testing.T) {
	const email = "bearer-existing@resolvetest.com"
	userID := createTestUser(t, email, user.RoleDev)

	e := echo.New()
	var resolvedID int
	mw := handler.ResolveUser(testDB, nil)
	h := mw(func(c echo.Context) error {
		resolvedID, _ = c.Get("user_id").(int)
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)
	c.Set("user_email", email)

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolvedID != userID {
		t.Errorf("expected user_id %d, got %d", userID, resolvedID)
	}
}

// TestResolveUser_NewUser_ProvisionedWithCorrectRole verifies that an unknown email
// (first bearer token call) is provisioned in the DB with the correct role and
// user_id is set on context.
func TestResolveUser_NewUser_ProvisionedWithCorrectRole(t *testing.T) {
	const email = "bearer-new@resolvetest.com"
	ctx := context.Background()
	t.Cleanup(func() { testDB.User.Delete().Where(user.Email(email)).ExecX(ctx) })

	adminEmails := map[string]struct{}{email: {}}

	e := echo.New()
	var resolvedID int
	mw := handler.ResolveUser(testDB, adminEmails)
	h := mw(func(c echo.Context) error {
		resolvedID, _ = c.Get("user_id").(int)
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)
	c.Set("user_email", email)

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolvedID == 0 {
		t.Fatal("expected user_id to be set after provisioning")
	}

	u, err := testDB.User.Get(ctx, resolvedID)
	if err != nil {
		t.Fatalf("provisioned user not found: %v", err)
	}
	if u.Role != user.RoleAdmin {
		t.Errorf("expected admin role for admin email, got %q", u.Role)
	}
}

// TestResolveUser_NoEmail_Returns401 verifies that a missing email (bearer token
// with no email claim) results in 401.
func TestResolveUser_NoEmail_Returns401(t *testing.T) {
	e := echo.New()
	called := false
	mw := handler.ResolveUser(testDB, nil)
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "should not reach")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)
	// user_email not set

	renderErr(c, h(c))
	if called {
		t.Error("handler must not be called when email is missing")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// ── App-principal authorization (ADR 010 §App Caller Authorization) ──────────

// TestRequireRole_AppPrincipal_DevMinRole_Allowed verifies the MVP policy: an
// app-only caller (BearerVerifier set user_name="app:<appid>", user_id=0) passes
// RequireRole(dev) without a users-table row. The allowlist gate happened in
// BearerVerifier; RequireRole grants dev-equivalent access.
func TestRequireRole_AppPrincipal_DevMinRole_Allowed(t *testing.T) {
	e := echo.New()
	mw := handler.RequireRole(testDB, user.RoleDev)
	called := false
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)
	c.Set("user_name", auth.AppPrincipalPrefix+"5fc832f6-843e-4207-93dd-b3c3a77c06f2")

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler should have been called for app principal on dev route")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// TestRequireRole_AppPrincipal_AdminMinRole_Forbidden verifies that the MVP
// dev-equivalent policy denies app callers on admin-gated routes — the inverse
// of the path above. When App Roles land (post-MVP), the `roles` claim will
// gate this instead.
func TestRequireRole_AppPrincipal_AdminMinRole_Forbidden(t *testing.T) {
	e := echo.New()
	mw := handler.RequireAdmin(testDB)
	called := false
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "should not reach")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/1/role", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)
	c.Set("user_name", auth.AppPrincipalPrefix+"5fc832f6-843e-4207-93dd-b3c3a77c06f2")

	err := h(c)
	if err == nil {
		t.Fatal("expected 403 error, got nil")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusForbidden {
		t.Errorf("expected 403 HTTPError, got: %v", err)
	}
	if called {
		t.Error("handler must not be called when app caller is below admin")
	}
}

// TestRequireRole_AppPrincipal_GetPassesThrough verifies the existing GET
// pass-through still applies for app principals (sanity check that the new
// branch doesn't break the read path).
func TestRequireRole_AppPrincipal_GetPassesThrough(t *testing.T) {
	e := echo.New()
	mw := handler.RequireAdmin(testDB)
	called := false
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)
	c.Set("user_name", auth.AppPrincipalPrefix+"5fc832f6-843e-4207-93dd-b3c3a77c06f2")

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("GET request from app principal should pass through")
	}
}

// TestResolveUser_AppPrincipal_NoDBLookup verifies that with a real DB attached,
// an app principal still bypasses the users-table lookup and does NOT get a
// provisioned row (no fake `app:...@something` user appears in the table).
func TestResolveUser_AppPrincipal_NoDBLookup(t *testing.T) {
	const appID = "5fc832f6-843e-4207-93dd-b3c3a77c06f2"
	ctx := context.Background()

	e := echo.New()
	mw := handler.ResolveUser(testDB, nil)
	called := false
	h := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/graphql", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", 0)
	c.Set("user_name", auth.AppPrincipalPrefix+appID)
	c.Set("user_email", "")

	if err := h(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler should have been called for app principal")
	}
	if userID, _ := c.Get("user_id").(int); userID != 0 {
		t.Errorf("app principal must not get a user_id; got %d", userID)
	}
	// Sanity check: confirm no user row was provisioned.
	cnt, err := testDB.User.Query().Where(user.NameContains(appID)).Count(ctx)
	if err != nil {
		t.Fatalf("user count query: %v", err)
	}
	if cnt != 0 {
		t.Errorf("expected 0 user rows containing appid, got %d (provisioning leak)", cnt)
	}
}

// ── ReconcileAdminEmails ──────────────────────────────────────────────────────

// TestReconcileAdminEmails_ReadonlyPromotedToAdmin verifies that a readonly user listed in
// adminEmails is promoted to admin and an audit event is written.
func TestReconcileAdminEmails_ReadonlyPromotedToAdmin(t *testing.T) {
	const email = "reconcile-readonly@authztest.com"
	ctx := context.Background()
	userID := createTestUser(t, email, user.RoleReadonly)
	t.Cleanup(func() { testDB.AuditEvent.Delete().Where(auditevent.Actor("system:adminEmailsConfig")).ExecX(ctx) })

	handler.ReconcileAdminEmails(ctx, testDB, map[string]struct{}{email: {}}, nil)

	fromDB, err := testDB.User.Get(ctx, userID)
	if err != nil {
		t.Fatalf("re-fetch user: %v", err)
	}
	if fromDB.Role != user.RoleAdmin {
		t.Errorf("expected admin role after reconcile, got %q", fromDB.Role)
	}

	time.Sleep(50 * time.Millisecond)
	ev, err := testDB.AuditEvent.Query().Where(auditevent.Actor("system:adminEmailsConfig")).Only(ctx)
	if err != nil {
		t.Fatalf("audit event not found: %v", err)
	}
	if len(ev.Operations) == 0 || ev.Operations[0] != "updateUserRole" {
		t.Errorf("expected updateUserRole audit event, got %v", ev.Operations)
	}
}

// TestReconcileAdminEmails_DevPromotedToAdmin verifies that a dev user is also promoted.
func TestReconcileAdminEmails_DevPromotedToAdmin(t *testing.T) {
	const email = "reconcile-dev@authztest.com"
	ctx := context.Background()
	userID := createTestUser(t, email, user.RoleDev)

	handler.ReconcileAdminEmails(ctx, testDB, map[string]struct{}{email: {}}, nil)

	fromDB, err := testDB.User.Get(ctx, userID)
	if err != nil {
		t.Fatalf("re-fetch user: %v", err)
	}
	if fromDB.Role != user.RoleAdmin {
		t.Errorf("expected admin role after reconcile, got %q", fromDB.Role)
	}
}

// TestReconcileAdminEmails_AdminNoOp verifies that an already-admin user is unchanged.
func TestReconcileAdminEmails_AdminNoOp(t *testing.T) {
	const email = "reconcile-admin@authztest.com"
	ctx := context.Background()
	userID := createTestUser(t, email, user.RoleAdmin)

	handler.ReconcileAdminEmails(ctx, testDB, map[string]struct{}{email: {}}, nil)

	fromDB, err := testDB.User.Get(ctx, userID)
	if err != nil {
		t.Fatalf("re-fetch user: %v", err)
	}
	if fromDB.Role != user.RoleAdmin {
		t.Errorf("expected role unchanged (admin), got %q", fromDB.Role)
	}
}

// TestReconcileAdminEmails_UnknownEmailSkipped verifies that an email not in the DB
// is silently skipped — it will get the correct role on first provision via RoleForEmail.
func TestReconcileAdminEmails_UnknownEmailSkipped(t *testing.T) {
	ctx := context.Background()
	// Should not panic or error even with an email that has no user row.
	handler.ReconcileAdminEmails(ctx, testDB, map[string]struct{}{"nobody-yet@authztest.com": {}}, nil)
}

// TestDeviceCodePoll_AdminEmail_SetsAdminRole verifies that a user whose email is in
// ORBITAL_ADMIN_EMAILS is provisioned with the admin role on first device code login.
func TestDeviceCodePoll_AdminEmail_SetsAdminRole(t *testing.T) {
	t.Chdir("../..") // template file paths are relative to the repo root

	const testEmail = "device-admin@authztest.com"
	ctx := context.Background()

	t.Cleanup(func() { testDB.User.Delete().Where(user.Email(testEmail)).ExecX(ctx) })

	p := newOIDCProvider(t)
	p.TokenClaims["email"] = testEmail
	p.TokenClaims["preferred_username"] = testEmail
	p.TokenClaims["name"] = "Device Admin"

	adminEmails := map[string]struct{}{testEmail: {}}

	h, err := handler.NewOIDC(
		ctx,
		testDB,
		oidcSessionKeys,
		p.Server.URL,
		"test-client-id",
		"test-client-secret",
		p.Server.URL+"/callback",
		"",
		nil,
		adminEmails,
		true, // deviceCodeEnabled
	)
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/device/poll", strings.NewReader(`{"device_code":"test-device-code"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.DeviceCodePoll(c); err != nil {
		t.Fatalf("DeviceCodePoll: %v", err)
	}

	u, err := testDB.User.Query().Where(user.Email(testEmail)).Only(ctx)
	if err != nil {
		t.Fatalf("user not found after provisioning: %v", err)
	}
	if u.Role != user.RoleAdmin {
		t.Errorf("expected admin role for %q, got %q", testEmail, u.Role)
	}
}

// TestDeviceCodePoll_RegularEmail_SetsReadonlyRole verifies that a user whose email is NOT
// in ORBITAL_ADMIN_EMAILS is provisioned with the readonly role.
func TestDeviceCodePoll_RegularEmail_SetsReadonlyRole(t *testing.T) {
	t.Chdir("../..") // template file paths are relative to the repo root

	const testEmail = "device-readonly@authztest.com"
	ctx := context.Background()

	t.Cleanup(func() { testDB.User.Delete().Where(user.Email(testEmail)).ExecX(ctx) })

	p := newOIDCProvider(t)
	p.TokenClaims["email"] = testEmail
	p.TokenClaims["preferred_username"] = testEmail
	p.TokenClaims["name"] = "Device Readonly"

	h, err := handler.NewOIDC(
		ctx,
		testDB,
		oidcSessionKeys,
		p.Server.URL,
		"test-client-id",
		"test-client-secret",
		p.Server.URL+"/callback",
		"",
		nil,
		nil, // no admin emails
		true,
	)
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/device/poll", strings.NewReader(`{"device_code":"test-device-code"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.DeviceCodePoll(c); err != nil {
		t.Fatalf("DeviceCodePoll: %v", err)
	}

	u, err := testDB.User.Query().Where(user.Email(testEmail)).Only(ctx)
	if err != nil {
		t.Fatalf("user not found after provisioning: %v", err)
	}
	if u.Role != user.RoleReadonly {
		t.Errorf("expected readonly role for %q, got %q", testEmail, u.Role)
	}
}
