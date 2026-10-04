package configitems

import "testing"

// Menu order is DataCenter, Server, KubernetesCluster, NetworkDevice — the
// order the hand-written menu had before derivation made it alphabetical.
func TestMenuWeightFor_PinsAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want int
		ok   bool
	}{
		{"pinned", "menuWeight: 20", 20, true},
		{"unannotated sorts last", "filterBy: dataCenter", MenuWeightUnpinned, true},
		{"non-numeric is unpinned and reported", "menuWeight: left", MenuWeightUnpinned, false},
		{"multiline block", "editable: name\nmenuWeight: 10", 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MenuWeightFor(tc.doc)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("MenuWeightFor(%q) = (%d,%v), want (%d,%v)", tc.doc, got, ok, tc.want, tc.ok)
			}
		})
	}
}
