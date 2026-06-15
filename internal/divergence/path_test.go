package divergence

import "testing"

func TestSnapshotKey_MatchesIngesterPrefix(t *testing.T) {
	repoPath := "orbital/colo-galleon"
	ts := "2026-06-13T23-28-13Z"

	key := SnapshotKey(repoPath, ts)
	prefix := PrefixForRepo(repoPath)

	if got, want := key, "divergence/orbital/colo-galleon/2026-06-13T23-28-13Z.json"; got != want {
		t.Errorf("SnapshotKey = %q, want %q", got, want)
	}
	if got, want := prefix, "divergence/orbital/colo-galleon/"; got != want {
		t.Errorf("PrefixForRepo = %q, want %q", got, want)
	}

	// The boundary contract: any key produced by SnapshotKey must be discoverable
	// by listing PrefixForRepo. If this assertion fails, producer and consumer
	// have drifted — exactly the bug class this package exists to prevent.
	if got := key[:len(prefix)]; got != prefix {
		t.Errorf("SnapshotKey prefix = %q, does not match PrefixForRepo = %q", got, prefix)
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
