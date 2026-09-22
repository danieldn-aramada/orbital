package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var testKeys = SessionKeys{HMACKey: "test-hmac-key-for-unit-tests"}

// copyCookies copies Set-Cookie headers from a response recorder into a new request.
func copyCookies(t *testing.T, rec *httptest.ResponseRecorder) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestSetAndGetUserSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	if err := SetUserSession(testKeys, req, rec, 42, "Alice", "alice@example.com", "admin"); err != nil {
		t.Fatalf("SetUserSession: %v", err)
	}

	got, err := GetUserSession(testKeys, copyCookies(t, rec))
	if err != nil {
		t.Fatalf("GetUserSession: %v", err)
	}
	if got.ID != 42 || got.Name != "Alice" || got.Email != "alice@example.com" || got.Role != "admin" {
		t.Errorf("got %+v, want {42, Alice, alice@example.com, admin}", got)
	}
}

func TestGetUserSession_RoleDefaultsToReadonly(t *testing.T) {
	// Sessions created before the role field was added have no userRoleKey entry.
	// GetUserSession must fall back to "readonly" rather than returning empty string.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	// Write a session without a role field by temporarily using the old pattern.
	store := newStore(testKeys)
	session, _ := store.Get(req, cookieName)
	session.Values[userIDKey] = 7
	session.Values[userNameKey] = "Bob"
	session.Values[userEmailKey] = "bob@example.com"
	// intentionally omit userRoleKey
	_ = session.Save(req, rec)

	got, err := GetUserSession(testKeys, copyCookies(t, rec))
	if err != nil {
		t.Fatalf("GetUserSession: %v", err)
	}
	if got.Role != "readonly" {
		t.Errorf("expected default role %q, got %q", "readonly", got.Role)
	}
}

func TestGetUserSession_NoSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := GetUserSession(testKeys, req)
	if err != ErrNotAuthenticated {
		t.Errorf("want ErrNotAuthenticated, got %v", err)
	}
}

func TestClearSession_SetsDeleteHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	if err := SetUserSession(testKeys, req, rec, 1, "Bob", "bob@example.com", "readonly"); err != nil {
		t.Fatalf("SetUserSession: %v", err)
	}

	rec2 := httptest.NewRecorder()
	if err := ClearSession(testKeys, copyCookies(t, rec), rec2); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}

	var found bool
	for _, c := range rec2.Result().Cookies() {
		if c.Name == cookieName {
			found = true
			if c.MaxAge >= 0 {
				t.Errorf("expected MaxAge < 0 (browser delete), got %d", c.MaxAge)
			}
		}
	}
	if !found {
		t.Errorf("expected %q Set-Cookie header after ClearSession", cookieName)
	}
}

func TestCSRFRoundtrip(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	token, err := GetOrCreateCSRF(testKeys, req, rec)
	if err != nil {
		t.Fatalf("GetOrCreateCSRF: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty CSRF token")
	}

	if !ValidateCSRF(testKeys, copyCookies(t, rec), token) {
		t.Error("ValidateCSRF: expected true for correct token")
	}
}

func TestValidateCSRF_WrongToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	if _, err := GetOrCreateCSRF(testKeys, req, rec); err != nil {
		t.Fatalf("GetOrCreateCSRF: %v", err)
	}

	if ValidateCSRF(testKeys, copyCookies(t, rec), "wrong-token") {
		t.Error("ValidateCSRF: expected false for wrong token")
	}
}

func TestValidateCSRF_NoSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if ValidateCSRF(testKeys, req, "any-token") {
		t.Error("ValidateCSRF: expected false when no session exists")
	}
}

func TestGetOrCreateCSRF_Idempotent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	token1, err := GetOrCreateCSRF(testKeys, req, rec)
	if err != nil {
		t.Fatalf("first GetOrCreateCSRF: %v", err)
	}

	rec2 := httptest.NewRecorder()
	token2, err := GetOrCreateCSRF(testKeys, copyCookies(t, rec), rec2)
	if err != nil {
		t.Fatalf("second GetOrCreateCSRF: %v", err)
	}

	if token1 != token2 {
		t.Errorf("expected same token on re-call, got %q and %q", token1, token2)
	}
}

func TestOIDCLogin_StateVerifierAndNonceRoundTripTogether(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	want := OIDCLogin{State: "state-xyz", Verifier: "verifier-xyz", Nonce: "nonce-xyz"}
	if err := SetOIDCLogin(testKeys, req, rec, want); err != nil {
		t.Fatalf("SetOIDCLogin: %v", err)
	}

	rec2 := httptest.NewRecorder()
	got, err := GetAndClearOIDCLogin(testKeys, copyCookies(t, rec), rec2)
	if err != nil {
		t.Fatalf("GetAndClearOIDCLogin: %v", err)
	}
	// All three, not just the state: a verifier that failed to survive the
	// round trip turns the PKCE exchange into an unexplained 400 at the IdP,
	// and a lost nonce silently disables the replay check.
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestOIDCLogin_NoSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	_, err := GetAndClearOIDCLogin(testKeys, req, rec)
	if err == nil {
		t.Error("expected error when no OIDC login in session")
	}
}

func TestOIDCLogin_ClearedAfterGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	if err := SetOIDCLogin(testKeys, req, rec, OIDCLogin{State: "state-abc", Verifier: "v", Nonce: "n"}); err != nil {
		t.Fatalf("SetOIDCLogin: %v", err)
	}

	rec2 := httptest.NewRecorder()
	_, _ = GetAndClearOIDCLogin(testKeys, copyCookies(t, rec), rec2)

	rec3 := httptest.NewRecorder()
	_, err := GetAndClearOIDCLogin(testKeys, copyCookies(t, rec2), rec3)
	if err == nil {
		t.Error("expected error on second get — the attempt should have been cleared")
	}
}

// TestCookieSecure_FollowsConfig asserts the Secure attribute on the
// emitted Set-Cookie tracks SessionKeys.Secure independently of Dev. This
// is the regression class that locked HTTP-only AKS dev out: Secure=true
// causes the browser to silently drop the cookie on non-HTTPS connections.
func TestCookieSecure_FollowsConfig(t *testing.T) {
	cases := []struct {
		name   string
		keys   SessionKeys
		expect bool
	}{
		{"secure true", SessionKeys{HMACKey: "k", Secure: true}, true},
		{"secure false", SessionKeys{HMACKey: "k", Secure: false}, false},
		{"secure false with dev true", SessionKeys{HMACKey: "k", Dev: true, Secure: false}, false},
		{"secure true with dev true", SessionKeys{HMACKey: "k", Dev: true, Secure: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			if err := SetUserSession(tc.keys, req, rec, 1, "x", "x@x", "readonly"); err != nil {
				t.Fatalf("SetUserSession: %v", err)
			}
			cookies := rec.Result().Cookies()
			if len(cookies) == 0 {
				t.Fatal("no Set-Cookie header emitted")
			}
			if got := cookies[0].Secure; got != tc.expect {
				t.Errorf("Secure attribute: got %v, want %v", got, tc.expect)
			}
		})
	}
}
