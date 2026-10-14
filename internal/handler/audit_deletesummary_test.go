package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// A cascade delete stores `before`/`input`/`result` and NO `variables`, so the
// audit column that reads variables rendered "—" for it. The most consequential
// row in the log was the least informative, and a reader could not tell a
// delete from an event with nothing to say.
//
// The regression guarded here is the summary going back to reading only
// `variables`: every assertion below is about data the API already carried.
func TestBuildVarSummary_CascadeDelete(t *testing.T) {
	raw := json.RawMessage(`{
		"input":  {"orbId": "colo-aep:colo-aep-galleon"},
		"before": {"name": "colo-aep-galleon", "type": "DataCenter", "orbId": "colo-aep:colo-aep-galleon"},
		"result": {"totalDeleted": 4, "breakdown": [
			{"label": "Data Center", "count": 1},
			{"label": "Racks", "items": ["A.OF.C.09", "A.OF.C.10"], "extra": 1}
		]}
	}`)
	got := string(buildVarSummary(raw))

	for _, want := range []string{"colo-aep-galleon", "DataCenter", "4 deleted", "1&nbsp;Data Center", "3&nbsp;Racks"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q\ngot: %s", want, got)
		}
	}
	// `items` + `extra` is the count — a group that names two and hides one is
	// three, not two.
	if strings.Contains(got, "2&nbsp;Racks") {
		t.Errorf("named items were counted without `extra`\ngot: %s", got)
	}
}

// The negative: an ordinary mutation must still render its variables, and must
// NOT be mistaken for a delete. `result` is the discriminator, so an event
// without one falls through untouched.
func TestBuildVarSummary_NonDeleteIsUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"variables": {"orbId": "colo:server-X", "set": {"model": "R650"}}}`)
	got := string(buildVarSummary(raw))
	if !strings.Contains(got, "orbId") {
		t.Errorf("an ordinary mutation lost its variables\ngot: %s", got)
	}
	if strings.Contains(got, "deleted") {
		t.Errorf("an ordinary mutation was rendered as a delete\ngot: %s", got)
	}
}

// A delete whose entity had no name still says what happened — falling back to
// "—" would put us back where we started for exactly the rows least likely to
// be self-explanatory.
func TestBuildVarSummary_DeleteWithoutName(t *testing.T) {
	raw := json.RawMessage(`{"before": {"type": "Rack"}, "result": {"totalDeleted": 1}}`)
	got := string(buildVarSummary(raw))
	if !strings.Contains(got, "1 deleted") {
		t.Errorf("a nameless delete must still report the count\ngot: %s", got)
	}
}

// An entity NAME is caller-controlled and lands in HTML.
func TestBuildVarSummary_EscapesName(t *testing.T) {
	raw := json.RawMessage(`{"before": {"name": "<img src=x onerror=alert(1)>", "type": "Rack"},
		"result": {"totalDeleted": 1}}`)
	got := string(buildVarSummary(raw))
	if strings.Contains(got, "<img") {
		t.Errorf("entity name was not escaped\ngot: %s", got)
	}
}
