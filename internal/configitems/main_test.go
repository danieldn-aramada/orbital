package configitems

import (
	"os"
	"testing"
)

// TestMain wires the schema-backed interface lookup for the whole package.
//
// Interface-typed ownership used to read a hand-maintained `Implements` list on
// each Type. That list now comes from the deployed schema, reached through a
// package-level hook that production sets at startup. Unit tests have no
// resolver, so without this the hook is nil, every implementsInterface call
// answers false, and the backup sub-kinds silently stop resolving their owner —
// which is exactly what TestOwnedChildren_DriftFixes and
// TestBuildEditTargets_Cluster caught.
//
// The fixture is the one interface relationship the registry actually uses.
func TestMain(m *testing.M) {
	SetImplementsLookup(func(typeName string) []string {
		switch typeName {
		case "EksaKubernetesCluster":
			return []string{"KubernetesCluster", "ConfigItem"}
		default:
			return []string{"ConfigItem"}
		}
	})
	os.Exit(m.Run())
}
