package configitems

import (
	"os"
	"testing"
)

// TestMain exists only to run the integration setup when the integration tag is
// on. It used to wire a package-level interface-lookup hook that containment
// needed; containment moved to the views config and the hook is gone with it,
// so unit tests need no global state at all.
func TestMain(m *testing.M) {
	ensureIntegrationSchema()
	os.Exit(m.Run())
}
