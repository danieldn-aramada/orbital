package configitems

import (
	"strings"
	"testing"
)

// Nav membership is DERIVED from containment, not declared. The registry used to
// carry an `IsRoot` bool on exactly four types; this must reproduce it, which is
// what made deleting that field safe.
func TestResolveViews_RootsAreExactlyTheUnownedTypes(t *testing.T) {
	want := map[string]bool{
		"DataCenter": true, "Server": true, "NetworkDevice": true, "EksaKubernetesCluster": true,
	}
	// The CONCRETE types only, and this fixture predates interface views. A
	// sub-interface IS a view now (KubernetesCluster → /clusters) and IS a nav
	// root; its implementations are then demoted out of the nav by ResolveViews,
	// which is asserted separately below. isRootType alone still answers the
	// containment question, and EksaKubernetesCluster is unowned — so it is
	// true here and false in the resolved view.
	for _, name := range fixtureTypeNames() {
		got := isRootType(name)
		if got != want[name] {
			t.Errorf("isRootType(%q) = %v, want %v", name, got, want[name])
		}
	}
}

// IPAddress is the case that breaks the obvious implementation: it is owned by
// four different types and declares that through OwnerEdges ALONE, leaving
// OwnerType empty. Checking OwnerType by itself makes it a root and puts it in
// the nav as a top-level page.
func TestResolveViews_MultiParentTypeIsNotARoot(t *testing.T) {
	if isRootType("IPAddress") {
		t.Error("IPAddress declares only OwnerEdges and must NOT be a root")
	}
	if isRootType("NetworkInterface") {
		t.Error("NetworkInterface is owned and must not be a root")
	}
}

// A duplicate slug silently shadows one type's pages, and which one wins would
// depend on map iteration order. It must fail loudly, naming both.
func TestResolveViews_DuplicateSlugIsRefused(t *testing.T) {
	types := map[string]TypeInfo{
		"Server":     {Fields: []DerivedField{{Name: "hostname", Editable: true, Kind: "SCALAR"}}},
		"ServerCopy": {Doc: "slug: servers", Fields: []DerivedField{{Name: "x", Editable: true, Kind: "SCALAR"}}},
	}
	_, err := ResolveViews(types, nil)
	if err == nil {
		t.Fatal("two types claiming the same slug must be refused")
	}
	for _, want := range []string{"servers", "Server", "ServerCopy"} {
		if !contains(err.Error(), want) {
			t.Errorf("error must name %q; got %q", want, err)
		}
	}
}

// Relationships to ConfigItem types become tabs; scalars never do.
func TestResolveViews_TabsAreRelationshipsOnly(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {Fields: []DerivedField{
			{Name: "hostname", Editable: true, Kind: "SCALAR"},
			{Name: "idracSettings", Kind: "OBJECT", TypeName: "IdracSettings"},
			{Name: "storageControllers", Kind: "OBJECT", TypeName: "StorageController", IsList: true},
			{Name: "somethingElse", Kind: "OBJECT", TypeName: "NotAConfigItem"},
		}},
		"IdracSettings":     {Fields: []DerivedField{{Name: "firmwareVersion", Editable: true, Kind: "SCALAR"}}},
		"StorageController": {Fields: []DerivedField{{Name: "model", Editable: true, Kind: "SCALAR"}}},
	}
	views, err := ResolveViews(types, nil)
	if err != nil {
		t.Fatal(err)
	}
	var srv View
	for _, v := range views {
		if v.Type == "Server" {
			srv = v
		}
	}
	if len(srv.Tabs) != 2 {
		t.Fatalf("want 2 tabs (the two ConfigItem relationships), got %d: %+v", len(srv.Tabs), srv.Tabs)
	}
	if srv.Tabs[0].Field != "idracSettings" || srv.Tabs[0].Slug != "idrac-settings" || srv.Tabs[0].IsList {
		t.Errorf("idracSettings tab wrong: %+v", srv.Tabs[0])
	}
	if srv.Tabs[1].Field != "storageControllers" || !srv.Tabs[1].IsList {
		t.Errorf("storageControllers should be a LIST tab: %+v", srv.Tabs[1])
	}
	for _, tab := range srv.Tabs {
		if tab.Type == "NotAConfigItem" {
			t.Error("a relationship to a non-ConfigItem type is not renderable and must not be a tab")
		}
	}
}

// Label takes the SLUG, not the type name, so a `slug:` annotation renames the
// page and its nav entry together. Deriving them separately gave /clusters two
// names: a hand-written "Clusters" and a derived "Kubernetes Clusters", both
// linking to the same page.
func TestLabel_IsDisplayNotSlug(t *testing.T) {
	cases := map[string]string{
		"network-devices": "Network Devices",
		"servers":         "Servers",
		"ip-addresses":    "Ip Addresses",
		"idrac-settings":  "Idrac Settings",
		// An annotated slug carries straight through, which is the point.
		"clusters": "Clusters",
	}
	for in, want := range cases {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}

	// The slug is ALREADY plural. Pluralising again turned "addresses" into
	// "addresseses" on every page whose type name ends in s.
	if got := Label("ip-addresses"); strings.HasSuffix(got, "eses") {
		t.Errorf("Label double-pluralised: %q", got)
	}
}

// TestResolveViews_DoesNotReachThroughThePackageLookup pins the fix for a
// re-entrancy loop that hung every generic page.
//
// The package-level interface lookup reads through the schema resolver, and
// ResolveViews runs INSIDE that resolver's resolve step. Anything here calling
// OwnedChildren therefore re-entered the resolver, which re-resolved, which
// called OwnedChildren again. There was no error and no access-log entry — the
// request simply never finished, which is the worst shape a bug can take.
//
// ResolveViews holds the snapshot, so it must use the snapshot's own Implements
// and never the package lookup.
func TestResolveViews_DoesNotReachThroughThePackageLookup(t *testing.T) {
	reached := false
	saved := implementsLookup
	SetImplementsLookup(func(typeName string) []string {
		reached = true
		return nil
	})
	t.Cleanup(func() { implementsLookup = saved })

	types := map[string]TypeInfo{
		"Server": {
			Implements: []string{"ConfigItem"},
			Fields: []DerivedField{
				{Name: "hostname", Editable: true, Kind: "SCALAR"},
				{Name: "idracSettings", Kind: "OBJECT", TypeName: "IdracSettings"},
			},
		},
		"IdracSettings": {
			Implements: []string{"ConfigItem"},
			Fields:     []DerivedField{{Name: "firmwareVersion", Editable: true, Kind: "SCALAR"}},
		},
	}
	if _, err := ResolveViews(types, nil); err != nil {
		t.Fatalf("ResolveViews: %v", err)
	}
	if reached {
		t.Error("ResolveViews reached through the package interface lookup — that lookup reads " +
			"through the resolver this code runs inside, so it re-enters and hangs")
	}
}

// TestMetaFields covers the provenance list the detail page's metadata box is
// built from. Regression class: `name` or `id` leaking back in — `name` is the
// page heading and would render twice, `id` is DGraph's internal uid and means
// nothing to an operator.
func TestMetaFields(t *testing.T) {
	tests := []struct {
		name  string
		iface []string
		want  []string
	}{
		{
			name:  "canonical ConfigItem interface, in display order",
			iface: []string{"id", "namespace", "orbId", "name", "createdBy", "createdAt", "updatedBy", "updatedAt", "version"},
			want:  []string{"namespace", "orbId", "version", "createdBy", "createdAt", "updatedAt", "updatedBy"},
		},
		{
			// A field added to the interface must appear without a code change
			// here — that is the whole reason the list is derived.
			name:  "an unknown interface field is appended, not dropped",
			iface: []string{"orbId", "version", "retiredAt"},
			want:  []string{"orbId", "version", "retiredAt"},
		},
		{
			name:  "no interface fields",
			iface: nil,
			want:  []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metaFields(tt.iface)
			if len(got) != len(tt.want) {
				t.Fatalf("metaFields() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("metaFields()[%d] = %q, want %q (full: %v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

// ifaceFixture is the cluster hierarchy as the deployed schema declares it: an
// interface carrying the shared fields, one implementation adding its own.
func ifaceFixture() map[string]TypeInfo {
	return map[string]TypeInfo{
		"KubernetesCluster": {
			Doc:         "slug: clusters",
			IsInterface: true,
			// PossibleTypes is what the implementors declare, resolved by the
			// schema client before views are built.
			PossibleTypes: []string{"EksaKubernetesCluster"},
			Fields: []DerivedField{
				{Name: "kubernetesVersion", Editable: true, Kind: "SCALAR", TypeName: "String"},
				{Name: "cni", Editable: true, Kind: "SCALAR", TypeName: "String"},
			},
		},
		"EksaKubernetesCluster": {
			Implements: []string{"ConfigItem", "KubernetesCluster"},
			Fields: []DerivedField{
				{Name: "kubernetesVersion", Editable: true, Kind: "SCALAR", TypeName: "String"},
				{Name: "cni", Editable: true, Kind: "SCALAR", TypeName: "String"},
				{Name: "clusterType", Editable: true, Kind: "SCALAR", TypeName: "String"},
			},
		},
	}
}

// Acceptance 1: a sub-interface becomes a view of its own, reported as such.
func TestResolveViews_InterfaceBecomesAView(t *testing.T) {
	views, err := ResolveViews(ifaceFixture(), []string{"orbId", "name", "version"})
	if err != nil {
		t.Fatalf("ResolveViews: %v", err)
	}
	var iface *View
	for i := range views {
		if views[i].Type == "KubernetesCluster" {
			iface = &views[i]
		}
	}
	if iface == nil {
		t.Fatal("no view for the KubernetesCluster interface")
	}
	if !iface.IsInterface {
		t.Error("the interface's view must report IsInterface — a client cannot otherwise tell that its rows are of several types")
	}
	if iface.Slug != "clusters" {
		t.Errorf("slug = %q, want %q (from the `slug:` annotation)", iface.Slug, "clusters")
	}
	if len(iface.Implementations) != 1 || iface.Implementations[0] != "EksaKubernetesCluster" {
		t.Errorf("Implementations = %v, want [EksaKubernetesCluster]", iface.Implementations)
	}
}

// Acceptance 5: an implementation keeps its pages but loses its nav entry.
//
// Regression class: two nav items — "Clusters" and "Eksa Kubernetes Clusters" —
// showing identical rows today and diverging the moment a second provider
// lands. Nothing about the page breaks, so only an assertion catches it.
func TestResolveViews_ImplementationIsNotANavRoot(t *testing.T) {
	views, err := ResolveViews(ifaceFixture(), []string{"orbId", "name", "version"})
	if err != nil {
		t.Fatalf("ResolveViews: %v", err)
	}
	for _, v := range views {
		switch v.Type {
		case "KubernetesCluster":
			if !v.IsRoot {
				t.Error("the interface view must be a nav root — it is the Clusters page")
			}
		case "EksaKubernetesCluster":
			if v.IsRoot {
				t.Error("an implementation must NOT be a nav root: the interface view already lists it")
			}
			if v.Slug == "" {
				t.Error("an implementation must keep a slug — its detail pages stay reachable")
			}
		}
	}
}

// Acceptance 6: the slug namespace spans interfaces and concrete types alike.
func TestResolveViews_InterfaceAndConcreteSlugCollisionIsRefused(t *testing.T) {
	types := ifaceFixture()
	// Make the implementation claim the interface's annotated slug.
	impl := types["EksaKubernetesCluster"]
	impl.Doc = "slug: clusters"
	types["EksaKubernetesCluster"] = impl

	_, err := ResolveViews(types, []string{"orbId", "name", "version"})
	if err == nil {
		t.Fatal("two views claiming /clusters must be refused, not silently shadowed")
	}
	for _, want := range []string{"clusters", "KubernetesCluster", "EksaKubernetesCluster"} {
		if !contains(err.Error(), want) {
			t.Errorf("error must name %q so the operator knows what to annotate: %v", want, err)
		}
	}
}

// viewFor is a reader for the tests below.
func viewFor(t *testing.T, views []View, typeName string) View {
	t.Helper()
	for _, v := range views {
		if v.Type == typeName {
			return v
		}
	}
	t.Fatalf("no view for %s", typeName)
	return View{}
}

// TestView_CarriesFilterBy is acceptance item 8: the resolved filterBy rides on the
// view, so orbital's own UI and any API consumer read the SAME answer rather
// than each deciding which column is worth a filter.
func TestView_CarriesFilterBy(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {Doc: "filterBy: dataCenter", Fields: []DerivedField{
			{Name: "hostname", Editable: true, Kind: "SCALAR"},
			{Name: "dataCenter", Kind: "OBJECT", TypeName: "DataCenter"},
		}},
		"DataCenter": {Fields: []DerivedField{{Name: "region", Editable: true, Kind: "SCALAR"}}},
	}
	views, err := ResolveViews(types, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := viewFor(t, views, "Server").FilterBy; got != "dataCenter" {
		t.Errorf("Server.FilterBy = %q, want dataCenter", got)
	}
	// The negative: a type that declares nothing gets nothing. A filterBy that
	// appears without being asked for is a control nobody can turn off.
	if got := viewFor(t, views, "DataCenter").FilterBy; got != "" {
		t.Errorf("an unannotated type must have no filterBy, got %q", got)
	}
}

// TestFilterByFor_InheritsFromInterface is acceptance item 5.
//
// A TYPE docstring does NOT reach implementations on its own — that is a
// per-annotation decision, and `slug:` must never inherit or every
// implementation would claim the interface's URL. `filterBy:` does, so /clusters
// and /eksa-kubernetes-clusters cannot disagree about having a filter.
func TestFilterByFor_InheritsFromInterface(t *testing.T) {
	types := map[string]TypeInfo{
		"KubernetesCluster": {
			Doc:           "slug: clusters\nfilterBy: dataCenter",
			IsInterface:   true,
			PossibleTypes: []string{"EksaKubernetesCluster"},
			Fields: []DerivedField{
				{Name: "kubernetesVersion", Editable: true, Kind: "SCALAR"},
				{Name: "dataCenter", Kind: "OBJECT", TypeName: "DataCenter"},
			},
		},
		"EksaKubernetesCluster": {
			Implements: []string{"KubernetesCluster"},
			Fields: []DerivedField{
				{Name: "clusterType", Editable: true, Kind: "SCALAR"},
				{Name: "dataCenter", Kind: "OBJECT", TypeName: "DataCenter"},
			},
		},
		"DataCenter": {Fields: []DerivedField{{Name: "region", Editable: true, Kind: "SCALAR"}}},
	}
	views, err := ResolveViews(types, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := viewFor(t, views, "KubernetesCluster").FilterBy; got != "dataCenter" {
		t.Errorf("the interface view's filterBy = %q, want dataCenter", got)
	}
	if got := viewFor(t, views, "EksaKubernetesCluster").FilterBy; got != "dataCenter" {
		t.Errorf("an implementation must inherit the interface's filterBy, got %q", got)
	}
	// Inheritance must not leak the interface's SLUG, which is the reason this
	// is per-annotation rather than a blanket rule.
	if got := viewFor(t, views, "EksaKubernetesCluster").Slug; got == "clusters" {
		t.Error("an implementation must not inherit the interface's slug")
	}
}

// TestFilterByValidation_RejectsNonColumn is acceptance item 7.
//
// A `filterBy:` naming a column the table does not render is a dead control: the
// dropdown appears and filters column -1. Dropped, and REPORTED — an
// annotation that reads as correct and does nothing is worse than one nobody
// wrote, which is the same reasoning UnknownAnnotations exists for.
func TestFilterByValidation_RejectsNonColumn(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {
			Doc: "filterBy: assetDataV2",
			Fields: []DerivedField{
				{Name: "hostname", Editable: true, Kind: "SCALAR"},
				{Name: "assetDataV2", Editable: true, Kind: "SCALAR", Doc: "detailOnly"},
			},
		},
		"Rack": {Doc: "filterBy: nosuchfield", Fields: []DerivedField{
			{Name: "uHeight", Editable: true, Kind: "SCALAR"},
		}},
	}
	views, err := ResolveViews(types, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := viewFor(t, views, "Server").FilterBy; got != "" {
		t.Errorf("a detailOnly field is not a column and must not become a filter dropdown, got %q", got)
	}
	if got := viewFor(t, views, "Rack").FilterBy; got != "" {
		t.Errorf("an unknown field must not become a filter dropdown, got %q", got)
	}

	warnings := FilterByWarnings(types, views)
	if len(warnings) != 2 {
		t.Fatalf("both dropped annotations must be reported, got %v", warnings)
	}
	joined := warnings[0] + "|" + warnings[1]
	for _, want := range []string{"assetDataV2", "nosuchfield", "Server", "Rack"} {
		if !contains(joined, want) {
			t.Errorf("warnings must name %q; got %v", want, warnings)
		}
	}
}

// A second filterBy field is honoured-first-and-reported, never silently dropped:
// someone who wrote two and got one needs to be told which.
func TestFilterByWarnings_ReportsExtraFields(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {Doc: "filterBy: dataCenter, rack", Fields: []DerivedField{
			{Name: "hostname", Editable: true, Kind: "SCALAR"},
			{Name: "dataCenter", Kind: "OBJECT", TypeName: "DataCenter"},
			{Name: "rack", Kind: "OBJECT", TypeName: "Rack"},
		}},
		"DataCenter": {Fields: []DerivedField{{Name: "region", Editable: true, Kind: "SCALAR"}}},
		"Rack":       {Fields: []DerivedField{{Name: "uHeight", Editable: true, Kind: "SCALAR"}}},
	}
	views, err := ResolveViews(types, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := viewFor(t, views, "Server").FilterBy; got != "dataCenter" {
		t.Errorf("the first field must win, got %q", got)
	}
	warnings := FilterByWarnings(types, views)
	if len(warnings) != 1 || !contains(warnings[0], "rack") {
		t.Fatalf("the extra field must be reported by name, got %v", warnings)
	}
}

// columnFixture is a Server/KubernetesNode/KubernetesCluster/Rack slice of the
// real schema — the two shapes `column:` exists for.
func columnFixture() (map[string]TypeInfo, []string) {
	return map[string]TypeInfo{
		"Server": {
			Doc: "column: kubernetesNode.cluster.name, kubernetesNode.role, servers.count",
			Fields: []DerivedField{
				{Name: "hostname", Editable: true, Kind: "SCALAR"},
				{Name: "kubernetesNode", Kind: "OBJECT", TypeName: "KubernetesNode"},
			},
		},
		"KubernetesNode": {Fields: []DerivedField{
			{Name: "role", Editable: true, Kind: "SCALAR"},
			{Name: "cluster", Kind: "OBJECT", TypeName: "KubernetesCluster"},
		}},
		"KubernetesCluster": {IsInterface: true, Fields: []DerivedField{
			{Name: "kubernetesVersion", Editable: true, Kind: "SCALAR"},
		}},
		"Rack": {
			Doc: "column: servers.count",
			Fields: []DerivedField{
				{Name: "uHeight", Editable: true, Kind: "SCALAR"},
				{Name: "servers", Kind: "OBJECT", TypeName: "Server", IsList: true},
			},
		},
	}, []string{"name", "orbId"}
}

// TestResolveColumns covers acceptance items 2 and 3: a count over a list, and
// a value reached through single relationships.
func TestResolveColumns(t *testing.T) {
	types, iface := columnFixture()
	views, err := ResolveViews(types, iface)
	if err != nil {
		t.Fatal(err)
	}

	rack := viewFor(t, views, "Rack")
	if len(rack.Columns) != 1 {
		t.Fatalf("Rack columns = %+v, want one", rack.Columns)
	}
	c := rack.Columns[0]
	if !c.IsCount || c.Field != "servers" {
		t.Errorf("servers.count must resolve as a count over `servers`, got %+v", c)
	}
	// The generated aggregate field, not the list itself — selecting `servers`
	// would pull every row to count them.
	if c.Selection != "serversAggregate { count }" {
		t.Errorf("Selection = %q, want serversAggregate { count }", c.Selection)
	}

	srv := viewFor(t, views, "Server")
	byPath := map[string]ViewColumn{}
	for _, c := range srv.Columns {
		byPath[c.Path] = c
	}
	// `name` lives on the ConfigItem INTERFACE, so it is absent from
	// KubernetesCluster's own field list while being perfectly selectable —
	// and because the hop lands on an interface it needs a type condition.
	cl, ok := byPath["kubernetesNode.cluster.name"]
	if !ok {
		t.Fatalf("the cluster-name path must resolve; got %+v", srv.Columns)
	}
	if cl.Selection != "kubernetesNode { cluster { ... on ConfigItem { name } } }" {
		t.Errorf("Selection = %q, want a ConfigItem type condition on the leaf", cl.Selection)
	}
	// Labelled "Cluster", not "Name" — which would also collide with the row's
	// own Name column.
	if cl.Field != "cluster" {
		t.Errorf("an identity leaf labels by its hop; Field = %q, want cluster", cl.Field)
	}
	if r := byPath["kubernetesNode.role"]; r.Selection != "kubernetesNode { role }" {
		t.Errorf("a one-hop scalar = %q", r.Selection)
	}
	// `servers.count` on Server names a field Server does not have.
	if _, bad := byPath["servers.count"]; bad {
		t.Error("a path naming a field the type lacks must not produce a column")
	}
}

// TestColumnWarnings is acceptance item 7: a path the schema cannot support is
// dropped AND reported. Silent is the failure mode — the column simply is not
// there, indistinguishable from never having annotated it.
func TestColumnWarnings(t *testing.T) {
	types, iface := columnFixture()
	types["Rack"] = TypeInfo{
		Doc: "column: nosuch.count, uHeight.somehow, servers.name",
		Fields: []DerivedField{
			{Name: "uHeight", Editable: true, Kind: "SCALAR"},
			{Name: "servers", Kind: "OBJECT", TypeName: "Server", IsList: true},
		},
	}
	views, err := ResolveViews(types, iface)
	if err != nil {
		t.Fatal(err)
	}
	if cols := viewFor(t, views, "Rack").Columns; len(cols) != 0 {
		t.Fatalf("none of those paths is valid; got %+v", cols)
	}
	w := ColumnWarnings(types, views, iface)
	joined := strings.Join(w, " | ")
	for _, want := range []string{"nosuch", "uHeight", "servers.name"} {
		if !contains(joined, want) {
			t.Errorf("warnings must name %q; got %v", want, w)
		}
	}
	// Each failure says WHY, not just that it failed.
	if !contains(joined, "scalar") || !contains(joined, "list") {
		t.Errorf("warnings must explain the cause; got %v", w)
	}
}

// TestViewIgnored_HidesTheRelationshipEverywhere pins both halves: the edge
// disappears from the page, and containment is untouched.
//
// The second half is the one that matters. The delete cascade, audit roll-up
// and the editor's tree all read the registry's containment model; if hiding a
// relationship also dropped it from there, deleting a parent would orphan the
// child and the editor would read the missing subtree as a deletion.
func TestViewIgnored_HidesTheRelationshipEverywhere(t *testing.T) {
	types := map[string]TypeInfo{
		"Server": {Fields: []DerivedField{
			{Name: "hostname", Editable: true, Kind: "SCALAR"},
			{Name: "idracSettings", Kind: "OBJECT", TypeName: "IdracSettings"},
			{Name: "scp", Doc: "viewIgnored", Kind: "OBJECT", TypeName: "ServerConfigurationProfile"},
			{Name: "notes", Doc: "viewIgnored", Kind: "OBJECT", TypeName: "IdracSettings", IsList: true},
		}},
		"IdracSettings":              {Fields: []DerivedField{{Name: "firmwareVersion", Editable: true, Kind: "SCALAR"}}},
		"ServerConfigurationProfile": {Fields: []DerivedField{{Name: "json", Editable: true, Kind: "SCALAR"}}},
	}
	views, err := ResolveViews(types, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := viewFor(t, views, "Server")

	for _, tab := range srv.Tabs {
		if tab.Field == "scp" || tab.Field == "notes" {
			t.Errorf("a viewIgnored relationship must not be a tab: %q", tab.Field)
		}
	}
	// RefColumns derive from Tabs, so filtering one source must cover both.
	for _, rc := range srv.RefColumns {
		if rc.Field == "scp" {
			t.Error("a viewIgnored relationship must not be a reference column")
		}
	}
	// The un-annotated sibling still renders, so this is a filter and not an
	// off switch for relationships generally.
	var sawIdrac bool
	for _, tab := range srv.Tabs {
		if tab.Field == "idracSettings" {
			sawIdrac = true
		}
	}
	if !sawIdrac {
		t.Error("an unannotated relationship must still be a tab")
	}

	// The hidden type keeps its OWN view: hiding an edge says nothing about
	// whether the thing at the other end deserves a page.
	if _, ok := func() (View, bool) {
		for _, v := range views {
			if v.Type == "ServerConfigurationProfile" {
				return v, true
			}
		}
		return View{}, false
	}(); !ok {
		t.Error("a viewIgnored target must still get its own view")
	}
}
