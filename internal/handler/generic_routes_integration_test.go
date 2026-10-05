//go:build integration

package handler

import (
	"context"
	"log/slog"
	"testing"

	"github.com/armada/orbital/internal/testutil"
)

// staticTopLevelPaths are orbital's own pages, which are registered as literal
// routes. Echo resolves a static segment before a parameterised one, so these
// win over /{slug} today — but a derived slug that EQUALS one of them would
// make that type permanently unreachable, silently, and only for that type.
//
// Kept as an explicit list rather than read from the router: the point is to
// state which names are spoken for, so adding a page called "Backups" fails
// here instead of shadowing a hypothetical Backup ConfigItem later.
var staticTopLevelPaths = []string{
	"api", "graphql", "healthz", "metrics", "swagger", "static", "login", "logout",
	"inventory", "datacenters", "network",
	// "network" is a REDIRECT to /network-devices, not a page — but it is still
	// a literal route, so a type deriving that slug would still be shadowed.
	"backups", "divergence-reports", "audit-log", "change-requests",
	"approval-policies", "restore", "schema", "export", "publish-history", "users", "views",
}

// knownShadowed are slugs a bespoke page currently owns. They are NOT bugs:
// /servers is served by the hand-written Servers page today and moves onto the
// generic renderer in the migration (items 10-16). Listing them here means a
// NEW collision fails this test, while the expected ones stay visible — and the
// list shrinks to empty as migration lands.
// Empty: every ConfigItem page has migrated. A NEW collision now fails this
// test with nothing to excuse it, which is the state this list was counting
// down to.
var knownShadowed = map[string]string{}

// `clusters` and `servers` came OFF this list when their bespoke pages were
// deleted:
// /clusters is now the KubernetesCluster interface's own generic view, so the
// slug is the view's, not a static route's. /datacenters stays — it is still a
// literal route, redirecting to /data-centers.

// Item 17: no derived slug may collide with a static page, except knowingly.
//
// Echo resolves a static segment before a parameterised one, so a colliding
// slug makes that ONE type permanently unreachable — silently, and without
// affecting any other type.
func TestGenericRoutes_DoNotSwallowStaticPages(t *testing.T) {
	sf := NewSharedFieldSource(testutil.DGraphURL(), ViewsSource{Path: testutil.ViewsPath()}, slog.Default())
	views, err := sf.Views(context.Background())
	if err != nil {
		t.Fatalf("resolve views: %v", err)
	}
	if len(views) == 0 {
		t.Fatal("no views resolved — this test would be vacuous")
	}

	static := make(map[string]bool, len(staticTopLevelPaths))
	for _, p := range staticTopLevelPaths {
		static[p] = true
	}
	seen := map[string]bool{}
	for _, v := range views {
		if !static[v.Slug] {
			continue
		}
		if owner, known := knownShadowed[v.Slug]; known && owner == v.Type {
			seen[v.Slug] = true
			continue
		}
		t.Errorf("%s derives slug %q, which is already a static page — /%s would never reach the "+
			"generic renderer, and only this type would be affected.\n"+
			"  Fix: give the type a `\"\"\"slug: ...\"\"\"` annotation, or rename the static page.",
			v.Type, v.Slug, v.Slug)
	}

	// A stale entry means migration finished and nobody trimmed the list, which
	// would hide the next real collision behind an exemption nothing needs.
	for slug := range knownShadowed {
		if !seen[slug] {
			t.Errorf("knownShadowed lists %q but nothing derives it any more — remove the stale entry", slug)
		}
	}
}

// The derived slugs for the other two bespoke pages do NOT collide:
// /datacenters and /network are the static paths, while derivation gives
// data-centers and network-devices. That difference IS the URL move the
// migration makes deliberate, with redirects.
func TestGenericRoutes_TwoBespokePagesMoveRatherThanCollide(t *testing.T) {
	sf := NewSharedFieldSource(testutil.DGraphURL(), ViewsSource{Path: testutil.ViewsPath()}, slog.Default())
	views, err := sf.Views(context.Background())
	if err != nil {
		t.Fatalf("resolve views: %v", err)
	}
	bySlug := map[string]string{}
	for _, v := range views {
		bySlug[v.Slug] = v.Type
	}
	for slug, want := range map[string]string{
		"data-centers":    "DataCenter",
		"network-devices": "NetworkDevice",
	} {
		if got := bySlug[slug]; got != want {
			t.Errorf("slug %q maps to %q, want %q", slug, got, want)
		}
	}
	for _, old := range []string{"datacenters", "network"} {
		if typeName, exists := bySlug[old]; exists {
			t.Errorf("%q is a STATIC path and should not also be a derived slug (got %s)", old, typeName)
		}
	}
}
