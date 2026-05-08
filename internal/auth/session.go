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
	cookieName   = "orbital_session"
	userIDKey    = "user_id"
	userNameKey  = "user_name"
	userEmailKey = "user_email"
	csrfKey      = "csrf_token"
	oidcStateKey = "oidc_state"
)

var ErrNotAuthenticated = errors.New("not authenticated")

func newStore(secret string) *sessions.CookieStore {
	s := sessions.NewCookieStore([]byte(secret))
	s.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	return s
}

func SetUserSession(secret string, r *http.Request, w http.ResponseWriter, id int, name, email string) error {
	store := newStore(secret)
	session, err := store.Get(r, cookieName)
	if err != nil {
		session, _ = store.New(r, cookieName)
	}
	session.Values[userIDKey] = id
	session.Values[userNameKey] = name
	session.Values[userEmailKey] = email
	return session.Save(r, w)
}

// SetUserID is kept for callers that don't have name/email available.
func SetUserID(secret string, r *http.Request, w http.ResponseWriter, id int) error {
	return SetUserSession(secret, r, w, id, "", "")
}

type UserSession struct {
	ID    int
	Name  string
	Email string
}

func GetUserSession(secret string, r *http.Request) (UserSession, error) {
	store := newStore(secret)
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
	return UserSession{ID: id, Name: name, Email: email}, nil
}

func GetUserID(secret string, r *http.Request) (int, error) {
	u, err := GetUserSession(secret, r)
	return u.ID, err
}

func ClearSession(secret string, r *http.Request, w http.ResponseWriter) error {
	store := newStore(secret)
	session, err := store.Get(r, cookieName)
	if err != nil {
		return nil
	}
	session.Options.MaxAge = -1
	return session.Save(r, w)
}

// GetOrCreateCSRF returns the CSRF token for the current session, creating one
// if it doesn't exist yet. The token is stored in the session cookie.
func GetOrCreateCSRF(secret string, r *http.Request, w http.ResponseWriter) (string, error) {
	store := newStore(secret)
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
	token := base64.StdEncoding.EncodeToString(b)
	session.Values[csrfKey] = token
	if err := session.Save(r, w); err != nil {
		return "", err
	}
	return token, nil
}

// SetOIDCState stores a random state value in the session for OIDC callback verification.
func SetOIDCState(secret string, r *http.Request, w http.ResponseWriter, state string) error {
	store := newStore(secret)
	session, err := store.Get(r, cookieName)
	if err != nil {
		session, _ = store.New(r, cookieName)
	}
	session.Values[oidcStateKey] = state
	return session.Save(r, w)
}

// GetAndClearOIDCState returns the stored OIDC state and removes it from the session.
func GetAndClearOIDCState(secret string, r *http.Request, w http.ResponseWriter) (string, error) {
	store := newStore(secret)
	session, err := store.Get(r, cookieName)
	if err != nil {
		return "", errors.New("no session")
	}
	state, ok := session.Values[oidcStateKey].(string)
	if !ok || state == "" {
		return "", errors.New("no oidc state in session")
	}
	delete(session.Values, oidcStateKey)
	session.Save(r, w) //nolint:errcheck
	return state, nil
}

// ValidateCSRF compares the submitted token against the one stored in the session.
func ValidateCSRF(secret string, r *http.Request, submitted string) bool {
	store := newStore(secret)
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
