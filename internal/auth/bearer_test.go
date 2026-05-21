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
			"authorization_endpoint":               srv.URL + "/auth",
			"token_endpoint":                       srv.URL + "/token",
			"jwks_uri":                             srv.URL + "/jwks",
			"response_types_supported":             []string{"code"},
			"subject_types_supported":              []string{"public"},
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

	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test")
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

	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test")
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

	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test")
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

	v, err := NewBearerVerifier(context.Background(), issuerURL, "orbital-test")
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
