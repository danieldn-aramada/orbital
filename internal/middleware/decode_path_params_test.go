package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// Pinned regression: Echo doesn't auto-decode path params, so a route like
// /datacenters/:orbId receiving "colo%3Acolo-galleon" used to forward the raw
// percent-encoded string to the handler. The middleware decodes once at the
// framework layer so handlers can use c.Param() directly. If this middleware
// is dropped or its registration omitted, this test fails.
func TestDecodePathParams(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"encoded colon", "colo%3Acolo-galleon", "colo:colo-galleon"},
		{"already plain", "0x53", "0x53"},
		{"uuid", "06ec3f7e-1234-5678-9abc-def012345678", "06ec3f7e-1234-5678-9abc-def012345678"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			e.Use(DecodePathParams)
			var captured string
			e.GET("/x/:p", func(c echo.Context) error {
				captured = c.Param("p")
				return c.NoContent(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/x/"+tc.raw, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if captured != tc.want {
				t.Errorf("captured = %q, want %q", captured, tc.want)
			}
		})
	}
}
