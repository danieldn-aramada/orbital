package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// buildExternalJWTVerifier is a thin helper that mints an ExternalJWTVerifier
// against the shared test OIDC server. Passing "" for clientID uses the AEP
// demo default so per-test overrides only need to be specified when relevant.
func buildExternalJWTVerifier(t *testing.T, issuerURL, audience, clientID, defaultRole string) *ExternalJWTVerifier {
	t.Helper()
	if clientID == "" {
		clientID = "aep-fleet-commander"
	}
	if defaultRole == "" {
		defaultRole = "admin"
	}
	v, err := NewExternalJWTVerifier(context.Background(), ExternalJWTConfig{
		IssuerURL:   issuerURL,
		Audience:    audience,
		ClientID:    clientID,
		DefaultRole: defaultRole,
	})
	if err != nil {
		t.Fatalf("NewExternalJWTVerifier: %v", err)
	}
	return v
}

func TestExternalJWT_MissingBearer_Returns401(t *testing.T) {
	issuerURL, _ := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "", "")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		return nil
	})(c)

	if called {
		t.Error("expected next NOT to be called without bearer")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if wwwAuth := rec.Header().Get("WWW-Authenticate"); wwwAuth == "" {
		t.Error("expected WWW-Authenticate header on 401")
	}
}

func TestExternalJWT_ValidToken_SetsContext(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	token := sign(map[string]any{
		"iss":                issuerURL,
		"aud":                "account",
		"azp":                "aep-fleet-commander",
		"sub":                "4255defa-b501-43d7-8995-a575c61fe5e3",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"name":               "Daniel Nguyen",
		"preferred_username": "daniel.nguyen@armada.ai",
		"email":              "daniel.nguyen@armada.ai",
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		if got := c.Get("user_email"); got != "daniel.nguyen@armada.ai" {
			t.Errorf("user_email: got %v, want daniel.nguyen@armada.ai", got)
		}
		if got := c.Get("user_name"); got != "Daniel Nguyen" {
			t.Errorf("user_name: got %v, want Daniel Nguyen", got)
		}
		if got := c.Get("user_sub"); got != "4255defa-b501-43d7-8995-a575c61fe5e3" {
			t.Errorf("user_sub: got %v", got)
		}
		if got := c.Get("role"); got != "admin" {
			t.Errorf("role: got %v, want admin", got)
		}
		if got, _ := c.Get("is_authn").(bool); !got {
			t.Error("is_authn should be true")
		}
		return nil
	})(c)

	if !called {
		t.Errorf("expected next to be called (status %d)", rec.Code)
	}
}

func TestExternalJWT_EmailFallsBackToPreferredUsername(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	// Token with preferred_username but no email — orbital should fall back.
	token := sign(map[string]any{
		"iss":                issuerURL,
		"aud":                "account",
		"azp":                "aep-fleet-commander",
		"sub":                "some-sub",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"preferred_username": "user@example.com",
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, _ := echoCtx(req)

	_ = v.RequireAuth()(func(c echo.Context) error {
		if got := c.Get("user_email"); got != "user@example.com" {
			t.Errorf("user_email fallback: got %v, want user@example.com", got)
		}
		return nil
	})(c)
}

func TestExternalJWT_ExpiredToken_Returns401(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	token := sign(map[string]any{
		"iss": issuerURL,
		"aud": "account",
		"azp": "aep-fleet-commander",
		"sub": "s",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		return nil
	})(c)

	if called {
		t.Error("expected next NOT to be called for expired token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired, got %d", rec.Code)
	}
}

func TestExternalJWT_WrongAudience_Returns401(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	token := sign(map[string]any{
		"iss": issuerURL,
		"aud": "orbital", // wrong: verifier expects "account"
		"azp": "aep-fleet-commander",
		"sub": "s",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		return nil
	})(c)

	if called {
		t.Error("expected next NOT to be called for wrong audience")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong audience, got %d", rec.Code)
	}
}

// TestExternalJWT_WrongAZP_Returns401 pins the trust anchor.
//
// Regression class: if aud validation is bypassed (Keycloak default aud=account
// is generic), removing the azp check would silently accept tokens issued by
// ANY client in the realm — including Keycloak internals and future
// registrations — as orbital admins. This test guards against that regression.
func TestExternalJWT_WrongAZP_Returns401(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	token := sign(map[string]any{
		"iss": issuerURL,
		"aud": "account",
		"azp": "some-other-client", // wrong: verifier expects "aep-fleet-commander"
		"sub": "s",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		return nil
	})(c)

	if called {
		t.Error("expected next NOT to be called for wrong azp — trust anchor violated")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong azp, got %d", rec.Code)
	}
}

func TestExternalJWT_MissingAZP_Returns401(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	// Token with no azp claim at all — must be rejected.
	token := sign(map[string]any{
		"iss": issuerURL,
		"aud": "account",
		"sub": "s",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		return nil
	})(c)

	if called {
		t.Error("expected next NOT to be called for missing azp")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing azp, got %d", rec.Code)
	}
}

// TestExternalJWT_ErrorResponseShape pins the RFC 6749 §5.2 / RFC 6750 §3.1
// contract. Callers rely on {error, error_description} to distinguish token-
// expired from other rejection paths and decide whether to refresh or fail.
// The WWW-Authenticate header carries the same info for HTTP intermediaries.
func TestExternalJWT_ErrorResponseShape(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	token := sign(map[string]any{
		"iss": issuerURL,
		"aud": "account",
		"azp": "aep-fleet-commander",
		"sub": "s",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, rec := echoCtx(req)

	_ = v.RequireAuth()(func(c echo.Context) error { return nil })(c)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not JSON: %v — got %s", err, rec.Body.String())
	}
	if body["error"] != "invalid_token" {
		t.Errorf("body.error: got %q, want invalid_token", body["error"])
	}
	if !strings.Contains(strings.ToLower(body["error_description"]), "expire") {
		t.Errorf("body.error_description should mention expiration; got %q", body["error_description"])
	}

	wwwAuth := rec.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(wwwAuth, "Bearer ") {
		t.Errorf("WWW-Authenticate: got %q, want Bearer scheme", wwwAuth)
	}
	if !strings.Contains(wwwAuth, `error="invalid_token"`) {
		t.Errorf("WWW-Authenticate should carry error=invalid_token; got %q", wwwAuth)
	}
}

// TestExternalJWT_SessionFallback checks that a request with no bearer but an
// authenticated session (is_authn=true, set by the global session middleware)
// passes through. This is what keeps orbital's own UI usable in external-jwt
// mode — a human signs in via local/OIDC and browses with a session cookie.
// The middleware must NOT stamp a `role` on session callers: their role is
// resolved from the DB by RequireRole via the session's user_id.
func TestExternalJWT_SessionFallback(t *testing.T) {
	issuerURL, _ := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c, _ := echoCtx(req)
	c.Set("is_authn", true) // session middleware would have set this

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		if got := c.Get("role"); got != nil {
			t.Errorf("session caller should have no role stamped by external-jwt (DB is source of truth); got %v", got)
		}
		return nil
	})(c)

	if !called {
		t.Error("expected next to be called for authenticated session (no bearer)")
	}
}

// TestExternalJWT_MissingBearer_UsesInvalidRequest checks that the "no bearer
// header at all" path uses OAuth's `invalid_request` code — distinguishable
// from `invalid_token` so clients can decide whether to prompt the user (no
// creds) versus refresh (bad creds).
func TestExternalJWT_MissingBearer_UsesInvalidRequest(t *testing.T) {
	issuerURL, _ := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "", "")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c, rec := echoCtx(req)

	_ = v.RequireAuth()(func(c echo.Context) error { return nil })(c)

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v — %s", err, rec.Body.String())
	}
	if body["error"] != "invalid_request" {
		t.Errorf("missing bearer should be invalid_request, got %q", body["error"])
	}
}

// TestExternalJWT_ExpiredToken_LogsIdentity pins the auth-log convention: an
// expired token (signature already validated by go-oidc) attributes the
// authentic identity it was minted for, while a signature/audience failure
// (untrusted claims) does NOT — logging attacker-controllable claims as
// identity is a log-forging vector.
func TestExternalJWT_ExpiredToken_LogsIdentity(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "admin")

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)

	t.Run("expired attributes identity", func(t *testing.T) {
		buf.Reset()
		token := sign(map[string]any{
			"iss":                issuerURL,
			"aud":                "account",
			"azp":                "aep-fleet-commander",
			"sub":                "4255defa-b501-43d7-8995-a575c61fe5e3",
			"exp":                time.Now().Add(-time.Hour).Unix(),
			"preferred_username": "daniel.nguyen@armada.ai",
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		c, _ := echoCtx(req)
		_ = v.RequireAuth()(func(c echo.Context) error { return nil })(c)

		if !strings.Contains(buf.String(), "daniel.nguyen@armada.ai") {
			t.Errorf("expired-token log should attribute preferred_username; got %s", buf.String())
		}
		if !strings.Contains(buf.String(), "4255defa-b501-43d7-8995-a575c61fe5e3") {
			t.Errorf("expired-token log should attribute subject; got %s", buf.String())
		}
	})

	t.Run("wrong audience does not attribute identity", func(t *testing.T) {
		buf.Reset()
		// aud is checked before signature in go-oidc, so claims are untrusted.
		token := sign(map[string]any{
			"iss":                issuerURL,
			"aud":                "wrong-audience",
			"azp":                "aep-fleet-commander",
			"sub":                "attacker-sub",
			"exp":                time.Now().Add(time.Hour).Unix(),
			"preferred_username": "attacker@evil.example",
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		c, _ := echoCtx(req)
		_ = v.RequireAuth()(func(c echo.Context) error { return nil })(c)

		if strings.Contains(buf.String(), "attacker@evil.example") {
			t.Errorf("untrusted-claim identity must NOT be logged; got %s", buf.String())
		}
	})
}

func TestParseUnverifiedClaims(t *testing.T) {
	mkToken := func(payload map[string]any) string {
		b, _ := json.Marshal(payload)
		return "header." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}

	t.Run("extracts identity fields", func(t *testing.T) {
		tok := mkToken(map[string]any{
			"sub":                "s1",
			"preferred_username": "user@example.com",
			"name":               "User One",
		})
		claims, err := parseUnverifiedClaims(tok)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if claims.Sub != "s1" || claims.PreferredUsername != "user@example.com" || claims.Name != "User One" {
			t.Errorf("got %+v", claims)
		}
	})

	t.Run("rejects wrong segment count", func(t *testing.T) {
		if _, err := parseUnverifiedClaims("only.two"); err == nil {
			t.Error("expected error for 2-segment token")
		}
	})

	t.Run("rejects non-base64 payload", func(t *testing.T) {
		if _, err := parseUnverifiedClaims("h.!!!not-base64!!!.s"); err == nil {
			t.Error("expected error for invalid base64 payload")
		}
	})
}

// TestExternalJWT_FallbackRoutesByIssuer pins the regression that broke the
// demo publish: external-jwt mode must NOT drop the AAD service-token path the
// in-pod bundler depends on. A Keycloak-issued bearer is verified by the
// external-jwt verifier (role stamped); a bearer from a different issuer (an
// AAD service token) is routed to the fallback BearerVerifier so internal
// service auth keeps working. If someone removes the fallback or the
// issuer-routing, this test fails.
func TestExternalJWT_FallbackRoutesByIssuer(t *testing.T) {
	kcIssuer, kcSign := newTestOIDCServer(t)   // Keycloak sim
	aadIssuer, aadSign := newTestOIDCServer(t) // AAD sim (the fallback's issuer)

	const appID = "5fc832f6-843e-4207-93dd-b3c3a77c06f2"
	bv, err := NewBearerVerifier(context.Background(), aadIssuer, "aad-audience", []string{appID})
	if err != nil {
		t.Fatalf("NewBearerVerifier (fallback): %v", err)
	}
	ejv, err := NewExternalJWTVerifier(context.Background(), ExternalJWTConfig{
		IssuerURL:   kcIssuer,
		Audience:    "account",
		ClientID:    "aep-fleet-commander",
		DefaultRole: "admin",
		Fallback:    bv,
	})
	if err != nil {
		t.Fatalf("NewExternalJWTVerifier: %v", err)
	}

	t.Run("keycloak issuer → external-jwt verifier (role stamped)", func(t *testing.T) {
		token := kcSign(map[string]any{
			"iss":                kcIssuer,
			"aud":                "account",
			"azp":                "aep-fleet-commander",
			"sub":                "u1",
			"exp":                time.Now().Add(time.Hour).Unix(),
			"preferred_username": "user@example.com",
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		c, _ := echoCtx(req)

		called := false
		_ = ejv.RequireAuth()(func(c echo.Context) error {
			called = true
			if c.Get("role") != "admin" {
				t.Errorf("keycloak caller should get role=admin; got %v", c.Get("role"))
			}
			return nil
		})(c)
		if !called {
			t.Error("expected next called for keycloak token")
		}
	})

	t.Run("AAD issuer → fallback verifier (bundler service token)", func(t *testing.T) {
		// AAD app-only token: appid allowlisted, no user identity.
		token := aadSign(map[string]any{
			"iss":   aadIssuer,
			"aud":   "aad-audience",
			"sub":   appID,
			"exp":   time.Now().Add(time.Hour).Unix(),
			"appid": appID,
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		c, _ := echoCtx(req)

		called := false
		_ = ejv.RequireAuth()(func(c echo.Context) error {
			called = true
			if got := c.Get("user_name"); got != AppPrincipalPrefix+appID {
				t.Errorf("AAD app token should route to fallback (user_name=%s...); got %v", AppPrincipalPrefix, got)
			}
			if c.Get("role") != nil {
				t.Errorf("fallback (AAD) caller must NOT get a stamped role; got %v", c.Get("role"))
			}
			return nil
		})(c)
		if !called {
			t.Error("expected next called for AAD service token via fallback")
		}
	})
}

func TestExternalJWT_DefaultRoleDev(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)
	v := buildExternalJWTVerifier(t, issuerURL, "account", "aep-fleet-commander", "dev")

	token := sign(map[string]any{
		"iss":                issuerURL,
		"aud":                "account",
		"azp":                "aep-fleet-commander",
		"sub":                "s",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"preferred_username": "user@example.com",
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, _ := echoCtx(req)

	_ = v.RequireAuth()(func(c echo.Context) error {
		if got := c.Get("role"); got != "dev" {
			t.Errorf("role: got %v, want dev — ORBITAL_JWT_DEFAULT_ROLE must be honored", got)
		}
		return nil
	})(c)
}
