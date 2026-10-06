//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/testutil"
)

// AC 4 — the detail page's audit roll-up and the change-request scope pin must
// return the IDENTICAL owned subtree for the same root.
//
// They now share ownedSubtreeOrbIDs, but they feed it from DIFFERENT queries:
// the page walks what genericDetailQuery fetched, the pin walks what
// OwnedOrbIDSelection fetched. Sharing the walk is not enough — if the page's
// query stops short of an owned child, the two still disagree, which is the
// state this change exists to end (a server rolled up 3 orbIds on its audit tab
// and ~30 on its scope pin).
//
// Run against the SHIPPED config/views.yaml, not a fixture: the claim is about
// what orbital actually ships, and a fixture that drifted would pass while
// production was wrong.
func TestOwnedSubtree_PageAndScopePinAgree(t *testing.T) {
	ctx := context.Background()
	url := testutil.DGraphURL()
	views := liveViewSet(t, url)
	byType := map[string]configitems.View{}
	for _, vv := range views {
		byType[vv.Type] = vv
	}

	// A seeded fixture, not whatever the cluster happens to hold: a comparison
	// over an empty graph is two empty sets agreeing about nothing. This one
	// carries list dependents (adapters, racks, servers), a grandchild, and an
	// INTERFACE-typed member (DataCenter.kubernetesClusters) — the shape that
	// broke the scope pin's query outright.
	seedCascadeFixture(t)

	for _, tc := range []struct{ rootType, orbID string }{
		{"Server", cascadeServer},
		{"DataCenter", cascadeDC},
		// NetworkDevice is deliberately absent: the shared fixture gives its
		// switch no owned children, and the vacuity guard below would (rightly)
		// fail on a root with nothing to compare.
	} {
		t.Run(tc.rootType, func(t *testing.T) {
			v := byType[tc.rootType]
			q := genericDetailQuery(v,
				func(n string) configitems.View { return byType[n] },
				func(n string) []string { return byType[n].Display },
				func(n string) []configitems.ViewRefColumn { return byType[n].RefColumns },
				func(n string) string { return views.OwnedOrbIDSelection(n) },
				tc.orbID)
			raw, err := runGraphQL(ctx, url, q, "get"+tc.rootType)
			if err != nil {
				t.Fatalf("detail query: %v", err)
			}
			var entity map[string]any
			if err := json.Unmarshal(raw, &entity); err != nil {
				t.Fatalf("decode detail result: %v", err)
			}
			fromPage := ownedSubtreeOrbIDs(views, tc.rootType, tc.orbID, entity)

			fromPin := collectRelatedOrbIDsBatch(ctx, url, views, []string{tc.orbID},
				map[string]approval.EntityRef{tc.orbID: {Type: tc.rootType}})[tc.orbID]

			sort.Strings(fromPage)
			sort.Strings(fromPin)
			if strings.Join(fromPage, "\n") != strings.Join(fromPin, "\n") {
				t.Errorf("the two paths disagree about what %s owns\n  page (%d): %v\n  pin  (%d): %v",
					tc.orbID, len(fromPage), fromPage, len(fromPin), fromPin)
			}
			if len(fromPage) < 2 {
				t.Errorf("%s rolled up only %v — the fixture gives it owned children, so "+
					"this comparison would pass vacuously", tc.orbID, fromPage)
			}
		})
	}
}

// An INTERFACE-typed owned member must not break the selection. `orbId` lives
// on ConfigItem, not on a sub-interface, so a bare selection is rejected at
// VALIDATION — and the scope pin batches every root into one aliased query, so
// one bad member returned an empty subtree for EVERY root, silently.
func TestOwnedOrbIDSelection_SurvivesAnInterfaceTypedMember(t *testing.T) {
	ctx := context.Background()
	url := testutil.DGraphURL()
	views := liveViewSet(t, url)

	sel := views.OwnedOrbIDSelection("DataCenter")
	if !strings.Contains(sel, "kubernetesClusters") {
		t.Fatalf("the fixture for this test is gone — DataCenter no longer owns an "+
			"interface-typed member:\n%s", sel)
	}
	if _, err := runGraphQL(ctx, url,
		"{ getDataCenter(orbId: \"no-such-dc\") { orbId "+sel+" } }", "getDataCenter"); err != nil {
		t.Errorf("DGraph rejected the owned-subtree selection: %v\n%s", err, sel)
	}
}
