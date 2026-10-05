//go:build integration

package configitems

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// These measure the PRODUCTION resolution against the running schema and the
// SHIPPED views config: DGraphSchemaClient introspects, ResolveViewsFromConfig
// applies the declarations. Nothing here reimplements either — a test that
// reimplements the rule it checks can only confirm its own copy, and a test
// against a fixture views document could pass while the shipped one is wrong.

func dgraphURLs() (graphql, admin string) {
	base := os.Getenv("ORBITAL_DGRAPH_BASE")
	if base == "" {
		base = "http://localhost:8083"
	}
	return base + "/graphql", base + "/admin"
}

// derivedScalars runs the production path and additionally reports why each
// non-derived field was dropped, for the human-readable report below.
func derivedScalars(t *testing.T) (map[string][]string, []string, map[string][]string) {
	t.Helper()
	gql, admin := dgraphURLs()
	client := NewDGraphSchemaClient(gql, admin)

	byType, ifaceFields, err := client.Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	sort.Strings(ifaceFields)

	iface := map[string]bool{}
	for _, f := range ifaceFields {
		iface[f] = true
	}
	// Sub-interfaces are views, not editable types.
	//
	// Introspect returns them alongside the concrete types so /clusters can be
	// interface-backed. They implement nothing, have no Add payload and appear
	// in no registry, and every assertion below is about what an operator may
	// EDIT — which happens on a concrete type. Their annotations are already
	// counted on each implementation, which inherits them.
	// Collected BEFORE the interfaces are removed, because the views config is
	// resolved against the whole schema: KubernetesCluster is an interface AND a
	// view, and validating against a map it had been deleted from reported every
	// member pointing at it as stale.
	full := make(map[string]TypeInfo, len(byType))
	for name, info := range byType {
		full[name] = info
	}
	for name, info := range byType {
		if info.IsInterface {
			delete(byType, name)
			_ = name
		}
	}

	excluded := map[string][]string{}
	for typeName, info := range byType {
		for _, f := range info.Fields {
			switch {
			case !f.Editable:
				excluded[typeName] = append(excluded[typeName], f.Name+" (not a plain scalar)")
			case iface[f.Name]:
				excluded[typeName] = append(excluded[typeName], f.Name+" (ConfigItem interface)")
			}
		}
	}

	cfg, _, _, err := LoadViewConfig(viewsConfigPath(t), "")
	if err != nil {
		t.Fatalf("load the shipped views config: %v", err)
	}
	validated, warn := cfg.Validate(full, "")
	if len(warn) > 0 {
		t.Fatalf("the shipped views config must validate clean against the deployed schema:\n  %s",
			strings.Join(warn, "\n  "))
	}
	views, err := ResolveViewsFromConfig(full, ifaceFields, validated)
	if err != nil {
		t.Fatalf("resolve views: %v", err)
	}
	derived := map[string][]string{}
	for _, v := range views {
		if full[v.Type].IsInterface {
			continue
		}
		derived[v.Type] = v.Fields
	}
	return derived, ifaceFields, excluded
}

// viewsConfigPath is the repo's shipped views document, resolved absolutely —
// a test's working directory is its own package, so the relative default in
// config.go points at nothing from here.
func schemaVersionLabel(t *testing.T) string {
	_, thisFile, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "schema", "VERSION"))
	if err != nil {
		t.Fatalf("read schema/VERSION: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

func viewsConfigPath(t *testing.T) string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "config", "views.yaml")
}

// TestDerivedFieldsVsRegistry_ExactlyMatchesToday is acceptance item 8.
//
// With the editorIgnoredBridge applied, derivation must reproduce today's
// hand-maintained FormFields EXACTLY, for every type, with no exception list.
// That is what makes the swap provably behaviour-neutral: an operator can do
// exactly what they could before, no more and no less.
//
// It fails on any difference in either direction:
//
//   - derived but not declared  -> a field silently BECOMES editable
//   - declared but not derived  -> a field silently STOPS being editable
//
// The second is the dangerous one: it is how `name` on DataCenter and Rack would
// have vanished, since `name` lives on the ConfigItem interface.
//
// A newly added schema field therefore fails this test until someone decides
// whether it is editable — which is the point. The decision happens when the
// field is added, not silently by default.
func TestDerivedFieldsVsRegistry_ExactlyMatchesToday(t *testing.T) {
	derived, ifaceFields, excluded := derivedScalars(t)
	_ = excluded

	t.Logf("ConfigItem interface fields (never editable on any type): %v", ifaceFields)
	t.Logf("types implementing ConfigItem: %d", len(derived))

	// Baseline is the fixtureFields SNAPSHOT, not the registry: the
	// hand-maintained FormFields it used to read have been deleted, which is the
	// point of this whole change. The snapshot records what was editable on
	// 2026-09-24, so this still proves derivation reproduces the pre-swap
	// behaviour — it just no longer needs the thing it replaced to still exist.
	declared := map[string][]string{}
	for _, name := range fixtureTypeNames() {
		f := append([]string(nil), fixtureFields(name)...)
		sort.Strings(f)
		declared[name] = f
	}

	var names []string
	for n := range derived {
		names = append(names, n)
	}
	sort.Strings(names)

	agree := 0
	for _, n := range names {
		d, has := declared[n]
		if !has {
			t.Errorf("%s implements ConfigItem but is not in registry.Types; derived=%v", n, derived[n])
			continue
		}
		gained := difference(derived[n], d)
		lost := difference(d, derived[n])
		if len(gained) == 0 && len(lost) == 0 {
			agree++
			continue
		}
		if len(gained) > 0 {
			t.Errorf("%s: derivation would newly expose %v for editing.\n"+
				"  Decide: add it to editorIgnoredBridge to keep it read-only, or to FormFields to open it\n"+
				"  deliberately. Silently becoming editable is not an option.", n, gained)
		}
		if len(lost) > 0 {
			t.Errorf("%s: derivation would REMOVE %v, editable today — a silent capability regression.\n"+
				"  If the field is on the ConfigItem interface, add it to the type's `editable:` annotation\n"+
				"  in schema/schema.graphql (e.g. \"\"\"editable: name\"\"\") and re-apply the schema.\n"+
				"  NOTE: this test reads the DEPLOYED schema, so it fails once after an annotation change\n"+
				"  until the schema is applied to the test alpha.", n, lost)
		}
	}
	t.Logf("types where derived == declared: %d/%d", agree, len(names))
	if agree != len(names) {
		t.Errorf("only %d of %d types derive exactly as declared; the swap is NOT behaviour-neutral", agree, len(names))
	}
}

// TestDerivedBeforeFieldsVsRegistry_Measurement was DELETED 2026-09-25.
//
// It measured whether the hand-maintained BeforeFields strings were derivable
// from the same model as the field list. They were — 12 of 19 matched exactly
// and all 7 differences were the declared string lagging behind — so they are
// now GENERATED by BeforeSelection and the hand-maintained strings are gone.
// The measurement cannot run without the thing it measured, and its finding is
// recorded in the plan and CHANGELOG.

func difference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}

// TestDeployedSchemaCarriesTheExpectedAnnotations guards the schema half of
// editability, which nothing else fails loudly on.
//
// The ENVIRONMENT COUPLING hazard did not go away when the annotations did — it
// moved. Editability is a property of the views document a deployment is
// running, so a deployment carrying an older views.yaml silently gets a
// different editable set from every other one. Nothing fails; the editor simply
// offers fields it should not, in one environment and not another.
//
// Zero is the signature of exactly that, and it must fail rather than pass
// quietly. The count is a smoke alarm, not a guarantee: it says this environment
// differs, not which answer was intended.
func TestDeployedViewsCarryTheExpectedSuppressions(t *testing.T) {
	derived, _, _ := derivedScalars(t)

	// Fields the views config takes OUT of the editor: scanned hardware facts a
	// human must not type. Measured, not asserted from the file — the question
	// is what the resolver produced, not what the YAML says.
	suppressed := 0
	gql, admin := dgraphURLs()
	byType, ifaceFields, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	iface := map[string]bool{}
	for _, f := range ifaceFields {
		iface[f] = true
	}
	for typeName, info := range byType {
		if info.IsInterface {
			continue
		}
		editable := map[string]bool{}
		for _, f := range derived[typeName] {
			editable[f] = true
		}
		for _, f := range info.Fields {
			if f.Editable && !iface[f.Name] && !editable[f.Name] {
				suppressed++
			}
		}
	}

	const want = 29
	if suppressed == 0 {
		t.Fatalf("the resolved views suppress NO fields. This environment is running a views "+
			"document that predates them, so %d fields are editable here that are not editable "+
			"elsewhere. Check ORBITAL_VIEWS_PATH and the overlay.", want)
	}
	if suppressed != want {
		t.Errorf("resolved views suppress %d fields, want %d", suppressed, want)
	}

	// A misspelled annotation in the SCHEMA still suppresses nothing and still
	// says nothing — more so now that the vocabulary is three words.
	if u := UnknownAnnotationsIn(byType); len(u) > 0 {
		t.Errorf("unrecognised annotations in the deployed schema — these do NOTHING and are "+
			"silent without this check: %v", u)
	}
}

// TestDerivedPayloadField pins the mutation payload field against a dated
// snapshot, now that the hand-maintained list is gone.
//
// It is derived from Add<Type>Payload — the field whose unwrapped type IS the
// type — which is what gets the irregular cases right without anyone listing
// them. Two of them are irregular, and the hand-maintained list got one wrong:
// IPAddress is `iPAddress`, because DGraph lower-cases only the first character.
func TestDerivedPayloadField(t *testing.T) {
	gql, admin := dgraphURLs()
	byType, _, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	for typeName, want := range map[string]string{
		"Server":        "server",
		"DataCenter":    "dataCenter",
		"S3Sync":        "s3Sync",    // does not pluralise cleanly
		"IPAddress":     "iPAddress", // acronym-initial: NOT ipAddress
		"IdracSettings": "idracSettings",
	} {
		info, ok := byType[typeName]
		if !ok {
			t.Errorf("%s missing from the snapshot", typeName)
			continue
		}
		if info.PayloadField != want {
			t.Errorf("%s: derived PayloadField %q, want %q", typeName, info.PayloadField, want)
		}
	}
	// Every CONCRETE type must resolve one, or the audit extractor cannot find
	// the orbId in a mutation response. A sub-interface has no Add payload —
	// DGraph generates mutations for concrete types only — and that absence is
	// the normal case, not a gap.
	for name, info := range byType {
		if info.IsInterface {
			continue
		}
		if info.PayloadField == "" {
			t.Errorf("%s resolved no PayloadField", name)
		}
	}
}

// Implements resolves interface-typed ownership, so every ConfigItem type must
// report the interface it is discovered through.
func TestDerivedImplements(t *testing.T) {
	gql, admin := dgraphURLs()
	byType, _, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	for name, info := range byType {
		if info.IsInterface {
			// An interface implements nothing; it is implemented BY things.
			continue
		}
		if !containsField(info.Implements, "ConfigItem") {
			t.Errorf("%s does not report implementing ConfigItem: %v", name, info.Implements)
		}
	}
	// The case the lookup exists for: a concrete cluster implements the
	// interface its backup sub-kinds name as their owner.
	if info, ok := byType["EksaKubernetesCluster"]; ok {
		if !containsField(info.Implements, "KubernetesCluster") {
			t.Errorf("EksaKubernetesCluster must implement KubernetesCluster: %v", info.Implements)
		}
	}
}

func containsField(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestDerivedOrbIDSuffixMatchesRegistry proves the orbId suffix convention can
// leave Go: the annotated value must equal what leafSuffix/wrapperSuffix return.
//
// Those were switch statements naming six irregular types — IdracSettings is
// `idrac`, not `idracsettings`. The convention belongs with the type it names,
// and an owned child's orbId is DERIVED from it (`<ns>:<parent>-<suffix>`), so a
// wrong value silently creates a phantom entity rather than editing the real one.
func TestDerivedOrbIDSuffix(t *testing.T) {
	gql, admin := dgraphURLs()
	byType, _, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	// The irregular ones, which are the whole reason this is not just ToLower.
	irregular := map[string]string{
		"IdracSettings":              "idrac",
		"ServerConfigurationProfile": "scp",
		"EtcdBackup":                 "etcd-backup",
		"VeleroBackup":               "velero-backup",
		"S3Sync":                     "s3sync",
		"ClusterBackup":              "backup",
	}
	for typeName, want := range irregular {
		info, ok := byType[typeName]
		if !ok {
			t.Errorf("%s missing from the snapshot", typeName)
			continue
		}
		if info.OrbIDSuffix != want {
			t.Errorf("%s: derived orbId suffix %q, want %q — an owned child's orbId is built from "+
				"this, so a wrong value addresses an entity that does not exist", typeName, info.OrbIDSuffix, want)
		}
	}
	// And a regular one still falls back to the lower-cased type name.
	if info, ok := byType["StorageDevice"]; ok && info.OrbIDSuffix != "storagedevice" {
		t.Errorf("StorageDevice suffix = %q, want the ToLower default %q", info.OrbIDSuffix, "storagedevice")
	}
}

// TestDerivedJSONStringFields pins the JSON-in-a-String set against a dated
// snapshot, now that the hand-maintained list is gone.
//
// DataCenter.assetDataV2 is declared `String` but holds JSON. Nothing about the
// TYPE says so — it is String either way — which is why it is annotated rather
// than derived, and why it was hand-listed before. Getting it wrong is silent
// in one direction and loud in the other: unmarked, the editor sends structure
// and DGraph rejects with "cannot use as String"; wrongly marked, a real string
// is stringified again and the stored value gains quotes.
func TestDerivedJSONStringFields(t *testing.T) {
	gql, admin := dgraphURLs()
	byType, _, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}

	want := map[string][]string{"DataCenter": {"assetDataV2"}}
	for typeName, info := range byType {
		got := JSONStringFieldsFor(info)
		expect := want[typeName]
		if len(got) != len(expect) {
			t.Errorf("%s: jsonString fields = %v, want %v", typeName, got, expect)
			continue
		}
		for _, f := range expect {
			if !containsField(got, f) {
				t.Errorf("%s: expected %q to be annotated jsonString, got %v", typeName, f, got)
			}
		}
	}
}
