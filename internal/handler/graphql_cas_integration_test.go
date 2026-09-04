//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
)

// The race itself, against real DGraph. Everything else about CAS can be
// asserted from a stub; this cannot.
//
// Deliberately NOT an e2e test: the plan's rule is that a flaky test on a safety
// property trains people to re-run until green. This drives Handle directly with
// N goroutines sharing one entity, which makes the race deterministic enough to
// assert on — exactly one winner — rather than probabilistic.

// ── 1. concurrent writers, one winner ──────────────────────────────────────

func TestCASRace_ConcurrentUpdatesWithTheSameIfVersionYieldExactlyOneWinner(t *testing.T) {
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, slog.Default(), false)

	start := readVersion(t, crServerA)
	const writers = 12

	var wg sync.WaitGroup
	codes := make([]int, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, rec := newGQLCtx(t, map[string]any{
				"query":         `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`,
				"operationName": "UpdateServer",
				"variables": map[string]any{
					"orbId": crServerA, "ifVersion": start,
					"set": map[string]any{"hostname": "racer"},
				},
			})
			c.Set("user_email", "racer@test.com")
			c.Set("role", string(user.RoleAdmin))
			if err := gql.Handle(c); err != nil {
				t.Errorf("Handle: %v", err)
				return
			}
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	var won, conflicted, other int
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			won++
		case http.StatusConflict:
			conflicted++
		default:
			other++
		}
	}

	// The property. Before CAS, several writers reported 200 for the same
	// starting version and all but one were silently lost.
	if won != 1 {
		t.Errorf("winners = %d, want exactly 1 — %d writers claimed success against version %d", won, won, start)
	}
	if won+conflicted != writers {
		t.Errorf("codes: %d ok, %d conflict, %d other — every writer must land on one or the other", won, conflicted, other)
	}

	// ── 3. and the winner is an ordinary successful write ──────────────────
	if got := readVersion(t, crServerA); got != start+1 {
		t.Errorf("version = %d, want %d — exactly one increment for one winner", got, start+1)
	}
	if got := readHostname(t, crServerA); got != "racer" {
		t.Errorf("hostname = %q, want racer", got)
	}
}

// The negative that keeps the guard usable: without `ifVersion`, concurrent
// writers are last-writer-wins as they always were. A guard that fired on
// unguarded traffic would make every bulk client start seeing 409s.
func TestCASRace_UnguardedConcurrentUpdatesAllSucceed(t *testing.T) {
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, slog.Default(), false)

	const writers = 6
	var wg sync.WaitGroup
	codes := make([]int, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, rec := newGQLCtx(t, map[string]any{
				"query":         `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`,
				"operationName": "UpdateServer",
				"variables":     map[string]any{"orbId": crServerB, "set": map[string]any{"hostname": "unguarded"}},
			})
			c.Set("user_email", "racer@test.com")
			c.Set("role", string(user.RoleAdmin))
			if err := gql.Handle(c); err != nil {
				t.Errorf("Handle: %v", err)
				return
			}
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code == http.StatusConflict {
			t.Errorf("writer %d got 409 with no ifVersion supplied — the guard fired on unguarded traffic", i)
		}
	}
}

// ── the forwarded query really is valid GraphQL against the deployed schema ──

// The unit tests assert the SHAPE of the injected query. Only a real DGraph
// answers whether `version` is actually filterable — which is the half that
// depends on the schema having been applied, and the half that fails loudly if
// code ships before schema.
func TestCASRace_VersionIsFilterableOnTheDeployedSchema(t *testing.T) {
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, slog.Default(), false)

	c, rec := newGQLCtx(t, map[string]any{
		"query":         `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: { filter: { orbId: { eq: $orbId } }, set: $set }) { numUids } }`,
		"operationName": "UpdateServer",
		"variables": map[string]any{
			"orbId": crServerA, "ifVersion": readVersion(t, crServerA),
			"set": map[string]any{"hostname": "filterable"},
		},
	})
	c.Set("user_email", "schema@test.com")
	c.Set("role", string(user.RoleAdmin))
	if err := gql.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d — if this says the field is not defined by ServerFilter, the schema is older than the code: %s",
			rec.Code, rec.Body.String())
	}
	var env struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if len(env.Errors) > 0 {
		t.Fatalf("DGraph rejected the injected filter: %s", env.Errors[0].Message)
	}
}

// ── 9. change-request merge inherits the guard ─────────────────────────────

// applyItem already passed the version it planned against; CAS moves that check
// inside the write. What this asserts is the consequence: an item that loses to
// a concurrent direct edit is a FAILED item, not a silent success that reports
// applied while writing nothing.
func TestCASRace_MergeItemLosingToAConcurrentEditFailsRatherThanSilentlySucceeding(t *testing.T) {
	f := newCRFixture(t)

	// Drive applyItem with a target whose version has already moved on — the
	// state fetchMergeTargets would have produced had someone written between
	// the read and the write.
	item := approval.ChangeItem{OrbID: crServerA, Type: "Server", Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "merge-loser"}}
	stale := mergeTarget{Exists: true, Version: readVersion(t, crServerA) - 1, Current: map[string]any{}}

	err := f.crh.applyItem(context.Background(), author,
		callerRole{Role: user.RoleAdmin, Source: "user"}, 1, item, stale)
	if err == nil {
		t.Fatal("a merge item that lost the race reported success")
	}
	if got := readHostname(t, crServerA); got == "merge-loser" {
		t.Error("the losing item's write landed anyway")
	}
}
