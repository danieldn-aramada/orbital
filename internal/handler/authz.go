package handler

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
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
			userID, _ := c.Get("user_id").(int)
			if userID == 0 {
				slog.Default().Warn("authorization denied",
					"actor", "",
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"reason", "unauthenticated",
				)
				return echo.ErrForbidden
			}
			u, err := db.User.Get(c.Request().Context(), userID)
			if err != nil {
				slog.Default().Warn("authorization denied",
					"actor", "",
					"method", c.Request().Method,
					"uri", c.Request().URL.Path,
					"reason", "user_not_found",
				)
				return echo.ErrForbidden
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
				return echo.ErrForbidden
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
