package divergence

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestMapping(t *testing.T) *Mapping {
	t.Helper()
	m, err := ParseMapping([]byte(`{
		"bundleDigest": "sha256:abc",
		"items": [
			{"path": "spec", "orbId": "colo:colo-galleon", "type": "DataCenter"},
			{"path": "spec.servers[serviceTag=3RK3V64]", "orbId": "colo:srv-001", "type": "Server"},
			{"path": "spec.servers[serviceTag=3RK3V64].idrac", "orbId": "colo:srv-001-idrac", "type": "IdracSettings"},
			{"path": "spec.servers[serviceTag=JL3PV82]", "orbId": "colo:srv-002", "type": "Server"},
			{"path": "spec.servers[serviceTag=JL3PV82].idrac", "orbId": "colo:srv-002-idrac", "type": "IdracSettings"}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseMapping: %v", err)
	}
	return m
}

func TestResolve_LongestPrefixWins(t *testing.T) {
	m := newTestMapping(t)

	cases := []struct {
		name      string
		path      string
		wantOrbID string
		wantField string
		wantType  string
	}{
		{
			name:      "idrac field resolves to IdracSettings orbId, not Server",
			path:      "spec.servers[serviceTag=3RK3V64].idrac.sshEnabled",
			wantOrbID: "colo:srv-001-idrac",
			wantField: "sshEnabled",
			wantType:  "IdracSettings",
		},
		{
			name:      "top-level server field resolves to Server orbId",
			path:      "spec.servers[serviceTag=3RK3V64].oobIP",
			wantOrbID: "colo:srv-001",
			wantField: "oobIP",
			wantType:  "Server",
		},
		{
			name:      "datacenter-level field resolves to DataCenter orbId",
			path:      "spec.datacenter",
			wantOrbID: "colo:colo-galleon",
			wantField: "datacenter",
			wantType:  "DataCenter",
		},
		{
			name:      "second server resolves independently",
			path:      "spec.servers[serviceTag=JL3PV82].idrac.ipmiEnabled",
			wantOrbID: "colo:srv-002-idrac",
			wantField: "ipmiEnabled",
			wantType:  "IdracSettings",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOrbID, gotField, gotType, err := m.Resolve(tc.path)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.path, err)
			}
			if gotOrbID != tc.wantOrbID {
				t.Errorf("orbId: got %q, want %q", gotOrbID, tc.wantOrbID)
			}
			if gotField != tc.wantField {
				t.Errorf("field: got %q, want %q", gotField, tc.wantField)
			}
			if gotType != tc.wantType {
				t.Errorf("type: got %q, want %q", gotType, tc.wantType)
			}
		})
	}
}

func TestResolve_NoPrefixMatch(t *testing.T) {
	m := newTestMapping(t)
	// "status.foo" shares no prefix with anything under "spec"
	_, _, _, err := m.Resolve("status.foo")
	if err == nil {
		t.Fatal("expected error for unmatched path, got nil")
	}
	if !strings.Contains(err.Error(), "no mapping prefix matches") {
		t.Errorf("unexpected error: %v", err)
	}
}

// When a mapping has a shallow prefix but no entry for an intermediate
// ConfigItem (e.g. has "spec" but no "spec.clusters[...]"), Resolve must
// reject the lookup rather than emit a multi-segment "field" name.
// This catches cb-bundler bugs where a domain mapping was omitted.
func TestResolve_RefusesShallowMatch(t *testing.T) {
	m := newTestMapping(t)
	_, _, _, err := m.Resolve("spec.clusters[clusterName=prod-1].config.someField")
	if err == nil {
		t.Fatal("expected error when shallow prefix would produce structured leaf, got nil")
	}
	if !strings.Contains(err.Error(), "too shallow") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolve_ExactPathIsRejected(t *testing.T) {
	// An exact match to a ConfigItem boundary is not a divergence entry —
	// there's no leaf field. cb-controller should only emit leaf paths.
	m := newTestMapping(t)
	_, _, _, err := m.Resolve("spec.servers[serviceTag=3RK3V64].idrac")
	if err == nil {
		t.Fatal("expected error for ConfigItem-boundary path, got nil")
	}
}

func TestResolve_PrefixBoundaryDot(t *testing.T) {
	// A path that is a string prefix of a mapping item but does NOT have
	// the next dot separator must not falsely match. Example:
	// mapping has "spec.servers[serviceTag=3RK3V64]", lookup for
	// "spec.servers[serviceTag=3RK3V64Z].field" must not match the first.
	m, err := ParseMapping([]byte(`{
		"bundleDigest": "sha256:abc",
		"items": [
			{"path": "spec.servers[serviceTag=3RK3V64]", "orbId": "colo:srv-001"}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseMapping: %v", err)
	}
	_, _, _, err = m.Resolve("spec.servers[serviceTag=3RK3V64Z].field")
	if err == nil {
		t.Fatal("expected mismatch for prefix without dot boundary, got match")
	}
}

func TestParseMapping_EmptyItems(t *testing.T) {
	_, err := ParseMapping([]byte(`{"bundleDigest":"sha256:abc","items":[]}`))
	if err == nil {
		t.Fatal("expected error for empty items, got nil")
	}
}

func TestParseMapping_InvalidJSON(t *testing.T) {
	_, err := ParseMapping([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestMappingStore_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewMappingStore(dir)
	payload := []byte(`{
		"bundleDigest": "sha256:abc",
		"items": [
			{"path": "spec", "orbId": "colo:colo-galleon"}
		]
	}`)
	const digest = "sha256:abc123"

	if err := store.Save(digest, payload); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !store.Has(digest) {
		t.Fatal("Has: expected true after Save")
	}
	m, err := store.Load(digest)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Items) != 1 || m.Items[0].OrbID != "colo:colo-galleon" {
		t.Errorf("loaded mapping mismatch: %+v", m)
	}
}

func TestMappingStore_SaveRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	store := NewMappingStore(dir)
	if err := store.Save("sha256:bad", []byte(`{not json`)); err == nil {
		t.Fatal("expected Save to reject invalid mapping")
	}
	// File should not exist after failed save.
	if _, err := store.Load("sha256:bad"); err == nil {
		t.Error("expected file to be absent after rejected Save")
	}
}

func TestMappingStore_DeleteIdempotent(t *testing.T) {
	dir := t.TempDir()
	store := NewMappingStore(dir)
	if err := store.Delete("sha256:does-not-exist"); err != nil {
		t.Errorf("Delete on missing should be no-op, got %v", err)
	}
}

func TestMappingStore_Has_MissingFile(t *testing.T) {
	store := NewMappingStore(t.TempDir())
	if store.Has("sha256:nope") {
		t.Error("Has: expected false for missing file")
	}
}

func TestMappingStore_PathForUsesDigestVerbatim(t *testing.T) {
	store := NewMappingStore("/tmp/orb-data")
	got := store.pathFor("sha256:abc123")
	want := filepath.Join("/tmp/orb-data/mappings", "sha256:abc123.json")
	if got != want {
		t.Errorf("pathFor: got %q, want %q", got, want)
	}
}
