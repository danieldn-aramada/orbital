package handler

import (
	"reflect"
	"testing"
)

// TestStripDGraphIDs pins the before-state UID scrub: every "id" key removed at
// any depth (map, nested map, array of maps), everything else kept, and — the
// race-safety property — the caller's map left untouched (writeEvent runs in a
// goroutine, so an in-place strip could race the request path).
func TestStripDGraphIDs(t *testing.T) {
	in := map[string]any{
		"id":            "0xc28",
		"orbId":         "colo:dev-main-velero-backup",
		"retentionDays": 7,
		"nested":        map[string]any{"id": "0xd99", "orbId": "colo:child"},
		"list":          []any{map[string]any{"id": "0xe11", "name": "a"}},
	}
	got := stripDGraphIDs(in)
	want := map[string]any{
		"orbId":         "colo:dev-main-velero-backup",
		"retentionDays": 7,
		"nested":        map[string]any{"orbId": "colo:child"},
		"list":          []any{map[string]any{"name": "a"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stripDGraphIDs:\n got  %#v\n want %#v", got, want)
	}
	// Copy-based: the caller's map (and nested maps) must still have their ids.
	if _, ok := in["id"]; !ok {
		t.Error("input map was mutated in place — strip must be copy-based")
	}
	if _, ok := in["nested"].(map[string]any)["id"]; !ok {
		t.Error("nested input map was mutated in place — strip must be copy-based")
	}
}

func TestStripDGraphIDs_Nil(t *testing.T) {
	if got := stripDGraphIDs(nil); got != nil {
		t.Errorf("stripDGraphIDs(nil) = %v, want nil", got)
	}
}
