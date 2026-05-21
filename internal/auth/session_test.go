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
	if err := SetUserSession(testKeys, req, rec, 42, "Alice", "alice@example.com"); err != nil {
		t.Fatalf("SetUserSession: %v", err)
	}

	got, err := GetUserSession(testKeys, copyCookies(t, rec))
	if err != nil {
		t.Fatalf("GetUserSession: %v", err)
	}
	if got.ID != 42 || got.Name != "Alice" || got.Email != "alice@example.com" {
		t.Errorf("got %+v, want {42, Alice, alice@example.com}", got)
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
	if err := SetUserSession(testKeys, req, rec, 1, "Bob", "bob@example.com"); err != nil {
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

func TestOIDCState_SetAndGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	if err := SetOIDCState(testKeys, req, rec, "state-xyz"); err != nil {
		t.Fatalf("SetOIDCState: %v", err)
	}

	rec2 := httptest.NewRecorder()
	state, err := GetAndClearOIDCState(testKeys, copyCookies(t, rec), rec2)
	if err != nil {
		t.Fatalf("GetAndClearOIDCState: %v", err)
	}
	if state != "state-xyz" {
		t.Errorf("got state %q, want %q", state, "state-xyz")
	}
}

func TestOIDCState_NoSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	_, err := GetAndClearOIDCState(testKeys, req, rec)
	if err == nil {
		t.Error("expected error when no OIDC state in session")
	}
}

func TestOIDCState_ClearedAfterGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	if err := SetOIDCState(testKeys, req, rec, "state-abc"); err != nil {
		t.Fatalf("SetOIDCState: %v", err)
	}

	rec2 := httptest.NewRecorder()
	_, _ = GetAndClearOIDCState(testKeys, copyCookies(t, rec), rec2)

	rec3 := httptest.NewRecorder()
	_, err := GetAndClearOIDCState(testKeys, copyCookies(t, rec2), rec3)
	if err == nil {
		t.Error("expected error on second get — state should have been cleared")
	}
}
