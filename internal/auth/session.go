package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"

	"github.com/gorilla/sessions"
)

const (
	cookieName      = "orbital_session"
	userIDKey       = "user_id"
	userNameKey     = "user_name"
	userEmailKey    = "user_email"
	userRoleKey     = "user_role"
	csrfKey         = "csrf_token"
	oidcStateKey    = "oidc_state"
	oidcVerifierKey = "oidc_verifier"
	oidcNonceKey    = "oidc_nonce"
)

var ErrNotAuthenticated = errors.New("not authenticated")

// SessionKeys holds the HMAC signing key and optional AES-256 encryption key
// for the session cookie. HMACKey is required. EncryptionKey must be exactly
// 32 bytes when set — if empty, cookie contents are signed but not encrypted.
//
// Secure controls the cookie's Secure attribute independently of Dev. Default
// in production is true (HTTPS-only). The HTTP-only AKS dev cluster sets it
// false explicitly so the browser will accept the cookie over plain HTTP;
// when AKS dev gains TLS this override comes back out.
//
// Construct with NewSessionKeys so the underlying cookie store is built once
// and reused across requests. Zero-value SessionKeys (e.g. in tests) still
// work — each auth function falls back to building its own store if store is nil.
type SessionKeys struct {
	HMACKey       string
	EncryptionKey string // 32 bytes for AES-256; empty = no encryption
	Dev           bool   // true in local dev; relaxes other dev-only behavior unrelated to cookies
	Secure        bool   // true = Secure cookie attribute set (HTTPS-only)
	store         *sessions.CookieStore
}

// NewSessionKeys constructs a SessionKeys with a pre-built cookie store so the
// store is created once at startup and shared across all requests.
func NewSessionKeys(hmacKey, encryptionKey string, dev, secure bool) SessionKeys {
	keys := SessionKeys{
		HMACKey:       hmacKey,
		EncryptionKey: encryptionKey,
		Dev:           dev,
		Secure:        secure,
	}
	keys.store = newStore(keys)
	return keys
}

func newStore(keys SessionKeys) *sessions.CookieStore {
	var s *sessions.CookieStore
	if keys.EncryptionKey != "" {
		s = sessions.NewCookieStore([]byte(keys.HMACKey), []byte(keys.EncryptionKey))
	} else {
		s = sessions.NewCookieStore([]byte(keys.HMACKey))
	}
	s.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   keys.Secure,
	}
	return s
}

func getStore(keys SessionKeys) *sessions.CookieStore {
	if keys.store != nil {
		return keys.store
	}
	return newStore(keys)
}

func SetUserSession(keys SessionKeys, r *http.Request, w http.ResponseWriter, id int, name, email, role string) error {
	store := getStore(keys)
	session, err := store.Get(r, cookieName)
	if err != nil {
		session, _ = store.New(r, cookieName)
	}
	session.Values[userIDKey] = id
	session.Values[userNameKey] = name
	session.Values[userEmailKey] = email
	session.Values[userRoleKey] = role
	return session.Save(r, w)
}

type UserSession struct {
	ID    int
	Name  string
	Email string
	Role  string // "readonly", "dev", or "admin"; empty string treated as "readonly"
}

func GetUserSession(keys SessionKeys, r *http.Request) (UserSession, error) {
	store := getStore(keys)
	session, err := store.Get(r, cookieName)
	if err != nil {
		return UserSession{}, ErrNotAuthenticated
	}
	id, ok := session.Values[userIDKey].(int)
	if !ok || id == 0 {
		return UserSession{}, ErrNotAuthenticated
	}
	name, _ := session.Values[userNameKey].(string)
	email, _ := session.Values[userEmailKey].(string)
	role, _ := session.Values[userRoleKey].(string)
	if role == "" {
		role = "readonly" // safe default for sessions that predate this field
	}
	return UserSession{ID: id, Name: name, Email: email, Role: role}, nil
}

func GetUserID(keys SessionKeys, r *http.Request) (int, error) {
	u, err := GetUserSession(keys, r)
	return u.ID, err
}

func ClearSession(keys SessionKeys, r *http.Request, w http.ResponseWriter) error {
	store := getStore(keys)
	session, err := store.Get(r, cookieName)
	if err != nil {
		return nil
	}
	session.Options.MaxAge = -1
	return session.Save(r, w)
}

// GetOrCreateCSRF returns the CSRF token for the current session, creating one
// if it doesn't exist yet. The token is stored in the session cookie.
func GetOrCreateCSRF(keys SessionKeys, r *http.Request, w http.ResponseWriter) (string, error) {
	store := getStore(keys)
	session, err := store.Get(r, cookieName)
	if err != nil {
		session, _ = store.New(r, cookieName)
	}
	if token, ok := session.Values[csrfKey].(string); ok && token != "" {
		return token, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// RawURLEncoding is URL-safe (no `+`/`/`/`=`) so the token doesn't get
	// HTML-entity-encoded when rendered inside a template attribute. Clients
	// (curl, CLI scripts) can read the token verbatim without HTML decoding.
	token := base64.RawURLEncoding.EncodeToString(b)
	session.Values[csrfKey] = token
	if err := session.Save(r, w); err != nil {
		return "", err
	}
	return token, nil
}

// OIDCLogin is the per-attempt state a browser login carries across the redirect
// to the provider:
//
//   - State    — CSRF protection on the callback.
//   - Verifier — the PKCE code_verifier. OAuth 2.1 requires PKCE for ALL clients,
//     confidential included: it binds the code to this attempt, so an
//     intercepted code cannot be redeemed by anyone else.
//   - Nonce    — binds the returned ID token to this attempt, which is what makes
//     a replayed token detectable.
//
// Stored and cleared as ONE record. Clearing the state while leaving the
// verifier or nonce behind is how a stale value gets reused on a later attempt.
type OIDCLogin struct {
	State    string
	Verifier string
	Nonce    string
}

// SetOIDCLogin stores the attempt in the session for the callback to check.
func SetOIDCLogin(keys SessionKeys, r *http.Request, w http.ResponseWriter, l OIDCLogin) error {
	store := getStore(keys)
	session, err := store.Get(r, cookieName)
	if err != nil {
		session, _ = store.New(r, cookieName)
	}
	session.Values[oidcStateKey] = l.State
	session.Values[oidcVerifierKey] = l.Verifier
	session.Values[oidcNonceKey] = l.Nonce
	return session.Save(r, w)
}

// GetAndClearOIDCLogin returns the stored attempt and removes all of it from the
// session, so a second callback carrying the same state finds nothing.
func GetAndClearOIDCLogin(keys SessionKeys, r *http.Request, w http.ResponseWriter) (OIDCLogin, error) {
	store := getStore(keys)
	session, err := store.Get(r, cookieName)
	if err != nil {
		return OIDCLogin{}, errors.New("no session")
	}
	state, ok := session.Values[oidcStateKey].(string)
	if !ok || state == "" {
		return OIDCLogin{}, errors.New("no oidc state in session")
	}
	verifier, _ := session.Values[oidcVerifierKey].(string)
	nonce, _ := session.Values[oidcNonceKey].(string)
	delete(session.Values, oidcStateKey)
	delete(session.Values, oidcVerifierKey)
	delete(session.Values, oidcNonceKey)
	session.Save(r, w) //nolint:errcheck
	return OIDCLogin{State: state, Verifier: verifier, Nonce: nonce}, nil
}

// ValidateCSRF compares the submitted token against the one stored in the session.
func ValidateCSRF(keys SessionKeys, r *http.Request, submitted string) bool {
	store := getStore(keys)
	session, err := store.Get(r, cookieName)
	if err != nil {
		return false
	}
	stored, ok := session.Values[csrfKey].(string)
	if !ok || stored == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(submitted)) == 1
}
