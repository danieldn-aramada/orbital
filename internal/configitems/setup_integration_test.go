//go:build integration

package configitems

import (
	"log"

	"github.com/armada/orbital/internal/testutil"
)

// ensureIntegrationSchema makes this package self-sufficient on the test
// cluster — see internal/dgraphschema/main_integration_test.go for why.
//
// Split across build tags rather than living in TestMain directly: this
// package's TestMain is shared with the unit run, which has no services at
// all, so a DGraph call there would break `make test-unit`.
//
// EnsureSchema, not ResetDGraph: the derived-field tests only introspect.
func ensureIntegrationSchema() {
	if err := testutil.EnsureSchema(testutil.DGraphAdminURL(), testutil.SchemaPath()); err != nil {
		log.Fatalf("configitems integration setup: %v", err)
	}
}
