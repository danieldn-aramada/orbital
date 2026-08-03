package orbmetrics

import (
	"testing"
	"time"
)

// TestHops guards the anchor→duration contract: correct per-hop deltas when all
// anchors are present, and a zero (dropped-by-ObserveHop) duration for any hop
// whose endpoint is missing — so an absent timestamp never becomes a bogus
// observation.
func TestHops(t *testing.T) {
	base := time.Date(2026, 8, 3, 20, 0, 0, 0, time.UTC)
	build := base
	push := base.Add(8 * time.Second)
	imp := base.Add(12 * time.Second)
	disp := base.Add(13 * time.Second)

	t.Run("all present", func(t *testing.T) {
		h := Hops(build, push, imp, disp)
		if h[HopPublishToEdge] != 8*time.Second {
			t.Errorf("publish_to_edge = %v, want 8s", h[HopPublishToEdge])
		}
		if h[HopEdgeToImport] != 4*time.Second {
			t.Errorf("edge_to_import = %v, want 4s", h[HopEdgeToImport])
		}
		if h[HopImportToDispatch] != 1*time.Second {
			t.Errorf("import_to_dispatch = %v, want 1s", h[HopImportToDispatch])
		}
	})

	t.Run("missing build zeros only publish_to_edge", func(t *testing.T) {
		h := Hops(time.Time{}, push, imp, disp)
		if h[HopPublishToEdge] != 0 {
			t.Errorf("publish_to_edge should be 0, got %v", h[HopPublishToEdge])
		}
		if h[HopEdgeToImport] != 4*time.Second {
			t.Errorf("edge_to_import should survive, got %v", h[HopEdgeToImport])
		}
	})

	t.Run("missing push zeros both touching hops", func(t *testing.T) {
		h := Hops(build, time.Time{}, imp, disp)
		if h[HopPublishToEdge] != 0 || h[HopEdgeToImport] != 0 {
			t.Errorf("both hops touching push should be 0, got %+v", h)
		}
		if h[HopImportToDispatch] != 1*time.Second {
			t.Errorf("import_to_dispatch should survive, got %v", h[HopImportToDispatch])
		}
	})

	t.Run("missing dispatch zeros import_to_dispatch", func(t *testing.T) {
		h := Hops(build, push, imp, time.Time{})
		if h[HopImportToDispatch] != 0 {
			t.Errorf("import_to_dispatch should be 0, got %v", h[HopImportToDispatch])
		}
	})
}
