package auth

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// CSRFHeader carries the session's CSRF token on cookie-authenticated API calls.
const CSRFHeader = "X-CSRF-Token"

// RequireCSRFOnCookieAuth guards state-changing API calls made with a session
// cookie. A cookie is an AMBIENT credential — the browser attaches it to any
// request to this origin, including one a third-party page caused — so
// authentication alone does not prove the user intended the call. A bearer token
// is not ambient, so bearer callers are exempt: they must be, or every API client
// would have to fetch a CSRF token it has no session to get one from.
//
// Until now the only thing standing between a cookie-authenticated mutation and
// a cross-site POST was SameSite=Lax on the cookie. That is a real control and it
// stays, but it is one attribute: set SameSite=None to embed orbital's UI in
// another product's frame and every mutation becomes forgeable, with nothing in
// the code to notice. Defence in depth, in the OWASP sense — the token cannot be
// read cross-origin (no CORS policy is configured) and a custom header cannot be
// set by a plain cross-site form at all.
func RequireCSRFOnCookieAuth(keys SessionKeys) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}
			// Bearer callers carry no ambient credential; CSRF does not apply.
			if _, ok := bearerFrom(c); ok {
				return next(c)
			}
			// Header only — never c.FormValue, which would consume a JSON body
			// before the handler reads it.
			if !ValidateCSRF(keys, c.Request(), c.Request().Header.Get(CSRFHeader)) {
				return echo.NewHTTPError(http.StatusForbidden,
					"missing or invalid "+CSRFHeader+" — a state-changing call authenticated by session cookie must carry the CSRF token")
			}
			return next(c)
		}
	}
}
