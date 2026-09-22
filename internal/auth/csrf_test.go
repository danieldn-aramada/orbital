package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// Names transcribed from the phase-2 acceptance list, item 2e.
func TestRequireCSRFOnCookieAuth(t *testing.T) {
	keys := SessionKeys{HMACKey: "csrf-test-hmac-key"}

	// A real session carrying a CSRF token, as the browser would hold.
	setup := httptest.NewRecorder()
	token, err := GetOrCreateCSRF(keys, httptest.NewRequest(http.MethodGet, "/", nil), setup)
	if err != nil {
		t.Fatalf("GetOrCreateCSRF: %v", err)
	}
	cookies := setup.Result().Cookies()

	call := func(method string, header, bearer string, withCookie bool) int {
		req := httptest.NewRequest(method, "/api/v1/backup", nil)
		if withCookie {
			for _, c := range cookies {
				req.AddCookie(c)
			}
		}
		if header != "" {
			req.Header.Set(CSRFHeader, header)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		h := RequireCSRFOnCookieAuth(keys)(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
		if err := h(c); err != nil {
			if he, ok := err.(*echo.HTTPError); ok {
				return he.Code
			}
			t.Fatalf("unexpected error: %v", err)
		}
		return rec.Code
	}

	if got := call(http.MethodPost, "", "", true); got != http.StatusForbidden {
		t.Errorf("cookie POST without the header = %d, want 403", got)
	}
	if got := call(http.MethodPost, token, "", true); got != http.StatusOK {
		t.Errorf("cookie POST with the header = %d, want 200", got)
	}
	if got := call(http.MethodPost, "wrong-token", "", true); got != http.StatusForbidden {
		t.Errorf("cookie POST with a wrong token = %d, want 403", got)
	}
	// The exemption that matters: an API client has no session to fetch a token
	// from, so requiring one here would break every bearer caller.
	if got := call(http.MethodPost, "", "any-token", false); got != http.StatusOK {
		t.Errorf("bearer POST without the header = %d, want 200 — bearer carries no ambient credential", got)
	}
	if got := call(http.MethodGet, "", "", true); got != http.StatusOK {
		t.Errorf("cookie GET = %d, want 200 — reads are not state-changing", got)
	}
}
