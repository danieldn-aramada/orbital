//go:build integration

package handler_test

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/auditevent"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

var loginTestKeys = auth.SessionKeys{HMACKey: "test-login-hmac-key"}

// loginFormTmpl renders just the error message so tests can assert on it.
var loginFormTmpl = template.Must(
	template.New("login-form.gohtml").Parse(`error:{{.ErrorMsg}}`),
)

func newLoginHandler() *handler.Login {
	return handler.NewLogin(testDB, loginTestKeys, loginFormTmpl, "", slog.Default())
}

// sessionWithCSRF creates a fresh CSRF token in a session cookie and returns
// the cookie jar (for the next request) and the token value.
func sessionWithCSRF(t *testing.T) ([]*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	token, err := auth.GetOrCreateCSRF(loginTestKeys, req, rec)
	if err != nil {
		t.Fatalf("GetOrCreateCSRF: %v", err)
	}
	return rec.Result().Cookies(), token
}

// postLogin builds an Echo context for POST /user/login with the given form values
// and the provided session cookies.
func postLogin(t *testing.T, cookies []*http.Cookie, form url.Values) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	body := strings.NewReader(form.Encode())
	req := httptest.NewRequest(http.MethodPost, "/user/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// hashPassword returns a bcrypt hash suitable for storing in ent.
func hashPassword(t *testing.T, password string) string {
	t.Helper()
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(b)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestLogin_CSRFFailure(t *testing.T) {
	h := newLoginHandler()
	cookies, _ := sessionWithCSRF(t)

	c, rec := postLogin(t, cookies, url.Values{
		"email":    {"someone@example.com"},
		"password": {"password"},
		"csrf":     {"wrong-csrf-token"},
	})

	if err := h.Post(c); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "Invalid request") {
		t.Errorf("expected 'Invalid request' in body, got: %s", rec.Body.String())
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Error("should not redirect on CSRF failure")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	ctx := context.Background()
	hash := hashPassword(t, "correct-password")
	u := testDB.User.Create().
		SetEmail("wrongpw@example.com").
		SetName("Wrong PW").
		SetPreferredUsername("wrongpw@example.com").
		SetPasswordHash(hash).
		SaveX(ctx)
	t.Cleanup(func() { testDB.User.DeleteOne(u).ExecX(ctx) })

	h := newLoginHandler()
	cookies, csrf := sessionWithCSRF(t)

	c, rec := postLogin(t, cookies, url.Values{
		"email":    {"wrongpw@example.com"},
		"password": {"wrong-password"},
		"csrf":     {csrf},
	})

	if err := h.Post(c); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "Invalid email or password") {
		t.Errorf("expected 'Invalid email or password' in body, got: %s", rec.Body.String())
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Error("should not redirect on wrong password")
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	h := newLoginHandler()
	cookies, csrf := sessionWithCSRF(t)

	c, rec := postLogin(t, cookies, url.Values{
		"email":    {"nobody@example.com"},
		"password": {"password"},
		"csrf":     {csrf},
	})

	if err := h.Post(c); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "Invalid email or password") {
		t.Errorf("expected 'Invalid email or password' in body, got: %s", rec.Body.String())
	}
}

func TestLogin_SSOAccount(t *testing.T) {
	ctx := context.Background()
	// SSO user: no password hash
	u := testDB.User.Create().
		SetEmail("sso@example.com").
		SetName("SSO User").
		SetPreferredUsername("sso@example.com").
		SaveX(ctx)
	t.Cleanup(func() { testDB.User.DeleteOne(u).ExecX(ctx) })

	h := newLoginHandler()
	cookies, csrf := sessionWithCSRF(t)

	c, rec := postLogin(t, cookies, url.Values{
		"email":    {"sso@example.com"},
		"password": {"any-password"},
		"csrf":     {csrf},
	})

	if err := h.Post(c); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "SSO login") {
		t.Errorf("expected 'SSO login' in body, got: %s", rec.Body.String())
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Error("should not redirect for SSO account")
	}
}

func TestLogin_Success(t *testing.T) {
	ctx := context.Background()
	hash := hashPassword(t, "securepassword")
	u := testDB.User.Create().
		SetEmail("success@example.com").
		SetName("Success User").
		SetPreferredUsername("success@example.com").
		SetPasswordHash(hash).
		SaveX(ctx)
	t.Cleanup(func() { testDB.User.DeleteOne(u).ExecX(ctx) })

	h := newLoginHandler()
	cookies, csrf := sessionWithCSRF(t)

	c, rec := postLogin(t, cookies, url.Values{
		"email":    {"success@example.com"},
		"password": {"securepassword"},
		"csrf":     {csrf},
	})

	if err := h.Post(c); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !strings.HasSuffix(rec.Header().Get("HX-Redirect"), "/?fresh=1") {
		t.Errorf("expected HX-Redirect to /?fresh=1, got: %q", rec.Header().Get("HX-Redirect"))
	}
}

func TestLogout_ValidCSRF(t *testing.T) {
	h := newLoginHandler()
	cookies, csrf := sessionWithCSRF(t)

	e := echo.New()
	body := strings.NewReader(url.Values{"csrf": {csrf}}.Encode())
	req := httptest.NewRequest(http.MethodPost, "/user/logout", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Logout(c); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
}

func TestLogout_InvalidCSRF(t *testing.T) {
	h := newLoginHandler()
	cookies, _ := sessionWithCSRF(t)

	e := echo.New()
	body := strings.NewReader(url.Values{"csrf": {"bad-csrf"}}.Encode())
	req := httptest.NewRequest(http.MethodPost, "/user/logout", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Logout(c); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	// Invalid CSRF still redirects (graceful degradation)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", rec.Code)
	}
}

// ── Audit event tests ─────────────────────────────────────────────────────────

func TestLogin_Success_WritesAuditEvent(t *testing.T) {
	ctx := context.Background()
	const email = "audit-login-success@example.com"
	hash := hashPassword(t, "pass")
	u := testDB.User.Create().
		SetEmail(email).
		SetName("Audit User").
		SetPreferredUsername(email).
		SetPasswordHash(hash).
		SaveX(ctx)
	t.Cleanup(func() {
		testDB.User.DeleteOne(u).ExecX(ctx)
		testDB.AuditEvent.Delete().Where(auditevent.Actor(email)).ExecX(ctx)
	})

	h := newLoginHandler()
	cookies, csrf := sessionWithCSRF(t)
	c, _ := postLogin(t, cookies, url.Values{"email": {email}, "password": {"pass"}, "csrf": {csrf}})

	if err := h.Post(c); err != nil {
		t.Fatalf("Post: %v", err)
	}

	// Give writeAuditEvent (async-ish via goroutine-free path) time to commit.
	time.Sleep(50 * time.Millisecond)

	ev, err := testDB.AuditEvent.Query().Where(auditevent.Actor(email)).Only(ctx)
	if err != nil {
		t.Fatalf("audit event not found: %v", err)
	}
	if len(ev.Operations) == 0 || ev.Operations[0] != "loginSuccess" {
		t.Errorf("expected operation loginSuccess, got %v", ev.Operations)
	}
	if ev.EventCategory != "auth" {
		t.Errorf("expected category auth, got %q", ev.EventCategory)
	}
}

func TestLogin_WrongPassword_WritesAuditEvent(t *testing.T) {
	ctx := context.Background()
	const email = "audit-login-fail@example.com"
	hash := hashPassword(t, "correct")
	u := testDB.User.Create().
		SetEmail(email).
		SetName("Audit Fail User").
		SetPreferredUsername(email).
		SetPasswordHash(hash).
		SaveX(ctx)
	t.Cleanup(func() {
		testDB.User.DeleteOne(u).ExecX(ctx)
		testDB.AuditEvent.Delete().Where(auditevent.Actor(email)).ExecX(ctx)
	})

	h := newLoginHandler()
	cookies, csrf := sessionWithCSRF(t)
	c, _ := postLogin(t, cookies, url.Values{"email": {email}, "password": {"wrong"}, "csrf": {csrf}})

	if err := h.Post(c); err != nil {
		t.Fatalf("Post: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	ev, err := testDB.AuditEvent.Query().Where(auditevent.Actor(email)).Only(ctx)
	if err != nil {
		t.Fatalf("audit event not found: %v", err)
	}
	if len(ev.Operations) == 0 || ev.Operations[0] != "loginFailed" {
		t.Errorf("expected operation loginFailed, got %v", ev.Operations)
	}
}
