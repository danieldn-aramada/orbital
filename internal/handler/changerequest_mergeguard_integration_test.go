//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/graphdiff"
	"github.com/labstack/echo/v4"
)

// Merge-time entity guard.
//
// `base_hash` has always refused a stale merge; what it could never do is say
// WHICH entity moved, because it is a fingerprint of the version vector rather
// than the vector. `base_versions` stores the vector it fingerprints, so the
// refusal can name the offender — for every request, including ones whose
// author never sent a precondition.
//
// Deliberately NOT the author's `version`: that is their read at proposal
// time, so once anything moves the entity it is permanently unsatisfiable and
// re-approval — one click, by design — could never clear it.

// ── 11. a merge whose entity moved is refused, naming it, applying nothing ──

func TestCRMerge_EntityMovedIsRefusedByNameAndAppliesNothing(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "a-proposed"}},
		approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "b-proposed"}},
	)
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// A third party moves ONE of the two entities.
	setHostname(t, crServerB, "moved-by-someone-else")

	_, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	if err == nil {
		t.Fatal("a merge whose target moved after approval was allowed")
	}
	// Still the same sentinel — the status, code and every errors.Is call site
	// are unchanged; what is added is the detail.
	if !errors.Is(err, errCRStale) {
		t.Errorf("err = %v, want it to still wrap errCRStale", err)
	}
	var sw *staleWithEntities
	if !errors.As(err, &sw) {
		t.Fatalf("err = %v, want a staleWithEntities naming the offender", err)
	}
	if len(sw.Problems) != 1 {
		t.Fatalf("problems = %d, want exactly the one entity that moved: %v", len(sw.Problems), sw.Problems)
	}
	if sw.Problems[0].OrbID != crServerB {
		t.Errorf("named %q, want %q — the refusal blames the wrong entity", sw.Problems[0].OrbID, crServerB)
	}
	if !strings.Contains(sw.Problems[0].Msg, "version 1") || !strings.Contains(sw.Problems[0].Msg, "now 2") {
		t.Errorf("message does not carry both versions: %q", sw.Problems[0].Msg)
	}

	// Atomic: the untouched item must NOT have been applied.
	if got := readHostname(t, crServerA); got == "a-proposed" {
		t.Error("a refused merge applied one of its items — the pre-check is not atomic")
	}
}

// ── 12. all current: applies, and each version advances by exactly one ──────

func TestCRMerge_AllCurrentAppliesAndBumpsEachVersionOnce(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "a-merged"}},
		approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "b-merged"}},
	)
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge: %v", err)
	}

	for orbID, want := range map[string]string{crServerA: "a-merged", crServerB: "b-merged"} {
		if got := readHostname(t, orbID); got != want {
			t.Errorf("%s hostname = %q, want %q", orbID, got, want)
		}
		if v := readVersion(t, orbID); v != 2 {
			t.Errorf("%s version = %d, want exactly 2 — the write-time guard double-stamped or skipped", orbID, v)
		}
	}
}

// ── 13. the two refusals read differently ──────────────────────────────────

// An operator has to be able to tell "reload the entity, it moved" from "your
// value is out of date". Entity-level problems carry no Field; that absence is
// the machine-readable half of the distinction.
func TestCRMerge_EntityMoveAndFieldMoveAreDistinguishable(t *testing.T) {
	ctx := context.Background()
	// ONE fixture: the approval policy is unique per namespace, and a second
	// fixture's cleanup does not run until the test ends, so two would collide
	// rather than isolate.
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	// Entity-level: the version moved, so the whole review is stale.
	crA := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "proposed-a"}})
	if _, err := f.crh.Approve(ctx, crA.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve A: %v", err)
	}
	setHostname(t, crServerA, "third-party")

	_, errA := f.crh.Merge(ctx, crA.ID, author, user.RoleDev, false)
	var sw *staleWithEntities
	if !errors.As(errA, &sw) || len(sw.Problems) == 0 {
		t.Fatalf("expected an entity-level refusal, got %v", errA)
	}
	entity := sw.Problems[0]
	if entity.Field != "" {
		t.Errorf("entity-level problem carries Field=%q — indistinguishable from a field-level refusal", entity.Field)
	}
	if !strings.Contains(entity.Msg, "version") {
		t.Errorf("entity-level message does not talk about versions: %q", entity.Msg)
	}

	// Field-level, for contrast: it names a FIELD and talks about values. It
	// comes from the server-recorded ancestor (`base_values`) at merge, not from
	// anything a client asserted — the client-supplied `before` was removed once
	// `version` covered the entity-level question.
	//
	// Reaching it needs the VERSION to be current while a covered FIELD has
	// moved to a third value. Writing straight to DGraph without bumping the
	// counter produces exactly that — the class base_values exists to catch, and
	// the one version is blind to.
	crB := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "proposed-b"}})
	if _, err := f.crh.Approve(ctx, crB.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve B: %v", err)
	}
	crGQL(t, `mutation($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": crServerB, "set": map[string]any{"hostname": "someone-elses-value"}})

	_, errB := f.crh.Merge(ctx, crB.ID, author, user.RoleDev, false)
	var pf *preconditionFailed
	if !errors.As(errB, &pf) || len(pf.Problems) == 0 {
		t.Fatalf("expected a field-level refusal from base_values, got %v", errB)
	}
	if pf.Problems[0].Field == "" {
		t.Error("field-level problem carries no Field — the two refusals are indistinguishable")
	}
}

// ── 14. enforced at WRITE time, not only in the pre-check ──────────────────

// The pre-check runs against st.Versions; fetchMergeTargets reads again; then
// each item is written. A version moving inside that window used to be
// invisible. applyItem now carries the version it planned against into the
// dispatch, and the single write path re-reads immediately before the POST.
//
// NOTE: this narrows the window, it does not close it. DGraph's GraphQL layer
// has no conditional update, so a genuine compare-and-swap needs a DQL upsert
// block — the backlog item stays open.
func TestCRMerge_WriteTimeGuardRefusesAVersionThatMovedAfterPlanning(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)

	// Drive applyItem directly with a stale target, which is exactly the state
	// fetchMergeTargets would have produced had someone written between the
	// read and the write.
	item := approval.ChangeItem{OrbID: crServerA, Type: "Server", Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "written-against-a-stale-plan"}}
	stale := mergeTarget{Exists: true, Version: readVersion(t, crServerA) - 1, Current: map[string]any{}}

	err := f.crh.applyItem(ctx, author, callerRole{Role: user.RoleAdmin, Source: "user"}, 1, item, stale)
	if err == nil {
		t.Fatal("a write planned against a version that had already moved was applied")
	}
	if got := readHostname(t, crServerA); got == "written-against-a-stale-plan" {
		t.Error("the refused write landed anyway")
	}

	// The negative: a CURRENT target still applies. A guard that refused
	// everything would pass the test above and break every merge.
	current := mergeTarget{Exists: true, Version: readVersion(t, crServerA), Current: map[string]any{}}
	if err := f.crh.applyItem(ctx, author, callerRole{Role: user.RoleAdmin, Source: "user"}, 1, item, current); err != nil {
		t.Fatalf("a write planned against the current version was refused: %v", err)
	}
	if got := readHostname(t, crServerA); got != "written-against-a-stale-plan" {
		t.Errorf("hostname = %q, want the write to have landed", got)
	}
}

// ── the vector must not drift from the fingerprint it explains ─────────────

// base_hash and base_versions are written at four sites. If one is updated and
// the other is not they disagree silently: staleness still fires from the hash,
// but the explanation names the wrong entities — or none, and the refusal
// quietly degrades to the unnamed one. Nothing observable says so.
func TestCRMerge_BaseVersionsStaysInLockstepWithBaseHash(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	assertLockstep := func(t *testing.T, when string, id int64) {
		t.Helper()
		cr, err := f.crh.Get(ctx, id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if len(cr.BaseVersions) == 0 {
			t.Fatalf("%s: base_versions is empty", when)
		}
		if got := versionHash(cr.BaseVersions); got != cr.BaseHash {
			t.Errorf("%s: versionHash(base_versions) = %s, base_hash = %s — the vector and its fingerprint disagree",
				when, got, cr.BaseHash)
		}
	}

	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "v1"}})
	assertLockstep(t, "after create", cr.ID)

	// Amend re-captures the base.
	amended, problems, err := f.crh.Amend(ctx, cr.ID, author, user.RoleDev, nil, nil,
		&approval.Changeset{Namespace: crNS, Changes: []approval.ChangeItem{
			{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "v2"}},
		}})
	if err != nil || len(problems) > 0 {
		t.Fatalf("amend: err=%v problems=%v", err, problems)
	}
	assertLockstep(t, "after amend", amended.ID)

	// Approving a stale request re-anchors it.
	setHostname(t, crServerA, "third-party")
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	assertLockstep(t, "after re-approval rebase", cr.ID)
}

// ── the whole point: re-review still clears it in one click ────────────────

// The reason base_versions carries the merge guard rather than the author's
// version. A token captured at proposal time would make this flow impossible:
// the entity moved, so it can never match again, and no amount of re-approving
// would help.
func TestCRMerge_ReApprovalAfterAThirdPartyWriteStillMerges(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "proposed"}})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	setHostname(t, crServerA, "third-party-edit")

	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); !errors.Is(err, errCRStale) {
		t.Fatalf("merge err = %v, want stale", err)
	}
	// One click.
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "re-reviewed"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge after re-approval was refused: %v — re-review no longer clears staleness", err)
	}
	if got := readHostname(t, crServerA); got != "proposed" {
		t.Errorf("hostname = %q, want proposed", got)
	}
}

// ── the wire ───────────────────────────────────────────────────────────────

func TestCRMerge_StaleRefusalCarriesProblemsOverHTTP(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "proposed"}})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	setHostname(t, crServerA, "third-party")

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/change-requests/1/merge", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(crHumanID(cr))
	c.Set("user_email", author)
	c.Set("role", string(user.RoleDev))
	if err := f.crh.MergeChangeRequest(c); err != nil {
		t.Fatalf("MergeChangeRequest returned a transport error: %v", err)
	}

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Code     string `json:"code"`
		Problems []struct {
			OrbID string `json:"orbId"`
			Msg   string `json:"message"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if out.Code != CodeMVCCConflict {
		t.Errorf("code = %q, want %s", out.Code, CodeMVCCConflict)
	}
	if len(out.Problems) == 0 || out.Problems[0].OrbID != crServerA {
		t.Fatalf("the 409 does not name the entity that moved: %s", rec.Body.String())
	}
	if !strings.Contains(out.Problems[0].Msg, "version") {
		t.Errorf("problems[0].message = %q, want it to carry the versions", out.Problems[0].Msg)
	}
}

// The SECOND staleness checkpoint in Merge, which the test above never reaches.
//
// When a governed request's target moves, its approvals stop counting, so the
// refusal comes from the not-approved branch. The final checkpoint is reachable
// only when status is approved AND the base moved — which is an UNGOVERNED
// request (no policy, so required is 0 and it reads approved from birth). Its
// comment says so: "it is the ONLY guard on an ungoverned request".
//
// Found by mutation testing: blanking the named refusal on that line changed
// nothing, because nothing exercised it.
func TestCRMerge_UngovernedRequestGetsTheNamedRefusalToo(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t) // deliberately no policy

	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "proposed"}})
	if st := f.state(t, cr.ID); st.Status != approval.StatusApproved {
		t.Fatalf("status = %q, want approved — an ungoverned request needs no approvals", st.Status)
	}

	setHostname(t, crServerA, "third-party")

	_, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	if !errors.Is(err, errCRStale) {
		t.Fatalf("err = %v, want stale", err)
	}
	var sw *staleWithEntities
	if !errors.As(err, &sw) {
		t.Fatalf("err = %v, want the refusal to name the entity on the ungoverned path too", err)
	}
	if len(sw.Problems) != 1 || sw.Problems[0].OrbID != crServerA {
		t.Errorf("problems = %v, want exactly %s", sw.Problems, crServerA)
	}
	if got := readHostname(t, crServerA); got != "third-party" {
		t.Errorf("hostname = %q — the refused merge wrote anyway", got)
	}
}

// ── the banner's data comes from the API, not from the browser ─────────────

// CLAUDE.md's API-first rule: a view renders what the response carries. A client
// working out WHICH entity moved would need the base vector, the current vector
// and the scope-expansion rules — three things orbital already has.
//
// It must also agree with the merge refusal. A banner naming different entities
// from the error you get when you click Merge would be worse than one naming
// none, so both read the same diff.
func TestCRMerge_StaleEntitiesAreCarriedOnTheGetResponse(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "proposed"}, Version: intp(readVersion(t, crServerA))})

	get := func(t *testing.T) struct {
		Stale         bool `json:"stale"`
		StaleEntities []struct {
			OrbID    string `json:"orbId"`
			Reviewed int    `json:"reviewedVersion"`
			Current  *int   `json:"currentVersion"`
		} `json:"staleEntities"`
	} {
		t.Helper()
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/change-requests/"+crHumanID(cr), nil), rec)
		c.SetParamNames("id")
		c.SetParamValues(crHumanID(cr))
		c.Set("user_email", reviewer)
		c.Set("role", string(user.RoleDev))
		if err := f.crh.GetChangeRequest(c); err != nil {
			t.Fatalf("GetChangeRequest: %v", err)
		}
		var out struct {
			Stale         bool `json:"stale"`
			StaleEntities []struct {
				OrbID    string `json:"orbId"`
				Reviewed int    `json:"reviewedVersion"`
				Current  *int   `json:"currentVersion"`
			} `json:"staleEntities"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
		}
		return out
	}

	// The negative first: a fresh request must carry nothing. A field that is
	// always populated trains its reader to ignore it.
	if fresh := get(t); fresh.Stale || len(fresh.StaleEntities) != 0 {
		t.Fatalf("a fresh request reports stale=%v entities=%v", fresh.Stale, fresh.StaleEntities)
	}

	setHostname(t, crServerA, "third-party")

	got := get(t)
	if !got.Stale {
		t.Fatal("the request is not stale after its target moved")
	}
	if len(got.StaleEntities) != 1 {
		t.Fatalf("staleEntities = %v, want exactly the one that moved", got.StaleEntities)
	}
	e := got.StaleEntities[0]
	if e.OrbID != crServerA {
		t.Errorf("named %q, want %q", e.OrbID, crServerA)
	}
	if e.Reviewed != 1 {
		t.Errorf("reviewedVersion = %d, want 1", e.Reviewed)
	}
	if e.Current == nil || *e.Current != 2 {
		t.Errorf("currentVersion = %v, want 2", e.Current)
	}

	// And it agrees with what a merge attempt would say.
	_, mergeErr := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	var sw *staleWithEntities
	if !errors.As(mergeErr, &sw) || len(sw.Problems) != 1 || sw.Problems[0].OrbID != e.OrbID {
		t.Errorf("the banner and the merge refusal name different entities: banner=%v merge=%v", e.OrbID, sw)
	}
}

// ── the review table's data ────────────────────────────────────────────────

// `fields` resolves every field to what a merge would DO with it. It exists
// because a value pair cannot tell an ordinary change from a conflict — both are
// two differing values — and the third value that separates them (what it was
// when reviewed) lives only on the conflict rows.
//
// Same classifier the merge uses, so a preview that says "conflict" and a merge
// that refuses cannot disagree. That agreement is asserted at the bottom.
func TestCRFields_ResolvesAppliesSatisfiedAndConflict(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	// Three fields on two entities, engineered into the three outcomes.
	cr := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{
			"hostname": "a-proposed",  // will stay APPLIES
			"model":    "already-set", // will be made SATISFIED below
		}},
		approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{
			"hostname": "b-proposed", // will be made CONFLICT below
		}},
	)

	// Approve FIRST. Approving re-anchors the ancestor to current state — it is
	// the act of attesting to it — so a third-party write made BEFORE approval
	// would be absorbed into the base and could never read as a conflict. The
	// realistic sequence is: reviewed, then someone else changed it.
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Written WITHOUT bumping `version`, so the request does not go stale — the
	// entity-level guard stays quiet and the field-level one is what fires.
	// Someone else independently sets model to the proposed value → satisfied.
	crGQL(t, `mutation($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": crServerA, "set": map[string]any{"model": "already-set"}})
	// And moves B's hostname to a THIRD value → conflict.
	crGQL(t, `mutation($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": crServerB, "set": map[string]any{"hostname": "someone-elses"}})

	rows := fieldOutcomeBodies(classifyChangeset(
		snapshotFor(t, f, cr), mustChangeset(t, cr), mustGet(t, f, cr.ID).BaseValues))

	byKey := map[string]fieldOutcomeBody{}
	for _, r := range rows {
		byKey[r.OrbID+"/"+r.Field] = r
	}

	applies, ok := byKey[crServerA+"/hostname"]
	if !ok || applies.Outcome != "applies" {
		t.Errorf("hostname on A = %+v, want outcome applies", applies)
	}
	if applies.Reviewed != nil {
		t.Errorf("an applying row carries `reviewed` (%v) — it only repeats current", applies.Reviewed)
	}

	satisfied, ok := byKey[crServerA+"/model"]
	if !ok || satisfied.Outcome != "satisfied" {
		t.Errorf("model on A = %+v, want outcome satisfied", satisfied)
	}
	// The whole point of the Current column: on a satisfied row the reader sees
	// the two values are equal instead of inferring it from a caption.
	if fmt.Sprint(satisfied.Current) != fmt.Sprint(satisfied.Proposed) {
		t.Errorf("satisfied row: current %v != proposed %v", satisfied.Current, satisfied.Proposed)
	}

	conflict, ok := byKey[crServerB+"/hostname"]
	if !ok || conflict.Outcome != "conflict" {
		t.Fatalf("hostname on B = %+v, want outcome conflict", conflict)
	}
	// The third value, and the only row that carries it.
	if fmt.Sprint(conflict.Reviewed) != "b-original" {
		t.Errorf("conflict.reviewed = %v, want b-original — without it a conflict is indistinguishable from an ordinary change", conflict.Reviewed)
	}
	if fmt.Sprint(conflict.Current) != "someone-elses" {
		t.Errorf("conflict.current = %v, want someone-elses", conflict.Current)
	}

	// And the preview agrees with the refusal: merge must refuse, naming the
	// same field the table marked.
	_, mergeErr := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false)
	var pf *preconditionFailed
	if !errors.As(mergeErr, &pf) {
		t.Fatalf("merge err = %v, want a field conflict — the table and the merge disagree", mergeErr)
	}
	if len(pf.Problems) != 1 || !strings.Contains(pf.Problems[0].Field, "hostname") {
		t.Errorf("merge refused on %v, want the one field the table called a conflict", pf.Problems)
	}
}

// snapshotFor reads the same base snapshot the diff endpoint uses.
func snapshotFor(t *testing.T, f *crFixture, cr *ent.ApprovalRequest) graphdiff.Snapshot {
	t.Helper()
	st, err := f.crh.StateWithSnapshot(context.Background(), mustGet(t, f, cr.ID))
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	return st.Snapshot
}

func mustChangeset(t *testing.T, cr *ent.ApprovalRequest) approval.Changeset {
	t.Helper()
	var cs approval.Changeset
	if err := json.Unmarshal(cr.Payload, &cs); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return cs
}
