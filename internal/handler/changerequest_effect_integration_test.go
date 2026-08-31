//go:build integration

package handler

import (
	"encoding/json"
	"testing"

	"github.com/armada/orbital/internal/approval"
)

// A change request's `effect` must describe what it would DO, not what it says.
//
// The API accepts a complete end-state on purpose, so a reconcile-style client
// can assert one. That makes payload width a bad proxy for how much a reviewer
// is approving: a request naming six fields where one differs is a one-field
// change, and a queue saying "6 fields" sends someone to read a diff to find
// out it was nothing.
//
// Read back through ListChangeRequests — the endpoint an operator and every
// other client actually use. Asserting on the ent row would prove the column
// was written and not that anyone can see it.

func effectFromList(t *testing.T, f *crFixture, id string) changeEffect {
	t.Helper()
	rec := callList(t, f.crh, "")
	var out changeRequestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, it := range out.Items {
		if it.ID == id {
			return it.Effect
		}
	}
	t.Fatalf("change request %s not in the list response", id)
	return changeEffect{}
}

// seedModel gives the fixture server a second field with a known value, so a
// changeset can assert two fields with only one of them differing. Written
// straight to DGraph like every other arrange step in this package.
func seedModel(t *testing.T, value string) {
	t.Helper()
	crGQL(t, `mutation($input:[AddServerInput!]!){ addServer(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			// AddServerInput requires namespace, version and dataCenter even on an
			// upsert, so they are re-stated at their fixture values. `model` is the
			// only field this actually changes.
			"orbId": crServerA, "namespace": crNS, "version": 1,
			"dataCenter": map[string]any{"orbId": crDC},
			"model":      value,
		}}})
}

func TestCREffect_WidePayloadReportsItsNarrowEffect(t *testing.T) {
	f := newCRFixture(t)
	seedModel(t, "PowerEdge R650")

	// Exactly the shape a reconcile client produces: every field asserted, one
	// of them different. `hostname` is "a-original" in the fixture; `model` is
	// re-asserted at the value it already has.
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{
			"hostname": "renamed-by-effect-test",
			"model":    "PowerEdge R650",
		},
	})

	got := effectFromList(t, f, crHumanID(cr))
	if got.Fields != 1 {
		t.Errorf("effect.fields = %d, want 1 — the payload names 2 fields but only hostname differs", got.Fields)
	}
	if got.Field != "hostname" {
		t.Errorf("effect.field = %q, want hostname", got.Field)
	}
	if got.Value != "renamed-by-effect-test" {
		t.Errorf("effect.value = %v, want the proposed hostname", got.Value)
	}
	// Only an effect-derived summary can know what a field WAS — the payload
	// says what it becomes. This is the assertion that fails if the fallback
	// ever silently takes over.
	if got.Before != "a-original" {
		t.Errorf("effect.before = %v, want a-original", got.Before)
	}
	if got.OrbID != crServerA {
		t.Errorf("effect.orbId = %q, want %q", got.OrbID, crServerA)
	}
}

// The negative: a request whose payload changes nothing must not claim it does.
// Without this, "counts the effect" would be satisfied by a summary that merely
// counted differently — this is the case where payload and effect diverge most.
func TestCREffect_NoOpPayloadReportsNothing(t *testing.T) {
	f := newCRFixture(t)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "a-original"}, // already the current value
	})

	got := effectFromList(t, f, crHumanID(cr))
	if got.Fields != 0 {
		t.Errorf("effect.fields = %d, want 0 — the payload names a field but changes nothing", got.Fields)
	}
	if got.Field != "" {
		t.Errorf("effect.field = %q, want empty when nothing changes", got.Field)
	}
}

// A row written before base_effect existed — or one whose effect could not be
// computed — must still report something true rather than zeros. Simulated by
// clearing the stored effect, which is exactly the state those rows are in.
func TestCREffect_FallsBackToScopeCountsWhenAbsent(t *testing.T) {
	ctx := t.Context()
	f := newCRFixture(t)

	seedModel(t, "PowerEdge R650")
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "renamed", "model": "PowerEdge R650"},
	})
	if _, err := cr.Update().ClearBaseEffect().Save(ctx); err != nil {
		t.Fatalf("clear base_effect: %v", err)
	}

	got := effectFromList(t, f, crHumanID(cr))
	// Two payload fields, so the fallback says 2 where the effect said 1. That
	// is the fallback working: it reports the scope, honestly, rather than the
	// zeros an unpopulated column would give.
	if got.Fields != 2 {
		t.Errorf("effect.fields = %d, want 2 from the payload fallback", got.Fields)
	}
	if got.Before != nil {
		t.Errorf("effect.before = %v, want absent — a payload cannot know a prior value", got.Before)
	}
}

// Amending re-anchors the base, so the delta captured against the old base must
// go with it. A stale effect would describe changes the request no longer
// proposes — worse than no effect at all, because it looks authoritative.
func TestCREffect_AmendRecomputesTheEffect(t *testing.T) {
	ctx := t.Context()
	f := newCRFixture(t)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "first-name"},
	})
	if got := effectFromList(t, f, crHumanID(cr)); got.Value != "first-name" {
		t.Fatalf("effect.value = %v before amend, want first-name", got.Value)
	}

	amended, problems, err := f.crh.Amend(ctx, cr.ID, author, "dev", nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": "second-name"},
		}}})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if len(problems) > 0 {
		t.Fatalf("unexpected validation problems: %v", problems)
	}

	got := effectFromList(t, f, crHumanID(amended))
	if got.Value != "second-name" {
		t.Errorf("effect.value = %v after amend, want second-name — the effect was not recomputed", got.Value)
	}
}
