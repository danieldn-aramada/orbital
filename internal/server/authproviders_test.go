package server

import (
	"testing"

	"github.com/armada/orbital/internal/config"
)

// authProviderSpecs is the only link between ORBITAL_AUTH_PROVIDERS and the
// verifier. A principalType dropped here puts every ServiceAccount back on the
// person path, where it is provisioned into the users table.
func TestAuthProviderSpecs_PrincipalTypeReachesTheVerifier(t *testing.T) {
	var providers config.AuthProviders
	err := providers.Decode(`[
	  {"issuer":{"url":"https://oidc.cluster-a.example.com","audiences":["orbital"]},"principalType":"app",
	   "claimMappings":{"username":{"claim":"sub"}},"defaultRole":"readonly"},
	  {"issuer":{"url":"https://kc.example.com/realms/armada","audiences":["armada-orbital"]},"principalType":"user",
	   "claimMappings":{"username":{"claim":"email"}},"defaultRole":"readonly"},
	  {"issuer":{"url":"https://kc.example.com/realms/armada","audiences":["armada-orbital"]},"clientID":"armada-orbital",
	   "claimMappings":{"username":{"claim":"email"}},"defaultRole":"readonly"}
	]`)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	specs := authProviderSpecs(&config.Config{AuthProviders: providers})
	for i, want := range []bool{true, false, false} {
		if specs[i].App != want {
			t.Errorf("spec %d (principalType %q): App = %v, want %v", i, providers[i].PrincipalType, specs[i].App, want)
		}
	}
}
