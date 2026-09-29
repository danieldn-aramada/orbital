package dgraphschema

import "testing"

// TestDeclaredTypes covers the schema half of orphan detection.
//
// Regression class: the complement filter is built from this list, so a type
// this fails to see becomes "undeclared" and every one of its nodes is reported
// as orphaned. On a real schema that is thousands of false alarms, which is
// worse than the silence it replaced — nobody reads a check that cries wolf.
func TestDeclaredTypes(t *testing.T) {
	sdl := `
# type CommentedOut implements ConfigItem { x: String }
interface ConfigItem {
    orbId: String!
}

"""slug: clusters"""
interface KubernetesCluster {
    cni: String
}

type Server implements ConfigItem {
    hostname: String   # type Decoy is not a declaration
}

type  EksaKubernetesCluster  implements KubernetesCluster & ConfigItem {
    clusterType: String
}
`
	got := declaredTypes(sdl)
	want := []string{"Server", "EksaKubernetesCluster"}
	if len(got) != len(want) {
		t.Fatalf("declaredTypes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("declaredTypes()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// Interfaces must NOT be collected: nothing is ever stored as an interface, so
// including one in the complement filter would exclude every implementing
// node and hide real orphans behind it.
func TestDeclaredTypes_ExcludesInterfaces(t *testing.T) {
	for _, name := range declaredTypes(`interface ConfigItem { orbId: String! }`) {
		if name == "ConfigItem" {
			t.Fatal("ConfigItem is an interface and must not be treated as a declared node type")
		}
	}
}

// An unreadable or empty schema must report NOTHING.
//
// Regression class: with no declared types the complement filter matches every
// node in the graph, so a momentarily unreachable schema would report the whole
// database as orphaned. Failing loud is right for a real violation and wrong
// for a failure to look.
func TestDeclaredTypes_EmptySchemaYieldsNoTypes(t *testing.T) {
	if got := declaredTypes(""); len(got) != 0 {
		t.Fatalf("declaredTypes(\"\") = %v, want none", got)
	}
}

func TestQueryURLFor(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://localhost:8080/admin", "http://localhost:8080/query"},
		{"http://dgraph-blue:8080/admin", "http://dgraph-blue:8080/query"},
	}
	for _, tt := range tests {
		if got := queryURLFor(tt.in); got != tt.want {
			t.Errorf("queryURLFor(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	got := Describe([]Orphan{{Type: "Foo", Count: 1}, {Type: "Bar", Count: 12}})
	want := "Foo (1 node), Bar (12 nodes)"
	if got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}
