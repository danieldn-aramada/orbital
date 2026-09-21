package auth

import "github.com/labstack/echo/v4"

// AppPrincipalPrefix labels a caller synthesized from an app-only
// (machine-to-machine) bearer token — a client-credentials caller with no human
// behind it, the in-pod cb-bundler being the worked example.
//
// DISPLAY ONLY. `actorFromContext` has no email to fall back on for such a
// caller, so the audit record reads `app:<azp>` rather than blank. Nothing
// branches on this string, and nothing may: a human's `user_name` comes from the
// token's unvalidated `name` claim, so anyone could set it. Whether a caller is
// an app principal is carried by MarkAppPrincipal/IsAppPrincipal below, which no
// token claim can reach. **Do NOT reintroduce a HasPrefix check** — that was the
// bug this replaced.
const AppPrincipalPrefix = "app:"

// principalKindKey holds the caller's principal kind on the request context.
// Unexported so the value cannot be written by spelling the key: it is set by
// MarkAppPrincipal, which only the bearer middleware calls.
const principalKindKey = "principal_kind"

const principalKindApp = "app"

// MarkAppPrincipal records that this request was authenticated as an app
// principal: a client-credentials caller with no human behind it, admitted by
// the provider entry whose clientID matched the token's azp.
//
// Exported for the bearer middleware and for tests that need to construct the
// state a verifier produces. It is only ever reachable from server-side code —
// a request cannot set context values.
func MarkAppPrincipal(c echo.Context) { c.Set(principalKindKey, principalKindApp) }

// IsAppPrincipal reports whether this request was authenticated as an app
// principal. The single definition of that question: ResolveUser uses it to skip
// provisioning (an app principal must never become a row in the users table) and
// RequireRole uses it to grant dev-equivalent access, and two copies of the test
// is how they would drift.
func IsAppPrincipal(c echo.Context) bool {
	kind, _ := c.Get(principalKindKey).(string)
	return kind == principalKindApp
}
