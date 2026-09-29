//go:build integration

package dgraphschema

import (
	"context"
	"testing"

	"github.com/armada/orbital/internal/testutil"
)

// TestOrphans_AgainstTheLiveGraph is the end-to-end half: the detection must
// work against a real DGraph, because the query it depends on is DQL and the
// thing it inspects is `dgraph.type`, neither of which a fake can represent.
//
// Regression class: a clean graph reporting orphans. The complement filter is
// built from the schema, so any mistake in parsing it — or a DGraph that starts
// naming types differently — turns every node into an orphan. This asserts the
// NEGATIVE, which is the case that must hold on every healthy deployment.
func TestOrphans_CleanGraphReportsNothing(t *testing.T) {
	admin := testutil.DGraphAdminURL()
	live, err := Active(context.Background(), admin)
	if err != nil {
		t.Fatalf("read active schema: %v", err)
	}
	orphans, err := Orphans(context.Background(), admin, live)
	if err != nil {
		t.Fatalf("Orphans: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("the test graph reports orphaned types %s — either the detection is wrong, "+
			"or a type was removed from the schema without deleting its nodes", Describe(orphans))
	}
}

// An empty schema must report nothing rather than the entire graph. Asserted
// against real data because that is what makes the failure catastrophic: the
// complement of "no declared types" is every node there is.
func TestOrphans_NoSchemaReportsNothing(t *testing.T) {
	orphans, err := Orphans(context.Background(), testutil.DGraphAdminURL(), "")
	if err != nil {
		t.Fatalf("Orphans: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("an unreadable schema must report nothing, got %s", Describe(orphans))
	}
}
