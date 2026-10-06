package configitems

import (
	"strings"
	"testing"
)

// A malformed pattern must be REFUSED, not silently treated as literal text.
// A pattern that degraded into the literal string "{serviceTag}" would mint a
// colliding orbId for every row of its type, which is the exact failure the
// annotation exists to prevent.
func TestParseOrbIDPattern_RefusesMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		wantErr   bool
	}{
		{"own field", "{kind}-{serviceTag}", false},
		{"crosses an edge", "{kind}-{server.serviceTag}", false},
		{"two hops", "{storageController.server.serviceTag}-{name}", false},
		{"literal only", "idrac", false},
		{"external opt-out", "external", false},
		{"unclosed placeholder", "{kind}-{serviceTag", true},
		{"unmatched close", "kind}-{serviceTag}", true},
		{"empty placeholder", "{kind}-{}", true},
		{"empty pattern", "", true},
		{"whitespace only", "   ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseOrbIDPattern(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatalf("ParseOrbIDPattern(%q) = nil error, want refusal", tc.raw)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ParseOrbIDPattern(%q) = %v, want accepted", tc.raw, err)
			}
		})
	}
}

// `external` is the ONLY way to have no constructable pattern, and it must be
// distinguishable from a type that simply forgot one — the build-time guard
// below depends on telling those apart.
func TestParseOrbIDPattern_ExternalIsAnOptOut(t *testing.T) {
	p, err := ParseOrbIDPattern(OrbIDPatternExternal)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.External {
		t.Fatal("orbIdPattern: external did not set External")
	}
	if _, err := ConstructOrbID("ns", "foo", []OrbIDPattern{p}, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("constructing an external orbId succeeded; it must refuse")
	}
}

// `{kind}` defaults to the KEBAB type name, not the lower-cased one that
// OrbIDSuffixFor falls back to. This is the whole reason the two helpers are
// separate: `networkdevice` would be wrong against every stored orbId, which
// all read `network-device-`.
func TestOrbIDKindFor_DefaultsToKebabNotLowercase(t *testing.T) {
	for _, tc := range []struct{ typeName, doc, want string }{
		{"Server", "", "server"},
		{"NetworkDevice", "", "network-device"},
		{"ServerMaintenance", "", "server-maintenance"},
		{"IPAddress", "", "ip-address"},
		// An orbIdSuffix annotation wins — that is how the irregular legacy
		// tokens (`idrac`, not `idrac-settings`) stay correct.
		{"IdracSettings", "orbIdSuffix: idrac", "idrac"},
		{"S3Sync", "orbIdSuffix: s3sync", "s3sync"},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			if got := OrbIDKindFor(tc.typeName, tc.doc); got != tc.want {
				t.Errorf("OrbIDKindFor(%q, %q) = %q, want %q", tc.typeName, tc.doc, got, tc.want)
			}
		})
	}
}

// With several patterns the first whose placeholders ALL resolve wins. That is
// how an XOR-parented type picks the owner it actually has, and it is what
// `derivesIdFrom:` could not express.
func TestConstructOrbID_FirstResolvablePatternWins(t *testing.T) {
	patterns := mustParse(t,
		"{kind}-{server.serviceTag}-{name}",
		"{kind}-{networkDevice.serial}-{name}")

	t.Run("server-owned", func(t *testing.T) {
		got, err := ConstructOrbID("colo", "network-interface", patterns, fromMap(map[string]string{
			"server.serviceTag": "BMP6K74",
			"name":              "NIC.Integrated.1-1",
		}))
		if err != nil {
			t.Fatalf("construct: %v", err)
		}
		if want := "colo:network-interface-BMP6K74-NIC.Integrated.1-1"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("device-owned falls through to the second pattern", func(t *testing.T) {
		got, err := ConstructOrbID("colo", "network-interface", patterns, fromMap(map[string]string{
			"networkDevice.serial": "JX3623130496",
			"name":                 "ge-0/0/0",
		}))
		if err != nil {
			t.Fatalf("construct: %v", err)
		}
		if want := "colo:network-interface-JX3623130496-ge-0/0/0"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// A placeholder that does not resolve must FAIL the whole construction rather
// than contribute an empty segment. `network-interface--NIC.Integrated.1-1` and
// `server-` would both be accepted by DGraph, look approximately right, and
// collide or orphan later — the worst available outcome.
func TestConstructOrbID_FailsRatherThanHalfInterpolate(t *testing.T) {
	patterns := mustParse(t, "{kind}-{server.serviceTag}-{name}")
	for _, tc := range []struct {
		name string
		vals map[string]string
	}{
		{"missing placeholder", map[string]string{"name": "NIC.Integrated.1-1"}},
		{"empty value", map[string]string{"server.serviceTag": "", "name": "NIC.Integrated.1-1"}},
		{"nothing resolves", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConstructOrbID("colo", "network-interface", patterns, fromMap(tc.vals))
			if err == nil {
				t.Fatalf("construct returned %q, want an error", got)
			}
		})
	}
	t.Run("no patterns at all", func(t *testing.T) {
		if _, err := ConstructOrbID("colo", "server", nil, fromMap(nil)); err == nil {
			t.Fatal("construct with no patterns succeeded")
		}
	})
	t.Run("no namespace", func(t *testing.T) {
		if _, err := ConstructOrbID("", "server", mustParse(t, "{kind}"), fromMap(nil)); err == nil {
			t.Fatal("construct with no namespace succeeded")
		}
	})
}

// The shipped patterns must reproduce what is ACTUALLY STORED, including the
// legacy shapes — otherwise the audit that compares stored against constructed
// reports the whole graph as wrong and nobody reads it again.
//
// Samples were read from the blue dev graph on 2026-10-06, one real row per
// type. This is the test that would have caught `{kind}` defaulting to
// `networkdevice`, and the one that pins the two-hop paths.
func TestShippedPatterns_ReproduceStoredOrbIDs(t *testing.T) {
	for _, tc := range []struct {
		typeName, kind, pattern, namespace, want string
		vals                                     map[string]string
	}{
		{"DataCenter", "data-center", "{name}", "2f-uae", "2f-uae:2f-uae",
			map[string]string{"name": "2f-uae"}},
		{"Rack", "rack", "{name}", "2f-uae", "2f-uae:Rack-3",
			map[string]string{"name": "Rack-3"}},
		{"Server", "server", "{kind}-{serviceTag}", "2f-uae", "2f-uae:server-2MLN3D4",
			map[string]string{"serviceTag": "2MLN3D4"}},
		{"IdracSettings", "idrac", "{server.serviceTag}-{kind}", "2f-uae", "2f-uae:7MLN3D4-idrac",
			map[string]string{"server.serviceTag": "7MLN3D4"}},
		{"ServerMaintenance", "server-maintenance", "{kind}-{server.serviceTag}", "colo", "colo:server-maintenance-4RK3V64",
			map[string]string{"server.serviceTag": "4RK3V64"}},
		{"StorageController", "storage-controller", "{server.serviceTag}-{name}", "alaska-dot-cruiser", "alaska-dot-cruiser:GT36XB4-BOSS-S1",
			map[string]string{"server.serviceTag": "GT36XB4", "name": "BOSS-S1"}},
		{"StorageDevice", "storage-device", "{serialNumber}", "alaska-dot-cruiser", "alaska-dot-cruiser:YEQ0A0KG0KG3",
			map[string]string{"serialNumber": "YEQ0A0KG0KG3"}},
		{"StorageVolume", "storage-volume", "{storageController.server.serviceTag}-{name}", "alaska-dot-cruiser", "alaska-dot-cruiser:JT36XB4-VirtualDisk-2",
			map[string]string{"storageController.server.serviceTag": "JT36XB4", "name": "VirtualDisk-2"}},
		{"NetworkAdapter", "network-adapter", "{kind}-{server.serviceTag}-{name}", "colo", "colo:network-adapter-BFRHDX3-NIC.Slot.1",
			map[string]string{"server.serviceTag": "BFRHDX3", "name": "NIC.Slot.1"}},
		{"NetworkDevice", "network-device", "{kind}-{serial}", "colo", "colo:network-device-JX3623130496",
			map[string]string{"serial": "JX3623130496"}},
		{"NetworkInterface", "network-interface", "{kind}-{server.serviceTag}-{name}", "colo", "colo:network-interface-BMP6K74-NIC.Integrated.1-1",
			map[string]string{"server.serviceTag": "BMP6K74", "name": "NIC.Integrated.1-1"}},
		{"EksaKubernetesCluster", "eksa-kubernetes-cluster", "{name}", "alaska-dot-cruiser", "alaska-dot-cruiser:adot-m",
			map[string]string{"name": "adot-m"}},
		{"KubernetesNode", "kubernetes-node", "{name}", "alaska-dot-cruiser", "alaska-dot-cruiser:alaska-m-cp1",
			map[string]string{"name": "alaska-m-cp1"}},
		{"ClusterBackup", "backup", "{cluster.name}-{kind}", "colo", "colo:dev-main-backup",
			map[string]string{"cluster.name": "dev-main"}},
		{"EtcdBackup", "etcd-backup", "{clusterBackupEtcd.cluster.name}-{kind}", "colo", "colo:dev-main-etcd-backup",
			map[string]string{"clusterBackupEtcd.cluster.name": "dev-main"}},
		{"VeleroBackup", "velero-backup", "{clusterBackupVelero.cluster.name}-{kind}", "colo", "colo:dev-main-velero-backup",
			map[string]string{"clusterBackupVelero.cluster.name": "dev-main"}},
		{"S3Sync", "s3sync", "{clusterBackupS3Sync.cluster.name}-{kind}", "colo", "colo:dev-main-s3sync",
			map[string]string{"clusterBackupS3Sync.cluster.name": "dev-main"}},
		{"IPAddress", "ip-address", "{address}", "2f-uae", "2f-uae:10.96.127.54",
			map[string]string{"address": "10.96.127.54"}},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			got, err := ConstructOrbID(tc.namespace, tc.kind, mustParse(t, tc.pattern), fromMap(tc.vals))
			if err != nil {
				t.Fatalf("construct: %v", err)
			}
			if got != tc.want {
				t.Errorf("pattern %q built %q, but the graph stores %q", tc.pattern, got, tc.want)
			}
		})
	}
}

// The annotation must be RECOGNISED, or every pattern in the schema would be
// reported at boot as an unknown annotation that does nothing.
func TestOrbIDPatternAnnotation_IsRecognised(t *testing.T) {
	if u := UnknownAnnotations("orbIdPattern: {kind}-{serviceTag}"); len(u) > 0 {
		t.Errorf("orbIdPattern reported as unknown: %v", u)
	}
	// And a typo of it must still be caught — the whole point of the unknown
	// reporter is that a misspelled annotation is otherwise a valid docstring.
	if u := UnknownAnnotations("orbIdPatern: {kind}-{serviceTag}"); len(u) == 0 {
		t.Error("a misspelled orbIdPattern was not reported as unknown")
	}
}

// A pattern naming a field the type does not have is reported, because it can
// never resolve — the id would silently fall through to the next alternative,
// or fail construction entirely.
func TestOrbIDPatternWarnings_ReportUnresolvableFields(t *testing.T) {
	scalar := DerivedField{Name: "serviceTag", Kind: "SCALAR"}
	edge := DerivedField{Name: "server", Kind: "OBJECT"}
	list := DerivedField{Name: "servers", Kind: "OBJECT", IsList: true}
	types := map[string]TypeInfo{
		"Good":      {Doc: "orbIdPattern: {kind}-{serviceTag}", Fields: []DerivedField{scalar}},
		"GoodEdge":  {Doc: "orbIdPattern: {kind}-{server.serviceTag}", Fields: []DerivedField{edge}},
		"Bad":       {Doc: "orbIdPattern: {kind}-{nope}", Fields: []DerivedField{scalar}},
		"Malformed": {Doc: "orbIdPattern: {kind}-{unclosed", Fields: []DerivedField{scalar}},
		// A bare relationship interpolates a NODE, not a value.
		"BareEdge": {Doc: "orbIdPattern: {kind}-{server}", Fields: []DerivedField{edge}},
		// A list cannot say WHICH row.
		"ListEdge": {Doc: "orbIdPattern: {kind}-{servers.serviceTag}", Fields: []DerivedField{list}},
	}
	w := strings.Join(OrbIDPatternWarnings(types), "\n")
	for _, ok := range []string{"Good:", "GoodEdge:"} {
		if strings.Contains(w, ok) {
			t.Errorf("a valid pattern was reported (%s): %s", ok, w)
		}
	}
	for _, bad := range []string{"Bad:", "Malformed:", "BareEdge:", "ListEdge:"} {
		if !strings.Contains(w, bad) {
			t.Errorf("%s was not reported: %s", bad, w)
		}
	}
}

// The EDITOR's first-time-create id must match what is already stored.
//
// This is the regression this whole change exists for. The editor used to build
// `<namespace>:<rootName>-<suffix>`, and on a Server `name` is the hostname —
// so configuring iDRAC for the first time would have minted
// `2f-uae:r04-u25.2f-uae-idrac` against 155 stored `2f-uae:<serviceTag>-idrac`.
// Nothing would have errored: the next scan computes the conventional id,
// misses, and creates a SECOND node, orphaning the first with its audit history.
//
// Expected values are real rows from the dev graph, 2026-10-06.
func TestDerivedChildOrbID_MatchesStoredChildren(t *testing.T) {
	meta := fixtureMetaWithPatterns(t)

	t.Run("children of a Server", func(t *testing.T) {
		// A Server whose NAME and serviceTag differ — the case that was broken.
		root := map[string]string{"name": "r04-u25.2f-uae", "serviceTag": "2MLN3D4"}
		for _, tc := range []struct{ childType, want string }{
			{"IdracSettings", "2f-uae:2MLN3D4-idrac"},
			{"ServerConfigurationProfile", "2f-uae:2MLN3D4-scp"},
			{"ServerMaintenance", "2f-uae:server-maintenance-2MLN3D4"},
		} {
			t.Run(tc.childType, func(t *testing.T) {
				got := derivedChildOrbID(meta, tc.childType, "2f-uae", root["name"], root)
				if got != tc.want {
					t.Errorf("derived %q, but the graph stores %q", got, tc.want)
				}
				if strings.Contains(got, root["name"]) {
					t.Errorf("derived id %q embeds the root's NAME; it must use the natural key", got)
				}
			})
		}
	})

	t.Run("children of a cluster, including through the wrapper", func(t *testing.T) {
		root := map[string]string{"name": "dev-main"}
		for _, tc := range []struct{ childType, want string }{
			{"ClusterBackup", "colo:dev-main-backup"},
			{"EtcdBackup", "colo:dev-main-etcd-backup"},
			{"VeleroBackup", "colo:dev-main-velero-backup"},
			{"S3Sync", "colo:dev-main-s3sync"},
		} {
			t.Run(tc.childType, func(t *testing.T) {
				if got := derivedChildOrbID(meta, tc.childType, "colo", root["name"], root); got != tc.want {
					t.Errorf("derived %q, but the graph stores %q", got, tc.want)
				}
			})
		}
	})

	// Degrading to the old formula is deliberate: a deployed schema that has not
	// been re-applied since the annotations landed has no patterns, and today's
	// behaviour is the right thing to fall back to. A MISSING pattern is caught
	// at build time instead.
	t.Run("falls back when the type declares no pattern", func(t *testing.T) {
		none := func(string) TypeInfo { return TypeInfo{} }
		got := derivedChildOrbID(none, "IdracSettings", "2f-uae", "r04-u25.2f-uae", map[string]string{})
		if want := "2f-uae:r04-u25.2f-uae-idracsettings"; got != want {
			t.Errorf("fallback = %q, want %q", got, want)
		}
	})
}

// NewOrbID builds a ROOT entity's id from the same patterns, with the caller
// supplying placeholder paths as keys.
func TestNewOrbID_UsesTheSchemaPattern(t *testing.T) {
	v := View{Type: "Server", OrbIDKind: "server", OrbIDPattern: mustParse(t, "{kind}-{serviceTag}")}
	if got := v.NewOrbID("2f-uae", map[string]string{"serviceTag": "2MLN3D4"}); got != "2f-uae:server-2MLN3D4" {
		t.Errorf("NewOrbID = %q, want %q", got, "2f-uae:server-2MLN3D4")
	}
	// A partial id is worse than none — it collides with every other node
	// missing the same field.
	for _, tc := range []struct {
		name   string
		ns     string
		values map[string]string
	}{
		{"blank key field", "2f-uae", map[string]string{"serviceTag": "  "}},
		{"missing key field", "2f-uae", map[string]string{}},
		{"no namespace", "", map[string]string{"serviceTag": "2MLN3D4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := v.NewOrbID(tc.ns, tc.values); got != "" {
				t.Errorf("NewOrbID = %q, want \"\"", got)
			}
		})
	}
	t.Run("no pattern declared", func(t *testing.T) {
		bare := View{Type: "Server", OrbIDKind: "server"}
		if got := bare.NewOrbID("2f-uae", map[string]string{"serviceTag": "2MLN3D4"}); got != "" {
			t.Errorf("NewOrbID with no pattern = %q, want \"\"", got)
		}
	})
}

// fixtureMetaWithPatterns mirrors the shipped schema for the types the editor
// can create, so the test does not need a deployed graph.
func fixtureMetaWithPatterns(t *testing.T) MetaFor {
	t.Helper()
	docs := map[string]string{
		"IdracSettings":              "orbIdSuffix: idrac\norbIdPattern: {server.serviceTag}-{kind}",
		"ServerConfigurationProfile": "orbIdSuffix: scp\norbIdPattern: {server.serviceTag}-{kind}",
		"ServerMaintenance":          "orbIdPattern: {kind}-{server.serviceTag}",
		"ClusterBackup":              "orbIdSuffix: backup\norbIdPattern: {cluster.name}-{kind}",
		"EtcdBackup":                 "orbIdSuffix: etcd-backup\norbIdPattern: {clusterBackupEtcd.cluster.name}-{kind}",
		"VeleroBackup":               "orbIdSuffix: velero-backup\norbIdPattern: {clusterBackupVelero.cluster.name}-{kind}",
		"S3Sync":                     "orbIdSuffix: s3sync\norbIdPattern: {clusterBackupS3Sync.cluster.name}-{kind}",
	}
	return func(typeName string) TypeInfo {
		doc := docs[typeName]
		pats, err := OrbIDPatternsFor(doc)
		if err != nil {
			t.Fatalf("fixture %s: %v", typeName, err)
		}
		return TypeInfo{Doc: doc, OrbIDSuffix: OrbIDSuffixFor(typeName, doc), OrbIDPattern: pats}
	}
}

func mustParse(t *testing.T, raws ...string) []OrbIDPattern {
	t.Helper()
	var out []OrbIDPattern
	for _, r := range raws {
		p, err := ParseOrbIDPattern(r)
		if err != nil {
			t.Fatalf("ParseOrbIDPattern(%q): %v", r, err)
		}
		out = append(out, p)
	}
	return out
}

func fromMap(m map[string]string) OrbIDResolver {
	return func(path string) (string, bool) {
		v, ok := m[path]
		return v, ok
	}
}
