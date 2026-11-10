package configitems

import "testing"

// A miniature of the deployed schema, shaped like the real one where the level
// rule has decisions to make: a required back-edge, an orbId built from a
// nullable one, an interface with an implementor, a child typed by that
// interface, a type with two parents, and types with no parent at all.
func schemaGraphFixture(t *testing.T) (map[string]TypeInfo, map[string]string) {
	t.Helper()
	rel := func(name, target string, list, nonNull bool) DerivedField {
		kind := "OBJECT"
		if target == "KubernetesCluster" {
			kind = "INTERFACE"
		}
		return DerivedField{Name: name, Kind: kind, TypeName: target, IsList: list, NonNull: nonNull}
	}
	pattern := func(raw string) []OrbIDPattern {
		p, err := ParseOrbIDPattern(raw)
		if err != nil {
			t.Fatalf("pattern %q: %v", raw, err)
		}
		return []OrbIDPattern{p}
	}
	clusterFields := []DerivedField{
		rel("dataCenter", "DataCenter", false, true),
		rel("nodes", "KubernetesNode", true, false),
		rel("backup", "ClusterBackup", false, false),
	}
	types := map[string]TypeInfo{
		"DataCenter": {Fields: []DerivedField{
			{Name: "name", Kind: "SCALAR", Editable: true},
			rel("servers", "Server", true, false),
			rel("racks", "Rack", true, false),
			rel("kubernetesClusters", "KubernetesCluster", true, false),
		}},
		"Rack": {Fields: []DerivedField{
			rel("dataCenter", "DataCenter", false, true),
			rel("servers", "Server", true, false),
		}},
		"Server": {Fields: []DerivedField{
			rel("dataCenter", "DataCenter", false, true),
			rel("rack", "Rack", false, false),
			rel("networkAdapters", "NetworkAdapter", true, false),
			rel("networkInterfaces", "NetworkInterface", true, false),
			rel("kubernetesNode", "KubernetesNode", false, false),
			rel("oobIP", "IPAddress", false, false),
		}},
		"NetworkAdapter": {Fields: []DerivedField{
			rel("server", "Server", false, true),
			rel("networkInterfaces", "NetworkInterface", true, false),
		}},
		"NetworkInterface": {
			OrbIDPattern: pattern("{type}-{server.serviceTag}-{name}"),
			Fields: []DerivedField{
				rel("server", "Server", false, false),
				rel("networkAdapter", "NetworkAdapter", false, false),
			},
		},
		"KubernetesCluster": {
			IsInterface:   true,
			PossibleTypes: []string{"EksaKubernetesCluster"},
			Fields:        clusterFields,
		},
		"EksaKubernetesCluster": {
			Implements: []string{"KubernetesCluster", "ConfigItem"},
			Fields: append(append([]DerivedField{}, clusterFields...),
				rel("managementCluster", "EksaKubernetesCluster", false, false),
				rel("workloadClusters", "EksaKubernetesCluster", true, false),
			),
		},
		"KubernetesNode": {Fields: []DerivedField{
			rel("server", "Server", false, true),
			rel("cluster", "KubernetesCluster", false, true),
		}},
		"ClusterBackup": {Fields: []DerivedField{
			rel("cluster", "KubernetesCluster", false, true),
		}},
		"IPAddress": {
			OrbIDPattern: pattern("{address}"),
			Fields:       []DerivedField{rel("serverOobIP", "Server", false, false)},
		},
		"Widget": {Fields: []DerivedField{{Name: "name", Kind: "SCALAR", Editable: true}}},
	}
	inverse := map[string]string{}
	pair := func(a, b string) { inverse[a], inverse[b] = fieldOf(b), fieldOf(a) }
	pair("DataCenter.servers", "Server.dataCenter")
	pair("DataCenter.racks", "Rack.dataCenter")
	pair("DataCenter.kubernetesClusters", "KubernetesCluster.dataCenter")
	pair("Rack.servers", "Server.rack")
	pair("Server.networkAdapters", "NetworkAdapter.server")
	pair("Server.networkInterfaces", "NetworkInterface.server")
	pair("NetworkAdapter.networkInterfaces", "NetworkInterface.networkAdapter")
	pair("Server.kubernetesNode", "KubernetesNode.server")
	pair("KubernetesCluster.nodes", "KubernetesNode.cluster")
	pair("KubernetesCluster.backup", "ClusterBackup.cluster")
	pair("Server.oobIP", "IPAddress.serverOobIP")
	pair("EksaKubernetesCluster.workloadClusters", "EksaKubernetesCluster.managementCluster")
	return types, inverse
}

func fieldOf(typeDotField string) string {
	for i := len(typeDotField) - 1; i >= 0; i-- {
		if typeDotField[i] == '.' {
			return typeDotField[i+1:]
		}
	}
	return typeDotField
}

// The shipped views declare these homes; the tests that are about the
// derivation alone pass none.
var schemaGraphHomes = map[string][]CanonicalParentDecl{
	"NetworkInterface": {
		{Type: "NetworkAdapter", Field: "networkAdapter", Down: "networkInterfaces"},
		{Type: "Server", Field: "server", Down: "networkInterfaces"},
	},
	"IPAddress": {{Type: "Server", Field: "serverOobIP", Down: "oobIP"}},
}

func TestBuildSchemaGraph(t *testing.T) {
	types, inverse := schemaGraphFixture(t)
	g := BuildSchemaGraph(types, inverse, schemaGraphHomes, []string{"Rack", "Server"})
	derivedOnly := BuildSchemaGraph(types, inverse, nil, nil)

	node := func(g SchemaGraph, id string) SchemaGraphNode {
		t.Helper()
		for _, n := range g.Nodes {
			if n.ID == id {
				return n
			}
		}
		t.Fatalf("no node %q", id)
		return SchemaGraphNode{}
	}
	edge := func(g SchemaGraph, a, b string) SchemaGraphEdge {
		t.Helper()
		var found []SchemaGraphEdge
		for _, e := range g.Edges {
			if (e.Source == a && e.Target == b) || (e.Source == b && e.Target == a) {
				found = append(found, e)
			}
		}
		if len(found) != 1 {
			t.Fatalf("want exactly one edge between %s and %s, got %d: %+v", a, b, len(found), found)
		}
		return found[0]
	}
	level := func(g SchemaGraph, id string, want int) {
		t.Helper()
		if got := node(g, id).Level; got != want {
			t.Errorf("%s is on level %d, want %d", id, got, want)
		}
	}

	// Item 2 — levels.
	t.Run("DataCenter is at the top", func(t *testing.T) {
		level(g, "DataCenter", 0)
	})
	t.Run("a type sits one level below the type it lives inside", func(t *testing.T) {
		level(g, "Server", 1)
		level(g, "Rack", 1)
		level(g, "NetworkAdapter", 2)
		if e := edge(g, "DataCenter", "Server"); !e.SetsLevel || e.Source != "DataCenter" {
			t.Errorf("DataCenter→Server must be the level-setting line, parent as source: %+v", e)
		}
	})
	t.Run("an ID built from a parent counts as living inside it", func(t *testing.T) {
		// NetworkInterface.server is nullable; only its orbIdPattern ties it to the server.
		level(derivedOnly, "NetworkInterface", 2)
		if e := edge(derivedOnly, "Server", "NetworkInterface"); !e.SetsLevel {
			t.Errorf("with no declared home, the server the orbId is built from sets the level: %+v", e)
		}
	})
	t.Run("canonicalParent picks among several parents", func(t *testing.T) {
		level(g, "NetworkInterface", 3)
		if e := edge(g, "NetworkAdapter", "NetworkInterface"); !e.SetsLevel {
			t.Errorf("the declared home must set the level: %+v", e)
		}
		if e := edge(g, "Server", "NetworkInterface"); e.SetsLevel {
			t.Errorf("an undeclared second parent is a cross-link: %+v", e)
		}
	})
	t.Run("canonicalParent places a type that lives inside nothing", func(t *testing.T) {
		level(g, "IPAddress", 2)
		level(derivedOnly, "IPAddress", 0)
		if e := edge(g, "Server", "IPAddress"); !e.SetsLevel || e.Source != "Server" {
			t.Errorf("the home's edge sets the level, parent as source: %+v", e)
		}
	})
	t.Run("an implementor sits one level below its interface", func(t *testing.T) {
		level(g, "KubernetesCluster", 1)
		level(g, "EksaKubernetesCluster", 2)
		e := edge(g, "KubernetesCluster", "EksaKubernetesCluster")
		if e.Kind != "implements" || !e.SetsLevel || e.Source != "KubernetesCluster" {
			t.Errorf("want a level-setting implements edge from the interface: %+v", e)
		}
	})
	t.Run("a child of an interface sits below the implementor, not the interface", func(t *testing.T) {
		level(g, "ClusterBackup", 3)
	})
	t.Run("the nearest parent sets the level and a farther one is a cross-link", func(t *testing.T) {
		level(g, "KubernetesNode", 2)
		if e := edge(g, "KubernetesCluster", "KubernetesNode"); e.SetsLevel {
			t.Errorf("the cluster is the farther parent; its edge must not set the level: %+v", e)
		}
	})
	t.Run("a type with no parent sits at the top", func(t *testing.T) {
		level(g, "Widget", 0)
	})

	// Item 3 — cross-links never set a level.
	t.Run("every type below the top has exactly one level-setting line into it", func(t *testing.T) {
		into := map[string]int{}
		for _, e := range g.Edges {
			if e.SetsLevel {
				into[e.Target]++
			}
		}
		for _, n := range g.Nodes {
			want := 1
			if n.Level == 0 {
				want = 0
			}
			if into[n.ID] != want {
				t.Errorf("%s (level %d) has %d level-setting lines into it, want %d", n.ID, n.Level, into[n.ID], want)
			}
		}
	})
	t.Run("a cross-link never sets a level", func(t *testing.T) {
		if e := edge(g, "Rack", "Server"); e.SetsLevel {
			t.Errorf("Rack↔Server is a cross-link: %+v", e)
		}
	})

	// Item 4 — interfaces.
	t.Run("interfaces are marked, ConfigItem included, and ConfigItem carries no lines", func(t *testing.T) {
		if k := node(g, "KubernetesCluster").Kind; k != "interface" {
			t.Errorf("KubernetesCluster kind = %q", k)
		}
		ci := node(g, "ConfigItem")
		if ci.Kind != "interface" || ci.Level != 0 {
			t.Errorf("ConfigItem must be an interface on the top level: %+v", ci)
		}
		for _, e := range g.Edges {
			if e.Source == "ConfigItem" || e.Target == "ConfigItem" {
				t.Errorf("every type implements ConfigItem; a line for each says nothing: %+v", e)
			}
		}
	})
	t.Run("a field declared on an interface is drawn once, from the interface", func(t *testing.T) {
		for _, e := range g.Edges {
			if e.Source == "EksaKubernetesCluster" && (e.Field == "nodes" || e.Field == "backup" || e.Field == "dataCenter") {
				t.Errorf("an inherited field was drawn again on the implementor: %+v", e)
			}
		}
	})

	// Item 5 — one line per relationship.
	t.Run("a two-way pair is one line carrying both field names and the cardinality", func(t *testing.T) {
		e := edge(g, "Rack", "Server")
		if e.Source != "Rack" || e.Field != "servers" || e.Inverse != "rack" || e.Cardinality != "1:N" {
			t.Errorf("want Rack.servers ↔ Server.rack as 1:N from Rack: %+v", e)
		}
		if e := edge(g, "Server", "KubernetesNode"); e.Cardinality != "1:1" {
			t.Errorf("Server.kubernetesNode ↔ KubernetesNode.server is 1:1: %+v", e)
		}
		self := edge(g, "EksaKubernetesCluster", "EksaKubernetesCluster")
		if self.Field != "workloadClusters" || self.Inverse != "managementCluster" || self.Cardinality != "1:N" {
			t.Errorf("a self-pair is one line from its list side: %+v", self)
		}
	})
	t.Run("siblings carry the configured order, unlisted types after", func(t *testing.T) {
		if r, s, a := node(g, "Rack").Order, node(g, "Server").Order, node(g, "NetworkAdapter").Order; r != 0 || s != 1 || a != 2 {
			t.Errorf("want Rack 0, Server 1, unlisted 2; got %d, %d, %d", r, s, a)
		}
	})
	t.Run("labels are readable names", func(t *testing.T) {
		for id, want := range map[string]string{"NetworkAdapter": "Network Adapter", "IPAddress": "IP Address"} {
			if got := node(g, id).Label; got != want {
				t.Errorf("%s label = %q, want %q", id, got, want)
			}
		}
	})
}
