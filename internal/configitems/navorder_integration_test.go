//go:build integration

package configitems

import (
	"context"
	"testing"
)

// The Config Items menu reads DataCenter, Server, Clusters, Network Devices —
// the hand-written order, restored by `menuWeight:` after derivation made it
// alphabetical (Clusters had moved above Servers).
func TestNavOrder_RootsFollowAnnotatedOrder(t *testing.T) {
	gql, admin := dgraphURLs()
	c := NewDGraphSchemaClient(gql, admin)
	types, iface, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatalf("fetch live schema: %v", err)
	}
	views, err := ResolveViews(types, iface)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var roots []string
	for _, v := range views {
		if v.IsRoot {
			roots = append(roots, v.Label)
		}
	}
	want := []string{"Data Centers", "Servers", "Clusters", "Network Devices"}
	if len(roots) < len(want) {
		t.Fatalf("got %v, want it to start with %v", roots, want)
	}
	for i, w := range want {
		if roots[i] != w {
			t.Fatalf("root[%d] = %q, want %q (full order: %v)", i, roots[i], w, roots)
		}
	}
	t.Logf("menu order: %v", roots)
}
