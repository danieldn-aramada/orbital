//go:build integration

package handler_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

var oidcSessionKeys = auth.SessionKeys{HMACKey: "oidc-test-hmac-key"}

// oidcProvider is a minimal OIDC/OAuth2 provider for handler tests.
// TokenClaims is mutable — tests configure it before calling Callback.
type oidcProvider struct {
	Server      *httptest.Server
	TokenClaims map[string]any
	sign        func(claims map[string]any) string
}

func newOIDCProvider(t *testing.T) *oidcProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	const kid = "oidc-test-key"

	p := &oidcProvider{}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p.Server = srv

	p.sign = func(claims map[string]any) string {
		hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
		pay, _ := json.Marshal(claims)
		h := base64.RawURLEncoding.EncodeToString(hdr)
		pl := base64.RawURLEncoding.EncodeToString(pay)
		digest := sha256.Sum256([]byte(h + "." + pl))
		sig, signErr := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if signErr != nil {
			t.Fatalf("sign JWT: %v", signErr)
		}
		return h + "." + pl + "." + base64.RawURLEncoding.EncodeToString(sig)
	}

	p.TokenClaims = map[string]any{
		"iss":                srv.URL,
		"aud":                "test-client-id",
		"sub":                "oidc-sub-1",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"email":              "oidc@example.com",
		"name":               "OIDC User",
		"preferred_username": "oidc@example.com",
	}

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

	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		idToken := p.sign(p.TokenClaims)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	})

	return p
}

func newOIDCHandler(t *testing.T, p *oidcProvider) *handler.OIDC {
	t.Helper()
	h, err := handler.NewOIDC(
		context.Background(),
		testDB,
		oidcSessionKeys,
		p.Server.URL,
		"test-client-id",
		"test-client-secret",
		p.Server.URL+"/callback",
		"",
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("NewOIDC: %v", err)
	}
	return h
}

// oidcStateSession stores an OIDC state value in a session and returns the cookies.
func oidcStateSession(t *testing.T, state string) []*http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	if err := auth.SetOIDCState(oidcSessionKeys, req, rec, state); err != nil {
		t.Fatalf("SetOIDCState: %v", err)
	}
	return rec.Result().Cookies()
}

// oidcCallbackCtx builds an Echo context for GET /auth/callback?state=...&code=...
func oidcCallbackCtx(cookies []*http.Cookie, state, code string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	url := "/auth/callback?state=" + state + "&code=" + code
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// ── Login ─────────────────────────────────────────────────────────────────────

func TestOIDCLogin_RedirectsToProvider(t *testing.T) {
	p := newOIDCProvider(t)
	h := newOIDCHandler(t, p)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Login(c); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, p.Server.URL+"/auth") {
		t.Errorf("expected Location to point to provider /auth, got %q", loc)
	}
	if !strings.Contains(loc, "state=") {
		t.Errorf("expected state= in redirect URL, got %q", loc)
	}
	var found bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "orbital_session" {
			found = true
		}
	}
	if !found {
		t.Error("expected orbital_session cookie to be set after Login")
	}
}

// ── Callback — state validation ───────────────────────────────────────────────

func TestOIDCCallback_NoState_RedirectsInvalidState(t *testing.T) {
	p := newOIDCProvider(t)
	h := newOIDCHandler(t, p)

	// No session cookies — GetAndClearOIDCState will return an error.
	c, rec := oidcCallbackCtx(nil, "some-state", "some-code")
	if err := h.Callback(c); err != nil {
		t.Fatalf("Callback: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "error=invalid_state") {
		t.Errorf("expected error=invalid_state, got %q", rec.Header().Get("Location"))
	}
}

func TestOIDCCallback_WrongState_RedirectsInvalidState(t *testing.T) {
	p := newOIDCProvider(t)
	h := newOIDCHandler(t, p)

	cookies := oidcStateSession(t, "correct-state")
	c, rec := oidcCallbackCtx(cookies, "wrong-state", "some-code")

	if err := h.Callback(c); err != nil {
		t.Fatalf("Callback: %v", err)
	}
	if !strings.Contains(rec.Header().Get("Location"), "error=invalid_state") {
		t.Errorf("expected error=invalid_state, got %q", rec.Header().Get("Location"))
	}
}

// ── Callback — happy paths ────────────────────────────────────────────────────

func TestOIDCCallback_NewUser_ProvisionedAndSessionSet(t *testing.T) {
	const testEmail = "new-oidc-provision@example.com"
	ctx := context.Background()

	p := newOIDCProvider(t)
	p.TokenClaims["email"] = testEmail
	p.TokenClaims["preferred_username"] = testEmail
	p.TokenClaims["name"] = "Provisioned OIDC User"

	h := newOIDCHandler(t, p)

	t.Cleanup(func() {
		testDB.User.Delete().Where(user.Email(testEmail)).ExecX(ctx)
	})

	cookies := oidcStateSession(t, "state-abc")
	c, rec := oidcCallbackCtx(cookies, "state-abc", "code-xyz")

	if err := h.Callback(c); err != nil {
		t.Fatalf("Callback: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
	if !strings.HasSuffix(rec.Header().Get("Location"), "/?fresh=1") {
		t.Errorf("expected /?fresh=1 redirect, got %q", rec.Header().Get("Location"))
	}

	// User should be provisioned in the DB.
	u, err := testDB.User.Query().Where(user.Email(testEmail)).Only(ctx)
	if err != nil {
		t.Fatalf("user not found after OIDC provisioning: %v", err)
	}
	if u.Name != "Provisioned OIDC User" {
		t.Errorf("user name: got %q, want %q", u.Name, "Provisioned OIDC User")
	}
	if !u.Verified {
		t.Error("provisioned OIDC user should be verified=true")
	}

	// Session cookie should be set.
	var sessionFound bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "orbital_session" {
			sessionFound = true
		}
	}
	if !sessionFound {
		t.Error("expected orbital_session cookie after successful OIDC login")
	}
}

func TestOIDCCallback_ExistingUser_SessionSet(t *testing.T) {
	const testEmail = "existing-oidc@example.com"
	ctx := context.Background()

	// Pre-create the user (simulates a previously provisioned account).
	existing := testDB.User.Create().
		SetEmail(testEmail).
		SetName("Existing OIDC").
		SetPreferredUsername(testEmail).
		SetVerified(true).
		SaveX(ctx)
	t.Cleanup(func() { testDB.User.DeleteOne(existing).ExecX(ctx) })

	p := newOIDCProvider(t)
	p.TokenClaims["email"] = testEmail
	p.TokenClaims["preferred_username"] = testEmail

	h := newOIDCHandler(t, p)

	cookies := oidcStateSession(t, "state-def")
	c, rec := oidcCallbackCtx(cookies, "state-def", "code-ghi")

	if err := h.Callback(c); err != nil {
		t.Fatalf("Callback: %v", err)
	}
	if !strings.HasSuffix(rec.Header().Get("Location"), "/?fresh=1") {
		t.Errorf("expected /?fresh=1 redirect, got %q", rec.Header().Get("Location"))
	}

	// No duplicate user should be created.
	count := testDB.User.Query().Where(user.Email(testEmail)).CountX(ctx)
	if count != 1 {
		t.Errorf("expected exactly 1 user with email %q, got %d", testEmail, count)
	}
}

func TestOIDCCallback_EmptyEmail_RedirectsError(t *testing.T) {
	p := newOIDCProvider(t)
	p.TokenClaims["email"] = "" // empty — should trigger the no_email redirect

	h := newOIDCHandler(t, p)

	cookies := oidcStateSession(t, "state-jkl")
	c, rec := oidcCallbackCtx(cookies, "state-jkl", "code-mno")

	if err := h.Callback(c); err != nil {
		t.Fatalf("Callback: %v", err)
	}
	if !strings.Contains(rec.Header().Get("Location"), "error=no_email") {
		t.Errorf("expected error=no_email, got %q", rec.Header().Get("Location"))
	}
}
