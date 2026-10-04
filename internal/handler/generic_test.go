package handler

import (
	"strings"
	"testing"

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/web/data/page"
	"os"
	"regexp"
	"strconv"
)

// TestGenericDetailQuerySelectsMetaFields guards the metadata box's data.
//
// Regression class: the selection is built from View.Meta, and Meta is NOT in
// Display — displayScalars strips the interface out. If a refactor rebuilds the
// selection from Display alone the box renders every row as "—", which looks
// like an item with no provenance rather than like a broken query.
func TestGenericDetailQuerySelectsMetaFields(t *testing.T) {
	v := configitems.View{
		Type:    "Rack",
		Display: []string{"name", "uHeight"},
		Meta:    []string{"namespace", "orbId", "version", "createdAt"},
	}
	q := genericDetailQuery(v, func(string) configitems.View { return configitems.View{} }, func(string) []string { return nil }, func(string) []configitems.ViewRefColumn { return nil }, "colo:rack-1")

	for _, f := range []string{"namespace", "orbId", "version", "createdAt", "uHeight"} {
		if !strings.Contains(q, f) {
			t.Errorf("query does not select %q: %s", f, q)
		}
	}
	// Each field exactly once — orbId and version are selected up front AND are
	// interface fields, so a naive append emits them twice and DGraph refuses
	// the whole query.
	for _, f := range []string{"orbId", "version", "name"} {
		if n := strings.Count(q, " "+f+" "); n > 1 {
			t.Errorf("field %q selected %d times, want 1: %s", f, n, q)
		}
	}
}

// TestOwnedSubtreeOrbIDs covers the CSV the audit panel queries with.
//
// Regression class: an owned child missing from the list means a change to it
// records the CHILD's orbId, the panel asks only about the parent, and the page
// reports "no changes" while something is in flight. Silent, and exactly the
// question the panel exists to answer.
func TestOwnedSubtreeOrbIDs(t *testing.T) {
	// Server → IdracSettings is the real owned-child edge in the deployed
	// schema, so this exercises the actual containment table rather than a
	// fixture that could agree with a broken one.
	entity := map[string]any{
		"orbId":         "colo:server-ABC",
		"idracSettings": map[string]any{"orbId": "colo:idrac-settings-ABC"},
	}
	got := ownedSubtreeOrbIDs("colo:server-ABC", "Server", entity)
	if len(got) == 0 || got[0] != "colo:server-ABC" {
		t.Fatalf("root orbId must come first, got %v", got)
	}
	if len(got) != 2 || got[1] != "colo:idrac-settings-ABC" {
		t.Fatalf("owned child missing from the subtree: got %v", got)
	}

	// A child the query did not return must not be claimed.
	bare := ownedSubtreeOrbIDs("colo:server-ABC", "Server", map[string]any{"orbId": "colo:server-ABC"})
	if len(bare) != 1 {
		t.Errorf("with no children fetched, want just the root, got %v", bare)
	}

	// An unknown type is not an error — it owns nothing.
	unknown := ownedSubtreeOrbIDs("x:1", "NoSuchType", map[string]any{"orbId": "x:1"})
	if len(unknown) != 1 || unknown[0] != "x:1" {
		t.Errorf("unknown type: want [x:1], got %v", unknown)
	}
}

// TestHumanFieldLabel covers the camelCase → heading conversion.
//
// Regression class: acronyms. The naive "space before every capital" rule
// rendered `tinkerbellIP` as "Tinkerbell I P", which reads as a typo on a page
// an operator is meant to trust. Pure function, and the acronym cases ARE the
// regression class.
func TestHumanFieldLabel(t *testing.T) {
	tests := []struct{ field, want string }{
		{"name", "Name"},
		{"uHeight", "U Height"},
		{"kubernetesVersion", "Kubernetes Version"},
		{"tinkerbellIP", "Tinkerbell IP"},
		{"controlPlaneEndpoint", "Control Plane Endpoint"},
		{"oobIP", "Oob IP"},
		{"ipv4", "Ipv4"},
		{"s3Sync", "S3 Sync"},
		{"IPAddress", "IP Address"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			if got := humanFieldLabel(tt.field); got != tt.want {
				t.Errorf("humanFieldLabel(%q) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

// Acceptance 2: the list query fans out over every implementation.
//
// Regression class: a second provider type ships and its rows are missing, or
// its columns render blank, because the query asked only for what the interface
// declares. Both are silent — the page still renders.
func TestGenericListQuery_InterfaceFansOutOverImplementations(t *testing.T) {
	v := configitems.View{
		Type:            "KubernetesCluster",
		IsInterface:     true,
		Implementations: []string{"EksaKubernetesCluster", "MaasKubernetesCluster"},
		Display:         []string{"cni", "kubernetesVersion"},
	}
	display := map[string][]string{
		"EksaKubernetesCluster": {"cni", "kubernetesVersion", "clusterType"},
		"MaasKubernetesCluster": {"cni", "kubernetesVersion", "maasEndpoint"},
	}
	q := genericListQuery(v, func(string) configitems.View { return configitems.View{} },
		func(t string) []string { return display[t] }, 100)

	if !strings.Contains(q, "queryKubernetesCluster") {
		t.Errorf("must query the INTERFACE, not an implementation: %s", q)
	}
	// orbId and name live on ConfigItem, not on a sub-interface — selecting
	// them bare is rejected at validation, so the page would not render at all.
	if !strings.Contains(q, "... on ConfigItem { orbId name }") {
		t.Errorf("identity must be selected through ConfigItem: %s", q)
	}
	for _, want := range []string{"... on EksaKubernetesCluster { clusterType }", "... on MaasKubernetesCluster { maasEndpoint }"} {
		if !strings.Contains(q, want) {
			t.Errorf("missing fragment %q: %s", want, q)
		}
	}
	// A field the interface already declares must not be re-selected inside a
	// fragment — DGraph accepts it, but it says the builder does not know what
	// it already asked for.
	if strings.Contains(q, "{ cni") {
		t.Errorf("implementation fragment re-selects an interface field: %s", q)
	}
}

// Acceptance 3: columns are the union, so a provider-specific column survives.
func TestListColumns_UnionsImplementationColumns(t *testing.T) {
	v := configitems.View{
		Type:            "KubernetesCluster",
		IsInterface:     true,
		Implementations: []string{"EksaKubernetesCluster", "MaasKubernetesCluster"},
		Display:         []string{"cni", "kubernetesVersion"},
	}
	display := map[string][]string{
		"EksaKubernetesCluster": {"cni", "clusterType"},
		"MaasKubernetesCluster": {"cni", "maasEndpoint"},
	}
	got := listColumns(v, func(t string) []string { return display[t] })
	want := []string{"cni", "kubernetesVersion", "clusterType", "maasEndpoint"}
	if len(got) != len(want) {
		t.Fatalf("listColumns() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("listColumns()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// Acceptance 7: a relationship to an INTERFACE is selected in a form DGraph
// accepts, so the link is rendered instead of silently dropped.
func TestGenericDetailQuery_InterfaceTypedRelationship(t *testing.T) {
	node := configitems.View{
		Type: "KubernetesNode",
		Tabs: []configitems.ViewTab{{Field: "cluster", Type: "KubernetesCluster", Slug: "clusters"}},
	}
	viewOf := func(name string) configitems.View {
		if name == "KubernetesCluster" {
			return configitems.View{Type: name, IsInterface: true}
		}
		return configitems.View{Type: name}
	}
	q := genericDetailQuery(node, viewOf, func(string) []string { return nil },
		func(string) []configitems.ViewRefColumn { return nil }, "ns:node-1")

	if !strings.Contains(q, "cluster { __typename ... on ConfigItem { orbId name }") {
		t.Errorf("an interface-typed relationship must select identity through ConfigItem: %s", q)
	}
}

// TestWithoutBackReferences covers dropping a relationship table's column that
// points back at the page you are on.
//
// Regression class: dropping a column that carries real information. The type
// match alone is not enough — a type can hold two references to the same kind,
// and only one is the edge you traversed. Both halves are asserted.
func TestWithoutBackReferences(t *testing.T) {
	configitems.SetImplementsLookup(func(typeName string) []string {
		if typeName == "EksaKubernetesCluster" {
			return []string{"ConfigItem", "KubernetesCluster"}
		}
		return []string{"ConfigItem"}
	})
	t.Cleanup(func() { configitems.SetImplementsLookup(nil) })

	const parent = "colo:dev-main"
	refs := []configitems.ViewRefColumn{
		{Field: "cluster", Type: "KubernetesCluster", Slug: "clusters"},
		{Field: "server", Type: "Server", Slug: "servers"},
	}
	ref := func(id string) map[string]any { return map[string]any{"orbId": id, "name": id} }

	t.Run("a column naming the parent on every row is dropped", func(t *testing.T) {
		// `cluster` is typed as the INTERFACE while the page is a concrete
		// EksaKubernetesCluster, so the match runs through Implements.
		rows := []map[string]any{
			{"cluster": ref(parent), "server": ref("colo:server-1")},
			{"cluster": ref(parent), "server": ref("colo:server-2")},
		}
		got := withoutBackReferences(refs, rows, "EksaKubernetesCluster", parent)
		if len(got) != 1 || got[0].Field != "server" {
			t.Fatalf("want only the server column, got %+v", got)
		}
	})

	t.Run("a column that does NOT always name the parent is kept", func(t *testing.T) {
		rows := []map[string]any{
			{"cluster": ref(parent), "server": ref("colo:server-1")},
			{"cluster": ref("colo:other"), "server": ref("colo:server-2")},
		}
		if got := withoutBackReferences(refs, rows, "EksaKubernetesCluster", parent); len(got) != 2 {
			t.Fatalf("a column carrying a differing value must be kept, got %+v", got)
		}
	})

	t.Run("a row MISSING the reference keeps the column", func(t *testing.T) {
		// An absent link is information. Hiding the column would hide it.
		rows := []map[string]any{
			{"cluster": ref(parent), "server": ref("colo:server-1")},
			{"server": ref("colo:server-2")},
		}
		if got := withoutBackReferences(refs, rows, "EksaKubernetesCluster", parent); len(got) != 2 {
			t.Fatalf("a row with no value must keep the column, got %+v", got)
		}
	})

	t.Run("no rows leaves the columns alone", func(t *testing.T) {
		if got := withoutBackReferences(refs, nil, "EksaKubernetesCluster", parent); len(got) != 2 {
			t.Fatalf("want both columns, got %+v", got)
		}
	})
}

// TestTableColumns_ExcludesDetailOnly covers keeping declared fields out of
// tables.
//
// Regression class: a `detailOnly` field back in a list column. It is silent —
// the page renders, nothing errors — and it destroys the table: one data
// centre's assetDataV2 is ~600 characters of JSON in a single cell, which
// shoves every other column off the screen. Exactly what /data-centers did.
//
// Keyed on DetailOnly, never on JSONString: placement is declared, not inferred
// from what a field holds. Conflating them made "show this JSON as a column"
// unexpressible and forced a Go change to hide it.
func TestTableColumns_ExcludesDetailOnly(t *testing.T) {
	v := configitems.View{
		Display:    []string{"assetDataV2", "model", "name"},
		DetailOnly: []string{"assetDataV2"},
	}
	got := v.ColumnFields()
	for _, f := range got {
		if f == "assetDataV2" {
			t.Fatalf("a detailOnly field must not be a table column: %v", got)
		}
	}
	if len(got) != 2 || got[0] != "model" || got[1] != "name" {
		t.Fatalf("ColumnFields() = %v, want [model name]", got)
	}

	// A view with nothing detail-only is untouched — and returns the SAME
	// slice, so this cannot quietly reorder or copy every other view's columns.
	plain := configitems.View{Display: []string{"a", "b"}}
	if out := plain.ColumnFields(); len(out) != 2 || out[0] != "a" {
		t.Fatalf("a view with no blobs must pass through: %v", out)
	}
}

// TestPrettyJSON covers the detail-page rendering of a blob.
//
// Regression class: swallowing a value that does not parse. The field is
// ANNOTATED as holding JSON; if it does not, showing the raw string is how
// someone finds that out, and returning "" or an error string would hide it.
func TestPrettyJSON(t *testing.T) {
	out, ok := prettyJSON(`{"b":1,"a":2}`).(string)
	if !ok || !strings.Contains(out, "\n") {
		t.Fatalf("valid JSON must be re-indented, got %q", out)
	}

	for _, raw := range []any{"not json at all", "", 42, nil} {
		if got := prettyJSON(raw); got != raw {
			t.Errorf("prettyJSON(%v) = %v, want it returned untouched", raw, got)
		}
	}
}

// TestFilterByOptions_AreDistinctColumnValues is acceptance item 2.
//
// Resolved server-side, because orbital's UI is a consumer of orbital's API
// like any other: "walk the rows collecting distinct values" is exactly the
// client re-implementation the flattened export-preview response exists to
// avoid. The column INDEX matters as much as the options — it must match the
// template's header order (Name, scalars, references, Orb ID) or the dropdown
// filters the wrong column, which looks like a broken filter rather than a
// broken index.
func TestFilterByOptions_AreDistinctColumnValues(t *testing.T) {
	cols := []page.ColumnHeader{{Field: "hostname", Label: "Hostname"}, {Field: "model", Label: "Model"}}
	refs := []page.RefHeader{{Field: "rack", Label: "Rack"}, {Field: "dataCenter", Label: "Data Center"}}
	rows := []map[string]any{
		{"hostname": "a", "model": "R650", "dataCenter": map[string]any{"name": "colo"}},
		{"hostname": "b", "model": "R650", "dataCenter": map[string]any{"name": "alaska"}},
		{"hostname": "c", "model": "R750", "dataCenter": map[string]any{"name": "colo"}},
		// A row with no data center at all must not contribute a blank option.
		{"hostname": "d", "model": "R750"},
	}

	t.Run("reference column", func(t *testing.T) {
		f := buildFilterBy(configitems.View{FilterBy: "dataCenter"}, cols, refs, rows)
		if f == nil {
			t.Fatal("dataCenter is a reference column and must produce a filter dropdown")
		}
		// Name(0) hostname(1) model(2) rack(3) dataCenter(4).
		if f.Column != 4 {
			t.Errorf("Column = %d, want 4", f.Column)
		}
		if f.Label != "Data Center" {
			t.Errorf("Label = %q, want the column heading", f.Label)
		}
		// The empty option names the whole SET, so it is plural — the column
		// heading is singular because it heads one cell. "All Data Center"
		// is what reading the heading straight through gives you.
		if f.All != "All Data Centers" {
			t.Errorf("All = %q, want \"All Data Centers\"", f.All)
		}
		if len(f.Options) != 2 || f.Options[0] != "alaska" || f.Options[1] != "colo" {
			t.Errorf("Options = %v, want [alaska colo] — distinct, sorted, no blank", f.Options)
		}
	})

	t.Run("scalar column", func(t *testing.T) {
		f := buildFilterBy(configitems.View{FilterBy: "model"}, cols, refs, rows)
		if f == nil {
			t.Fatal("model is a scalar column and must produce a filter dropdown")
		}
		if f.Column != 2 {
			t.Errorf("Column = %d, want 2", f.Column)
		}
		if len(f.Options) != 2 {
			t.Errorf("Options = %v, want the two distinct models", f.Options)
		}
	})

	// The negatives. A dropdown whose only choice selects everything is a
	// control that cannot do anything, and an unannotated view must render no
	// control at all — acceptance item 1's other half.
	t.Run("single distinct value yields nothing", func(t *testing.T) {
		one := []map[string]any{{"model": "R650"}, {"model": "R650"}}
		if f := buildFilterBy(configitems.View{FilterBy: "model"}, cols, refs, one); f != nil {
			t.Errorf("one distinct value must not produce a filter dropdown, got %+v", f)
		}
	})
	t.Run("no annotation yields nothing", func(t *testing.T) {
		if f := buildFilterBy(configitems.View{}, cols, refs, rows); f != nil {
			t.Errorf("an unannotated view must produce no filterBy, got %+v", f)
		}
	})
	t.Run("field that is not a column yields nothing", func(t *testing.T) {
		if f := buildFilterBy(configitems.View{FilterBy: "serialNumber"}, cols, refs, rows); f != nil {
			t.Errorf("a non-column must produce no filterBy, got %+v", f)
		}
	})
}

// TestIsTruncated covers the row-cap notice decision.
//
// The edge that matters is a page that is exactly full: a type with precisely
// `limit` rows renders a full page with nothing missing, and announcing
// truncation there sends someone looking for rows that are all already on
// screen. The opposite edge — a full page with more behind it — is the case
// the notice exists for.
func TestIsTruncated(t *testing.T) {
	for _, tc := range []struct {
		name               string
		rows, total, limit int
		want               bool
	}{
		{"well under the cap", 190, 190, 500, false},
		{"exactly at the cap, nothing more", 500, 500, 500, false},
		{"exactly at the cap, more behind it", 500, 501, 500, true},
		{"far over", 500, 12000, 500, true},
		{"empty", 0, 0, 500, false},
		// A count that is missing or unparseable leaves total at 0. Saying
		// nothing beats inventing a denominator.
		{"no count available", 500, 0, 500, false},
		// A short page cannot be truncated, whatever the count claims — the
		// graph may have changed between the two root fields.
		{"short page with a larger count", 12, 900, 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTruncated(tc.rows, tc.total, tc.limit); got != tc.want {
				t.Errorf("isTruncated(%d, %d, %d) = %v, want %v", tc.rows, tc.total, tc.limit, got, tc.want)
			}
		})
	}
}

// The two defaults must not drift: envconfig's tag is a string literal and the
// handler's fallback is a Go const, so nothing connects them but this.
//
// Drift is silent and asymmetric — an operator who sets nothing gets the
// envconfig value, a caller who forgets WithRowCap gets the const, and the two
// pages would then cap differently with no way to tell from the UI.
func TestDefaultListMaxRows_MatchesConfigDefault(t *testing.T) {
	for _, tc := range []struct{ file, env string }{
		{"../config/config.go", "ORBITAL_LIST_MAX_ROWS"},
		{"../orbconfig/config.go", "ORB_LIST_MAX_ROWS"},
	} {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		re := regexp.MustCompile(`envconfig:"` + tc.env + `"\s+default:"(\d+)"`)
		m := re.FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s: no %s default found — did the tag change?", tc.file, tc.env)
		}
		want, err := strconv.Atoi(string(m[1]))
		if err != nil {
			t.Fatal(err)
		}
		if want != DefaultListMaxRows {
			t.Errorf("%s default is %d but handler.DefaultListMaxRows is %d — they must match",
				tc.env, want, DefaultListMaxRows)
		}
	}
}

// A cap of zero must never mean "no rows".
func TestWithRowCap_NonPositiveFallsBackToDefault(t *testing.T) {
	for _, n := range []int{0, -1} {
		g := (&GenericRenderer{}).WithRowCap(n, "X")
		if g.listMaxRows != DefaultListMaxRows {
			t.Errorf("WithRowCap(%d) = %d, want the default %d", n, g.listMaxRows, DefaultListMaxRows)
		}
	}
	if g := (&GenericRenderer{}).WithRowCap(37, "X"); g.listMaxRows != 37 {
		t.Errorf("an explicit cap must be honoured, got %d", g.listMaxRows)
	}
}

// TestColumnValue pulls a computed column out of the nested response shape.
//
// The absent cases are the point: a server with no Kubernetes node has no
// cluster, and that must render an em dash like any other missing value rather
// than panic on a nil map.
func TestColumnValue(t *testing.T) {
	row := map[string]any{
		"orbId": "colo:server-X",
		"kubernetesNode": map[string]any{
			"role":    "worker",
			"cluster": map[string]any{"name": "colo-prod"},
		},
		"serversAggregate": map[string]any{"count": float64(22)},
	}
	for _, tc := range []struct {
		name string
		col  configitems.ViewColumn
		want any
	}{
		{"two hops", configitems.ViewColumn{Path: "kubernetesNode.cluster.name"}, "colo-prod"},
		{"one hop", configitems.ViewColumn{Path: "kubernetesNode.role"}, "worker"},
		// The count is fetched as `<field>Aggregate { count }`, so the response
		// path is not the declared path.
		{"count", configitems.ViewColumn{Path: "servers.count", Field: "servers", IsCount: true}, float64(22)},
		{"missing hop", configitems.ViewColumn{Path: "nothing.here"}, nil},
		{"missing leaf", configitems.ViewColumn{Path: "kubernetesNode.nosuch"}, nil},
		{"hop is not an object", configitems.ViewColumn{Path: "orbId.deeper"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := columnValue(row, tc.col); got != tc.want {
				t.Errorf("columnValue(%q) = %v, want %v", tc.col.Path, got, tc.want)
			}
		})
	}

	// A row missing the relationship entirely — the common case for a server
	// that is not a cluster node.
	bare := map[string]any{"orbId": "colo:server-Y"}
	if got := columnValue(bare, configitems.ViewColumn{Path: "kubernetesNode.role"}); got != nil {
		t.Errorf("an absent relationship must yield nil, got %v", got)
	}
}
