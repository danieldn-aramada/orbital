//go:build integration

package dgraphschema

import (
	"context"
	"strings"
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
	// Without this the test passes on a graph that does not exist.
	//
	// Verified 2026-09-29 by dropping :8083 entirely: `Active` returns "" with
	// no error, `Orphans("")` correctly reports nothing, and the assertion
	// below is satisfied. A clean-graph test that cannot tell "clean" from
	// "empty" reports green exactly when the cluster is least prepared, which
	// is the opposite of what it exists for.
	if strings.TrimSpace(live) == "" {
		t.Fatal("no GraphQL schema is deployed on the test cluster, so this proves nothing — " +
			"TestMain should have applied it via testutil.EnsureSchema")
	}
	if !strings.Contains(live, "type Server") {
		t.Fatalf("the deployed schema does not look like orbital's (no `type Server`), so a clean "+
			"result says nothing; got %d bytes", len(live))
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
