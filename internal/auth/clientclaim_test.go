package auth

import (
	"net/http"
	"testing"
)

// Which claim names the calling client is not a free choice: RFC 9068 §2.2 makes
// `client_id` REQUIRED on a JWT access token, while `azp` is OIDC Core §2 and is
// specified for ID TOKENS. Keycloak and Entra v2 emit `azp` on access tokens by
// convention rather than by spec, and orbital used to read only that — so a
// conformant issuer emitting `client_id` alone selected no provider and was
// refused with "token issuer is not trusted by this server", an error naming the
// issuer rather than the real cause.
//
// The regression this guards is a revert to a single claim. Any one of the three
// spellings, on its own, must select the provider.
func TestProviderSelection_AcceptsEitherClientClaim(t *testing.T) {
	cases := []struct {
		name  string
		claim string // the ONLY client-naming claim present
	}{
		{"RFC 9068 client_id", "client_id"},
		{"OIDC azp", "azp"},
		{"Entra v1 appid", "appid"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iss, sign := newTestOIDCServer(t)
			spec := specFor(iss, "aud")
			// PINNED to a client id. Without this the entry is issuer-wide and
			// matches any caller, so the test passes whichever claim the code
			// reads — which is exactly how this test was wrong the first time.
			spec.ClientID = "firmware-svc"
			spec.UsernameClaim = "preferred_username"
			ps := newSet(t, spec)

			cl := claimsFor(iss, "aud", "")
			delete(cl, "email")
			cl["preferred_username"] = "service-account-firmware"
			// Exactly one spelling, so the test fails if that spelling is the one
			// the code stopped reading.
			cl[tc.claim] = "firmware-svc"

			code, _ := callWith(t, ps, sign(cl))
			if code != http.StatusOK {
				t.Fatalf("got %d, want 200 — a token naming its client via %q was refused", code, tc.claim)
			}
		})
	}
}

// Keycloak emits BOTH, with the same value (verified against the dev realm
// 2026-10-06). Preferring the RFC claim must therefore be invisible for the
// issuer orbital actually runs against — this pins that the switch did not
// change behaviour where both are present.
func TestProviderSelection_BothClaimsPresentIsUnchanged(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	spec := specFor(iss, "aud")
	spec.ClientID = "firmware-svc"
	spec.UsernameClaim = "preferred_username"
	ps := newSet(t, spec)

	cl := claimsFor(iss, "aud", "")
	delete(cl, "email")
	cl["preferred_username"] = "service-account-firmware"
	cl["azp"] = "firmware-svc"
	cl["client_id"] = "firmware-svc"

	code, c := callWith(t, ps, sign(cl))
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	// And it is still recognised as an app principal, which is what gates the
	// role path — reading a different claim must not change that classification.
	if !IsAppPrincipal(c) {
		t.Error("caller is no longer an app principal; the client claim feeds that decision too")
	}
}

// The negative: a token naming no client at all must not select a client-pinned
// provider. It may still match an issuer-wide entry, but it must not be
// attributed to a client it never claimed — that would let a caller inherit
// another client's policy.
func TestProviderSelection_NoClientClaimDoesNotMatchAPinnedEntry(t *testing.T) {
	iss, sign := newTestOIDCServer(t)
	spec := specFor(iss, "aud")
	spec.ClientID = "firmware-svc" // pinned: only this client may use it
	spec.UsernameClaim = "preferred_username"
	ps := newSet(t, spec)

	cl := claimsFor(iss, "aud", "")
	delete(cl, "email")
	cl["preferred_username"] = "service-account-firmware"
	// no client_id, no azp, no appid

	if code, _ := callWith(t, ps, sign(cl)); code == http.StatusOK {
		t.Error("a token naming no client matched a client-pinned provider entry")
	}
}
