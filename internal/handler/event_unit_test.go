package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── event_category ────────────────────────────────────────────────────────────

// TestEventItem_HasEventCategoryField asserts that the eventItem response struct
// carries the EventCategory field and serialises it under the expected JSON key.
func TestEventItem_HasEventCategoryField(t *testing.T) {
	item := eventItem{
		ID:            "test-id",
		Actor:         "user@example.com",
		EventCategory: "management",
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cat, ok := m["eventCategory"]
	if !ok {
		t.Fatal("eventCategory key missing from JSON output")
	}
	if cat != "management" {
		t.Errorf("eventCategory: got %q, want %q", cat, "management")
	}
}

// TestEventItem_DataCategoryDefault asserts that a zero-value eventItem without
// an explicit EventCategory serialises to "data" when the field is set.
func TestEventItem_DataCategoryValue(t *testing.T) {
	item := eventItem{
		ID:            "test-id",
		Actor:         "user@example.com",
		EventCategory: "data",
	}
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["eventCategory"] != "data" {
		t.Errorf("eventCategory: got %q, want %q", m["eventCategory"], "data")
	}
}

// ── buildVarSummary ──────────────────────────────────────────────────────────

func TestBuildVarSummary_NilInput(t *testing.T) {
	got := buildVarSummary(nil)
	if got != "—" {
		t.Errorf("nil input: got %q, want —", got)
	}
}

func TestBuildVarSummary_EmptyVars(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"variables": map[string]any{}})
	got := buildVarSummary(json.RawMessage(raw))
	if got != "—" {
		t.Errorf("empty vars: got %q, want —", got)
	}
}

func TestBuildVarSummary_SkipsSystemFields(t *testing.T) {
	// updatedBy, updatedAt, id are in skipVarsSet — must not appear in output.
	raw, _ := json.Marshal(map[string]any{
		"variables": map[string]any{
			"updatedBy": "admin",
			"updatedAt": "2026-06-01",
			"id":        "0x1",
		},
	})
	got := buildVarSummary(json.RawMessage(raw))
	if got != "—" {
		t.Errorf("system fields only: got %q, want —", got)
	}
}

func TestBuildVarSummary_WithUserFields(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"variables": map[string]any{
			"name":      "alpha-dc",
			"updatedBy": "admin", // should be skipped
		},
	})
	got := string(buildVarSummary(json.RawMessage(raw)))
	if !strings.Contains(got, "name") {
		t.Errorf("expected 'name' in output, got: %s", got)
	}
	if strings.Contains(got, "updatedBy") {
		t.Errorf("updatedBy should be skipped, but appeared in: %s", got)
	}
	if !strings.Contains(got, "alpha-dc") {
		t.Errorf("expected value 'alpha-dc' in output, got: %s", got)
	}
}

// ── buildDiffHTML ────────────────────────────────────────────────────────────

func TestBuildDiffHTML_UnknownResourceTypeReturnsEmpty(t *testing.T) {
	got := buildDiffHTML(
		map[string]any{"foo": "bar"},
		map[string]any{"foo": "baz"},
		"UnknownType",
	)
	if got != "" {
		t.Errorf("unknown resource type: expected empty, got %q", got)
	}
}

func TestBuildDiffHTML_NoChange(t *testing.T) {
	before := map[string]any{"name": "alpha"}
	variables := map[string]any{"name": "alpha"}
	got := buildDiffHTML(before, variables, "DataCenter")
	if got != "" {
		t.Errorf("no change: expected empty HTML, got %q", got)
	}
}

func TestBuildDiffHTML_FieldChanged(t *testing.T) {
	before := map[string]any{"name": "alpha"}
	variables := map[string]any{"name": "beta"}
	got := string(buildDiffHTML(before, variables, "DataCenter"))
	if got == "" {
		t.Fatal("changed field: expected non-empty HTML, got empty")
	}
	if !strings.Contains(got, "-alpha") {
		t.Errorf("expected removed line '-alpha' in diff, got: %s", got)
	}
	if !strings.Contains(got, "+beta") {
		t.Errorf("expected added line '+beta' in diff, got: %s", got)
	}
}

// ── lineDiff ─────────────────────────────────────────────────────────────────

func TestLineDiff_Identical(t *testing.T) {
	lines := []string{"a", "b", "c"}
	got := lineDiff(lines, lines)
	for _, l := range got {
		if l[0] == '+' || l[0] == '-' {
			t.Errorf("identical inputs should have no +/- lines, got: %v", got)
		}
	}
}

func TestLineDiff_AddedLine(t *testing.T) {
	before := []string{"a", "b"}
	after := []string{"a", "b", "c"}
	got := lineDiff(before, after)
	added := 0
	for _, l := range got {
		if strings.HasPrefix(l, "+c") {
			added++
		}
	}
	if added != 1 {
		t.Errorf("expected one '+c' line, got: %v", got)
	}
}

func TestLineDiff_RemovedLine(t *testing.T) {
	before := []string{"a", "b", "c"}
	after := []string{"a", "c"}
	got := lineDiff(before, after)
	removed := 0
	for _, l := range got {
		if strings.HasPrefix(l, "-b") {
			removed++
		}
	}
	if removed != 1 {
		t.Errorf("expected one '-b' line, got: %v", got)
	}
}

// ── valStr ────────────────────────────────────────────────────────────────────

func TestValStr_NilWithStringRef(t *testing.T) {
	if got := valStr(nil, "somestring"); got != "" {
		t.Errorf("nil with string ref: got %q, want empty", got)
	}
}

func TestValStr_NilWithNumericRef(t *testing.T) {
	if got := valStr(nil, float64(1)); got != "0" {
		t.Errorf("nil with float64 ref: got %q, want 0", got)
	}
}

func TestValStr_NilWithBoolRef(t *testing.T) {
	if got := valStr(nil, false); got != "false" {
		t.Errorf("nil with bool ref: got %q, want false", got)
	}
}

func TestValStr_NonNil(t *testing.T) {
	if got := valStr("hello", ""); got != "hello" {
		t.Errorf("non-nil: got %q, want hello", got)
	}
}

func TestValStr_MapMarshalledToJSON(t *testing.T) {
	m := map[string]any{"key": "value"}
	got := valStr(m, m)
	if got != `{"key":"value"}` {
		t.Errorf("map: got %q, want {\"key\":\"value\"}", got)
	}
}

func TestValStr_JSONStringNormalized(t *testing.T) {
	// Raw JSON string from DGraph before-snapshot should normalize to same
	// form as a parsed map from mutation variables.
	jsonStr := `{"key":"value"}`
	parsedMap := map[string]any{"key": "value"}
	if valStr(jsonStr, jsonStr) != valStr(parsedMap, parsedMap) {
		t.Errorf("JSON string and equivalent map should normalize to same value")
	}
}

func TestBuildDiffHTML_UnchangedJSONFieldSuppressed(t *testing.T) {
	// assetDataV2 arrives as a raw JSON string in the before-snapshot (from
	// DGraph) but as a parsed map in mutation variables. They are logically
	// equal — the diff must not show assetDataV2 when only name changed.
	assetJSON := `{"id1":{"cfTunnelID":"abc","kvmURL":"https://example.com"}}`
	var assetMap any
	_ = json.Unmarshal([]byte(assetJSON), &assetMap)

	before := map[string]any{"name": "old-name", "assetDataV2": assetJSON}
	variables := map[string]any{"name": "new-name", "assetDataV2": assetMap}

	got := string(buildDiffHTML(before, variables, "DataCenter"))
	if strings.Contains(got, "assetDataV2") {
		t.Errorf("unchanged assetDataV2 should not appear in diff, got: %s", got)
	}
	if !strings.Contains(got, "-old-name") || !strings.Contains(got, "+new-name") {
		t.Errorf("name change should appear in diff, got: %s", got)
	}
}
