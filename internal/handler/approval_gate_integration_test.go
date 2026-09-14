//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
)

// The gate's whole job is to say no. These prove it says no to the right things
// and — just as important — stays out of the way of everything else, because a
// control that blocks legitimate work gets switched off.

func gateFixture(t *testing.T) (*GraphQL, *crFixture) {
	t.Helper()
	f := newCRFixture(t)
	// true is what production runs (ORBITAL_INLINE_SELECTOR_REJECT). It makes no
	// difference to the tests below — they drive writeToDGraph directly, and the
	// inline-selector guard lives in Handle — but passing production's value keeps
	// the fixture from asserting against a configuration nothing deploys.
	// The guard's own effect is pinned in TestHandle_* below, through Handle.
	return NewGraphQL(testutil.DGraphURL(), f.db, slog.Default(), true), f
}

// mutate runs a mutation through the chokepoint exactly as Handle would.
func mutate(t *testing.T, gql *GraphQL, caller callerRole, query string, vars map[string]any) error {
	t.Helper()
	_, err := mutateReportingBypass(t, gql, caller, query, vars)
	return err
}

// mutateReportingBypass also returns the policy the caller bypassed, if any.
func mutateReportingBypass(t *testing.T, gql *GraphQL, caller callerRole, query string, vars map[string]any) (string, error) {
	t.Helper()
	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := gql.writeToDGraph(context.Background(), body, "gate-test", caller, gateEnforce, nil)
	return res.Bypassed, err
}

func updateHostname(orbID, v string) (string, map[string]any) {
	return `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": orbID, "set": map[string]any{"hostname": v}}
}

func devCaller() callerRole   { return callerRole{Role: user.RoleDev, Source: "user"} }
func adminCaller() callerRole { return callerRole{Role: user.RoleAdmin, Source: "user"} }

// ── 1. a covered mutation by a non-bypass role is refused, and writes nothing ──

func TestGate_RefusesCoveredMutation(t *testing.T) {
	gql, f := gateFixture(t)
	f.requireApproval(t, 1)

	q, v := updateHostname(crServerA, "should-not-land")
	err := mutate(t, gql, devCaller(), q, v)
	if err == nil {
		t.Fatal("a covered mutation was allowed through")
	}
	var gerr *gatedError
	if !errors.As(err, &gerr) {
		t.Fatalf("error = %v, want a gatedError", err)
	}
	if gerr.Status != http.StatusForbidden || gerr.Code != CodeApprovalRequired {
		t.Errorf("status=%d code=%s, want 403 APPROVAL_REQUIRED", gerr.Status, gerr.Code)
	}
	// The hint has to name the way forward, or the refusal is a dead end.
	if !strings.Contains(gerr.Hint, "/api/v1/change-requests") {
		t.Errorf("hint does not point at change requests: %q", gerr.Hint)
	}
	if got := readHostname(t, crServerA); got == "should-not-land" {
		t.Fatal("the mutation was refused but the write still landed")
	}
}

// ── 2. bypass_roles writes straight through ────────────────────────────────

func TestGate_BypassRoleWritesThrough(t *testing.T) {
	gql, f := gateFixture(t)
	f.requireApproval(t, 1)

	q, v := updateHostname(crServerA, "privileged-write")
	bypassed, err := mutateReportingBypass(t, gql, adminCaller(), q, v)
	if err != nil {
		t.Fatalf("admin is in bypass_roles but was refused: %v", err)
	}
	if got := readHostname(t, crServerA); got != "privileged-write" {
		t.Errorf("hostname = %q, want privileged-write", got)
	}
	// The write must report WHICH policy it skipped, so the audit row can name
	// it. A frictionless bypass that leaves no durable trace is not the
	// "audited break-glass" D15 describes — it is just a hole with a log line.
	if bypassed != crNS {
		t.Errorf("bypassed policy = %q, want %q", bypassed, crNS)
	}
}

// A caller who did NOT bypass anything must not be marked privileged — a false
// positive here is worse than no flag at all, because it trains whoever reviews
// the audit log to ignore the field.
func TestGate_UngatedWriteIsNotMarkedPrivileged(t *testing.T) {
	gql, _ := gateFixture(t) // no policy at all

	q, v := updateHostname(crServerA, "ordinary-write")
	bypassed, err := mutateReportingBypass(t, gql, adminCaller(), q, v)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if bypassed != "" {
		t.Errorf("bypassed = %q, want empty — nothing was bypassed", bypassed)
	}
}

// ── 3 & 4. the opt-in promise ──────────────────────────────────────────────

func TestGate_NoPolicyChangesNothing(t *testing.T) {
	gql, _ := gateFixture(t) // deliberately no policy

	q, v := updateHostname(crServerA, "ungoverned-write")
	if err := mutate(t, gql, devCaller(), q, v); err != nil {
		t.Fatalf("a mutation was refused with no policy configured: %v", err)
	}
	if got := readHostname(t, crServerA); got != "ungoverned-write" {
		t.Errorf("hostname = %q — installing the gate changed behaviour with no policy", got)
	}
}

func TestGate_DisabledPolicyDoesNotGate(t *testing.T) {
	gql, f := gateFixture(t)
	f.requireApproval(t, 1)
	if _, err := f.db.ApprovalPolicy.Update().SetEnabled(false).Save(context.Background()); err != nil {
		t.Fatalf("disable: %v", err)
	}

	q, v := updateHostname(crServerA, "policy-off")
	if err := mutate(t, gql, devCaller(), q, v); err != nil {
		t.Fatalf("a disabled policy still gated the write: %v", err)
	}
}

// ── 5. a type-scoped policy leaves other types alone ───────────────────────

func TestGate_TypeScopedPolicyIsNarrow(t *testing.T) {
	ctx := context.Background()
	gql, f := gateFixture(t)
	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetAllTypes(false).
		SetTypes([]string{"Server"}).
		SetRequiredApprovals(1).
		Save(ctx); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	q, v := updateHostname(crServerA, "blocked")
	if err := mutate(t, gql, devCaller(), q, v); err == nil {
		t.Error("a Server mutation was allowed under a Server policy")
	}

	// IdracSettings is not named by the policy, so it must be untouched.
	err := mutate(t, gql, devCaller(),
		`mutation U($orbId: String!, $set: IdracSettingsPatch!) { updateIdracSettings(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		map[string]any{"orbId": crIdracA, "set": map[string]any{"firmwareVersion": "2.0.0"}})
	if err != nil {
		t.Errorf("a Server-scoped policy gated an IdracSettings mutation: %v", err)
	}
}

// ── 6. THE DEADLOCK — an approved request must be able to apply itself ─────

func TestGate_MergeOfApprovedRequestStillWorksWhileGated(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	// Prove the class really is gated for this caller before merging.
	gql := NewGraphQL(testutil.DGraphURL(), f.db, slog.Default(), false)
	q, v := updateHostname(crServerA, "direct-write")
	if err := mutate(t, gql, devCaller(), q, v); err == nil {
		t.Fatal("precondition failed: the class is not actually gated")
	}

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "via-change-request"},
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Merged by a plain dev — NOT in bypass_roles. Without the exemption the
	// merge is refused for lacking the approval it already has, and the request
	// becomes permanently unmergeable by anyone outside bypass_roles.
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("an approved change request could not apply itself: %v", err)
	}
	if got := readHostname(t, crServerA); got != "via-change-request" {
		t.Errorf("hostname = %q, want via-change-request", got)
	}
}

// ── 9. the inline-literal hole ─────────────────────────────────────────────

func TestGate_InlineAddIsRefusedOnlyWhenPoliciesExist(t *testing.T) {
	gql, f := gateFixture(t)

	// A fully inline add resolves its TYPE but yields no orbId, so no namespace,
	// so no policy can be found. Allowing it would make the gate optional for
	// anyone willing to inline their literals.
	inline := `mutation { addServer(input: [{orbId: "` + crNS + `:server-INLINE", namespace: "` + crNS + `", version: 1}], upsert: true) { numUids } }`

	// With no policy anywhere, the shape is none of the gate's business.
	if err := mutate(t, gql, devCaller(), inline, nil); err != nil {
		t.Fatalf("an inline add was refused with no policy configured: %v", err)
	}
	deleteEntity(t, "Server", crNS+":server-INLINE")

	f.requireApproval(t, 1)
	err := mutate(t, gql, devCaller(), inline, nil)
	if err == nil {
		t.Fatal("an inline add bypassed the gate while policies were in force")
	}
	var gerr *gatedError
	if !errors.As(err, &gerr) || gerr.Code != CodeVariableFormRequired {
		t.Fatalf("error = %v, want VARIABLE_FORM_REQUIRED", err)
	}
	// The variable form is the way out, so the hint must contain it.
	if !strings.Contains(gerr.Hint, "$input") {
		t.Errorf("hint does not show the variable form: %q", gerr.Hint)
	}
	// Even an admin gets this one: it is not an authorization refusal, it is
	// "orbital cannot tell which policy applies".
	if err := mutate(t, gql, adminCaller(), inline, nil); err == nil {
		t.Error("an admin's inline add was allowed while policies were in force")
	}
}

// ── 10. orb runs this handler with no policy store ─────────────────────────

func TestGate_InertWithoutAPolicyStore(t *testing.T) {
	seedCREngineFixture(t)
	// Exactly how internal/orbserver constructs it: nil db.
	gql := NewGraphQL(testutil.DGraphURL(), nil, slog.Default(), true)

	q, v := updateHostname(crServerA, "orb-side-write")
	if err := mutate(t, gql, callerRole{NoAuthz: true}, q, v); err != nil {
		t.Fatalf("the gate is not inert without a policy store — orb would break: %v", err)
	}
}

// ── unregistered types are not orbital's to govern ─────────────────────────

func TestGate_IgnoresNonConfigItemMutations(t *testing.T) {
	gql, f := gateFixture(t)
	f.requireApproval(t, 1)

	// Namespace is not a ConfigItem, so no policy can cover it and the gate must
	// not invent one. Uses upsert so a re-run is idempotent.
	err := mutate(t, gql, devCaller(),
		`mutation { addNamespace(input: [{name: "`+crNS+`"}], upsert: true) { numUids } }`, nil)
	if err != nil {
		t.Errorf("the gate blocked a non-ConfigItem mutation: %v", err)
	}
}

// ── multi-type mutations take the strictest match ──────────────────────────

func TestGate_MultiTypeMutationTakesTheStrictestPolicy(t *testing.T) {
	ctx := context.Background()
	gql, f := gateFixture(t)
	// Only IdracSettings is governed. A mutation touching both types must still
	// be refused, or bundling an ungoverned type would be a way around the gate.
	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetAllTypes(false).
		SetTypes([]string{"IdracSettings"}).
		SetRequiredApprovals(1).
		Save(ctx); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	both := `mutation M($s: ServerPatch!, $i: IdracSettingsPatch!) {
		updateServer(input: {filter: {orbId: {eq: "` + crServerA + `"}}, set: $s}) { numUids }
		updateIdracSettings(input: {filter: {orbId: {eq: "` + crIdracA + `"}}, set: $i}) { numUids }
	}`
	err := mutate(t, gql, devCaller(), both,
		map[string]any{"s": map[string]any{"hostname": "x"}, "i": map[string]any{"firmwareVersion": "3.0.0"}})
	if err == nil {
		t.Fatal("a multi-type mutation dodged the policy by including an ungoverned type")
	}
}

// ── 12. orbIds named through a VARIABLE REFERENCE ──────────────────────────
//
// The gate resolves the governing namespace from the orbIds a mutation names.
// Until 2026-09-14 it could only see them as literals, or behind a variable
// named exactly `orbId` — so a mutation in perfectly good variable form whose
// variable happened to be called something else resolved to NO namespace and
// was refused `400 VARIABLE_FORM_REQUIRED`, advising the caller to use the
// variable form they were already using.
//
// Fail-closed was right and is unchanged; the refusal was aimed at the wrong
// request. These pin the difference.

// updateHostnameAs is updateHostname with the orbId variable under a caller-
// chosen name — the shape any client not copying orbital's own examples writes.
func updateHostnameAs(varName, orbID, v string) (string, map[string]any) {
	return `mutation UpdateServer($` + varName + `: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $` + varName + `}}, set: $set}) { numUids } }`,
		map[string]any{varName: orbID, "set": map[string]any{"hostname": v}}
}

// Acceptance 1: a differently-named variable is gated IDENTICALLY — same
// refusal, same code, same bypass behaviour as the `$orbId` spelling.
func TestGate_OrbIdBehindADifferentlyNamedVariableIsGatedIdentically(t *testing.T) {
	gql, f := gateFixture(t)
	f.requireApproval(t, 1)

	q, v := updateHostnameAs("serverOrbId", crServerA, "should-not-land")
	err := mutate(t, gql, devCaller(), q, v)
	if err == nil {
		t.Fatal("a covered mutation was allowed through because its variable was not named orbId")
	}
	var gerr *gatedError
	if !errors.As(err, &gerr) {
		t.Fatalf("error = %v, want a gatedError", err)
	}
	// The whole bug: this used to be VARIABLE_FORM_REQUIRED, which is the wrong
	// refusal — the request IS in variable form.
	if gerr.Code != CodeApprovalRequired || gerr.Status != http.StatusForbidden {
		t.Fatalf("status=%d code=%s, want 403 APPROVAL_REQUIRED (not VARIABLE_FORM_REQUIRED)", gerr.Status, gerr.Code)
	}
	if got := readHostname(t, crServerA); got == "should-not-land" {
		t.Fatal("refused, but the write still landed")
	}

	// ...and the bypass path resolves the same policy, so break-glass still works.
	q, v = updateHostnameAs("serverOrbId", crServerA, "privileged-write")
	bypassed, err := mutateReportingBypass(t, gql, adminCaller(), q, v)
	if err != nil {
		t.Fatalf("admin bypass refused on the differently-named variable: %v", err)
	}
	if bypassed != crNS {
		t.Errorf("bypassed policy = %q, want %q", bypassed, crNS)
	}
}

// Acceptance 2: a compound mutation cannot have two variables named orbId, so
// this shape was unresolvable by construction. Every orbId it names must
// resolve, or a governed entity rides along inside an ungoverned request.
func TestGate_CompoundMutationResolvesEveryOrbIdItNames(t *testing.T) {
	gql, f := gateFixture(t)
	f.requireApproval(t, 1)

	q := `mutation Both($a: String!, $b: String!, $set: ServerPatch!) {
		one: updateServer(input: {filter: {orbId: {eq: $a}}, set: $set}) { numUids }
		two: updateServer(input: {filter: {orbId: {eq: $b}}, set: $set}) { numUids }
	}`
	v := map[string]any{"a": crServerA, "b": crServerB, "set": map[string]any{"hostname": "should-not-land"}}

	err := mutate(t, gql, devCaller(), q, v)
	if err == nil {
		t.Fatal("a compound mutation over two governed entities was allowed through")
	}
	var gerr *gatedError
	if !errors.As(err, &gerr) || gerr.Code != CodeApprovalRequired {
		t.Fatalf("error = %v, want APPROVAL_REQUIRED", err)
	}
	for _, id := range []string{crServerA, crServerB} {
		if got := readHostname(t, id); got == "should-not-land" {
			t.Fatalf("%s was written despite the refusal", id)
		}
	}
}

// Acceptance 4: resolution must not become a guess. A reference that cannot be
// resolved yields nothing and is still refused — the fail-closed default is
// exactly what must NOT be relaxed to fix the false refusal above.
func TestGate_UnresolvableVariableReferenceIsRefusedNotAllowed(t *testing.T) {
	gql, f := gateFixture(t)
	f.requireApproval(t, 1)

	// $missing is declared and referenced, but never supplied.
	q := `mutation U($missing: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $missing}}, set: $set}) { numUids } }`
	v := map[string]any{"set": map[string]any{"hostname": "should-not-land"}}

	err := mutate(t, gql, devCaller(), q, v)
	if err == nil {
		t.Fatal("an unresolvable mutation was waved through — the gate guessed instead of refusing")
	}
	var gerr *gatedError
	if !errors.As(err, &gerr) || gerr.Code != CodeVariableFormRequired {
		t.Fatalf("error = %v, want VARIABLE_FORM_REQUIRED", err)
	}

	// A filter on a non-orbId field is the same story: nothing to attribute.
	q2 := `mutation ByName($f: ServerFilter!, $set: ServerPatch!) { updateServer(input: {filter: $f, set: $set}) { numUids } }`
	v2 := map[string]any{
		"f":   map[string]any{"hostname": map[string]any{"eq": "nothing-here"}},
		"set": map[string]any{"model": "m"},
	}
	if err := mutate(t, gql, devCaller(), q2, v2); err == nil {
		t.Error("a non-orbId filter resolved to a namespace it never named")
	}
}

// Acceptance 5: an ungated deployment must not notice any of this. Resolution
// only ever ADDS orbIds, so the risk is a new refusal where there was none.
func TestGate_NewlyResolvableShapesChangeNothingWithoutAPolicy(t *testing.T) {
	gql, _ := gateFixture(t) // no policy anywhere

	named, namedV := updateHostnameAs("serverOrbId", crServerA, "ungated-named-var")
	shapes := []struct {
		name  string
		query string
		vars  map[string]any
	}{
		{"differently-named variable", named, namedV},
		{
			"filter object behind a variable",
			`mutation Apply($myFilter: ServerFilter!, $set: ServerPatch!) { updateServer(input: {filter: $myFilter, set: $set}) { numUids } }`,
			map[string]any{
				"myFilter": map[string]any{"orbId": map[string]any{"eq": crServerA}},
				"set":      map[string]any{"hostname": "ungated-filter-var"},
			},
		},
		{
			"in-list mixing a literal and a variable",
			`mutation Bulk($second: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {in: ["` + crServerA + `", $second]}}, set: $set}) { numUids } }`,
			map[string]any{"second": crServerB, "set": map[string]any{"hostname": "ungated-in-list"}},
		},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			if err := mutate(t, gql, devCaller(), s.query, s.vars); err != nil {
				t.Fatalf("an ungated deployment was refused: %v", err)
			}
		})
	}
}
