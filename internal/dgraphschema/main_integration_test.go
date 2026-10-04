//go:build integration

package dgraphschema

import (
	"log"
	"os"
	"testing"

	"github.com/armada/orbital/internal/testutil"
)

// TestMain makes this package self-sufficient on the test cluster.
//
// It used to free-ride: every test here queries :8083, but only `handler` and
// `divergenceingest` applied a schema to it, so these passed when one of those
// happened to run first and failed on a cold cluster. `go test` promises
// nothing about package order and runs packages in parallel unless -p 1, so
// that ordering was luck.
//
// EnsureSchema rather than ResetDGraph deliberately: this package only READS —
// it introspects the deployed schema and counts nodes by type — so dropping the
// graph would cost time and delete whatever a neighbouring package put there.
func TestMain(m *testing.M) {
	if err := testutil.EnsureSchema(testutil.DGraphAdminURL(), testutil.SchemaPath()); err != nil {
		log.Fatalf("dgraphschema integration setup: %v", err)
	}
	os.Exit(m.Run())
}
