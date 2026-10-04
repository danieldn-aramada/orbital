//go:build integration

package approval_test

import (
	"log"
	"os"
	"testing"

	"github.com/armada/orbital/internal/testutil"
)

// TestMain makes this package self-sufficient on the test cluster — see the
// same file in internal/dgraphschema for why these packages needed one.
//
// EnsureSchema, not ResetDGraph: these tests read the deployed schema to
// resolve entity types and validate changesets against it. They create their
// own fixtures and clean up after themselves, so wiping the graph here would
// only cost time.
func TestMain(m *testing.M) {
	if err := testutil.EnsureSchema(testutil.DGraphAdminURL(), testutil.SchemaPath()); err != nil {
		log.Fatalf("approval integration setup: %v", err)
	}
	os.Exit(m.Run())
}
