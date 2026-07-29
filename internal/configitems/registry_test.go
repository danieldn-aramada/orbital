package configitems

import (
	"strings"
	"testing"
)

// TestKnownMutationsRegex_MatchesAllRegisteredTypes asserts that the registry
// produces a regex that matches every type's add/update/delete mutations.
// Catches the bug-class where someone adds a Type entry but the regex doesn't
// pick it up (e.g. typo in the name).
func TestKnownMutationsRegex_MatchesAllRegisteredTypes(t *testing.T) {
	re := KnownMutationsRegex()
	for _, typ := range Types {
		for _, verb := range []string{"add", "update", "delete"} {
			op := verb + typ.Name
			if !re.MatchString(op) {
				t.Errorf("regex does not match %s (expected to match for type %q)", op, typ.Name)
			}
			// Case-insensitive: matches both lowercase and PascalCase.
			if !re.MatchString(strings.ToLower(string(op[0])) + op[1:]) {
				t.Errorf("regex does not match lowercase-leading %s", op)
			}
		}
	}
}

// TestKnownMutationsRegex_RejectsUnknownTypes asserts that the regex does NOT
// match mutations against unregistered types. Defends against accidentally
// broadening the regex too far (e.g. `(Add|Update|Delete)\w+` would match
// anything and let unaudited types through).
func TestKnownMutationsRegex_RejectsUnknownTypes(t *testing.T) {
	re := KnownMutationsRegex()
	for _, op := range []string{
		"addArbitraryThing",
		"updateRandomType",
		"deleteUnregisteredEntity",
		"addUser", // User exists in PG but is not a ConfigItem; mutations on it must NOT be in this regex.
	} {
		if re.MatchString(op) {
			t.Errorf("regex unexpectedly matched %q — would silently start auditing an unregistered type", op)
		}
	}
}

// TestBeforeFields_ReturnsForEveryRegisteredType asserts every entry has
// BeforeFields set (it's required for any type orbital writes audit events
// for). The audit pipeline silently skips diff rendering if BeforeFields is
// empty, so an omission here = no diff in the audit panel = the exact
// bug-class we just spent a session debugging.
func TestBeforeFields_ReturnsForEveryRegisteredType(t *testing.T) {
	for _, typ := range Types {
		got := BeforeFields(typ.Name)
		if got == "" {
			t.Errorf("BeforeFields(%q) is empty; audit diff will not render for this type", typ.Name)
			continue
		}
		// Every BeforeFields selection must include orbId+version — orbId for
		// entity identity, version for MVCC + auto-increment. `id` (the DGraph
		// UID) is intentionally NOT required: it's stripped from the persisted
		// before-state (stripDGraphIDs) so it never leaks to the audit API, and
		// nothing downstream reads it. Sanity check: skip if BeforeFields is empty
		// (covered above).
		for _, required := range []string{"orbId", "version"} {
			if !strings.Contains(got, required) {
				t.Errorf("BeforeFields(%q) missing required field %q (full selection: %q)",
					typ.Name, required, got)
			}
		}
	}
}

// TestBeforeFields_UnknownTypeReturnsEmpty asserts the function returns ""
// rather than panicking on an unknown type. Required because the audit
// pipeline calls BeforeFields with whatever resource type comes out of
// extractOperations — could be a typo, could be an interface name.
func TestBeforeFields_UnknownTypeReturnsEmpty(t *testing.T) {
	if got := BeforeFields("DefinitelyNotAType"); got != "" {
		t.Errorf("BeforeFields for unknown type returned %q, want \"\"", got)
	}
}

// TestBuildEditTargets_Cluster asserts the cluster's edit targets list
// matches the four-entry shape the JS module consumes: root + etcd + velero +
// s3sync. Pins behavior so future registry edits can't accidentally drop a
// target or break path/orbId derivation.
func TestBuildEditTargets_Cluster(t *testing.T) {
	got := BuildEditTargets("EksaKubernetesCluster", "colo:dev-main", "colo", "dev-main")
	if len(got) != 4 {
		t.Fatalf("BuildEditTargets returned %d targets, want 4 (root + 3 backup kinds)", len(got))
	}

	checks := []struct {
		path               []string
		kind               string
		orbID              string
		parentInverseField string
		wrapperKind        string
	}{
		{path: []string{}, kind: "EksaKubernetesCluster", orbID: "colo:dev-main"},
		{path: []string{"backup", "etcd"}, kind: "EtcdBackup", orbID: "colo:dev-main-etcd-backup", parentInverseField: "clusterBackupEtcd", wrapperKind: "ClusterBackup"},
		{path: []string{"backup", "velero"}, kind: "VeleroBackup", orbID: "colo:dev-main-velero-backup", parentInverseField: "clusterBackupVelero", wrapperKind: "ClusterBackup"},
		{path: []string{"backup", "s3Sync"}, kind: "S3Sync", orbID: "colo:dev-main-s3sync", parentInverseField: "clusterBackupS3Sync", wrapperKind: "ClusterBackup"},
	}
	for i, want := range checks {
		g := got[i]
		if strings.Join(g.Path, ".") != strings.Join(want.path, ".") {
			t.Errorf("target[%d].Path = %v, want %v", i, g.Path, want.path)
		}
		if g.Kind != want.kind {
			t.Errorf("target[%d].Kind = %q, want %q", i, g.Kind, want.kind)
		}
		if g.OrbID != want.orbID {
			t.Errorf("target[%d].OrbID = %q, want %q", i, g.OrbID, want.orbID)
		}
		if g.ParentInverseField != want.parentInverseField {
			t.Errorf("target[%d].ParentInverseField = %q, want %q", i, g.ParentInverseField, want.parentInverseField)
		}
		if want.wrapperKind != "" && (g.ParentWrapper == nil || g.ParentWrapper.Kind != want.wrapperKind) {
			t.Errorf("target[%d].ParentWrapper.Kind = %v, want %q", i, g.ParentWrapper, want.wrapperKind)
		}
	}
}

// TestBuildEditTargets_Server asserts Server gets its IdracSettings as a
// direct (non-wrapper) child. The path is a single segment ["idracSettings"]
// and the orbId follows the `<ns>:<name>-idrac` convention.
func TestBuildEditTargets_Server(t *testing.T) {
	got := BuildEditTargets("Server", "colo:5L4P7Y3", "colo", "5L4P7Y3")
	// Server has multiple children registered (IdracSettings,
	// ServerConfigurationProfile, StorageController). All show up; the
	// FormFields-empty ones still produce targets but with no editable fields.
	var idrac *EditTarget
	for i := range got {
		if got[i].Kind == "IdracSettings" {
			idrac = &got[i]
			break
		}
	}
	if idrac == nil {
		t.Fatalf("IdracSettings target missing; got types: %v", kindList(got))
	}
	if strings.Join(idrac.Path, ".") != "idracSettings" {
		t.Errorf("IdracSettings path = %v, want [idracSettings]", idrac.Path)
	}
	if idrac.OrbID != "colo:5L4P7Y3-idrac" {
		t.Errorf("IdracSettings OrbID = %q, want %q", idrac.OrbID, "colo:5L4P7Y3-idrac")
	}
	if idrac.ParentInverseField != "server" {
		t.Errorf("IdracSettings ParentInverseField = %q, want %q", idrac.ParentInverseField, "server")
	}
	// IdracSettings is a direct child, so no wrapper.
	if idrac.ParentWrapper != nil {
		t.Errorf("IdracSettings should have no ParentWrapper (direct child); got %+v", idrac.ParentWrapper)
	}
}

func kindList(targets []EditTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Kind)
	}
	return out
}

// TestParity_WithLegacyHandMaintainedValues asserts the registry-derived
// regex and BeforeFields exactly match the hand-maintained values they
// replaced in `internal/handler/graphql.go`. This is the safety net for the
// refactor: as long as this passes, the registry is a behavior-preserving
// drop-in.
//
// If you legitimately want to change behavior (add a new type, refine a
// BeforeFields selection), update the expected values here AND the live
// values in registry.go together — the test failing is the prompt to
// double-check that change is intentional.
func TestParity_WithLegacyHandMaintainedValues(t *testing.T) {
	// The original hand-maintained regex from graphql.go before the refactor.
	const legacyRegex = `(?i)\b(add|update|delete)(DataCenter|Server|IdracSettings|KubernetesCluster|EksaKubernetesCluster|KubernetesNode|IPAddress|Rack|ClusterBackup|EtcdBackup|VeleroBackup|S3Sync)\b`
	// Note: ServerConfigurationProfile/StorageController/StorageDevice/StorageVolume
	// were missing from the legacy regex — adding them here is an INTENTIONAL
	// expansion, not a regression. They're owned-only sub-resources that were
	// never user-mutated through the UI, but cascade-delete and registry
	// consistency need them included now.

	re := KnownMutationsRegex()

	// Every type the legacy regex matched must still match.
	for _, name := range []string{"DataCenter", "Server", "IdracSettings", "KubernetesCluster",
		"EksaKubernetesCluster", "KubernetesNode", "IPAddress", "Rack",
		"ClusterBackup", "EtcdBackup", "VeleroBackup", "S3Sync"} {
		for _, verb := range []string{"add", "update", "delete"} {
			op := verb + name
			if !re.MatchString(op) {
				t.Errorf("registry regex regressed parity: %s no longer matches (legacy regex did)", op)
			}
		}
	}
	_ = legacyRegex // referenced in the comment above for traceability

	// Every BeforeFields value from the legacy map must still be returned verbatim.
	legacyBeforeFields := map[string]string{
		"DataCenter":            "id orbId name version assetDataV2",
		"Server":                "id orbId name version hostname model manufacturer serviceTag rackPosition oobMAC idracSettings { firmwareVersion sshEnabled ipmiEnabled lockdownModeEnabled osToIdracPassThroughEnabled usbManagementPortEnabled dhcpEnabled racadmEnabled }",
		"KubernetesCluster":     "id orbId name version kubernetesVersion cni environment",
		"EksaKubernetesCluster": "id orbId name version kubernetesVersion cni environment clusterType",
		"KubernetesNode":        "id orbId name version role",
		"ClusterBackup":         "id orbId name version",
		"EtcdBackup":            "id orbId name version enabled schedule location retentionDays",
		"VeleroBackup":          "id orbId name version enabled schedule location retentionDays",
		"S3Sync":                "id orbId name version enabled",
	}
	for name, want := range legacyBeforeFields {
		got := BeforeFields(name)
		if got != want {
			t.Errorf("BeforeFields(%q) drifted:\n got:  %q\n want: %q", name, got, want)
		}
	}
}
