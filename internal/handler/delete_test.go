package handler

import (
	"testing"

	"github.com/armada/orbital/internal/configitems"
)

// TestDeletableType covers the concrete → cascade-plan mapping.
//
// Regression class: the plans are a hardcoded switch on three names. A page
// that sends the type it actually resolved — which is what the generic renderer
// does — gets "unsupported type" and a 400, and because the PREVIEW uses the
// same switch, the delete modal opens EMPTY rather than saying anything. Silent
// in exactly the way a delete path must not be.
func TestDeletableType(t *testing.T) {
	configitems.SetImplementsLookup(func(typeName string) []string {
		switch typeName {
		case "EksaKubernetesCluster":
			return []string{"ConfigItem", "KubernetesCluster"}
		case "Server", "DataCenter":
			return []string{"ConfigItem"}
		}
		return nil
	})
	t.Cleanup(func() { configitems.SetImplementsLookup(nil) })

	tests := []struct{ in, want string }{
		{"EksaKubernetesCluster", "KubernetesCluster"},
		// A type already naming a supported plan passes through — the
		// hand-written pages sent the interface name and must keep working.
		{"KubernetesCluster", "KubernetesCluster"},
		{"Server", "Server"},
		{"DataCenter", "DataCenter"},
		// Not deletable: unchanged, so the switch still refuses it rather than
		// this function inventing a plan.
		{"Rack", "Rack"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := deletableType(tt.in); got != tt.want {
				t.Errorf("deletableType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
