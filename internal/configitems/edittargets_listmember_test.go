package configitems

import (
	"strings"
	"testing"
)

// These two pin the behaviour that exists BEFORE list members become editable,
// so the change that adds them has something to be measured against. Written
// first, deliberately: a test authored afterwards from the new code can only
// confirm that code, and the thing most at risk here is the SINGLE-cardinality
// path that already works.

// Acceptance 12: a list member without `editable: true` yields no edit target,
// so the editor has nothing to render for it and nothing to write.
//
// The fixture carries three — storageControllers, networkAdapters and
// nodes — all of them lists, none editable. Whatever list support does, it must
// not start emitting targets for members nobody marked editable: that would
// turn every relationship tab into an editor on the strength of a schema edge.
// listFixture is LOCAL to these tests and deliberately uses Rack: it is the
// only type in fixtureFields with editable fields that is also a list member in
// the real config. The shared fixture's list members (StorageController,
// NetworkAdapter, KubernetesNode) all have NO editable fields, so they are
// dropped by the `len(fields) == 0` gate and can never tell the two gates apart
// — a test built on them passes whatever the list gate does.
func listFixture(racksEditable bool) ViewSet {
	return ViewSet{{
		Type: "DataCenter", Slug: "data-centers",
		Relations: map[string]string{"racks": "Rack", "servers": "Server"},
		Subgraph: []ViewTab{
			{Field: "racks", Type: "Rack", ParentEdge: "dataCenter", IsList: true, Editable: racksEditable},
		},
	}}
}

// Acceptance 12: a list member WITHOUT `editable: true` yields no edit target,
// so the editor has nothing to render for it and nothing to write.
//
// Whatever list support does, it must not start emitting targets for members
// nobody marked editable — that would turn every relationship tab into an
// editor on the strength of a schema edge.
func TestBuildEditTargets_NonEditableListMembersYieldNoTarget(t *testing.T) {
	got := BuildEditTargets(listFixture(false), fixtureFields, fixtureMeta,
		"DataCenter", "colo:colo-galleon", "colo", "colo-galleon", nil)

	for _, tg := range got {
		if strings.Join(tg.Path, ".") == "racks" {
			t.Errorf("a target exists for `racks`, which is NOT marked editable")
		}
	}
	if len(got) != 1 {
		t.Errorf("got %d targets, want 1 (the root alone)", len(got))
	}
}

// Acceptance 5: BuildEditTargets NEVER emits a target for a list member, even
// an editable one — list targets come from stored rows, via ExpandListTargets.
//
// This is a permanent split, not a stage of the work. BuildEditTargets sees
// types and conventions and never entity data, so the only id it could produce
// for a list member is a DERIVED one — and deriving an id for a row that
// already exists would address a node that is not the row, or mint one that
// never existed. The two paths cannot be merged without one of them lying.
func TestBuildEditTargets_NeverEmitsListTargets(t *testing.T) {
	got := BuildEditTargets(listFixture(true), fixtureFields, fixtureMeta,
		"DataCenter", "colo:colo-galleon", "colo", "colo-galleon", nil)
	if len(got) != 1 {
		t.Fatalf("got %d targets, want 1 — list members are not editable yet", len(got))
	}
	if len(got[0].Path) != 0 {
		t.Errorf("the one target must be the ROOT, got path %v", got[0].Path)
	}
}

// Acceptance 11: the single-cardinality editable members are addressed by PATH
// and their ids are DERIVED from the parent — the behaviour list members cannot
// share, because a list member's id is whatever the stored row already has.
//
// Pinned as an exact set. A target gained or lost here is the editor writing
// somewhere new, which is the failure mode worth catching early.
func TestBuildEditTargets_SingleCardinalityPathsAreDerived(t *testing.T) {
	want := map[string]string{ // path → derived orbId
		"":              "colo:dev-main",
		"backup.etcd":   "colo:dev-main-etcd-backup",
		"backup.velero": "colo:dev-main-velero-backup",
		"backup.s3Sync": "colo:dev-main-s3sync",
	}
	got := BuildEditTargets(fixtureViews(), fixtureFields, fixtureMeta, "EksaKubernetesCluster", "colo:dev-main", "colo", "dev-main", nil)
	if len(got) != len(want) {
		t.Fatalf("got %d targets, want %d — a target gained or lost means the editor writes somewhere new", len(got), len(want))
	}
	for _, tg := range got {
		p := strings.Join(tg.Path, ".")
		orbID, ok := want[p]
		if !ok {
			t.Errorf("unexpected target path %q", p)
			continue
		}
		if tg.OrbID != orbID {
			t.Errorf("target %q orbId = %q, want %q — derived from the PARENT, not read from a row", p, tg.OrbID, orbID)
		}
	}
}
