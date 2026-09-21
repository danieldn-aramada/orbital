package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// newTestOIDCServer starts a minimal OIDC provider backed by a fresh RSA key.
// Returns the issuer URL and a helper that signs JWTs with that key.
func newTestOIDCServer(t *testing.T) (issuerURL string, sign func(claims map[string]any) string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	const kid = "test-key-1"

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/auth",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &key.PublicKey
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"keys": []map[string]any{{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": kid,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})

	sign = func(claims map[string]any) string {
		hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
		pay, _ := json.Marshal(claims)
		h := base64.RawURLEncoding.EncodeToString(hdr)
		p := base64.RawURLEncoding.EncodeToString(pay)
		digest := sha256.Sum256([]byte(h + "." + p))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatalf("sign JWT: %v", err)
		}
		return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig)
	}

	return srv.URL, sign
}

func echoCtx(req *http.Request) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// ── Non-bearer paths (no OIDC server needed) ─────────────────────────────────

func TestRequireAuth_SessionPassThrough(t *testing.T) {
	v := &BearerVerifier{} // verifier unused — no Bearer header

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c, _ := echoCtx(req)
	c.Set("is_authn", true)

	called := false
	err := v.RequireAuth()(func(c echo.Context) error {
		called = true
		return nil
	})(c)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected next handler to be called when is_authn=true")
	}
}

func TestRequireAuth_NoAuth_Returns401(t *testing.T) {
	v := &BearerVerifier{}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		return nil
	})(c)

	if called {
		t.Error("expected next handler NOT to be called")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// ── Bearer token paths (local OIDC server) ────────────────────────────────────

func TestRequireAuth_ValidBearer_SetsContext(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)

	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test", nil)
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}

	token := sign(map[string]any{
		"iss":                issuerURL,
		"aud":                "orbital-test",
		"sub":                "u1",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"name":               "Test User",
		"preferred_username": "test@example.com",
		"roles":              []string{"orbital-admin"},
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		if c.Get("user_name") != "Test User" {
			t.Errorf("user_name: got %v, want %q", c.Get("user_name"), "Test User")
		}
		if c.Get("user_email") != "test@example.com" {
			t.Errorf("user_email: got %v, want %q", c.Get("user_email"), "test@example.com")
		}
		roles, _ := c.Get("roles").([]string)
		if len(roles) != 1 || roles[0] != "orbital-admin" {
			t.Errorf("roles: got %v, want [orbital-admin]", roles)
		}
		return nil
	})(c)

	if !called {
		t.Errorf("expected next handler to be called (status %d)", rec.Code)
	}
}

func TestRequireAuth_ValidBearer_UPNFallback(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)

	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test", nil)
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}

	// No preferred_username — should fall back to upn
	token := sign(map[string]any{
		"iss":  issuerURL,
		"aud":  "orbital-test",
		"sub":  "u2",
		"exp":  time.Now().Add(time.Hour).Unix(),
		"name": "Bob",
		"upn":  "bob@example.com",
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, _ := echoCtx(req)

	_ = v.RequireAuth()(func(c echo.Context) error {
		if c.Get("user_email") != "bob@example.com" {
			t.Errorf("user_email: got %v, want %q", c.Get("user_email"), "bob@example.com")
		}
		return nil
	})(c)
}

func TestRequireAuth_ExpiredBearer_Returns401(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)

	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test", nil)
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}

	token := sign(map[string]any{
		"iss": issuerURL,
		"aud": "orbital-test",
		"sub": "u3",
		"exp": time.Now().Add(-time.Hour).Unix(), // expired
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
		t.Error("expected next handler NOT to be called for expired token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired token, got %d", rec.Code)
	}
}

func TestRequireAuth_WrongAudience_Returns401(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)

	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test", nil)
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}

	token := sign(map[string]any{
		"iss": issuerURL,
		"aud": "wrong-audience",
		"sub": "u4",
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
		t.Error("expected next handler NOT to be called for wrong audience")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong audience, got %d", rec.Code)
	}
}

// ── App-only (client credentials) bearer tokens — ADR 010 ───────────────────

func TestRequireAuth_AppOnlyToken_v1_Accepted(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)

	// Allowlisted explicitly: this test is about v1 `appid` / v2 `azp` claim
	// shape, not authorization. It passed with nil until 2026-09-17 only
	// because an empty allowlist used to allow everything.
	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test",
		[]string{"5fc832f6-843e-4207-93dd-b3c3a77c06f2"})
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}

	// v1.0 app token: appid present, no user-identity claims.
	const appID = "5fc832f6-843e-4207-93dd-b3c3a77c06f2"
	token := sign(map[string]any{
		"iss":   issuerURL,
		"aud":   "orbital-test",
		"sub":   appID,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"appid": appID,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, rec := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		if got := c.Get("user_name"); got != AppPrincipalPrefix+appID {
			t.Errorf("user_name: got %v, want %q", got, AppPrincipalPrefix+appID)
		}
		if got := c.Get("user_email"); got != "" {
			t.Errorf("user_email should be empty for app token; got %v", got)
		}
		if got, _ := c.Get("is_authn").(bool); !got {
			t.Errorf("is_authn should be true")
		}
		return nil
	})(c)

	if !called {
		t.Errorf("expected next handler called (status %d)", rec.Code)
	}
}

func TestRequireAuth_AppOnlyToken_v2_AZP_Accepted(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)

	// Allowlisted explicitly: this test is about v1 `appid` / v2 `azp` claim
	// shape, not authorization. It passed with nil until 2026-09-17 only
	// because an empty allowlist used to allow everything.
	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test",
		[]string{"5fc832f6-843e-4207-93dd-b3c3a77c06f2"})
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}

	// v2.0 app token: azp present instead of (or alongside) appid.
	const appID = "5fc832f6-843e-4207-93dd-b3c3a77c06f2"
	token := sign(map[string]any{
		"iss": issuerURL,
		"aud": "orbital-test",
		"sub": appID,
		"exp": time.Now().Add(time.Hour).Unix(),
		"azp": appID,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, _ := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		if got := c.Get("user_name"); got != AppPrincipalPrefix+appID {
			t.Errorf("user_name: got %v, want %q (v2.0 azp claim should map identically to v1.0 appid)", got, AppPrincipalPrefix+appID)
		}
		return nil
	})(c)

	if !called {
		t.Errorf("expected next handler called for v2.0 azp app token")
	}
}

func TestRequireAuth_AppOnlyToken_AllowlistRejection(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)

	// Allowlist contains a different appid — caller's appid is not in it.
	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test", []string{"another-app-id"})
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}

	token := sign(map[string]any{
		"iss":   issuerURL,
		"aud":   "orbital-test",
		"sub":   "5fc832f6-843e-4207-93dd-b3c3a77c06f2",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"appid": "5fc832f6-843e-4207-93dd-b3c3a77c06f2",
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
		t.Errorf("next handler should NOT be called for allowlist-rejected appid")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestRequireAuth_AppOnlyToken_AllowlistAccepts(t *testing.T) {
	issuerURL, sign := newTestOIDCServer(t)

	const appID = "5fc832f6-843e-4207-93dd-b3c3a77c06f2"
	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test", []string{appID})
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}

	token := sign(map[string]any{
		"iss":   issuerURL,
		"aud":   "orbital-test",
		"sub":   appID,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"appid": appID,
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	c, _ := echoCtx(req)

	called := false
	_ = v.RequireAuth()(func(c echo.Context) error {
		called = true
		return nil
	})(c)

	if !called {
		t.Errorf("next handler should be called for allowlisted appid")
	}
}

// ── App-token allowlist ──────────────────────────────────────────────────────
//
// Empty means DENY and `*` means allow-any (2026-09-17). Before that, empty
// skipped the check entirely, so a valid app token from ANY app bound to the
// audience was accepted — the shape AWS eliminated from IAM/GitHub OIDC trust
// policies after it was found exploitable in the wild. These four tests are the
// guarantee; without the negatives a permanently-allowing gate would pass.

// appTokenRequest signs an app-only token (no email/upn, appid set) and returns
// a request carrying it, exercising the same middleware a real caller hits.
func appTokenRequest(sign func(map[string]any) string, issuerURL, appID string) *http.Request {
	tok := sign(map[string]any{
		"iss": issuerURL, "aud": "orbital-test",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"sub": appID, "appid": appID,
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}

func appTokenStatus(t *testing.T, allowed []string, appID string) int {
	t.Helper()
	issuerURL, sign := newTestOIDCServer(t)
	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test", allowed)
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}
	c, rec := echoCtx(appTokenRequest(sign, issuerURL, appID))
	h := v.RequireAuth()(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	if err := h(c); err != nil {
		t.Fatalf("handler: %v", err)
	}
	return rec.Code
}

func TestAppTokenAllowlist_UnsetRejectsAppTokens(t *testing.T) {
	for _, allowed := range [][]string{nil, {}, {""}, {"  "}} {
		if got := appTokenStatus(t, allowed, "some-app"); got != http.StatusUnauthorized {
			t.Errorf("allowed=%q: got %d, want 401 — an empty allowlist must DENY, not allow any app", allowed, got)
		}
	}
}

func TestAppTokenAllowlist_WildcardAcceptsAnyAppToken(t *testing.T) {
	if got := appTokenStatus(t, []string{"*"}, "any-unlisted-app"); got != http.StatusOK {
		t.Errorf("got %d, want 200 — `*` must accept any app token bound to the audience", got)
	}
}

func TestAppTokenAllowlist_SpecificListAcceptsListedRejectsOthers(t *testing.T) {
	if got := appTokenStatus(t, []string{"app-a", "app-b"}, "app-b"); got != http.StatusOK {
		t.Errorf("listed app: got %d, want 200", got)
	}
	if got := appTokenStatus(t, []string{"app-a", "app-b"}, "app-c"); got != http.StatusUnauthorized {
		t.Errorf("unlisted app: got %d, want 401", got)
	}
}

func TestAppTokenAllowlist_UserTokensUnaffected(t *testing.T) {
	// A user token carries an email; the allowlist gates app-only tokens and
	// must not become a second gate on human callers.
	issuerURL, sign := newTestOIDCServer(t)
	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test", nil)
	if err != nil {
		t.Fatalf("NewBearerVerifier: %v", err)
	}
	tok := sign(map[string]any{
		"iss": issuerURL, "aud": "orbital-test",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"sub": "u1", "preferred_username": "dev@armada.ai", "name": "Dev",
		"appid": "some-app", // present on user tokens too — must not trigger the gate
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	c, rec := echoCtx(req)
	h := v.RequireAuth()(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	if err := h(c); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("user token got %d, want 200 — the app allowlist must not gate user callers", rec.Code)
	}
	if got := c.Get("user_email"); got != "dev@armada.ai" {
		t.Errorf("user_email = %v, want dev@armada.ai", got)
	}
}
