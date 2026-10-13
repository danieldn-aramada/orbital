package handler

import "testing"

// naturalKeyOf labels a row that has NO name, so it is read by a human looking for
// which entity this is. The regression guarded here is it being reimplemented
// in JS by splitting on "-": the kind is not always a prefix, and orbital has
// live types where that would mangle the answer.
func TestNaturalKeyOf(t *testing.T) {
	cases := []struct {
		name, orbID, kind, want string
	}{
		{"kind is a prefix", "colo:server-GD206G4", "server", "GD206G4"},
		{"multi-word kind", "colo:network-device-JX3623130496", "network-device", "JX3623130496"},
		// Pattern is `{name}` — there is no kind token to remove, and removing
		// a "colo-" prefix because it looks like one would be wrong.
		{"no kind token at all", "colo:colo-galleon", "data-center", "colo-galleon"},
		// IdracSettings is `<serviceTag>-idrac`: the kind is a SUFFIX. Stripping
		// a prefix must leave it alone rather than cut into the service tag.
		{"kind is a suffix", "colo:CFRHDX3-idrac", "idrac-settings", "CFRHDX3-idrac"},
		{"namespace contains a dash", "alaska-dot-cruiser:adot-m", "eksa-kubernetes-cluster", "adot-m"},
		// Not the convention. Returning the orbId unchanged reads as "no key",
		// which is honest; inventing one would be a confident wrong answer.
		{"no namespace separator", "stray-id", "server", "stray-id"},
		{"nothing after the separator", "colo:", "server", "colo:"},
		{"empty", "", "server", ""},
		{"no kind supplied", "colo:server-GD206G4", "", "server-GD206G4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := naturalKeyOf(tc.orbID, tc.kind); got != tc.want {
				t.Errorf("naturalKeyOf(%q, %q) = %q, want %q", tc.orbID, tc.kind, got, tc.want)
			}
		})
	}
}
