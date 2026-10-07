//go:build integration

package configitems

import (
	"context"
	"testing"
)

// Nav membership IS membership of `pages:`; menuWeight orders it.
//
// The top level of `pages:` IS the menu — four entries, in menuWeight order.
// `menuWeight` orders and nothing else; conflating membership into it reversed a
// settled decision once already.
func TestNavOrder_IsPageMembership(t *testing.T) {
	gql, admin := dgraphURLs()
	types, iface, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	cfg, _, _, err := LoadViewConfig(viewsConfigPath(t), "")
	if err != nil {
		t.Fatal(err)
	}
	validated, _ := cfg.Validate(types, "")
	views, err := ResolveViewsFromConfig(types, iface, validated, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var nav []string
	for _, v := range views {
		if v.Slug != "" {
			nav = append(nav, v.Label)
		}
	}
	want := []string{"Data Centers", "Servers", "Clusters", "Network Devices"}
	if len(nav) != len(want) {
		t.Fatalf("menu = %v, want exactly %v — a type with no menuWeight is not in the menu", nav, want)
	}
	for i, w := range want {
		if nav[i] != w {
			t.Fatalf("menu[%d] = %q, want %q (full order: %v)", i, nav[i], w, nav)
		}
	}

	// A type with no PAGE has no menu entry AND no URL — that is what makes a
	// row of it a dead row rather than a link to nothing. Both halves matter:
	// the menu stays four items, and /idrac-settings/<id> stops existing.
	for _, v := range views {
		if v.Type != "IdracSettings" {
			continue
		}
		if v.Slug != "" {
			t.Errorf("IdracSettings has no page, so it must have no slug and no menu "+
				"entry — the two are one fact; got %q", v.Slug)
		}
		// It still RENDERS — as rows on the Server page — so it keeps its
		// fields and labels.
		if len(v.Display) == 0 {
			t.Error("a pageless type still renders as rows and must keep its display fields")
		}
	}
}
