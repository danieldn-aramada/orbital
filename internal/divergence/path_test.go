package divergence

import "testing"

func TestReportKey_MatchesIngesterPrefix(t *testing.T) {
	repoPath := "orbital/colo-galleon"

	key := ReportKey(repoPath)
	prefix := PrefixForRepo(repoPath)

	// Stable-key contract: ReportKey MUST be pure — same repoPath, same key.
	// The overwrite-in-place scheme depends on this. If a caller ever
	// reintroduces a timestamp or nonce, this assertion holds it accountable.
	if got, want := key, "divergence/orbital/colo-galleon/report.json"; got != want {
		t.Errorf("ReportKey = %q, want %q", got, want)
	}
	if got, want := prefix, "divergence/orbital/colo-galleon/"; got != want {
		t.Errorf("PrefixForRepo = %q, want %q", got, want)
	}

	// The boundary contract: any key produced by ReportKey must be discoverable
	// by listing PrefixForRepo. If this assertion fails, producer and consumer
	// have drifted — exactly the bug class this package exists to prevent.
	if got := key[:len(prefix)]; got != prefix {
		t.Errorf("ReportKey prefix = %q, does not match PrefixForRepo = %q", got, prefix)
	}
}

func TestNormalizeRepoPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"localhost:5001/orbital/colo-galleon", "orbital/colo-galleon"},
		{"myregistry.azurecr.io/orbital/colo-galleon", "orbital/colo-galleon"},
		{"orbital/colo-galleon", "colo-galleon"}, // already-stripped form: drops the first segment too
		{"single", "single"},                     // no separator → unchanged
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeRepoPath(tc.in); got != tc.want {
			t.Errorf("NormalizeRepoPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
