package configitems

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeYAML drops a views document in a temp dir and returns its path.
func writeYAML(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const twoViewDefault = `
schemaVersion: v12
pages:
  Server:
    menuWeight: 20
    summary:
      ignoreFields: [sku]
    subgraph:
      - { path: idracSettings, editable: true }
  Rack:
    subgraph:
      - servers
`

// Acceptance 2 — a partial overlay changes only the views it names.
//
// This is the property the whole two-layer design rests on. A deployment that
// customises one page must keep receiving shipped changes to every other page;
// an overlay that had to be a whole-file copy would freeze that deployment at
// the shape it copied, which is the seeding failure spike 38 ruled out for rows
// and which applies to files identically.
func TestLoadViews_PartialOverlayLeavesUnnamedViewsAtDefault(t *testing.T) {
	base := writeYAML(t, "views.yaml", twoViewDefault)
	overlay := writeYAML(t, "overlay.yaml", `
pages:
  Server:
    subgraph:
      - dataCenter
`)

	cfg, _, warnings, err := LoadViewConfig(base, overlay)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("a well-formed overlay must warn about nothing; got %v", warnings)
	}

	// The named page is replaced wholesale — the point of declaring it.
	server := cfg.Pages["Server"]
	if len(server.Subgraph) != 1 || server.Subgraph[0].Path != "dataCenter" {
		t.Errorf("Server subgraph = %+v, want only dataCenter", server.Subgraph)
	}
	if server.MenuWeight != 0 {
		t.Errorf("the overlay's Server declaration replaces the default's, menuWeight included; got %v", server.MenuWeight)
	}

	// The unnamed page is untouched, which is the half that keeps shipping.
	rack := cfg.Pages["Rack"]
	if len(rack.Subgraph) != 1 || rack.Subgraph[0].Path != "servers" {
		t.Errorf("Rack must still come from the shipped default; got %+v", rack.Subgraph)
	}
	if cfg.SchemaVersion != "v12" {
		t.Errorf("an overlay that declares no schemaVersion keeps the default's; got %q", cfg.SchemaVersion)
	}
}

// A views document still carrying the retired derived-ownership keys is SAID,
// key by key. The YAML decoder drops an unknown key in silence, so without this
// an overlay written against the old shape renders pages with no tabs and an
// empty delete, and nothing anywhere says why.
func TestLoadViews_RetiredKeysAreNamedNotSilentlyDropped(t *testing.T) {
	base := writeYAML(t, "views.yaml", twoViewDefault)
	overlay := writeYAML(t, "overlay.yaml", `
ownerReferences:
  NetworkInterface: [server]
pages:
  Server:
    summary:
      refs: [rack]
    tabs:
      - idracSettings
`)
	_, _, warnings, err := LoadViewConfig(base, overlay)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, want := range []string{"`ownerReferences:` is retired", "Server: `tabs:` is retired", "Server: `summary.refs:` is retired"} {
		if !anyContains(warnings, want) {
			t.Errorf("warnings %v must name %q", warnings, want)
		}
	}
	if _, _, clean, _ := LoadViewConfig(base, ""); len(clean) != 0 {
		t.Errorf("a document in the current shape must warn about nothing; got %v", clean)
	}
}

// A missing overlay is the normal state of a deployment that customises
// nothing, and a malformed one must not take the pages down with it. Both are
// reported, because an operator who edits a ConfigMap and sees no change needs
// something to read.
func TestLoadViews_BadOverlayDegradesToDefaultsAndSaysSo(t *testing.T) {
	base := writeYAML(t, "views.yaml", twoViewDefault)

	for _, tc := range []struct {
		name    string
		overlay string
		want    string
	}{
		{"absent", filepath.Join(t.TempDir(), "nothing-here.yaml"), "no views overlay"},
		{"malformed", writeYAML(t, "bad.yaml", "pages:\n  Server: [not, a, mapping]\n"), "not valid YAML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, warnings, err := LoadViewConfig(base, tc.overlay)
			if err != nil {
				t.Fatalf("a bad overlay must never be fatal; got %v", err)
			}
			if len(cfg.Pages) != 2 {
				t.Errorf("the shipped defaults must still be served; got %d pages", len(cfg.Pages))
			}
			if !anyContains(warnings, tc.want) {
				t.Errorf("warnings %v must mention %q", warnings, tc.want)
			}
		})
	}
}

// The hash is what the resolver watches to re-derive without a restart, and what
// the editor stamps so a save opened under an older view can be refused. It has
// to move when EITHER file moves, or one of those two misses a change.
func TestLoadViews_HashCoversBothFiles(t *testing.T) {
	base := writeYAML(t, "views.yaml", twoViewDefault)
	overlay := writeYAML(t, "overlay.yaml", "pages:\n  Server:\n    tabs: [rack]\n")

	_, bare, _, _ := LoadViewConfig(base, "")
	_, withOverlay, _, _ := LoadViewConfig(base, overlay)
	if bare == withOverlay {
		t.Fatal("adding an overlay must move the hash")
	}

	if err := os.WriteFile(overlay, []byte("pages:\n  Server:\n    tabs: [dataCenter]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, edited, _, _ := LoadViewConfig(base, overlay)
	if edited == withOverlay {
		t.Fatal("editing the overlay must move the hash")
	}
}

// A missing SHIPPED DEFAULT is the one fatal case: there is nothing to degrade
// to, and silently serving no views would read as "this schema has no pages".
func TestLoadViews_MissingDefaultIsAnError(t *testing.T) {
	_, _, _, err := LoadViewConfig(filepath.Join(t.TempDir(), "absent.yaml"), "")
	if err == nil {
		t.Fatal("a missing shipped default must be an error")
	}
	if !strings.Contains(err.Error(), "absent.yaml") {
		t.Errorf("the error must name the path it looked at; got %v", err)
	}
}

// validationFixture is a miniature schema with exactly the shapes the §10 table
// discriminates between: a single edge, a list edge, a scalar, and a
// relationship to something that is not a ConfigItem.
func validationFixture() map[string]TypeInfo {
	return map[string]TypeInfo{
		"Server": {Fields: []DerivedField{
			{Name: "hostname", Editable: true, Kind: "SCALAR"},
			{Name: "rack", Kind: "OBJECT", TypeName: "Rack"},
			{Name: "storageControllers", Kind: "OBJECT", TypeName: "StorageController", IsList: true},
			{Name: "oobIP", Kind: "OBJECT", TypeName: "IPAddress"},
			{Name: "auditTrail", Kind: "OBJECT", TypeName: "NotAConfigItem"},
		}},
		"Rack":              {Fields: []DerivedField{{Name: "servers", Kind: "OBJECT", TypeName: "Server", IsList: true}}},
		"StorageController": {Fields: []DerivedField{{Name: "storageDevices", Kind: "OBJECT", TypeName: "StorageDevice", IsList: true}}},
		"StorageDevice":     {Fields: []DerivedField{{Name: "wwn", Editable: true, Kind: "SCALAR"}}},
		"IPAddress": {Fields: []DerivedField{
			{Name: "address", Editable: true, Kind: "SCALAR"},
			{Name: "assignedTo", Kind: "OBJECT", TypeName: "Server"},
		}},
	}
}

// Acceptance 9 — every unsupportable declaration is dropped and logged, and the
// page still renders.
//
// Both halves are the point. Annotations had one property worth keeping — a
// declaration and the schema it described were the same artifact, deployed
// together, unable to disagree. A separate file loses that, so the file has to
// be checked against live introspection instead. NEVER FATAL, because a stale
// view must not stop a page rendering; NEVER SILENT, because a declaration that
// reads as correct and does nothing is worse than one nobody wrote.
func TestValidateViews_UnsupportableDeclarationsDroppedAndLogged(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      ViewConfig
		wantWarn string
		check    func(*testing.T, ViewConfig)
	}{
		{
			name: "tab names a field the deployed schema lacks",
			cfg: ViewConfig{Pages: map[string]PageDecl{"Server": {Subgraph: []MemberDecl{
				{Path: "rack"}, {Path: "tinkerbellIP"},
			}}}},
			wantWarn: "Server has no field tinkerbellIP",
			check: func(t *testing.T, got ViewConfig) {
				assertMembers(t, got, "Server", "rack")
			},
		},
		{
			name: "tab path names a scalar",
			cfg: ViewConfig{Pages: map[string]PageDecl{"Server": {Subgraph: []MemberDecl{
				{Path: "rack"}, {Path: "hostname"},
			}}}},
			wantWarn: "hostname is a scalar, not a relationship",
			check: func(t *testing.T, got ViewConfig) {
				assertMembers(t, got, "Server", "rack")
			},
		},
		{
			name: "member points at something that is not a ConfigItem",
			cfg: ViewConfig{Pages: map[string]PageDecl{"Server": {Subgraph: []MemberDecl{
				{Path: "rack"}, {Path: "auditTrail"},
			}}}},
			wantWarn: "NotAConfigItem is not a ConfigItem type",
			check: func(t *testing.T, got ViewConfig) {
				assertMembers(t, got, "Server", "rack")
			},
		},
		{
			name: "view root type is not in the schema",
			cfg: ViewConfig{Pages: map[string]PageDecl{
				"Server":     {Subgraph: []MemberDecl{{Path: "rack"}}},
				"Decomposed": {Subgraph: []MemberDecl{{Path: "anything"}}},
			}},
			wantWarn: "Decomposed: no such type in the deployed schema",
			check: func(t *testing.T, got ViewConfig) {
				if _, still := got.Pages["Decomposed"]; still {
					t.Error("a page whose root type is gone must be dropped")
				}
				assertMembers(t, got, "Server", "rack")
			},
		},
		{
			name: "canonicalParent edge does not exist",
			cfg: ViewConfig{
				Pages: map[string]PageDecl{"Server": {Subgraph: []MemberDecl{{Path: "oobIP"}}}},
				CanonicalParent: map[string][]CanonicalParentDecl{"IPAddress": {
					{Type: "Server", Field: "serverOobIP", Down: "oobIP"},
					{Type: "Server", Field: "address", Down: "oobIP"},
				}},
			},
			wantWarn: "canonicalParent edge serverOobIP is not a field on IPAddress",
			check: func(t *testing.T, got ViewConfig) {
				if len(got.CanonicalParent["IPAddress"]) != 0 {
					t.Errorf("both edges are unusable; got %+v", got.CanonicalParent["IPAddress"])
				}
				assertMembers(t, got, "Server", "oobIP")
			},
		},
		{
			name: "canonicalParent down-edge does not exist",
			cfg: ViewConfig{
				Pages: map[string]PageDecl{"Server": {Subgraph: []MemberDecl{{Path: "oobIP"}}}},
				CanonicalParent: map[string][]CanonicalParentDecl{"StorageController": {
					{Type: "Server", Field: "storageDevices", Down: "noSuchField"},
				}},
			},
			wantWarn: "canonicalParent down-edge Server.noSuchField does not exist",
			check: func(t *testing.T, got ViewConfig) {
				assertMembers(t, got, "Server", "oobIP")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, warn := tc.cfg.Validate(validationFixture(), "")
			if !anyContains(warn, tc.wantWarn) {
				t.Errorf("warnings %v must mention %q", warn, tc.wantWarn)
			}
			tc.check(t, got)
		})
	}
}

// A subgraph path that walks a LIST mid-way is the headline case, not an error:
// a server's disks hang off its storage controllers, and rendering them is why
// paths exist. The limit that applies to a `columns:` path — every hop a single
// relationship — guards a different hazard (one cell, not many rows) and the
// two must not be harmonised.
func TestValidateViews_ListHopInAMemberPathIsLegal(t *testing.T) {
	cfg := ViewConfig{Pages: map[string]PageDecl{"Server": {Subgraph: []MemberDecl{
		{Path: "storageControllers.storageDevices"},
	}}}}
	got, warn := cfg.Validate(validationFixture(), "")
	if len(warn) != 0 {
		t.Errorf("a list hop must not be reported; got %v", warn)
	}
	assertMembers(t, got, "Server", "storageControllers.storageDevices")
}

// `editable:` the editor cannot honour — on a LIST, or more than two hops
// deep — is refused, SAID, and the member still renders.
//
// The regression class is a silent drop: an editable list has never been
// writable — a target is addressed by path and a path cannot name one row — and
// the editor creates at most one missing intermediate. Quietly clearing the
// flag leaves a page that reads as configured for an edit that will never
// arrive. "Nothing happened" is the one outcome nobody can debug from outside.
func TestValidateViews_EditableTheEditorCannotHonourIsRefusedAndSaid(t *testing.T) {
	cfg := ViewConfig{Pages: map[string]PageDecl{"Server": {Subgraph: []MemberDecl{
		{Path: "storageControllers", Editable: true},
		{Path: "storageControllers.storageDevices", Editable: true},
		{Path: "oobIP.assignedTo.rack", Editable: true},
		{Path: "oobIP", Editable: true},
	}}}}
	got, warn := cfg.Validate(validationFixture(), "")

	for _, want := range []string{"storageControllers is a LIST", "storageControllers.storageDevices is a LIST", "oobIP.assignedTo.rack is more than two hops"} {
		if !anyContains(warn, want) {
			t.Errorf("a refused editable flag (%s) must be reported; got %v", want, warn)
		}
	}
	// The members SURVIVE — read-only, not dropped. Dropping them would take
	// the page's storage tables with them, and out of the delete — a far larger
	// blast radius than the flag that was wrong.
	assertMembers(t, got, "Server", "storageControllers", "storageControllers.storageDevices", "oobIP.assignedTo.rack", "oobIP")

	byPath := map[string]bool{}
	for _, m := range got.Pages["Server"].Subgraph {
		byPath[m.Path] = m.Editable
	}
	if byPath["storageControllers"] || byPath["storageControllers.storageDevices"] || byPath["oobIP.assignedTo.rack"] {
		t.Error("a refused flag must be CLEARED, or the editor would still try to honour it")
	}
	// A single-cardinality tab keeps the flag — this is the flag working, and
	// without it the test would pass for a Validate that cleared everything.
	if !byPath["oobIP"] {
		t.Error("editable on a SINGLE member is legal and must survive")
	}
}

// The schema version is the COARSE signal and must present itself as one.
// schema/VERSION is bumped by hand and only for DGraph-relevant changes, so a
// match does not prove currency — it is a prompt to look, and per-member
// validation is what actually catches a stale path.
func TestValidateViews_SchemaVersionMismatchIsReportedNotFatal(t *testing.T) {
	cfg := ViewConfig{
		SchemaVersion: "v11",
		Pages:         map[string]PageDecl{"Server": {Subgraph: []MemberDecl{{Path: "rack"}}}},
	}
	got, warn := cfg.Validate(validationFixture(), "v12")
	if !anyContains(warn, "v11") || !anyContains(warn, "v12") {
		t.Errorf("the warning must name both versions; got %v", warn)
	}
	assertMembers(t, got, "Server", "rack")

	_, quiet := cfg.Validate(validationFixture(), "v11")
	if len(quiet) != 0 {
		t.Errorf("a matching version must say nothing; got %v", quiet)
	}
}

// Nav membership IS membership of `pages:` — the top level of that section is
// the menu. `menuWeight` orders it and nothing else.
//
// Those were briefly the same key, which silently reversed a settled decision:
// menuWeight had always been order-only, and a type reached the menu on its own.
func TestViewConfig_NavMembershipIsPageMembership(t *testing.T) {
	cfg := ViewConfig{Pages: map[string]PageDecl{
		"DataCenter": {MenuWeight: 10},
	}}
	if w, in := cfg.InNav("DataCenter"); !in || w != 10 {
		t.Errorf("DataCenter must be in the nav at 10; got %d, %v", w, in)
	}
	if _, in := cfg.InNav("Rack"); in {
		t.Error("a type with no page entry is not in the menu")
	}
	if cfg.IsPage("Rack") {
		t.Error("…and has no page, so its rows must not link")
	}
	if _, in := cfg.InNav("NeverHeardOfIt"); in {
		t.Error("a type nothing declares is not in the menu")
	}
}

// Acceptance 6 — canonicalParent resolves a multi-parent node to the first
// matching edge in DECLARED order.
//
// Order is the whole reason this section exists: the candidate list falls out of
// which views claim the type, but the precedence does not, and a map would make
// the answer depend on Go's randomised iteration.
func TestCanonicalParent_FirstMatchingEdgeInDeclaredOrder(t *testing.T) {
	cfg := ViewConfig{CanonicalParent: map[string][]CanonicalParentDecl{
		"NetworkInterface": {
			{Type: "NetworkAdapter", Field: "networkAdapter", Down: "networkInterfaces"},
			{Type: "Server", Field: "server", Down: "networkInterfaces"},
			{Type: "NetworkDevice", Field: "networkDevice", Down: "networkInterfaces"},
		},
	}}
	has := func(fields ...string) func(string) bool {
		set := map[string]bool{}
		for _, f := range fields {
			set[f] = true
		}
		return func(f string) bool { return set[f] }
	}

	// A server NIC carries BOTH edges. Most-specific-first means the adapter
	// wins — a NIC nests under its card, not under the chassis.
	got, ok := cfg.CanonicalParentOf("NetworkInterface", has("networkAdapter", "server"))
	if !ok || got.Type != "NetworkAdapter" {
		t.Errorf("got %+v (%v), want NetworkAdapter to win over Server", got, ok)
	}
	// A LAG has no adapter, so the next declared edge answers.
	got, ok = cfg.CanonicalParentOf("NetworkInterface", has("server"))
	if !ok || got.Type != "Server" {
		t.Errorf("got %+v (%v), want Server", got, ok)
	}
	// A switch port skips two.
	got, ok = cfg.CanonicalParentOf("NetworkInterface", has("networkDevice"))
	if !ok || got.Type != "NetworkDevice" {
		t.Errorf("got %+v (%v), want NetworkDevice", got, ok)
	}
	// An orphan has no home, and saying so is different from picking one.
	if _, ok := cfg.CanonicalParentOf("NetworkInterface", has()); ok {
		t.Error("a node carrying none of the declared edges has no canonical parent")
	}
	if _, ok := cfg.CanonicalParentOf("Server", has("anything")); ok {
		t.Error("a type that declares no canonicalParent has none")
	}
}

func assertMembers(t *testing.T, cfg ViewConfig, typeName string, want ...string) {
	t.Helper()
	got := cfg.SubgraphOf(typeName)
	if len(got) != len(want) {
		t.Fatalf("%s members = %+v, want %v", typeName, got, want)
	}
	for i, w := range want {
		if got[i].Path != w {
			t.Errorf("%s member[%d] = %q, want %q", typeName, i, got[i].Path, w)
		}
	}
}

func anyContains(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// `types:` INHERITS along the schema's interfaces, and the type's own
// declaration wins field by field.
//
// The regression this guards is quiet and wide: `ConfigItem: fields: orbId:
// {label: Orb ID}` is declared ONCE and reaches all twenty types, because DGraph
// forbids an implementor from redeclaring an interface field — there is nowhere
// else that label could live. A plain map lookup instead of this walk turns every
// metadata box's "Orb ID" into "Orb Id" and drops EksaKubernetesCluster's column
// order, with nothing failing.
func TestTypeOf_InheritsAlongInterfacesAndTheTypeWins(t *testing.T) {
	cfg := ViewConfig{Types: map[string]TypeDecl{
		"ConfigItem": {Fields: map[string]FieldDecl{
			"orbId": {Label: "Orb ID"},
		}},
		"KubernetesCluster": {
			Order:   []string{"name", "provider"},
			Columns: []string{"nodes.count"},
			Fields: map[string]FieldDecl{
				"cni":      {Label: "CNI"},
				"provider": {Editable: boolPtr(false)},
			},
		},
		"EksaKubernetesCluster": {Fields: map[string]FieldDecl{
			// The one field this type overrides; everything else is inherited.
			"provider": {Label: "Provider (EKS-A)"},
		}},
	}}
	info := TypeInfo{Implements: []string{"ConfigItem", "KubernetesCluster"}}

	got := cfg.TypeOf(info, "EksaKubernetesCluster")

	if strings.Join(got.Order, ",") != "name,provider" {
		t.Errorf("order = %v, want the interface's — a concrete type declaring none inherits it", got.Order)
	}
	if strings.Join(got.Columns, ",") != "nodes.count" {
		t.Errorf("columns = %v, want the interface's", got.Columns)
	}
	if got.Fields["orbId"].Label != "Orb ID" {
		t.Error("a ConfigItem-level label must reach every implementation; " +
			"DGraph forbids redeclaring the field, so there is nowhere else it could be declared")
	}
	if got.Fields["cni"].Label != "CNI" {
		t.Error("an interface's field config must reach its implementations")
	}
	if got.Fields["provider"].Label != "Provider (EKS-A)" {
		t.Error("the TYPE's own declaration must win over the interface's")
	}
}

// Scalars and link rows are SUBTRACTIVE: everything renders unless something
// removes it. The SUBGRAPH is the opposite — listed, never derived.
//
// The regression this catches is the two crossing: a derived link row is
// navigation, and if it ever entered the subgraph, deleting a server would
// delete its rack. Inverting the scalar rule is the other plausible edit — the
// shipped file would need ~200 field names and every scalar added to the schema
// would silently never appear.
func TestResolveViews_ScalarsAndLinkRowsAreSubtractiveAndNeverSubgraph(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {Fields: []DerivedField{
			{Name: "hostname", Editable: true, Kind: "SCALAR"},
			// Nobody declares this anywhere. That is the point.
			{Name: "newlyAddedByTheSchema", Editable: true, Kind: "SCALAR"},
			{Name: "sku", Editable: true, Kind: "SCALAR"},
			// Undeclared SINGLE relationships: a link row each, never subgraph.
			{Name: "rack", Kind: "OBJECT", TypeName: "Rack"},
			{Name: "dataCenter", Kind: "OBJECT", TypeName: "DataCenter"},
			// Declared in the subgraph: a member, so not also a link row.
			{Name: "idracSettings", Kind: "OBJECT", TypeName: "IdracSettings"},
			// An undeclared LIST is neither: a list cannot be one row.
			{Name: "networkAdapters", Kind: "OBJECT", TypeName: "NetworkAdapter", IsList: true},
		}},
		"Rack":           {Fields: []DerivedField{{Name: "name", Editable: true, Kind: "SCALAR"}}},
		"DataCenter":     {Fields: []DerivedField{{Name: "name", Editable: true, Kind: "SCALAR"}}},
		"IdracSettings":  {Fields: []DerivedField{{Name: "sshEnabled", Editable: true, Kind: "SCALAR"}}},
		"NetworkAdapter": {Fields: []DerivedField{{Name: "model", Editable: true, Kind: "SCALAR"}}},
	}
	cfg := ViewConfig{Pages: map[string]PageDecl{"Server": {
		Summary:  SummaryDecl{IgnoreFields: []string{"sku", "dataCenter"}},
		Subgraph: []MemberDecl{{Path: "idracSettings"}},
	}}}
	views, err := ResolveViewsFromConfig(types, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := ViewSet(views).Of("Server")

	display := strings.Join(server.Display, ",")
	if !strings.Contains(display, "newlyAddedByTheSchema") {
		t.Errorf("display = %q — an undeclared scalar must appear on its own", display)
	}
	if strings.Contains(display, "sku") {
		t.Error("ignoreFields must remove a scalar")
	}

	var refs []string
	for _, r := range server.SummaryRefs {
		refs = append(refs, r.Field)
	}
	if strings.Join(refs, ",") != "rack" {
		t.Errorf("link rows = %v, want [rack] — derived for an undeclared single, "+
			"removed by ignoreFields, absent for a subgraph member and for a list", refs)
	}
	var members []string
	for _, m := range server.Subgraph {
		members = append(members, m.Field)
	}
	if strings.Join(members, ",") != "idracSettings" {
		t.Errorf("subgraph = %v, want [idracSettings] — a derived link row must NEVER enter the "+
			"subgraph, or deleting a server deletes its rack", members)
	}
}

// Promoting a type to a page is a ONE-LINE change: it gains a URL, a menu entry,
// and every row of it everywhere becomes a link.
//
// This is the designed extension point, so it is the thing most likely to be
// broken by a change elsewhere — and its failure is silent in the worst
// direction: the page exists and the rows still do not link, which reads as
// "pages don't work" rather than "one derivation was missed".
func TestPages_PromotingATypeGivesItAURLAMenuEntryAndLinks(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {Fields: []DerivedField{
			{Name: "hostname", Editable: true, Kind: "SCALAR"},
			{Name: "storageDevices", Kind: "OBJECT", TypeName: "StorageDevice", IsList: true},
		}},
		"StorageDevice": {Fields: []DerivedField{{Name: "wwn", Editable: true, Kind: "SCALAR"}}},
	}
	base := ViewConfig{Pages: map[string]PageDecl{
		"Server": {MenuWeight: 20, Subgraph: []MemberDecl{{Path: "storageDevices"}}},
	}}

	before := resolveOne(t, types, base, "Server")
	if before.Subgraph[0].Slug != "" {
		t.Fatal("precondition: StorageDevice has no page, so its tab carries no slug")
	}

	// The whole change.
	base.Pages["StorageDevice"] = PageDecl{MenuWeight: 90}

	after := resolveOne(t, types, base, "Server")
	if after.Subgraph[0].Slug != "storage-devices" {
		t.Errorf("the tab's rows must link once the type has a page; slug = %q", after.Subgraph[0].Slug)
	}
	sd := resolveOne(t, types, base, "StorageDevice")
	if sd.Slug != "storage-devices" {
		t.Errorf("the promoted type's own URL = %q, want storage-devices", sd.Slug)
	}
	if sd.MenuWeight != 90 {
		t.Errorf("menu weight = %d, want 90 — a page IS a menu entry", sd.MenuWeight)
	}
	// Nothing else moved: the Server page is unchanged apart from the link.
	if len(after.Subgraph) != len(before.Subgraph) || after.Subgraph[0].Field != before.Subgraph[0].Field {
		t.Error("promoting a type must change nothing about the pages that list it, except the link")
	}
}

func resolveOne(t *testing.T, types map[string]TypeInfo, cfg ViewConfig, typeName string) View {
	t.Helper()
	views, err := ResolveViewsFromConfig(types, nil, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Type == typeName {
			return v
		}
	}
	t.Fatalf("no view for %s", typeName)
	return View{}
}

func boolPtr(b bool) *bool { return &b }

// Reference columns are LISTED, never derived — the same rule the file states
// for every other relationship.
//
// The regression this guards is the one it was written for: deriving every
// single-cardinality edge put `Idrac Settings` and `Server Configuration
// Profile` on the servers list, which no hand-written page ever showed, and
// meant adding an edge to the schema silently widened every table of that type.
// "It is a property of the type, not the page" is TRUE and is why it lives in
// `types:` — it is not a reason to derive it.
func TestResolveViews_RefColumnsAreListedNotDerived(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {Fields: []DerivedField{
			{Name: "hostname", Editable: true, Kind: "SCALAR"},
			{Name: "dataCenter", Kind: "OBJECT", TypeName: "DataCenter"},
			{Name: "rack", Kind: "OBJECT", TypeName: "Rack"},
			// Single-cardinality, a ConfigItem, and NOT declared. Under the old
			// derivation every one of these became a column.
			{Name: "idracSettings", Kind: "OBJECT", TypeName: "IdracSettings"},
			{Name: "storageControllers", Kind: "OBJECT", TypeName: "StorageController", IsList: true},
		}},
		"DataCenter":        {Fields: []DerivedField{{Name: "name", Editable: true, Kind: "SCALAR"}}},
		"Rack":              {Fields: []DerivedField{{Name: "name", Editable: true, Kind: "SCALAR"}}},
		"IdracSettings":     {Fields: []DerivedField{{Name: "sshEnabled", Editable: true, Kind: "SCALAR"}}},
		"StorageController": {Fields: []DerivedField{{Name: "name", Editable: true, Kind: "SCALAR"}}},
	}
	cfg := ViewConfig{Types: map[string]TypeDecl{
		"Server": {RefColumns: []string{"dataCenter", "rack"}},
	}}
	validated, warn := cfg.Validate(types, "")
	if len(warn) != 0 {
		t.Fatalf("a clean declaration must not warn; got %v", warn)
	}
	got := resolveOne(t, types, validated, "Server")

	var fields []string
	for _, r := range got.RefColumns {
		fields = append(fields, r.Field)
	}
	if strings.Join(fields, ",") != "dataCenter,rack" {
		t.Errorf("refColumns = %v, want exactly what was declared, in declared order", fields)
	}
}

// An unsupportable refColumn is dropped and SAID, like every other declaration.
func TestValidateViews_RefColumnDeclarationsAreChecked(t *testing.T) {
	cfg := ViewConfig{Types: map[string]TypeDecl{"Server": {RefColumns: []string{
		"rack",               // fine
		"nope",               // not a field
		"hostname",           // a scalar: already a column
		"storageControllers", // a LIST cannot be one cell
		"auditTrail",         // points at a non-ConfigItem
	}}}}
	got, warn := cfg.Validate(validationFixture(), "")

	for _, want := range []string{"nope", "hostname", "storageControllers", "auditTrail"} {
		if !anyContains(warn, want) {
			t.Errorf("refColumn %s must be reported; got %v", want, warn)
		}
	}
	if strings.Join(got.Types["Server"].RefColumns, ",") != "rack" {
		t.Errorf("only the valid one survives; got %v", got.Types["Server"].RefColumns)
	}
}

// A default that cannot work is REFUSED and SAID, never silently ignored.
//
// The regression this guards is quiet: `default: "false "` with a trailing space
// is not `"false"`, so the Boolean select rendered with nothing chosen and the
// author saw an empty control with no explanation. Found by a user typing
// exactly that.
func TestValidateViews_DefaultsAreTrimmedAndTypeChecked(t *testing.T) {
	types := map[string]TypeInfo{"Thing": {Fields: []DerivedField{
		{Name: "enabled", Editable: true, Kind: "SCALAR", TypeName: "Boolean"},
		{Name: "uHeight", Editable: true, Kind: "SCALAR", TypeName: "Int"},
		{Name: "model", Editable: true, Kind: "SCALAR", TypeName: "String"},
		{Name: "owner", Kind: "OBJECT", TypeName: "Thing"},
	}}}
	cfg := ViewConfig{Types: map[string]TypeDecl{"Thing": {Fields: map[string]FieldDecl{
		"enabled": {CreateDefault: "false "},   // trailing space — must be TRIMMED, not refused
		"uHeight": {CreateDefault: "1u"},       // not an Int
		"model":   {CreateDefault: " R450 "},   // trimmed, kept
		"owner":   {CreateDefault: "x:y"},      // a relationship cannot be pre-filled
		"nope":    {CreateDefault: "anything"}, // not a field at all
	}}}}
	got, warn := cfg.Validate(types, "")
	fields := got.Types["Thing"].Fields

	if fields["enabled"].CreateDefault != "false" {
		t.Errorf(`enabled = %q, want "false" — whitespace is trimmed, not a refusal`, fields["enabled"].CreateDefault)
	}
	if fields["model"].CreateDefault != "R450" {
		t.Errorf(`model = %q, want "R450"`, fields["model"].CreateDefault)
	}
	for _, bad := range []string{"uHeight", "owner", "nope"} {
		if fields[bad].CreateDefault != "" {
			t.Errorf("%s kept an unusable default %q; it must drop to empty", bad, fields[bad].CreateDefault)
		}
		if !anyContains(warn, bad) {
			t.Errorf("%s must be REPORTED, not silently dropped; got %v", bad, warn)
		}
	}
	// A Boolean that is neither true nor false is refused with the two legal
	// values named — "it didn't work" is not actionable, "use true or false" is.
	cfg2 := ViewConfig{Types: map[string]TypeDecl{"Thing": {Fields: map[string]FieldDecl{
		"enabled": {CreateDefault: "maybe"},
	}}}}
	_, warn2 := cfg2.Validate(types, "")
	if !anyContains(warn2, "true") || !anyContains(warn2, "Boolean") {
		t.Errorf("a bad Boolean default must name the legal values; got %v", warn2)
	}
}

// A createDefault on a createHidden field is DEAD CONFIG, and said so.
//
// Nobody ever sees the prefill, so the two keys together express nothing — and
// silently ignoring one of a pair of keys an author deliberately wrote is how a
// config grows lines that look meaningful and are not.
func TestValidateViews_CreateDefaultOnHiddenFieldIsRefused(t *testing.T) {
	types := map[string]TypeInfo{"Thing": {Fields: []DerivedField{
		{Name: "reason", Editable: true, Kind: "SCALAR", TypeName: "String"},
	}}}
	cfg := ViewConfig{Types: map[string]TypeDecl{"Thing": {Fields: map[string]FieldDecl{
		"reason": {CreateHidden: true, CreateDefault: "scheduled"},
	}}}}
	got, warn := cfg.Validate(types, "")

	if !anyContains(warn, "dead config") {
		t.Errorf("the pair must be reported; got %v", warn)
	}
	if d := got.Types["Thing"].Fields["reason"].CreateDefault; d != "" {
		t.Errorf("the unusable default must drop to empty; got %q", d)
	}
	// ...and the hiding itself still stands — the refusal is about the default.
	if !got.Types["Thing"].Fields["reason"].CreateHidden {
		t.Error("createHidden must survive; only the default it shadowed is dropped")
	}
}
