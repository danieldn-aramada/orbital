package graphdiff

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The core de-risk (design §12): the two normalizers consume different wire
// shapes (native-export fragments vs merged DQL maps) with DIFFERENT UIDs, and
// must produce identical canonical snapshots. If this passes, the round-trip
// conformance test (export -> preview vs that artifact -> empty diff) will too.
func TestNormalizers_Agree(t *testing.T) {
	// Baseline: DGraph native export — flat per-predicate fragments, uid recurs,
	// dgraph.type is a string per fragment, version is noise.
	baselineJSON := []byte(`[
		{"uid":"0x1","namespace":"0x0","ConfigItem.orbId":"ns:server-A"},
		{"uid":"0x1","namespace":"0x0","Server.hostname":"h1"},
		{"uid":"0x1","namespace":"0x0","ConfigItem.version":5},
		{"uid":"0x1","namespace":"0x0","ConfigItem.updatedBy":"alice"},
		{"uid":"0x1","namespace":"0x0","dgraph.type":"Server"},
		{"uid":"0x1","namespace":"0x0","dgraph.type":"ConfigItem"},
		{"uid":"0x1","namespace":"0x0","Server.rack":[{"uid":"0x2"}]},
		{"uid":"0x2","namespace":"0x0","ConfigItem.orbId":"ns:rack-1"},
		{"uid":"0x2","namespace":"0x0","dgraph.type":"Rack"},
		{"uid":"0x99","namespace":"0x0","dgraph.type":"Namespace"}
	]`)

	// Current: merged DQL maps — dgraph.type is an array, DIFFERENT uids, and a
	// different (noise) version. Same logical graph.
	currentNodes := []map[string]any{
		{
			"uid":                  "0xAA",
			"dgraph.type":          []any{"Server", "ConfigItem"},
			"ConfigItem.orbId":     "ns:server-A",
			"Server.hostname":      "h1",
			"ConfigItem.version":   float64(9),
			"ConfigItem.updatedBy": "bob",
			"Server.rack":          []any{map[string]any{"uid": "0xBB"}},
		},
		{
			"uid":              "0xBB",
			"dgraph.type":      []any{"Rack", "ConfigItem"},
			"ConfigItem.orbId": "ns:rack-1",
		},
		{"uid": "0xCC", "dgraph.type": []any{"Namespace"}, "Namespace.name": "ns"},
	}

	base, err := NormalizeExport(baselineJSON)
	if err != nil {
		t.Fatalf("NormalizeExport: %v", err)
	}
	cur := NormalizeCurrent(currentNodes)

	if !reflect.DeepEqual(base, cur) {
		t.Fatalf("normalizers disagree:\n baseline=%s\n current =%s", dump(base), dump(cur))
	}
	// Namespace node (no orbId) dropped on both sides.
	if _, ok := base["ns:server-A"]; !ok {
		t.Errorf("server node missing")
	}
	if len(base) != 2 {
		t.Errorf("want 2 config-item nodes (Namespace dropped), got %d", len(base))
	}
}

func TestCompare_SelfDiffEmpty(t *testing.T) {
	s := NormalizeCurrent([]map[string]any{
		{"uid": "0x1", "dgraph.type": []any{"Server", "ConfigItem"}, "ConfigItem.orbId": "ns:s1", "Server.hostname": "h1"},
	})
	r := Compare(s, s)
	if r.Summary.Added+r.Summary.Removed+r.Summary.Modified != 0 {
		t.Fatalf("self-diff not empty: %+v", r.Summary)
	}
	if r.Summary.Unchanged != 1 {
		t.Errorf("want 1 unchanged, got %d", r.Summary.Unchanged)
	}
}

func TestCompare_NoiseAndUIDChurnAreUnchanged(t *testing.T) {
	// Same intent, different UIDs (restore), different version/updatedAt (noise)
	// -> must be unchanged.
	baseline, _ := NormalizeExport([]byte(`[
		{"uid":"0x1","ConfigItem.orbId":"ns:s1"},
		{"uid":"0x1","Server.hostname":"h1"},
		{"uid":"0x1","ConfigItem.version":1},
		{"uid":"0x1","dgraph.type":"Server"},
		{"uid":"0x1","Server.rack":[{"uid":"0x2"}]},
		{"uid":"0x2","ConfigItem.orbId":"ns:r1"},
		{"uid":"0x2","dgraph.type":"Rack"}
	]`))
	current := NormalizeCurrent([]map[string]any{
		{"uid": "0xFF", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:s1", "Server.hostname": "h1", "ConfigItem.version": float64(7), "Server.rack": []any{map[string]any{"uid": "0xEE"}}},
		{"uid": "0xEE", "dgraph.type": []any{"Rack"}, "ConfigItem.orbId": "ns:r1"},
	})
	r := Compare(baseline, current)
	if r.Summary.Modified != 0 || r.Summary.Added != 0 || r.Summary.Removed != 0 {
		t.Fatalf("expected all unchanged despite UID churn + noise, got %+v; changes=%s", r.Summary, dump(r.Changes))
	}
}

func TestCompare_DetectsAddRemoveModify(t *testing.T) {
	baseline := NormalizeCurrent([]map[string]any{
		{"uid": "0x1", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:s1", "Server.hostname": "old"},
		{"uid": "0x2", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:s2", "Server.hostname": "gone"},
	})
	current := NormalizeCurrent([]map[string]any{
		{"uid": "0x1", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:s1", "Server.hostname": "new"},
		{"uid": "0x3", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:s3", "Server.hostname": "fresh"},
	})
	r := Compare(baseline, current)
	if r.Summary.Modified != 1 || r.Summary.Added != 1 || r.Summary.Removed != 1 {
		t.Fatalf("want 1/1/1 modify/add/remove, got %+v", r.Summary)
	}
	// Find the modified change and check the field diff.
	var mod *Change
	for _, c := range r.Changes {
		if c.Change == "modified" {
			mod = c
		}
	}
	if mod == nil || len(mod.Fields) != 1 || mod.Fields[0].Field != "Server.hostname" ||
		mod.Fields[0].Before != "old" || mod.Fields[0].After != "new" {
		t.Fatalf("bad modified change: %+v", mod)
	}
}

func TestContentHash_StableAndSensitive(t *testing.T) {
	a := NormalizeCurrent([]map[string]any{{"uid": "0x1", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:s1", "Server.hostname": "h1", "ConfigItem.version": float64(1)}})
	b := NormalizeCurrent([]map[string]any{{"uid": "0x9", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:s1", "Server.hostname": "h1", "ConfigItem.version": float64(2)}})
	if a.ContentHash() != b.ContentHash() {
		t.Errorf("hash should ignore UID + noise version")
	}
	c := NormalizeCurrent([]map[string]any{{"uid": "0x1", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:s1", "Server.hostname": "CHANGED"}})
	if a.ContentHash() == c.ContentHash() {
		t.Errorf("hash should change when a real field changes")
	}
}

// Validates the baseline normalizer against a REAL DGraph native export.
func TestNormalizeExport_RealSample(t *testing.T) {
	matches, _ := filepath.Glob("../../subgraph-exports/scratch/*/*/g01.json.gz")
	if len(matches) == 0 {
		t.Skip("no sample export present")
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	plain, _ := io.ReadAll(gz)
	snap, err := NormalizeExport(plain)
	if err != nil {
		t.Fatalf("NormalizeExport: %v", err)
	}
	if len(snap) == 0 {
		t.Fatal("no nodes normalized from real export")
	}
	for orb, n := range snap {
		if orb == "" || n.OrbID != orb {
			t.Fatalf("node keyed wrong: %q -> %+v", orb, n)
		}
		for pred, targets := range n.Edges {
			for _, tgt := range targets {
				if tgt == "" {
					t.Fatalf("empty edge target on %s.%s", orb, pred)
				}
			}
		}
	}
	t.Logf("normalized %d config-item nodes from %s", len(snap), filepath.Base(matches[0]))
}

// Regression: native export encodes booleans as strings ("false"), live DQL as
// real bools (false). They must reconcile to unchanged — the bug found during
// local validation that produced 174 false-positive "modified" nodes.
func TestNormalize_BooleanEncodingReconciled(t *testing.T) {
	base, err := NormalizeExport([]byte(`[
		{"uid":"0x1","ConfigItem.orbId":"ns:i1"},
		{"uid":"0x1","dgraph.type":"IdracSettings"},
		{"uid":"0x1","IdracSettings.sshEnabled":"false"},
		{"uid":"0x1","IdracSettings.dhcpEnabled":"true"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	cur := NormalizeCurrent([]map[string]any{
		{"uid": "0x9", "dgraph.type": []any{"IdracSettings"}, "ConfigItem.orbId": "ns:i1", "IdracSettings.sshEnabled": false, "IdracSettings.dhcpEnabled": true},
	})
	r := Compare(base, cur)
	if r.Summary.Modified != 0 {
		t.Fatalf("bool string-vs-native must be unchanged, got modified=%d: %s", r.Summary.Modified, dump(r.Changes))
	}
}

// The response must stay FLAT: one row per changed entity, so len(Changes)
// equals the summary counts. An unchanged node is never emitted as a row.
// Catches a regression back to a nested/containerised shape.
func TestCompare_FlatOneRowPerChangedEntity(t *testing.T) {
	idracBase := map[string]any{"uid": "0x2", "dgraph.type": []any{"IdracSettings"}, "ConfigItem.orbId": "ns:server-A-idrac", "IdracSettings.sshEnabled": true, "IdracSettings.server": []any{map[string]any{"uid": "0x1"}}}
	idracCur := map[string]any{"uid": "0x2", "dgraph.type": []any{"IdracSettings"}, "ConfigItem.orbId": "ns:server-A-idrac", "IdracSettings.sshEnabled": false, "IdracSettings.server": []any{map[string]any{"uid": "0x1"}}}
	server := map[string]any{"uid": "0x1", "dgraph.type": []any{"Server"}, "ConfigItem.orbId": "ns:server-A", "Server.hostname": "h1"}

	baseline := NormalizeCurrent([]map[string]any{server, idracBase})
	current := NormalizeCurrent([]map[string]any{server, idracCur})
	res := Compare(baseline, current)

	// Exactly one row — the changed iDRAC. The unchanged Server is NOT a row.
	if len(res.Changes) != 1 {
		t.Fatalf("want 1 flat row, got %d: %s", len(res.Changes), dump(res.Changes))
	}
	row := res.Changes[0]
	if row.OrbID != "ns:server-A-idrac" || row.Change != "modified" {
		t.Fatalf("row should be the modified iDRAC, got %s/%s", row.OrbID, row.Change)
	}
	if got := res.Summary.Added + res.Summary.Removed + res.Summary.Modified; got != len(res.Changes) {
		t.Errorf("len(Changes)=%d must equal summary changes=%d", len(res.Changes), got)
	}
}

func dump(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
