package handler

import "github.com/labstack/echo/v4"

// actorFromContext extracts the canonical identity string from an Echo request
// context for use in audit records and "created by" fields.
//
// Email is preferred over display name because it is stable — a user's display
// name can change in Azure AD without their email changing, which would silently
// corrupt historical audit trails.
//
// Falls back to display name when email is absent. Three legitimate cases for
// the fallback:
//   - Local dev sessions that omit email
//   - App-only (client-credentials) bearer tokens — the bearer verifier sets
//     user_email="" and user_name="app:<appid>" so service callers are
//     attributable in audit logs (see auth.AppPrincipalPrefix + ADR 010)
//   - Test contexts that only populate user_name
//
// Returns empty string when neither is set (unauthenticated or unmiddlewared).
func actorFromContext(c echo.Context) string {
	if email, _ := c.Get("user_email").(string); email != "" {
		return email
	}
	name, _ := c.Get("user_name").(string)
	return name
}
