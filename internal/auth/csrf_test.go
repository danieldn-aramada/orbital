package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// Test names are transcribed from this change's acceptance list (items 1-4).

// call runs the guard and reports the status a client would see.
func call(t *testing.T, method, contentType, origin, referer, bearer string) int {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/export", nil)
	req.Host = "orbital.example.com"
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	h := RequireCSRFOnCookieAuth()(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	if err := h(c); err != nil {
		he, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("guard returned %T, want *echo.HTTPError so the central ErrorHandler renders the envelope", err)
		}
		return he.Code
	}
	return rec.Code
}

// Item 1: cookie POST, JSON body, same-origin Origin — allowed with no token.
func TestCSRF_JSONSameOriginCookiePost_Allowed(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "application/graphql"} {
		t.Run(ct, func(t *testing.T) {
			if got := call(t, http.MethodPost, ct, "https://orbital.example.com", "", ""); got != http.StatusOK {
				t.Errorf("= %d, want 200", got)
			}
		})
	}
}

// Item 2: with no Origin or Referer, form content types are refused.
func TestCSRF_FormContentTypeOnCookieAuth_Refused(t *testing.T) {
	for _, ct := range []string{
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=----x",
		"text/plain",
		"TEXT/PLAIN", // media types are case-insensitive; a casing trick must not pass
	} {
		t.Run(ct, func(t *testing.T) {
			if got := call(t, http.MethodPost, ct, "", "", ""); got != http.StatusForbidden {
				t.Errorf("= %d, want 403 — this is the cross-site form vector", got)
			}
		})
	}
}

// htmx defaults to form encoding, so a same-origin Origin must beat the
// content-type rule. Pins the check order.
func TestCSRF_HtmxFormEncodedSameOrigin_Allowed(t *testing.T) {
	if got := call(t, http.MethodPost, "application/x-www-form-urlencoded", "http://orbital.example.com", "", ""); got != http.StatusOK {
		t.Errorf("= %d, want 200 — htmx sends form encoding on a same-origin POST", got)
	}
	// Same, vouched for by Referer alone.
	if got := call(t, http.MethodPost, "application/x-www-form-urlencoded", "", "http://orbital.example.com/servers", ""); got != http.StatusOK {
		t.Errorf("referer-only = %d, want 200", got)
	}
	// A cross-site form is still refused.
	if got := call(t, http.MethodPost, "application/x-www-form-urlencoded", "https://evil.example.net", "", ""); got != http.StatusForbidden {
		t.Errorf("cross-site form = %d, want 403", got)
	}
}

// Item 3: a stated Origin naming another host is refused; Referer stands in.
func TestCSRF_ForeignOriginOnCookieAuth_Refused(t *testing.T) {
	tests := []struct {
		name            string
		origin, referer string
		want            int
	}{
		{"foreign origin", "https://evil.example.net", "", http.StatusForbidden},
		{"foreign referer, no origin", "", "https://evil.example.net/page", http.StatusForbidden},
		{"same-origin referer, no origin", "", "https://orbital.example.com/servers", http.StatusOK},
		{"origin wins over referer", "https://orbital.example.com", "https://evil.example.net/p", http.StatusOK},
		{"opaque origin is not waved through", "null", "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := call(t, http.MethodPost, "application/json", tt.origin, tt.referer, ""); got != tt.want {
				t.Errorf("= %d, want %d", got, tt.want)
			}
		})
	}
}

// Item 3, fail-open half: neither header present is not a browser form post.
// Keeps cookie-authenticated curl scripts (e2e-divergence.sh) working.
func TestCSRF_AbsentOriginOnCookieAuth_Allowed(t *testing.T) {
	if got := call(t, http.MethodPost, "application/json", "", "", ""); got != http.StatusOK {
		t.Errorf("= %d, want 200 — absent Origin and Referer is not a browser form post", got)
	}
	// A bodyless POST omits Content-Type entirely (export publish does this).
	if got := call(t, http.MethodPost, "", "", "", ""); got != http.StatusOK {
		t.Errorf("bodyless POST = %d, want 200", got)
	}
}

// Item 4: bearer callers (orbctl, AEP FC, cb-bundler) are exempt from both.
func TestCSRF_BearerCaller_Exempt(t *testing.T) {
	tests := []struct{ name, contentType, origin string }{
		{"form content type", "application/x-www-form-urlencoded", ""},
		{"foreign origin", "application/json", "https://evil.example.net"},
		{"no headers at all", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := call(t, http.MethodPost, tt.contentType, tt.origin, "", "aep-token"); got != http.StatusOK {
				t.Errorf("= %d, want 200 — a bearer caller must never be gated on browser headers", got)
			}
		})
	}
}

// Reads are never state-changing, whatever they carry.
func TestCSRF_SafeMethods_Allowed(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(m, func(t *testing.T) {
			if got := call(t, m, "text/plain", "https://evil.example.net", "", ""); got != http.StatusOK {
				t.Errorf("= %d, want 200", got)
			}
		})
	}
}
