package handler

import (
	"strings"
	"testing"

	"github.com/armada/orbital/internal/configitems"
)

// serverEntity is one Server as the detail query returns it: single members as
// objects, list members as arrays, plus a reference the page does not declare.
func serverEntity() map[string]any {
	return map[string]any{
		"orbId":             "colo:server-A",
		"idracSettings":     map[string]any{"orbId": "colo:A-idrac"},
		"serverMaintenance": map[string]any{"orbId": "colo:server-maintenance-A"},
		"storageControllers": []any{
			map[string]any{"orbId": "colo:ctrl-1", "storageDevices": []any{
				map[string]any{"orbId": "colo:disk-1"},
				map[string]any{"orbId": "colo:disk-2"},
			}},
			map[string]any{"orbId": "colo:ctrl-2"},
		},
		// NOT declared. A link row, never part of the subgraph — rolling a data
		// centre's audit onto every server would be absurd.
		"dataCenter": map[string]any{"orbId": "colo:colo-galleon"},
	}
}

// The subgraph is EXACTLY the declared paths: single members, list members,
// and a two-hop path, with an undeclared reference left out.
//
// Regression class: the walk once type-asserted map[string]any, so every LIST
// member was skipped in silence — a server rolled up 3 orbIds while its delete
// removed ~30. Exact-set rather than contains, because the failure to guard
// against is a MISSING id and a `contains` check passes while the set shrinks.
func TestSubgraphOrbIDs_IsExactlyTheDeclaredPaths(t *testing.T) {
	got := subgraphOrbIDs(fixtureViewSet(), "Server", "colo:server-A", serverEntity())
	want := []string{
		"colo:server-A", // root, always first
		"colo:A-idrac", "colo:ctrl-1", "colo:ctrl-2",
		"colo:disk-1", "colo:disk-2", "colo:server-maintenance-A",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("subgraph\n  got:  %v\n  want: %v", got, want)
	}
}

// An INTERMEDIATE hop — one a path passes through without declaring — is not
// in the subgraph. The dev listed the disks, not the controllers; collecting
// every orbId along the way would widen the audit tab and the scope pin past
// what the page declares, and the delete would disagree with both.
func TestSubgraphOrbIDs_AnUndeclaredIntermediateIsNotInIt(t *testing.T) {
	views := fixtureViewSet()
	for i := range views {
		if views[i].Type == "Server" {
			views[i].Subgraph = []configitems.ViewTab{
				{Field: "storageControllers.storageDevices", Type: "StorageDevice", IsList: true},
			}
		}
	}
	got := subgraphOrbIDs(views, "Server", "colo:server-A", serverEntity())
	want := []string{"colo:server-A", "colo:disk-1", "colo:disk-2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("subgraph\n  got:  %v\n  want: %v", got, want)
	}
}

// A concrete type with no page of its own takes its INTERFACE page's subgraph.
// An EksaKubernetesCluster renders on /clusters; if its audit tab, scope pin and
// delete read the concrete type's own (empty) subgraph, all three shrink to the
// cluster alone, silently.
func TestSubgraphOrbIDs_ConcreteTypeTakesItsInterfacePage(t *testing.T) {
	entity := map[string]any{
		"orbId": "colo:dev-main",
		"backup": map[string]any{"orbId": "colo:dev-main-backup",
			"etcd": map[string]any{"orbId": "colo:dev-main-etcd-backup"}},
		"nodes": []any{map[string]any{"orbId": "colo:node-1"}},
	}
	got := subgraphOrbIDs(fixtureViewSet(), "EksaKubernetesCluster", "colo:dev-main", entity)
	want := []string{"colo:dev-main", "colo:dev-main-backup", "colo:dev-main-etcd-backup", "colo:node-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("subgraph\n  got:  %v\n  want: %v", got, want)
	}
}

// Rendered into an HTML attribute, so the order must not churn between renders.
// Go map iteration is random; without the sort the CSV differs per request.
func TestSubgraphOrbIDs_IsDeterministicRootFirst(t *testing.T) {
	first := subgraphOrbIDs(fixtureViewSet(), "Server", "colo:server-A", serverEntity())
	if first[0] != "colo:server-A" {
		t.Fatalf("root must come first, got %v", first)
	}
	for i := 0; i < 20; i++ {
		again := subgraphOrbIDs(fixtureViewSet(), "Server", "colo:server-A", serverEntity())
		if strings.Join(again, ",") != strings.Join(first, ",") {
			t.Fatalf("order churned between runs:\n  %v\n  %v", first, again)
		}
	}
}

// Nothing fetched, or nothing declared, still answers with the root. An empty
// list would render an audit panel that fetches nothing at all.
func TestSubgraphOrbIDs_DegradesToRootAlone(t *testing.T) {
	cases := map[string]struct {
		rootType string
		entity   map[string]any
	}{
		"declares nothing": {"IdracSettings", map[string]any{"orbId": "colo:A-idrac"}},
		"nothing loaded":   {"Server", map[string]any{"orbId": "colo:server-A"}},
		"no entity":        {"Server", nil},
		"unknown type":     {"", serverEntity()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := subgraphOrbIDs(fixtureViewSet(), tc.rootType, "colo:server-A", tc.entity)
			if len(got) != 1 || got[0] != "colo:server-A" {
				t.Errorf("got %v, want the root alone", got)
			}
		})
	}
}

// The detail query selects every declared path, each field exactly once — a
// path sharing a prefix with another member merges into one selection, because
// DGraph refuses a query naming a dotted path and the walk can only report what
// the query fetched.
func TestGenericDetailQuery_SelectsEveryDeclaredPathOnce(t *testing.T) {
	views := fixtureViewSet()
	q := genericDetailQuery(views.Of("Server"),
		func(name string) configitems.View { return views.Of(name) },
		func(string) []string { return nil },
		func(string) []configitems.ViewRefColumn { return nil },
		"colo:server-A")

	for _, want := range []string{"idracSettings {", "serverMaintenance {", "storageControllers {", "storageDevices {"} {
		if n := strings.Count(q, want); n != 1 {
			t.Errorf("%q selected %d times, want 1:\n%s", want, n, q)
		}
	}
	if strings.Contains(q, ".") {
		t.Errorf("a dotted path leaked into the selection; DGraph refuses the whole query:\n%s", q)
	}
}
