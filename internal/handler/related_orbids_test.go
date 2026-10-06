package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/internal/configitems"
)

// serverEntity is one Server as the detail query returns it: single owned
// children as objects, list owned children as arrays, plus a non-owned
// reference the walk must not follow.
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
		// NOT owned. A Server references its data centre; it does not own it,
		// and rolling a data centre's audit onto every server would be absurd.
		"dataCenter": map[string]any{"orbId": "colo:colo-galleon"},
	}
}

// AC 1 + AC 2 — the roll-up is the WHOLE owned subtree: single children, list
// children, and grandchildren, with referenced-but-not-owned entities left out.
//
// Regression class: the walk type-asserted map[string]any, so every LIST
// dependent was skipped in silence. A server rolled up 3 orbIds while cascade
// delete removed ~30 — no error, no empty panel, just fewer events than had
// happened. Exact-set rather than contains, because the failure to guard
// against is a MISSING id and a `contains` check passes while the set shrinks.
func TestOwnedSubtreeOrbIDs_CoversSinglesListsAndGrandchildren(t *testing.T) {
	got := ownedSubtreeOrbIDs(fixtureViewSet(), "Server", "colo:server-A", serverEntity())
	want := []string{
		"colo:server-A", // root, always first
		"colo:A-idrac", "colo:ctrl-1", "colo:ctrl-2",
		"colo:disk-1", "colo:disk-2", "colo:server-maintenance-A",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("owned subtree\n  got:  %v\n  want: %v", got, want)
	}
}

// The walk is ownership-GUIDED, not a scrape for every orbId in the result.
// Scraping would roll a data centre's own audit events onto every server on it.
func TestOwnedSubtreeOrbIDs_IgnoresNonOwnedReferences(t *testing.T) {
	got := ownedSubtreeOrbIDs(fixtureViewSet(), "Server", "colo:server-A", serverEntity())
	if containsStr(got, "colo:colo-galleon") {
		t.Errorf("a referenced-but-not-owned entity leaked into the subtree: %v", got)
	}
}

// AC 7 — rendered into an HTML attribute, so the order must not churn between
// renders. Go map iteration is random; without the sort the CSV differs per
// request and every diff of the page is noise.
func TestOwnedSubtreeOrbIDs_IsDeterministicRootFirst(t *testing.T) {
	first := ownedSubtreeOrbIDs(fixtureViewSet(), "Server", "colo:server-A", serverEntity())
	if first[0] != "colo:server-A" {
		t.Fatalf("root must come first, got %v", first)
	}
	for i := 0; i < 20; i++ {
		again := ownedSubtreeOrbIDs(fixtureViewSet(), "Server", "colo:server-A", serverEntity())
		if strings.Join(again, ",") != strings.Join(first, ",") {
			t.Fatalf("order churned between runs:\n  %v\n  %v", first, again)
		}
	}
}

// AC 8 — nothing fetched, or nothing owned, still answers with the root. An
// empty list would render an audit panel that fetches nothing at all.
func TestOwnedSubtreeOrbIDs_DegradesToRootAlone(t *testing.T) {
	cases := map[string]struct {
		rootType string
		entity   map[string]any
	}{
		"owns nothing":   {"IdracSettings", map[string]any{"orbId": "colo:A-idrac"}},
		"nothing loaded": {"Server", map[string]any{"orbId": "colo:server-A"}},
		"no entity":      {"Server", nil},
		"unknown type":   {"", serverEntity()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ownedSubtreeOrbIDs(fixtureViewSet(), tc.rootType, "colo:server-A", tc.entity)
			if len(got) != 1 || got[0] != "colo:server-A" {
				t.Errorf("got %v, want the root alone", got)
			}
		})
	}
}

// A declared ownership cycle must terminate. `ownerReferences:` is
// operator-editable, so nothing stops someone writing one, and a page that
// hangs is worse than a page missing a row.
func TestOwnedSubtreeOrbIDs_TerminatesOnACycle(t *testing.T) {
	views := configitems.ViewSet{
		{Type: "A", Dependents: []configitems.OwnedMember{{ChildType: "B", ChildField: "b"}}},
		{Type: "B", Dependents: []configitems.OwnedMember{{ChildType: "A", ChildField: "a"}}},
	}
	node := map[string]any{"orbId": "ns:a-1"}
	child := map[string]any{"orbId": "ns:b-1"}
	node["b"] = child
	child["a"] = node // the cycle, in the DATA as well as the config

	done := make(chan []string, 1)
	go func() { done <- ownedSubtreeOrbIDs(views, "A", "ns:a-1", node) }()
	select {
	case got := <-done:
		if !containsStr(got, "ns:b-1") {
			t.Errorf("the one real child is missing: %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("walk did not terminate on a declared ownership cycle")
	}
}

// AC 5 — the roll-up covers what the root OWNS, so the detail query must fetch
// owned children the page does not tab. Deriving the fetch from `tabs:` made
// the audit tab silently narrow to whatever the page happened to show.
func TestGenericDetailQuery_SelectsOwnedChildrenWithNoTab(t *testing.T) {
	views := fixtureViewSet()
	v := views.Of("Server")
	// The page shows only its iDRAC; it still OWNS maintenance and controllers.
	v.Tabs = []configitems.ViewTab{{Field: "idracSettings", Type: "IdracSettings", Editable: true}}

	q := genericDetailQuery(v,
		func(name string) configitems.View { return views.Of(name) },
		func(string) []string { return nil },
		func(string) []configitems.ViewRefColumn { return nil },
		func(name string) string { return views.OwnedOrbIDSelection(name) },
		"colo:server-A")

	for _, want := range []string{"serverMaintenance {", "storageControllers {", "storageDevices {"} {
		if !strings.Contains(q, want) {
			t.Errorf("untabbed owned child not selected (%q missing):\n%s", want, q)
		}
	}
	if strings.Count(q, "idracSettings {") != 1 {
		t.Errorf("a tabbed owned child must be selected exactly once:\n%s", q)
	}
}

// pageBoundaryViews mirrors the SHIPPED shape of config/views.yaml for this
// rule: a slug is non-empty exactly for a type with a `pages:` entry.
//
// fixtureViewSet cannot express it — it hands a slug to IdracSettings,
// StorageDevice and Rack, none of which have pages in production — so a test
// written against that fixture would stop the walk everywhere and prove nothing.
func pageBoundaryViews() configitems.ViewSet {
	owned := func(field, typeName string, isList bool) configitems.OwnedMember {
		return configitems.OwnedMember{ChildType: typeName, ChildField: field, IsList: isList}
	}
	return configitems.ViewSet{
		{Type: "DataCenter", Slug: "data-centers", Dependents: []configitems.OwnedMember{
			owned("racks", "Rack", true), owned("servers", "Server", true),
		}},
		{Type: "Rack", Dependents: []configitems.OwnedMember{owned("servers", "Server", true)}},
		{Type: "Server", Slug: "servers", Dependents: []configitems.OwnedMember{
			owned("idracSettings", "IdracSettings", false),
			owned("storageControllers", "StorageController", true),
		}},
		{Type: "StorageController", Dependents: []configitems.OwnedMember{
			owned("storageDevices", "StorageDevice", true),
		}},
		{Type: "IdracSettings"},
		{Type: "StorageDevice"},
	}
}

func dcEntity() map[string]any {
	return map[string]any{
		"orbId": "colo:dc-1",
		"racks": []any{map[string]any{"orbId": "colo:rack-1", "servers": []any{
			map[string]any{"orbId": "colo:server-in-rack"},
		}}},
		"servers": []any{map[string]any{"orbId": "colo:server-1",
			"idracSettings": map[string]any{"orbId": "colo:server-1-idrac"}}},
	}
}

// The audit roll-up stops at a dependent that has its OWN page: its events
// already live there, and carrying them twice is what made a data centre send
// 1,159 orbIds to an endpoint that refuses more than 128.
func TestAuditRollup_StopsAtTypesWithTheirOwnPage(t *testing.T) {
	got := auditRollupOrbIDs(pageBoundaryViews(), "DataCenter", "colo:dc-1", dcEntity())
	want := []string{"colo:dc-1", "colo:rack-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("audit roll-up\n  got:  %v\n  want: %v (a rack has no page; a server does)", got, want)
	}
}

// ...and a type whose children have NO pages keeps its whole subtree. The rule
// must bound a container, not hollow out an ordinary page.
func TestAuditRollup_KeepsTheWholeSubtreeWhenNoChildHasAPage(t *testing.T) {
	entity := map[string]any{
		"orbId":         "colo:server-1",
		"idracSettings": map[string]any{"orbId": "colo:server-1-idrac"},
		"storageControllers": []any{map[string]any{"orbId": "colo:ctrl-1",
			"storageDevices": []any{map[string]any{"orbId": "colo:disk-1"}}}},
	}
	got := auditRollupOrbIDs(pageBoundaryViews(), "Server", "colo:server-1", entity)
	want := []string{"colo:server-1", "colo:ctrl-1", "colo:disk-1", "colo:server-1-idrac"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("audit roll-up\n  got:  %v\n  want: %v", got, want)
	}
}

// The change-request scope pin takes the FULL subtree — its staleness base has
// to cover everything a delete would cascade to, which is not a display subset.
// If these two ever return the same thing for a container, one of them is wrong.
func TestOwnedSubtree_IsWiderThanTheAuditRollup(t *testing.T) {
	views := pageBoundaryViews()
	full := ownedSubtreeOrbIDs(views, "DataCenter", "colo:dc-1", dcEntity())
	rollup := auditRollupOrbIDs(views, "DataCenter", "colo:dc-1", dcEntity())

	for _, want := range []string{"colo:server-1", "colo:server-1-idrac", "colo:server-in-rack"} {
		if !containsStr(full, want) {
			t.Errorf("the pin's subtree %v must still reach %s — it is in the delete cascade", full, want)
		}
	}
	if len(rollup) >= len(full) {
		t.Errorf("the roll-up (%d) must be narrower than the owned subtree (%d)", len(rollup), len(full))
	}
	for _, id := range rollup {
		if !containsStr(full, id) {
			t.Errorf("the roll-up names %s, which the owned subtree does not — it is meant to be a SUBSET", id)
		}
	}
}
