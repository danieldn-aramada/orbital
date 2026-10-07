package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// A rejected bearer token used to leave a WARN and nothing else, so "who was
// refused, and why" was answerable from the browser login path and not from the
// API one. The sink is how the bearer path reaches the audit writer at all —
// internal/auth imports neither ent nor internal/handler on purpose, so a
// forgotten injection is silent and the refusal simply vanishes.
//
// The regression this guards: someone adds a denial branch and calls
// denyBearer directly instead of ps.deny, which compiles, refuses correctly,
// and records nothing.
func TestBearerDenial_ReachesTheAuditSink(t *testing.T) {
	var gotReasons []string
	ps := newSetWithSink(t, func(_ echo.Context, reason string) {
		gotReasons = append(gotReasons, reason)
	})

	// A token that is not a JWT at all — refused before any provider lookup, so
	// this exercises the sink without needing a signing issuer.
	rec, _ := callSink(t, ps, "not-a-jwt")

	if rec != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec)
	}
	if len(gotReasons) != 1 {
		t.Fatalf("sink called %d times, want exactly 1 — got %v", len(gotReasons), gotReasons)
	}
	// The reason is the whole point: seven denial paths land in one table, and
	// without it an operator cannot tell a misconfigured audience from a forged
	// token. It is deliberately NOT the caller-facing description, which says
	// less on purpose.
	if gotReasons[0] != "malformed_token" {
		t.Errorf("reason = %q, want %q", gotReasons[0], "malformed_token")
	}
}

// The negative. A request carrying no credential at all is an unauthenticated
// request, not an authentication failure — it fires for every anonymous probe,
// and the access log already records the 401. Auditing it would bury the real
// refusals in noise, which is the failure mode that trains people to ignore a
// signal.
func TestBearerDenial_NoCredentialIsNotAnAuditedFailure(t *testing.T) {
	var called int
	ps := newSetWithSink(t, func(_ echo.Context, _ string) { called++ })

	rec, _ := callSink(t, ps, "")

	if rec != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec)
	}
	if called != 0 {
		t.Errorf("sink called %d times for a request with no credential, want 0", called)
	}
}

// A nil sink is the normal case for unit tests and for the auth-disabled path,
// and must not panic — the audit write is an addition to authentication, never
// a precondition for it.
func TestBearerDenial_NilSinkDoesNotPanic(t *testing.T) {
	iss, _ := newTestOIDCServer(t)
	ps := newSet(t, specFor(iss, "aud-a"))
	if code, _ := callSink(t, ps, "not-a-jwt"); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func newSetWithSink(t *testing.T, sink func(echo.Context, string)) *ProviderSet {
	t.Helper()
	iss, _ := newTestOIDCServer(t)
	ps, err := NewProviderSet(context.Background(), []ProviderSpec{specFor(iss, "aud-a")},
		slog.Default(), WithAuditSink(sink))
	if err != nil {
		t.Fatalf("NewProviderSet: %v", err)
	}
	return ps
}

// callSink mirrors callWith but tolerates the handler returning an error, which
// is what a denial does.
func callSink(t *testing.T, ps *ProviderSet, token string) (int, echo.Context) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/datacenters", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	h := ps.RequireAuth()(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	if err := h(c); err != nil {
		var he *echo.HTTPError
		if ok := func() bool { he, _ = err.(*echo.HTTPError); return he != nil }(); ok {
			return he.Code, c
		}
		t.Fatalf("handler: %v", err)
	}
	return rec.Code, c
}
