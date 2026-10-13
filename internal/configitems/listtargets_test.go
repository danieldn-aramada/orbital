package configitems

import (
	"strings"
	"testing"
)

// Acceptance 2 and 5: each stored member becomes its own target, carrying the
// row's OWN orbId — never one derived from the parent, which is what
// BuildEditTargets does for single-cardinality members and must not do here.
func TestExpandListTargets(t *testing.T) {
	m := ViewTab{Field: "racks", Type: "Rack", ParentEdge: "dataCenter", IsList: true, Editable: true}
	members := []ListMember{
		{OrbID: "colo:A.OF.C.09", Version: 3},
		{OrbID: "colo:A.OF.C.10", Version: 7},
	}
	got := ExpandListTargets(m, fixtureFields, fixtureMeta, "colo", "colo:colo-galleon", members)

	if len(got) != 2 {
		t.Fatalf("got %d targets, want one per stored member", len(got))
	}
	for i, tg := range got {
		if tg.OrbID != members[i].OrbID {
			t.Errorf("target[%d].OrbID = %q, want the ROW's id %q", i, tg.OrbID, members[i].OrbID)
		}
		// Acceptance 6: each member's own OCC counter, so a concurrent edit of
		// one is refused without blocking the others.
		if tg.Version != members[i].Version {
			t.Errorf("target[%d].Version = %d, want %d", i, tg.Version, members[i].Version)
		}
		if strings.Join(tg.Path, ".") != "racks" {
			t.Errorf("target[%d].Path = %v, want [racks]", i, tg.Path)
		}
		if tg.ParentOrbID != "colo:colo-galleon" || tg.ParentInverseField != "dataCenter" {
			t.Errorf("target[%d] lost its parent link: %q via %q", i, tg.ParentOrbID, tg.ParentInverseField)
		}
	}
}

// A member with no editable fields is not an editor — same rule the
// single-cardinality path applies, so a relationship tab of hardware facts does
// not become writable just because someone marked the list editable.
func TestExpandListTargets_NoEditableFieldsYieldsNothing(t *testing.T) {
	m := ViewTab{Field: "networkAdapters", Type: "NetworkAdapter", ParentEdge: "server", IsList: true, Editable: true}
	got := ExpandListTargets(m, fixtureFields, fixtureMeta, "colo", "colo:server-X",
		[]ListMember{{OrbID: "colo:network-adapter-X-NIC.1", Version: 1}})
	if len(got) != 0 {
		t.Errorf("got %d targets for a type with no editable fields, want 0", len(got))
	}
}

// A row with no id cannot be addressed. Writing it by POSITION would send the
// value to whichever row happened to sort there — a silent cross-write, which
// is the worst failure this feature could have.
func TestExpandListTargets_RowWithoutIDIsSkipped(t *testing.T) {
	m := ViewTab{Field: "racks", Type: "Rack", ParentEdge: "dataCenter", IsList: true, Editable: true}
	got := ExpandListTargets(m, fixtureFields, fixtureMeta, "colo", "colo:colo-galleon",
		[]ListMember{{OrbID: "", Version: 1}, {OrbID: "colo:A.OF.C.10", Version: 2}})
	if len(got) != 1 || got[0].OrbID != "colo:A.OF.C.10" {
		t.Errorf("got %+v, want only the row that can be addressed", got)
	}
}

// Acceptance 3 and 4, at this level: expansion reflects membership, it cannot
// change it. Fewer members in means fewer targets out and nothing else — no
// create, no delete, no linking.
func TestExpandListTargets_EmptyMembershipYieldsNoTargets(t *testing.T) {
	m := ViewTab{Field: "racks", Type: "Rack", ParentEdge: "dataCenter", IsList: true, Editable: true}
	if got := ExpandListTargets(m, fixtureFields, fixtureMeta, "colo", "colo:colo-galleon", nil); len(got) != 0 {
		t.Errorf("got %d targets for a list with no stored members, want 0", len(got))
	}
}
