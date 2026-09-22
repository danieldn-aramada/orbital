package auth

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
)

// formContentTypes are the three media types an HTML form can emit, and the
// only ones a cross-site form can send without a CORS preflight.
var formContentTypes = map[string]struct{}{
	"application/x-www-form-urlencoded": {},
	"multipart/form-data":               {},
	"text/plain":                        {},
}

// RequireCSRFOnCookieAuth guards state-changing API calls authenticated by a
// session cookie, which is an ambient credential: the browser attaches it to any
// request to this origin, so authentication alone does not prove intent. Bearer
// callers are exempt — a bearer is not ambient.
//
// A stated Origin/Referer decides it. Only when neither is present does the
// content type matter, as a backstop against a cross-site form.
//
// Threat model, ordering constraint and why this replaced a CSRF token:
// docs/reference/AUTH.md, "CSRF on cookie-authenticated API calls".
func RequireCSRFOnCookieAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}
			if _, ok := bearerFrom(c); ok {
				return next(c)
			}
			// Origin before content type: htmx defaults to form encoding, so
			// testing content type first refuses every htmx mutation. Do not
			// reorder — pinned by TestCSRF_HtmxFormEncodedSameOrigin_Allowed.
			if host, stated := statedOriginHost(r); stated {
				if !strings.EqualFold(host, r.Host) {
					return echo.NewHTTPError(http.StatusForbidden,
						"cross-origin state-changing call rejected — Origin does not match this host")
				}
				return next(c)
			}
			if isFormContentType(r.Header.Get("Content-Type")) {
				return echo.NewHTTPError(http.StatusForbidden,
					"a state-changing call authenticated by session cookie must state an Origin or use a non-form content type")
			}
			return next(c)
		}
	}
}

// isFormContentType reports whether the header names a media type an HTML form
// could have produced. Absent counts as no: a form always sets one, and bodyless
// POSTs (export publish) legitimately omit it.
func isFormContentType(header string) bool {
	if header == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	_, ok := formContentTypes[strings.ToLower(mt)]
	return ok
}

// statedOriginHost returns the authority the request claims to come from.
// stated=false only when neither header is present; a header that IS present but
// opaque ("null") or unparseable yields an empty host, which matches no real Host
// and is therefore refused rather than waved through.
func statedOriginHost(r *http.Request) (host string, stated bool) {
	raw := r.Header.Get("Origin")
	if raw == "" {
		raw = r.Header.Get("Referer")
	}
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", true
	}
	return u.Host, true
}
