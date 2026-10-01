package configitems

import "testing"

// Menu order is DataCenter, Server, KubernetesCluster, NetworkDevice — the
// order the hand-written menu had before derivation made it alphabetical.
func TestNavFor_PinsAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want int
		ok   bool
	}{
		{"pinned", "nav: 20", 20, true},
		{"unannotated sorts last", "facet: dataCenter", NavUnpinned, true},
		{"non-numeric is unpinned and reported", "nav: left", NavUnpinned, false},
		{"multiline block", "editable: name\nnav: 10", 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NavFor(tc.doc)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("NavFor(%q) = (%d,%v), want (%d,%v)", tc.doc, got, ok, tc.want, tc.ok)
			}
		})
	}
}
