package auth

import (
	"net/http"
	"testing"
)

// An app principal (a client-credentials caller: a client claim and no email)
// never becomes a user, so its role comes from its provider entry alone. Names
// transcribed from the machine-principal acceptance list (items 2 and 7); items
// 1, 5, 6 and 8 are in internal/handler.

func clientCredentialsClaims(iss, aud, client string) map[string]any {
	cl := claimsFor(iss, aud, "")
	delete(cl, "email")
	cl["azp"] = client
	cl["client_id"] = client
	cl["preferred_username"] = "service-account-" + client
	return cl
}

func TestServiceCaller_GetsExactlyTheRoleItsEntryConfiguresOrIsRefused(t *testing.T) {
	mapping := []struct{ Group, Role string }{{Group: "svc-writers", Role: "dev"}}
	cases := []struct {
		name     string
		spec     func(iss string) ProviderSpec
		groups   []any
		wantRole string // empty = refused
	}{
		{"delegatedAuthorization readonly", func(iss string) ProviderSpec {
			return ProviderSpec{IssuerURL: iss, ClientID: "svc", Audiences: []string{"aud"}, UsernameClaim: "preferred_username", DelegatedRole: "readonly"}
		}, nil, "readonly"},
		{"delegatedAuthorization admin", func(iss string) ProviderSpec {
			return ProviderSpec{IssuerURL: iss, ClientID: "svc", Audiences: []string{"aud"}, UsernameClaim: "preferred_username", DelegatedRole: "admin"}
		}, nil, "admin"},
		{"roleMapping match", func(iss string) ProviderSpec {
			return ProviderSpec{IssuerURL: iss, ClientID: "svc", Audiences: []string{"aud"}, UsernameClaim: "email", GroupsClaim: "groups", RoleMapping: mapping}
		}, []any{"svc-writers"}, "dev"},
		{"roleMapping no match falls to the defaultRole floor", func(iss string) ProviderSpec {
			return ProviderSpec{IssuerURL: iss, ClientID: "svc", Audiences: []string{"aud"}, UsernameClaim: "email", GroupsClaim: "groups", RoleMapping: mapping, DefaultRole: "readonly"}
		}, []any{"unrelated"}, "readonly"},
		{"defaultRole alone", func(iss string) ProviderSpec {
			return ProviderSpec{IssuerURL: iss, ClientID: "svc", Audiences: []string{"aud"}, UsernameClaim: "email", DefaultRole: "readonly"}
		}, nil, "readonly"},
		{"strict roleMapping with no match is refused", func(iss string) ProviderSpec {
			return ProviderSpec{IssuerURL: iss, ClientID: "svc", Audiences: []string{"aud"}, UsernameClaim: "email", GroupsClaim: "groups", RoleMapping: mapping}
		}, []any{"unrelated"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iss, sign := newTestOIDCServer(t)
			ps := newSet(t, tc.spec(iss))
			cl := clientCredentialsClaims(iss, "aud", "svc")
			if tc.groups != nil {
				cl["groups"] = tc.groups
			}
			code, c := callWith(t, ps, sign(cl))

			if tc.wantRole == "" {
				if code != http.StatusUnauthorized {
					t.Fatalf("got %d, want 401 — an entry that gives a service no role must refuse it", code)
				}
				if got := c.Get("role"); got != nil {
					t.Errorf("refused service was given role %v", got)
				}
				return
			}
			if code != http.StatusOK {
				t.Fatalf("got %d, want 200", code)
			}
			if got := c.Get("role"); got != tc.wantRole {
				t.Errorf("role = %v, want exactly %q — a service's role is its entry's, never an implicit grant", got, tc.wantRole)
			}
			if !IsAppPrincipal(c) {
				t.Error("a client-credentials token must be classified as an app principal")
			}
			if got := c.Get("user_email"); got != "" {
				t.Errorf("user_email = %v, want empty — an app principal is never a user", got)
			}
			for _, k := range []string{"provider_role", "provider_default_role"} {
				if got := c.Get(k); got != nil {
					t.Errorf("%s = %v, want unset — it would drive users-table provisioning", k, got)
				}
			}
		})
	}
}

func TestPersonCaller_EmailTokenKeepsTheUserPath(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	direct := ProviderSpec{
		IssuerURL: iss, ClientID: "armada-orbital", Audiences: []string{"armada-orbital"},
		SelfClientID: "armada-orbital", UsernameClaim: "email", GroupsClaim: "orbital_roles",
		RoleMapping: []struct{ Group, Role string }{{Group: "orbital-admin", Role: "admin"}},
		DefaultRole: "readonly",
	}
	aep := ProviderSpec{
		IssuerURL: iss, ClientID: "aep-fleet-commander", Audiences: []string{"armada-orbital"},
		SelfClientID: "armada-orbital", UsernameClaim: "email", DelegatedRole: "admin",
	}
	modeA := ProviderSpec{IssuerURL: iss, Audiences: []string{"armada-orbital"}, UsernameClaim: "email", DefaultRole: "readonly"}
	ps := newSet(t, direct, aep, modeA)

	person := func(email, azp string, groups []any) map[string]any {
		cl := claimsFor(iss, "armada-orbital", email)
		if azp != "" {
			cl["azp"], cl["client_id"] = azp, azp
		}
		if groups != nil {
			cl["orbital_roles"] = groups
		}
		return cl
	}

	cases := []struct {
		name   string
		claims map[string]any
		want   map[string]any // context key → value; nil value = must be unset
	}{
		{"mapped group", person("admin@example.com", "armada-orbital", []any{"orbital-admin"}), map[string]any{
			"provider_role": "admin", "provider_role_floored": nil, "role": nil, "acting_client": nil,
		}},
		{"no group matched — floor", person("reader@example.com", "armada-orbital", nil), map[string]any{
			"provider_role": "readonly", "provider_role_floored": true, "role": nil,
		}},
		{"defaultRole only — seeds a new user, leaves ORBITAL_ADMIN_EMAILS to ResolveUser", person("new@example.com", "", nil), map[string]any{
			"provider_default_role": "readonly", "provider_role": nil, "role": nil,
		}},
		{"AEP delegated admin", person("operator@example.com", "aep-fleet-commander", nil), map[string]any{
			"role": "admin", "acting_client": "aep-fleet-commander", "provider_role": nil, "provider_default_role": nil,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, c := callWith(t, ps, sign(tc.claims))
			if code != http.StatusOK {
				t.Fatalf("got %d, want 200", code)
			}
			if IsAppPrincipal(c) {
				t.Error("a token carrying an email was classified as a service")
			}
			if got := c.Get("user_email"); got != tc.claims["email"] {
				t.Errorf("user_email = %v, want %v — the person stays the subject", got, tc.claims["email"])
			}
			for k, want := range tc.want {
				if got := c.Get(k); got != want {
					t.Errorf("%s = %v, want %v", k, got, want)
				}
			}
		})
	}
}

// A declared app entry (principalType "app"): every caller through it is an app
// principal whatever its claims, named by the entry's username claim. Names
// transcribed from the declared-app-principal acceptance list (items 2-4).

const firmwareSA = "system:serviceaccount:firmware:firmware-svc"

// A Kubernetes ServiceAccount token: no email, no client claim; sub names it.
func serviceAccountClaims(iss, sub string) map[string]any {
	cl := claimsFor(iss, "orbital", "")
	delete(cl, "email")
	cl["sub"] = sub
	return cl
}

// clusterEntry declares a cluster issuer's callers to be services, with roles
// mapped per ServiceAccount: the groups claim is sub itself.
func clusterEntry(iss string, mapping ...struct{ Group, Role string }) ProviderSpec {
	return ProviderSpec{IssuerURL: iss, Audiences: []string{"orbital"}, App: true,
		UsernameClaim: "sub", GroupsClaim: "sub", RoleMapping: mapping}
}

func TestAppEntry_EveryCallerIsAnAppPrincipalRegardlessOfClaims(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	ps := newSet(t, ProviderSpec{IssuerURL: iss, Audiences: []string{"orbital"}, App: true,
		UsernameClaim: "sub", DefaultRole: "readonly"})

	withEmail := serviceAccountClaims(iss, firmwareSA)
	withEmail["email"] = "firmware@example.com"
	withClient := serviceAccountClaims(iss, firmwareSA)
	withClient["azp"], withClient["client_id"] = "firmware", "firmware"

	for name, cl := range map[string]map[string]any{
		"ServiceAccount token: no email, no client claim": serviceAccountClaims(iss, firmwareSA),
		"token carrying an email":                         withEmail,
		"token carrying a client claim":                   withClient,
	} {
		t.Run(name, func(t *testing.T) {
			code, c := callWith(t, ps, sign(cl))
			if code != http.StatusOK {
				t.Fatalf("got %d, want 200", code)
			}
			if !IsAppPrincipal(c) {
				t.Error("a caller through an app entry was not classified as an app principal")
			}
			if got := c.Get("user_email"); got != "" {
				t.Errorf("user_email = %v, want empty — ResolveUser would provision it as a user", got)
			}
			if got := c.Get("user_name"); got != AppPrincipalPrefix+firmwareSA {
				t.Errorf("user_name = %v, want %q — named by the entry's username claim", got, AppPrincipalPrefix+firmwareSA)
			}
			if got := c.Get("role"); got != "readonly" {
				t.Errorf("role = %v, want readonly", got)
			}
			for _, k := range []string{"provider_role", "provider_default_role", "acting_client"} {
				if got := c.Get(k); got != nil {
					t.Errorf("%s = %v, want unset", k, got)
				}
			}
		})
	}
}

func TestAppEntry_ServiceAccountGetsExactlyItsMappedRole(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	ps := newSet(t, clusterEntry(iss,
		struct{ Group, Role string }{firmwareSA, "readonly"},
		struct{ Group, Role string }{"system:serviceaccount:deploy:deployer", "dev"},
		struct{ Group, Role string }{"system:serviceaccount:ops:operator", "admin"},
	))
	for sub, want := range map[string]string{
		firmwareSA:                              "readonly",
		"system:serviceaccount:deploy:deployer": "dev",
		"system:serviceaccount:ops:operator":    "admin",
	} {
		t.Run(sub, func(t *testing.T) {
			code, c := callWith(t, ps, sign(serviceAccountClaims(iss, sub)))
			if code != http.StatusOK {
				t.Fatalf("got %d, want 200", code)
			}
			if got := c.Get("role"); got != want {
				t.Errorf("role = %v, want exactly %q", got, want)
			}
		})
	}
}

func TestAppEntry_UnlistedServiceAccountIsRefused(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	ps := newSet(t, clusterEntry(iss, struct{ Group, Role string }{firmwareSA, "readonly"}))

	code, c := callWith(t, ps, sign(serviceAccountClaims(iss, "system:serviceaccount:other:intruder")))
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 — a strict mapping enumerates which services may call", code)
	}
	if got := c.Get("role"); got != nil {
		t.Errorf("refused service was given role %v", got)
	}

	// Control: the listed one, same issuer and audience, is admitted. Without
	// it a verifier refusing every ServiceAccount would pass the check above.
	if code, _ := callWith(t, ps, sign(serviceAccountClaims(iss, firmwareSA))); code != http.StatusOK {
		t.Fatalf("listed ServiceAccount got %d, want 200", code)
	}
}
