//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/armada/orbital/ent/auditevent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

// RequireRole has five ways to refuse, and until 2026-10-06 exactly one of them
// wrote an audit row. So "who was refused" depended on WHICH way they were
// refused — a caller rejected for being unauthenticated, for being an app
// principal, or for carrying a delegated role below the bar left no trace at
// all, while a caller rejected on their user row did. A refusal record with
// that shape is worse than none, because it reads as complete.
//
// These drive the middleware directly: the branch is selected by what is on the
// context, and that is the only thing that distinguishes them.
func TestAuthzDenial_EveryRefusalBranchIsRecorded(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name       string
		actor      string
		wantReason string
		setup      func(c echo.Context)
	}{
		{
			// A delegated-authorization caller carries a pre-mapped role and
			// never touches the users table, so the user-row branch that used to
			// be the only audited one cannot see it.
			name:       "delegated role below required",
			actor:      "delegated@authzcov.com",
			wantReason: "context_role_below_required",
			setup: func(c echo.Context) {
				c.Set("role", string(user.RoleReadonly))
				c.Set("user_email", "delegated@authzcov.com")
			},
		},
		{
			// No identity at all. Still a 403, still a security event under
			// orbital's own second admission criterion.
			name:       "unauthenticated",
			actor:      "",
			wantReason: "unauthenticated",
			setup:      func(c echo.Context) {},
		},
		{
			// Authenticated against a user id that no longer resolves.
			name:       "user not found",
			actor:      "",
			wantReason: "user_not_found",
			setup:      func(c echo.Context) { c.Set("user_id", 999999999) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				testDB.AuditEvent.Delete().
					Where(auditevent.Actor(tc.actor), auditevent.EventCategory("management")).
					ExecX(ctx)
			})
			before := countDenials(t, tc.wantReason)

			mw := handler.RequireAdmin(testDB)
			h := mw(func(c echo.Context) error { return c.String(http.StatusOK, "reached") })

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/users/1/role", nil)
			c := e.NewContext(req, httptest.NewRecorder())
			tc.setup(c)

			if err := h(c); err == nil {
				t.Fatal("expected a 403, got nil — the branch under test was not reached")
			}

			// writeAuditEvent commits on its own connection; give it a moment.
			var after int
			for i := 0; i < 20; i++ {
				time.Sleep(25 * time.Millisecond)
				if after = countDenials(t, tc.wantReason); after > before {
					break
				}
			}
			if after != before+1 {
				t.Errorf("authorizationDenied events with reason %q: %d -> %d, want exactly one more",
					tc.wantReason, before, after)
			}
		})
	}
}

// The negative. A permitted request must leave no refusal behind — a denial
// signal that fires when nothing was denied trains whoever reads the table to
// ignore the operation entirely, which is the failure mode this whole change
// exists to fix.
func TestAuthzDenial_AllowedRequestRecordsNoRefusal(t *testing.T) {
	const email = "allowed@authzcov.com"
	userID := createTestUser(t, email, user.RoleAdmin)
	ctx := context.Background()
	t.Cleanup(func() { testDB.AuditEvent.Delete().Where(auditevent.Actor(email)).ExecX(ctx) })

	before := countDenials(t, "user_role_below_required")

	mw := handler.RequireAdmin(testDB)
	reached := false
	h := mw(func(c echo.Context) error { reached = true; return c.String(http.StatusOK, "reached") })

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/1/role", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	c.Set("user_id", userID)

	if err := h(c); err != nil {
		t.Fatalf("admin was refused: %v", err)
	}
	if !reached {
		t.Fatal("handler not reached")
	}

	time.Sleep(100 * time.Millisecond)
	if after := countDenials(t, "user_role_below_required"); after != before {
		t.Errorf("a permitted request wrote %d refusal event(s)", after-before)
	}
}

// countDenials counts authorizationDenied events carrying a given reason.
// Reads details rather than filtering in SQL: `reason` is the field that makes
// the five branches distinguishable, and asserting on it is the point.
func countDenials(t *testing.T, reason string) int {
	t.Helper()
	evs := testDB.AuditEvent.Query().
		Where(auditevent.EventCategory("management")).
		AllX(context.Background())

	n := 0
	for _, ev := range evs {
		var d struct {
			Reason string `json:"reason"`
		}
		if len(ev.Details) == 0 {
			continue
		}
		if err := json.Unmarshal(ev.Details, &d); err != nil {
			continue
		}
		if d.Reason == reason && len(ev.Operations) > 0 && ev.Operations[0] == "authorizationDenied" {
			n++
		}
	}
	return n
}
