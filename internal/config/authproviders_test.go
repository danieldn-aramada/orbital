package config

import "testing"

// Names transcribed from the Spike 26 phase-1 acceptance list, items 1-3.

func TestAuthProviders_UnsetLeavesTheListEmpty(t *testing.T) {
	var a AuthProviders
	if err := a.Decode(""); err != nil {
		t.Fatalf("empty value: %v", err)
	}
	if len(a) != 0 {
		t.Fatalf("got %d providers, want 0 — unset must mean 'use the legacy env vars'", len(a))
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("empty list must validate: %v", err)
	}
}

func TestAuthProviders_MalformedConfigIsRefusedAndNamesTheProblem(t *testing.T) {
	tests := []struct {
		name, json, wantSubstring string
	}{
		{"bad JSON", `[{`, "not valid JSON"},
		{"missing issuer url", `[{"issuer":{"audiences":["a"]},"claimMappings":{"username":{"claim":"email"}},"defaultRole":"dev"}]`, "issuer.url is required"},
		{"non-https issuer", `[{"issuer":{"url":"http://idp.example.com","audiences":["a"]},"claimMappings":{"username":{"claim":"email"}},"defaultRole":"dev"}]`, "must be https"},
		{"duplicate (issuer, clientID)", `[
			{"issuer":{"url":"https://a.example.com","audiences":["x"]},"claimMappings":{"username":{"claim":"email"}},"defaultRole":"dev"},
			{"issuer":{"url":"https://a.example.com","audiences":["y"]},"claimMappings":{"username":{"claim":"email"}},"defaultRole":"dev"}]`, "must be unique"},
		{"no audiences", `[{"issuer":{"url":"https://a.example.com","audiences":[]},"claimMappings":{"username":{"claim":"email"}},"defaultRole":"dev"}]`, "at least one audience"},
		{"no username claim", `[{"issuer":{"url":"https://a.example.com","audiences":["x"]},"defaultRole":"dev"}]`, "claimMappings.username.claim is required"},
		{"neither role mode", `[{"issuer":{"url":"https://a.example.com","audiences":["x"]},"claimMappings":{"username":{"claim":"email"}}}]`, "set defaultRole, roleMapping or trustedService"},
		{"bad default role", `[{"issuer":{"url":"https://a.example.com","audiences":["x"]},"claimMappings":{"username":{"claim":"email"}},"defaultRole":"superuser"}]`, "must be readonly, dev or admin"},
		{"bad mapped role", `[{"issuer":{"url":"https://a.example.com","audiences":["x"]},"claimMappings":{"username":{"claim":"email"},"groups":{"claim":"groups"}},"roleMapping":[{"group":"g","role":"root"}]}]`, "must be readonly, dev or admin"},
		{"trustedService combined with a role mode", `[{"issuer":{"url":"https://a.example.com","audiences":["x"]},"claimMappings":{"username":{"claim":"email"},"groups":{"claim":"g"}},"defaultRole":"dev","roleMapping":[{"group":"g","role":"admin"}],"trustedService":{"assignedRole":"admin"}}]`, "cannot be combined"},
		{"bad trusted role", `[{"issuer":{"url":"https://a.example.com","audiences":["x"]},"claimMappings":{"username":{"claim":"email"}},"trustedService":{"assignedRole":"root"}}]`, "must be readonly, dev or admin"},
		{"same issuer and client twice", `[
			{"issuer":{"url":"https://a.example.com","audiences":["x"]},"clientID":"c1","claimMappings":{"username":{"claim":"email"}},"defaultRole":"dev"},
			{"issuer":{"url":"https://a.example.com","audiences":["y"]},"clientID":"c1","claimMappings":{"username":{"claim":"email"}},"defaultRole":"dev"}]`, "must be unique"},
		{"mapping without groups claim", `[{"issuer":{"url":"https://a.example.com","audiences":["x"]},"claimMappings":{"username":{"claim":"email"}},"roleMapping":[{"group":"g","role":"admin"}]}]`, "requires claimMappings.groups.claim"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a AuthProviders
			err := a.Decode(tt.json)
			if err == nil {
				err = a.Validate()
			}
			if err == nil {
				t.Fatal("accepted a config that should have been refused")
			}
			if !contains(err.Error(), tt.wantSubstring) {
				t.Errorf("error did not name the problem.\n got: %v\nwant substring: %q", err, tt.wantSubstring)
			}
		})
	}
}

func TestAuthProviders_ValidConfigIsAccepted(t *testing.T) {
	// The negative table above would pass against a Validate that rejects
	// everything; this is the guard against that.
	const good = `[
	  {"issuer":{"url":"https://aad.example.com/v2.0","audiences":["client-guid"]},
	   "claimMappings":{"username":{"claim":"preferred_username"}},
	   "defaultRole":"readonly"},
	  {"issuer":{"url":"https://keycloak.example.com/realms/aep","audiences":["account"]},
	   "claimValidationRules":[{"claim":"azp","requiredValue":"aep-fleet-commander"}],
	   "claimMappings":{"username":{"claim":"email"},"groups":{"claim":"groups"}},
	   "roleMapping":[{"group":"orbital-admins","role":"admin"},{"group":"platform-engineers","role":"dev"}]}
	]`
	var a AuthProviders
	if err := a.Decode(good); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(a) != 2 {
		t.Fatalf("got %d providers, want 2", len(a))
	}
	if a[1].ClaimValidationRules[0].Claim != "azp" {
		t.Errorf("azp anchoring rule did not survive decode: %+v", a[1].ClaimValidationRules)
	}
	if a[1].RoleMapping[0].Role != "admin" {
		t.Errorf("role mapping did not survive decode: %+v", a[1].RoleMapping)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestAuthProviders_SameIssuerDifferentClientsIsAllowed(t *testing.T) {
	// One Keycloak realm hosts several clients orbital must treat differently:
	// a trusted upstream service and a direct caller share an issuer but not a
	// policy. The uniqueness rule is on the PAIR, not the issuer alone.
	const cfg = `[
	  {"issuer":{"url":"https://kc.example.com/realms/armada","audiences":["armada-orbital"]},
	   "clientID":"aep-fleet-commander",
	   "claimMappings":{"username":{"claim":"email"}},
	   "trustedService":{"assignedRole":"admin"}},
	  {"issuer":{"url":"https://kc.example.com/realms/armada","audiences":["account"]},
	   "clientID":"armada-orbital",
	   "claimMappings":{"username":{"claim":"email"},"groups":{"claim":"orbital_roles"}},
	   "roleMapping":[{"group":"orbital-admin","role":"admin"}]}
	]`
	var a AuthProviders
	if err := a.Decode(cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("two clients of one issuer must be allowed: %v", err)
	}
	if a[0].TrustedService == nil || a[0].TrustedService.AssignedRole != "admin" {
		t.Errorf("trustedService did not survive decode: %+v", a[0].TrustedService)
	}
}

func TestAuthProviders_DefaultRoleAlongsideRoleMappingIsTheFloor(t *testing.T) {
	// Mapping decides; defaultRole is what an unmatched token gets instead of
	// being refused. Same pair Grafana exposes as role_attribute_path plus
	// role_attribute_strict, where strict=true is this combination omitted.
	const cfg = `[{"issuer":{"url":"https://kc.example.com/realms/a","audiences":["x"]},
	  "claimMappings":{"username":{"claim":"email"},"groups":{"claim":"orbital_roles"}},
	  "roleMapping":[{"group":"orbital-admin","role":"admin"}],
	  "defaultRole":"readonly"}]`
	var a AuthProviders
	if err := a.Decode(cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("defaultRole + roleMapping must be allowed: %v", err)
	}
	if a[0].DefaultRole != "readonly" || len(a[0].RoleMapping) != 1 {
		t.Errorf("both survived decode? defaultRole=%q rules=%d", a[0].DefaultRole, len(a[0].RoleMapping))
	}
}
