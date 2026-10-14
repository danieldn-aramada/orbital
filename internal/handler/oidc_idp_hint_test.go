package handler

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/armada/orbital/internal/auth"
	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2"
)


func TestLogin_IDPHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		hint string
		want string // "" = the parameter must be absent
	}{
		{name: "sent when configured", hint: "armada_ai", want: "armada_ai"},
		{name: "omitted when empty", hint: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &OIDC{
				sessionKeys: auth.NewSessionKeys("unit-test-hmac-key", "", false),
				logger:      slog.Default(),
				oauth2Cfg: oauth2.Config{
					ClientID:    "armada-orbital",
					RedirectURL: "http://orbital.example/orbital/auth/callback",
					Endpoint: oauth2.Endpoint{
						AuthURL: "https://idp.example/realms/armada/protocol/openid-connect/auth",
					},
				},
				idpHint: tc.hint,
			}

			rec := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/auth/login", nil), rec)

			if err := h.Login(c); err != nil {
				t.Fatalf("Login: %v", err)
			}
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
			}

			loc, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatalf("parse redirect: %v", err)
			}
			q := loc.Query()
			if got := q.Get("kc_idp_hint"); got != tc.want {
				t.Errorf("kc_idp_hint = %q, want %q", got, tc.want)
			}
			// PKCE, state and nonce must survive alongside the new option.
			for _, k := range []string{"state", "nonce", "code_challenge"} {
				if q.Get(k) == "" {
					t.Errorf("%s missing from authorization request", k)
				}
			}
		})
	}
}
