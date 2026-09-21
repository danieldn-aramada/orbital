package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// Names transcribed from the Spike 26 phase-1 acceptance list, items 4-7.

func specFor(issuer, aud string) ProviderSpec {
	return ProviderSpec{
		IssuerURL:     issuer,
		Audiences:     []string{aud},
		UsernameClaim: "email",
		DefaultRole:   "readonly",
	}
}

func newSet(t *testing.T, specs ...ProviderSpec) *ProviderSet {
	t.Helper()
	ps, err := NewProviderSet(context.Background(), specs, slog.Default())
	if err != nil {
		t.Fatalf("NewProviderSet: %v", err)
	}
	return ps
}

func callWith(t *testing.T, ps *ProviderSet, token string) (int, echo.Context) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c, rec := echoCtx(req)
	h := ps.RequireAuth()(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	if err := h(c); err != nil {
		t.Fatalf("handler: %v", err)
	}
	return rec.Code, c
}

func claimsFor(iss, aud, email string) map[string]any {
	return map[string]any{
		"iss": iss, "aud": aud, "sub": "u1", "email": email,
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}
}

func TestProviderSet_TokenFromEachConfiguredProviderIsAccepted(t *testing.T) {
	issA, signA := newTestOIDCServer(t)
	issB, signB := newTestOIDCServer(t)
	ps := newSet(t, specFor(issA, "aud-a"), specFor(issB, "aud-b"))

	for _, tc := range []struct {
		name, iss, aud, email string
		sign                  func(map[string]any) string
	}{
		{"provider A", issA, "aud-a", "a@example.com", signA},
		{"provider B", issB, "aud-b", "b@example.com", signB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, c := callWith(t, ps, tc.sign(claimsFor(tc.iss, tc.aud, tc.email)))
			if code != http.StatusOK {
				t.Fatalf("got %d, want 200", code)
			}
			if got := c.Get("auth_issuer"); got != tc.iss {
				t.Errorf("auth_issuer = %v, want %s", got, tc.iss)
			}
			if got := c.Get("user_email"); got != tc.email {
				t.Errorf("user_email = %v, want %s", got, tc.email)
			}
		})
	}
}

func TestProviderSet_UnknownIssuerIsRejectedWithoutEchoingIt(t *testing.T) {
	issA, _ := newTestOIDCServer(t)
	_, signRogue := newTestOIDCServer(t)
	ps := newSet(t, specFor(issA, "aud-a"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+signRogue(claimsFor("https://unconfigured.example.com", "aud-a", "x@example.com")))
	c, rec := echoCtx(req)
	h := ps.RequireAuth()(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	if err := h(c); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "unconfigured.example.com") {
		t.Errorf("response echoed the unknown issuer back to the caller: %s", body)
	}
}

func TestProviderSet_TokenSignedByAnotherProviderIsRejected(t *testing.T) {
	// The core property of issuer-keyed selection: claiming provider A's issuer
	// does not get you provider B's signature accepted, and there is no fallback
	// to a second verifier that might say yes.
	issA, _ := newTestOIDCServer(t)
	issB, signB := newTestOIDCServer(t)
	ps := newSet(t, specFor(issA, "aud-a"), specFor(issB, "aud-b"))

	forged := signB(claimsFor(issA, "aud-a", "attacker@evil.com"))
	code, _ := callWith(t, ps, forged)
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 — a token claiming issuer A but signed by B must be refused", code)
	}
}

func TestProviderSet_AudienceAndClaimRulesAreEnforcedPerProvider(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	spec := specFor(iss, "account")
	spec.ValidationRules = []struct{ Claim, RequiredValue string }{{Claim: "azp", RequiredValue: "armada-orbital"}}
	ps := newSet(t, spec)

	ok := claimsFor(iss, "account", "u@example.com")
	ok["azp"] = "armada-orbital"
	if code, _ := callWith(t, ps, sign(ok)); code != http.StatusOK {
		t.Fatalf("matching azp got %d, want 200", code)
	}

	wrongAZP := claimsFor(iss, "account", "u@example.com")
	wrongAZP["azp"] = "some-other-client"
	if code, _ := callWith(t, ps, sign(wrongAZP)); code != http.StatusUnauthorized {
		t.Errorf("mismatched azp got %d, want 401 — the rule is what anchors trust when aud is generic", code)
	}

	wrongAud := claimsFor(iss, "not-account", "u@example.com")
	wrongAud["azp"] = "armada-orbital"
	if code, _ := callWith(t, ps, sign(wrongAud)); code != http.StatusUnauthorized {
		t.Errorf("mismatched audience got %d, want 401", code)
	}
}

func TestProviderSet_UnreachableProviderDoesNotPreventStartup(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	ps := newSet(t,
		specFor(iss, "aud-a"),
		specFor("https://127.0.0.1:1/unreachable", "aud-b"),
	)
	// The reachable provider still works.
	if code, _ := callWith(t, ps, sign(claimsFor(iss, "aud-a", "u@example.com"))); code != http.StatusOK {
		t.Fatalf("reachable provider got %d, want 200 — one bad provider must not disable the others", code)
	}
	if len(ps.Issuers()) != 1 {
		t.Errorf("Issuers() = %v, want only the reachable one", ps.Issuers())
	}
}

// ── Phase 2: roles, prefixes, app principals ────────────────────────────────
// Names transcribed from the Spike 26 phase-2 acceptance list.

func modeBSpec(issuer string) ProviderSpec {
	s := specFor(issuer, "aud")
	s.DefaultRole = ""
	s.GroupsClaim = "groups"
	s.RoleMapping = []struct{ Group, Role string }{
		{Group: "orbital-admins", Role: "admin"},
		{Group: "platform-engineers", Role: "dev"},
		{Group: "everyone", Role: "readonly"},
	}
	return s
}

func TestProviderSet_ModeBDerivesRoleFromGroups(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	ps := newSet(t, modeBSpec(iss))
	cl := claimsFor(iss, "aud", "u@example.com")
	cl["groups"] = []any{"platform-engineers"}
	code, c := callWith(t, ps, sign(cl))
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	if got := c.Get("provider_role"); got != "dev" {
		t.Errorf("provider_role = %v, want dev", got)
	}
	if got := c.Get("provider_role_group"); got != "platform-engineers" {
		t.Errorf("provider_role_group = %v, want platform-engineers", got)
	}
}

func TestProviderSet_ModeBMostPrivilegedRuleWins(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	ps := newSet(t, modeBSpec(iss))
	cl := claimsFor(iss, "aud", "u@example.com")
	cl["groups"] = []any{"everyone", "orbital-admins", "platform-engineers"}
	_, c := callWith(t, ps, sign(cl))
	if got := c.Get("provider_role"); got != "admin" {
		t.Errorf("provider_role = %v, want admin — the most privileged match must win", got)
	}
}

func TestProviderSet_ModeBNoMatchingGroupIsDenied(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	ps := newSet(t, modeBSpec(iss))
	for _, groups := range []any{[]any{"unrelated-group"}, []any{}, nil} {
		cl := claimsFor(iss, "aud", "u@example.com")
		if groups != nil {
			cl["groups"] = groups
		}
		if code, _ := callWith(t, ps, sign(cl)); code != http.StatusUnauthorized {
			t.Errorf("groups=%v got %d, want 401 — a mapping enumerates who may use orbital", groups, code)
		}
	}
}

func TestProviderSet_ModeASetsDefaultRoleOnly(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	ps := newSet(t, specFor(iss, "aud")) // DefaultRole readonly, no mapping
	_, c := callWith(t, ps, sign(claimsFor(iss, "aud", "u@example.com")))
	if got := c.Get("provider_default_role"); got != "readonly" {
		t.Errorf("provider_default_role = %v, want readonly", got)
	}
	if got := c.Get("provider_role"); got != nil {
		t.Errorf("provider_role = %v, want unset — mode A must not claim authority over the role", got)
	}
}

func TestProviderSet_ServiceAccountTokenIsAnAppPrincipalNotAUser(t *testing.T) {
	// Shape taken from a real Keycloak client-credentials token: preferred_username
	// populated, no email claim, azp naming the client.
	iss, sign := newTestOIDCServer(t)
	spec := specFor(iss, "aud")
	spec.UsernameClaim = "preferred_username"
	ps := newSet(t, spec)

	cl := claimsFor(iss, "aud", "")
	delete(cl, "email")
	cl["preferred_username"] = "service-account-armada-orbital"
	cl["azp"] = "armada-orbital"

	code, c := callWith(t, ps, sign(cl))
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200 — a service-account token is a valid caller", code)
	}
	if got, _ := c.Get("user_email").(string); got != "" {
		t.Errorf("user_email = %q, want empty — an app principal must not become a user row", got)
	}
	if got, _ := c.Get("user_name").(string); !strings.HasPrefix(got, AppPrincipalPrefix) {
		t.Errorf("user_name = %q, want the %q prefix", got, AppPrincipalPrefix)
	}
	if got := c.Get("provider_role"); got != nil {
		t.Errorf("provider_role = %v, want unset for an app principal", got)
	}
}

func TestProviderSet_ActingClientRecordedOnlyWhenItDiffersFromTheSubject(t *testing.T) {
	// A trusted upstream service presenting a token for a human: the client is
	// the actor, the human is the subject, and the audit log must be able to say
	// so. Orbital's own client is not an actor — the user is their own.
	iss, sign := newTestOIDCServer(t)
	spec := specFor(iss, "aud")
	spec.SelfClientID = "armada-orbital"
	ps := newSet(t, spec)

	viaService := claimsFor(iss, "aud", "daniel@example.com")
	viaService["azp"] = "aep-fleet-commander"
	_, c := callWith(t, ps, sign(viaService))
	if got := c.Get("acting_client"); got != "aep-fleet-commander" {
		t.Errorf("acting_client = %v, want aep-fleet-commander", got)
	}
	if got := c.Get("user_email"); got != "daniel@example.com" {
		t.Errorf("user_email = %v — the SUBJECT must stay the human, not the actor", got)
	}

	direct := claimsFor(iss, "aud", "daniel@example.com")
	direct["azp"] = "armada-orbital"
	_, c2 := callWith(t, ps, sign(direct))
	if got := c2.Get("acting_client"); got != nil {
		t.Errorf("acting_client = %v, want unset — a user signing in directly is their own actor", got)
	}
}

func TestProviderSet_TrustedServiceAssignsARoleAndProvisionsNoUser(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	trusted := specFor(iss, "aud")
	trusted.ClientID, trusted.DefaultRole, trusted.AssignedRole = "aep-fleet-commander", "", "admin"
	ps := newSet(t, trusted)

	cl := claimsFor(iss, "aud", "someone@example.com")
	cl["azp"] = "aep-fleet-commander"
	code, c := callWith(t, ps, sign(cl))
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	if got := c.Get("role"); got != "admin" {
		t.Errorf("role = %v, want admin — a trusted service assigns its role directly", got)
	}
	// No provisioning signals: ResolveUser short-circuits on a preset role, so
	// these must stay unset or a user row would be created for an AEP caller.
	for _, k := range []string{"provider_role", "provider_default_role"} {
		if got := c.Get(k); got != nil {
			t.Errorf("%s = %v, want unset — a trusted-service caller is not an orbital user", k, got)
		}
	}
}

func TestProviderSet_TwoClientsOfOneIssuerAreSelectedIndependently(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	trusted := specFor(iss, "aud")
	trusted.ClientID, trusted.DefaultRole, trusted.AssignedRole = "aep-fleet-commander", "", "admin"
	direct := modeBSpec(iss)
	direct.ClientID = "armada-orbital"
	ps := newSet(t, trusted, direct)

	viaAEP := claimsFor(iss, "aud", "p@example.com")
	viaAEP["azp"] = "aep-fleet-commander"
	_, c1 := callWith(t, ps, sign(viaAEP))
	if got := c1.Get("role"); got != "admin" {
		t.Errorf("AEP caller role = %v, want admin", got)
	}

	viaDirect := claimsFor(iss, "aud", "p@example.com")
	viaDirect["azp"] = "armada-orbital"
	viaDirect["groups"] = []any{"platform-engineers"}
	_, c2 := callWith(t, ps, sign(viaDirect))
	if got := c2.Get("provider_role"); got != "dev" {
		t.Errorf("direct caller provider_role = %v, want dev — same issuer, different policy", got)
	}
	if got := c2.Get("role"); got != nil {
		t.Errorf("direct caller must not get an assigned role: %v", got)
	}
}
