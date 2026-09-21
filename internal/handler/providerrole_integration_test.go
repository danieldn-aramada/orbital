//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/auditevent"
	"github.com/armada/orbital/ent/auditeventresource"
	"github.com/armada/orbital/ent/auditeventresourcetype"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

// Spike 26 phase-2 acceptance items 1, 2 and 7. Names transcribed from the list.

// resolveWith drives ResolveUser with the context keys a ProviderSet would set.
func resolveWith(t *testing.T, email string, ctxKeys map[string]any) {
	t.Helper()
	e := echo.New()
	mw := handler.ResolveUser(testDB, nil)
	h := mw(func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-log", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	c.Set("user_id", 0)
	c.Set("user_email", email)
	for k, v := range ctxKeys {
		c.Set(k, v)
	}
	if err := h(c); err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
}

func roleOf(t *testing.T, email string) user.Role {
	t.Helper()
	u, err := testDB.User.Query().Where(user.Email(email)).Only(context.Background())
	if err != nil {
		t.Fatalf("user %s not found: %v", email, err)
	}
	return u.Role
}

func TestProviderRole_ModeADefaultSeedsNewUserAndLeavesExistingAlone(t *testing.T) {
	const email = "modea@providerrole.test"
	ctx := context.Background()
	t.Cleanup(func() { testDB.User.Delete().Where(user.Email(email)).ExecX(ctx) })

	resolveWith(t, email, map[string]any{"provider_default_role": "readonly"})
	if got := roleOf(t, email); got != user.RoleReadonly {
		t.Fatalf("new user role = %s, want readonly", got)
	}

	// An admin promotes them in orbital.
	testDB.User.Update().Where(user.Email(email)).SetRole(user.RoleDev).ExecX(ctx)

	// They log in again. Mode A must not touch the stored role — this is the
	// joe-keeps-dev case that motivated the two-mode split.
	resolveWith(t, email, map[string]any{"provider_default_role": "readonly"})
	if got := roleOf(t, email); got != user.RoleDev {
		t.Errorf("after re-login role = %s, want dev — mode A must never overwrite the table", got)
	}
}

func TestProviderRole_ModeBAppliesGroupRoleAtEveryLogin(t *testing.T) {
	const email = "modeb@providerrole.test"
	ctx := context.Background()
	t.Cleanup(func() {
		testDB.User.Delete().Where(user.Email(email)).ExecX(ctx)
		purgeAuditFor(t, email)
	})

	resolveWith(t, email, map[string]any{
		"provider_role": "dev", "provider_role_group": "platform-engineers",
		"auth_issuer": "https://kc.test/realms/x",
	})
	if got := roleOf(t, email); got != user.RoleDev {
		t.Fatalf("new user role = %s, want dev", got)
	}

	// Promoted in the IdP: the group changes, and the provider wins.
	resolveWith(t, email, map[string]any{
		"provider_role": "admin", "provider_role_group": "orbital-admins",
		"auth_issuer": "https://kc.test/realms/x",
	})
	if got := roleOf(t, email); got != user.RoleAdmin {
		t.Errorf("role = %s, want admin — mode B is authoritative at every login", got)
	}

	// Removed from the group in the IdP: revocation must actually revoke.
	resolveWith(t, email, map[string]any{
		"provider_role": "readonly", "provider_role_group": "everyone",
		"auth_issuer": "https://kc.test/realms/x",
	})
	if got := roleOf(t, email); got != user.RoleReadonly {
		t.Errorf("role = %s, want readonly — losing a group must demote", got)
	}
}

func TestProviderRole_RoleChangeIsAuditedAndAnUnchangedRoleIsNot(t *testing.T) {
	const email = "audit@providerrole.test"
	ctx := context.Background()
	t.Cleanup(func() {
		testDB.User.Delete().Where(user.Email(email)).ExecX(ctx)
		purgeAuditFor(t, email)
	})

	// Provision, then change the role once, then log in twice more unchanged.
	resolveWith(t, email, map[string]any{"provider_role": "dev", "provider_role_group": "platform-engineers", "auth_issuer": "https://kc.test/realms/x"})
	resolveWith(t, email, map[string]any{"provider_role": "admin", "provider_role_group": "orbital-admins", "auth_issuer": "https://kc.test/realms/x"})
	resolveWith(t, email, map[string]any{"provider_role": "admin", "provider_role_group": "orbital-admins", "auth_issuer": "https://kc.test/realms/x"})
	resolveWith(t, email, map[string]any{"provider_role": "admin", "provider_role_group": "orbital-admins", "auth_issuer": "https://kc.test/realms/x"})

	// Read back through the consumer API, not the write path: the guarantee is
	// that an operator can find this in GET /api/v1/audit-log.
	all := testDB.AuditEvent.Query().Where(auditevent.Actor(email)).AllX(ctx)
	var events []*ent.AuditEvent
	for _, e := range all {
		for _, op := range e.Operations {
			if op == "providerRoleChange" {
				events = append(events, e)
			}
		}
	}

	if len(events) != 1 {
		t.Fatalf("got %d providerRoleChange events, want exactly 1 — audit the transition, not the evaluation", len(events))
	}
	var d map[string]any
	if err := json.Unmarshal(events[0].Details, &d); err != nil {
		t.Fatalf("details did not survive encode+decode: %v", err)
	}
	for k, want := range map[string]string{
		"before": "dev", "after": "admin",
		"provider": "https://kc.test/realms/x", "group": "orbital-admins",
	} {
		if got, _ := d[k].(string); got != want {
			t.Errorf("details[%q] = %q, want %q", k, got, want)
		}
	}
}

// purgeAuditFor removes this test's audit events and their child resource rows.
// Children first: audit_event_resources carries an FK to audit_events, so
// deleting the parent alone fails.
func purgeAuditFor(t *testing.T, actor string) {
	t.Helper()
	ctx := context.Background()
	evs := testDB.AuditEvent.Query().Where(auditevent.Actor(actor)).AllX(ctx)
	for _, e := range evs {
		testDB.AuditEventResource.Delete().Where(auditeventresource.AuditEventID(e.ID)).ExecX(ctx)
		testDB.AuditEventResourceType.Delete().Where(auditeventresourcetype.AuditEventID(e.ID)).ExecX(ctx)
	}
	testDB.AuditEvent.Delete().Where(auditevent.Actor(actor)).ExecX(ctx)
}

// Identity ownership: a provider may only resolve rows it owns. It never claims
// an existing row, so no configured provider can mint a token for an address
// that already belongs to a local account or a different provider.

func resolveExpectingRefusal(t *testing.T, email string, ctxKeys map[string]any) {
	t.Helper()
	e := echo.New()
	mw := handler.ResolveUser(testDB, nil)
	h := mw(func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-log", nil)
	c := e.NewContext(req, httptest.NewRecorder())
	c.Set("user_id", 0)
	c.Set("user_email", email)
	for k, v := range ctxKeys {
		c.Set(k, v)
	}
	err := h(c)
	if err == nil {
		t.Fatal("expected refusal, got success — the row was claimed")
	}
	if he, ok := err.(*echo.HTTPError); !ok || he.Code != http.StatusUnauthorized {
		t.Fatalf("got %v, want 401", err)
	}
}

func TestIdentityOwnership_LocalAccountCannotBeClaimedByAProvider(t *testing.T) {
	const email = "breakglass@ownership.test"
	ctx := context.Background()
	t.Cleanup(func() { testDB.User.Delete().Where(user.Email(email)).ExecX(ctx) })

	// A local account: password set, no issuer. This is the break-glass shape.
	testDB.User.Create().SetEmail(email).SetName("Break Glass").
		SetPreferredUsername(email).SetVerified(true).SetRole(user.RoleAdmin).
		SetPasswordHash("not-a-real-hash").SaveX(ctx)

	resolveExpectingRefusal(t, email, map[string]any{
		"auth_issuer": "https://evil.example.com", "provider_default_role": "readonly",
	})
	if got := roleOf(t, email); got != user.RoleAdmin {
		t.Errorf("role = %s, want admin unchanged — a refused login must not modify the row", got)
	}
}

func TestIdentityOwnership_SecondProviderCannotClaimAnotherProvidersUser(t *testing.T) {
	const email = "shared@ownership.test"
	ctx := context.Background()
	t.Cleanup(func() { testDB.User.Delete().Where(user.Email(email)).ExecX(ctx) })

	// Provider A creates the user as admin.
	resolveWith(t, email, map[string]any{
		"auth_issuer":   "https://a.example.com",
		"provider_role": "admin", "provider_role_group": "admins",
	})
	if got := roleOf(t, email); got != user.RoleAdmin {
		t.Fatalf("setup: role = %s, want admin", got)
	}

	// Provider B asserts the same address. Without this refusal it would inherit
	// provider A's user and their admin role.
	resolveExpectingRefusal(t, email, map[string]any{
		"auth_issuer":   "https://b.example.com",
		"provider_role": "admin", "provider_role_group": "admins",
	})
}

func TestIdentityOwnership_SameProviderResolvesItsOwnUserNormally(t *testing.T) {
	// The positive control: the two refusals above would both pass against an
	// implementation that refused everything.
	const email = "owned@ownership.test"
	ctx := context.Background()
	t.Cleanup(func() { testDB.User.Delete().Where(user.Email(email)).ExecX(ctx) })

	keys := map[string]any{
		"auth_issuer":   "https://a.example.com",
		"provider_role": "dev", "provider_role_group": "engineers",
	}
	resolveWith(t, email, keys)
	resolveWith(t, email, keys) // second login must still work
	if got := roleOf(t, email); got != user.RoleDev {
		t.Errorf("role = %s, want dev", got)
	}
}

func TestIdentityOwnership_UnownedRowWithNoPasswordIsClaimedOnFirstLogin(t *testing.T) {
	// The migration path: rows predating the issuer column have neither an
	// issuer nor a password. Without this they would be locked out of SSO until
	// an admin backfilled every one.
	const email = "legacy@ownership.test"
	ctx := context.Background()
	t.Cleanup(func() { testDB.User.Delete().Where(user.Email(email)).ExecX(ctx) })

	testDB.User.Create().SetEmail(email).SetName("Legacy").
		SetPreferredUsername(email).SetVerified(true).SetRole(user.RoleDev).SaveX(ctx)

	resolveWith(t, email, map[string]any{
		"auth_issuer": "https://a.example.com", "provider_default_role": "readonly",
	})

	u := testDB.User.Query().Where(user.Email(email)).OnlyX(ctx)
	if u.Issuer == nil || *u.Issuer != "https://a.example.com" {
		t.Fatalf("issuer = %v, want the claiming provider", u.Issuer)
	}
	if u.Role != user.RoleDev {
		t.Errorf("role = %s, want dev unchanged — claiming records ownership, it does not reassign", u.Role)
	}

	// Now owned: a different provider is refused.
	resolveExpectingRefusal(t, email, map[string]any{
		"auth_issuer": "https://b.example.com", "provider_default_role": "readonly",
	})
}
