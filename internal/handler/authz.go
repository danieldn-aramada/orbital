package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/labstack/echo/v4"
)

// roleLevel defines the linear ordering of roles: readonly < dev < admin.
var roleLevel = map[user.Role]int{
	user.RoleReadonly: 0,
	user.RoleDev:      1,
	user.RoleAdmin:    2,
}

// RoleAtLeast returns true if actual role is >= minimum role in the hierarchy.
func RoleAtLeast(actual, minimum user.Role) bool {
	return roleLevel[actual] >= roleLevel[minimum]
}

// RoleForEmail returns admin if email is in adminEmails, otherwise readonly.
func RoleForEmail(email string, adminEmails map[string]struct{}) user.Role {
	if _, ok := adminEmails[email]; ok {
		return user.RoleAdmin
	}
	return user.RoleReadonly
}

// ReconcileAdminEmails promotes every existing user whose email appears in
// adminEmails to admin if their current role is below admin. Called once at
// startup after migrations so that changes to ORBITAL_ADMIN_EMAILS take effect
// on the next deploy — no per-login check required. Users not yet in the DB are
// skipped; RoleForEmail handles the correct role on their first login.
func ReconcileAdminEmails(ctx context.Context, db *ent.Client, adminEmails map[string]struct{}, logger *slog.Logger) {
	if db == nil || len(adminEmails) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	for email := range adminEmails {
		u, err := db.User.Query().Where(user.Email(email)).Only(ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			logger.Warn("ReconcileAdminEmails: query user", "email", email, "err", err)
			continue
		}
		if RoleAtLeast(u.Role, user.RoleAdmin) {
			continue
		}
		prevRole := u.Role
		if _, err := db.User.UpdateOneID(u.ID).SetRole(user.RoleAdmin).Save(ctx); err != nil {
			logger.Warn("ReconcileAdminEmails: promote user", "email", email, "err", err)
			continue
		}
		logger.Info("promoted user to admin via ORBITAL_ADMIN_EMAILS", "email", email, "previous_role", string(prevRole))
		writeAuditEvent(db, logger, "management", "system:adminEmailsConfig", "updateUserRole",
			[]string{"updateUserRole"},
			[]string{},
			[]string{},
			map[string]any{
				"userEmail":    email,
				"previousRole": string(prevRole),
				"newRole":      "admin",
				"reason":       "ORBITAL_ADMIN_EMAILS reconciliation on startup",
			},
		)
	}
}

// ResolveUser is an Echo middleware that resolves a PostgreSQL user ID from the
// email already set on the context by a preceding auth middleware (e.g. bearer
// token verification). It is a no-op when user_id is already set (session auth).
//
// If the email has no matching user row, ResolveUser provisions a new one with
// the role determined by adminEmails — mirroring the OIDC login provisioning path.
// This ensures that service accounts and CI callers using bearer tokens are first-
// class users in the local role table.
func ResolveUser(db *ent.Client, adminEmails map[string]struct{}) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if id, _ := c.Get("user_id").(int); id != 0 {
				return next(c) // session auth already resolved
			}
			// App-only (client credentials) tokens have no email and no users-table
			// row, by design — the appid allowlist in BearerVerifier is the authz
			// gate. Pass through without a DB lookup. See ADR 010.
			if name, _ := c.Get("user_name").(string); strings.HasPrefix(name, auth.AppPrincipalPrefix) {
				return next(c)
			}
			email, _ := c.Get("user_email").(string)
			email = strings.ToLower(strings.TrimSpace(email))
			if email == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			}
			ctx := c.Request().Context()
			u, err := db.User.Query().Where(user.Email(email)).Only(ctx)
			if err != nil {
				// Provision on first bearer login, same as OIDC flow.
				u, err = db.User.Create().
					SetEmail(email).
					SetName(email).
					SetPreferredUsername(email).
					SetVerified(true).
					SetRole(RoleForEmail(email, adminEmails)).
					Save(ctx)
				if err != nil {
					slog.Default().Warn("ResolveUser: failed to provision user", "email", email, "err", err)
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				}
			}
			c.Set("user_id", u.ID)
			return next(c)
		}
	}
}

// RequireRole returns an Echo middleware that enforces the minimum role on mutating
// HTTP methods (POST, PUT, PATCH, DELETE). GET, HEAD, and OPTIONS pass through.
// If db is nil (dev mode without DB), all requests pass through.
// Authorization denials are logged (slog.Warn) and recorded as audit events.
func RequireRole(db *ent.Client, minRole user.Role) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}
			if db == nil {
				return next(c)
			}
			// App-only (client credentials) callers were authenticated by the
			// BearerVerifier allowlist (ORBITAL_APP_TOKEN_ALLOWED_APPIDS). MVP
			// policy: any allowlist-passed app caller is treated as `dev`-
			// equivalent — sufficient for all mutating API routes today. Future
			// best practice: check the `roles` claim against required role using
			// Microsoft Entra App Roles, once Application Administrator perms
			// allow defining them. See ADR 010 §App Caller Authorization.
			if name, _ := c.Get("user_name").(string); strings.HasPrefix(name, auth.AppPrincipalPrefix) {
				if RoleAtLeast(user.RoleDev, minRole) {
					return next(c)
				}
				slog.Default().Warn("authorization denied",
					"actor", name,
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"required_role", string(minRole),
					"reason", "app_caller_below_required_role",
				)
				return echo.ErrForbidden
			}
			userID, _ := c.Get("user_id").(int)
			if userID == 0 {
				slog.Default().Warn("authorization denied",
					"actor", "",
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"reason", "unauthenticated",
				)
				return echo.NewHTTPError(http.StatusForbidden, "not authenticated — sign in and retry")
			}
			u, err := db.User.Get(c.Request().Context(), userID)
			if err != nil {
				slog.Default().Warn("authorization denied",
					"actor", "",
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"reason", "user_not_found",
				)
				return echo.NewHTTPError(http.StatusForbidden, "user record not found — sign in and retry")
			}
			if !RoleAtLeast(u.Role, minRole) {
				slog.Default().Warn("authorization denied",
					"actor", u.Email,
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"required_role", string(minRole),
					"user_role", string(u.Role),
				)
				writeAuditEvent(db, slog.Default(), "management", u.Email, "authorizationDenied",
					[]string{"authorizationDenied"},
					[]string{},
					[]string{},
					map[string]any{
						"method":       c.Request().Method,
						"uri":          c.Request().URL.Path,
						"requiredRole": string(minRole),
						"userRole":     string(u.Role),
					},
				)
				return echo.NewHTTPError(http.StatusForbidden, fmt.Sprintf("role %q is below required %q for this action", u.Role, minRole))
			}
			return next(c)
		}
	}
}

// RequireAdmin is a convenience wrapper for RequireRole with admin as the minimum.
// Kept for backwards compatibility with existing tests and call sites.
func RequireAdmin(db *ent.Client) echo.MiddlewareFunc {
	return RequireRole(db, user.RoleAdmin)
}
