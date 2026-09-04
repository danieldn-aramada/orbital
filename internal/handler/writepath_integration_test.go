//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/approvalpolicy"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// The write pre-flight — before-fetch, version, stamping — moved out of the
// /graphql handler and into writeToDGraph on 2026-09-03 so that every write
// gets it, not only the ones arriving over HTTP.
//
// Every guarantee below fails OPEN when it breaks: the write lands, the caller
// sees success, and no screen looks wrong. A divergence Accept that skipped the
// version bump shipped for two months and was found only when someone asked why
// an approved change request still looked fresh. There is no visual check that
// substitutes for these.

// ── fixtures ─────────────────────────────────────────────────────────────────

// acceptFixture wires the divergence handler onto the change-request fixture's
// database and the REAL DGraph, so an Accept walks the same path production takes.
type acceptFixture struct {
	*crFixture
	gql *GraphQL
	dh  *DivergenceHandler
}

func newAcceptFixture(t *testing.T) *acceptFixture {
	t.Helper()
	f := newCRFixture(t)
	gql := NewGraphQL(testutil.DGraphURL(), f.db, slog.Default(), false)
	return &acceptFixture{crFixture: f, gql: gql, dh: NewDivergenceHandler(f.db, slog.Default(), gql)}
}

// seedEntry inserts one pending DivergenceEntry: the edge reports `field` as
// overrideValue where orbital intends intendedValue.
func (f *acceptFixture) seedEntry(t *testing.T, orbID, field, typeName string, intendedValue, overrideValue any) string {
	t.Helper()
	intended, _ := json.Marshal(intendedValue)
	override, _ := json.Marshal(overrideValue)
	e, err := f.db.DivergenceEntry.Create().
		SetDcOrbID(crDC).
		SetEntryOrbID(orbID).
		SetField(field).
		SetTypeName(typeName).
		SetIntendedValue(intended).
		SetOverrideValue(override).
		SetWho("local:admin").
		SetFirstSeenAt(time.Now().UTC().Add(-time.Hour)).
		SetLastSeenAt(time.Now().UTC()).
		SetLastReportPublishedAt(time.Now().UTC()).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed divergence entry: %v", err)
	}
	return e.ID.String()
}

// resolve drives PUT /api/v1/divergences/:id/resolution as `role`.
func (f *acceptFixture) resolve(t *testing.T, entryID, action, actor string, role user.Role) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/divergences/"+entryID+"/resolution",
		strings.NewReader(`{"action":"`+action+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(entryID)
	c.Set("user_email", actor)
	c.Set("role", string(role))
	if err := f.dh.PutResolution(c); err != nil {
		t.Fatalf("PutResolution(%s): %v", action, err)
	}
	return rec
}

// bypassPolicy governs crNS but lets `roles` write without review — the shape
// that lets a privileged caller's write actually land so the tests below can
// observe what the write path did with it.
func (f *acceptFixture) bypassPolicy(t *testing.T, roles ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetRequiredApprovals(1).
		SetBypassRoles(roles).
		Save(ctx); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.ApprovalPolicy.Delete().Where(approvalpolicy.NamespaceEQ(crNS)).Exec(ctx)
	})
}

// waitForAuditEvent polls GET /api/v1/audit-log until an event for `operation`
// appears. auditMutation runs in a goroutine, so a bare read races it; polling
// rather than sleeping keeps the test honest about what it is waiting for.
func waitForAuditEvent(t *testing.T, f *crFixture, operation string) auditEventView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if events := auditEvents(t, f, operation); len(events) > 0 {
			return events[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s event in GET /api/v1/audit-log after 5s", operation)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func changeFor(changes []fieldChange, field string) (fieldChange, bool) {
	for _, ch := range changes {
		if ch.Field == field {
			return ch, true
		}
	}
	return fieldChange{}, false
}

// ── 1. an Accept bumps version and stamps the actor ──────────────────────────

// The Track B defect, and the reason the pre-flight moved. Accept dispatched
// update{Type} with set:{field:value} and nothing else — no version, no
// updatedAt, no updatedBy — because stamping lived in Handle and Accept does not
// go through Handle. The graph's provenance named whoever last wrote via
// /graphql, and base_hash never moved, so the write was invisible to staleness.
func TestWritePath_DivergenceAcceptBumpsVersionAndStampsActor(t *testing.T) {
	f := newAcceptFixture(t)
	const actor = "accept-actor@test.com"

	if got := readVersion(t, crServerA); got != 1 {
		t.Fatalf("fixture version = %d, want 1", got)
	}

	entryID := f.seedEntry(t, crServerA, "hostname", "Server", "a-original", "edge-observed-name")
	f.resolve(t, entryID, "accept", actor, user.RoleAdmin)

	if got := readHostname(t, crServerA); got != "edge-observed-name" {
		t.Errorf("hostname = %q, want the accepted override to have landed", got)
	}
	if got := readVersion(t, crServerA); got != 2 {
		t.Errorf("version = %d, want exactly 2 — an Accept must advance the OCC counter by one", got)
	}
	node := getServer(t, crServerA, "updatedBy", "updatedAt")
	if got, _ := node["updatedBy"].(string); got != actor {
		t.Errorf("updatedBy = %q, want %q — the graph's provenance still names the wrong writer", got, actor)
	}
	if got, _ := node["updatedAt"].(string); got == "" {
		t.Error("updatedAt is empty — an Accept left no write timestamp")
	}
}

// The negative. A flag that fires when nothing happened trains whoever reads the
// record to ignore it: Reject and Ignore never touch intent, so they must leave
// the counter exactly where it was.
func TestWritePath_RejectAndIgnoreDoNotBumpVersion(t *testing.T) {
	f := newAcceptFixture(t)

	// A distinct field per action: (dc, orbId, field) is uniquely constrained, and
	// neither decision deletes the entry it resolved.
	for action, field := range map[string]string{"reject": "hostname", "ignore": "model"} {
		t.Run(action, func(t *testing.T) {
			before := readVersion(t, crServerA)
			entryID := f.seedEntry(t, crServerA, field, "Server", "intended", "edge-observed")
			f.resolve(t, entryID, action, "reject-actor@test.com", user.RoleAdmin)

			if after := readVersion(t, crServerA); after != before {
				t.Errorf("version %d → %d on %s — a decision that does not touch intent bumped the counter", before, after, action)
			}
			if got := readHostname(t, crServerA); got != "a-original" {
				t.Errorf("hostname = %q, want a-original — %s mutated intent", got, action)
			}
		})
	}
}

// ── 2. an Accept invalidates an open change request ─────────────────────────

// The consequence that makes item 1 matter rather than merely being untidy:
// base_hash is a hash of the scope's orbId@version vector, so a write that does
// not move `version` does not move the hash. Approvals cast before the Accept
// kept counting and the merge proceeded against a state nobody approved.
func TestWritePath_AcceptMakesAnOpenChangeRequestStaleAndVoidsApprovals(t *testing.T) {
	ctx := context.Background()
	f := newAcceptFixture(t)
	f.bypassPolicy(t, "admin") // dev proposes and approves; admin writes around it

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"model": "proposed-model"},
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if st := f.state(t, cr.ID); st.SubtreeChanged || st.Valid != 1 {
		t.Fatalf("before the Accept: subtreeChanged=%v valid=%d, want fresh with 1 approval", st.SubtreeChanged, st.Valid)
	}

	entryID := f.seedEntry(t, crServerA, "hostname", "Server", "a-original", "edge-observed-name")
	f.resolve(t, entryID, "accept", "admin@test.com", user.RoleAdmin)

	st := f.state(t, cr.ID)
	if !st.SubtreeChanged {
		t.Error("a third-party Accept moved the target without being noticed — an approval now covers a state nobody reviewed")
	}
	if st.Valid != 0 {
		t.Errorf("valid approvals = %d, want 0 — the approval predates the Accept", st.Valid)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err == nil {
		t.Error("a stale request merged")
	}
}

// ── 3. the Accept's audit event carries a field diff ────────────────────────

func TestWritePath_AcceptAuditEventCarriesAFieldDiff(t *testing.T) {
	f := newAcceptFixture(t)

	entryID := f.seedEntry(t, crServerA, "hostname", "Server", "a-original", "edge-observed-name")
	f.resolve(t, entryID, "accept", "diff-actor@test.com", user.RoleAdmin)

	ev := waitForAuditEvent(t, f.crFixture, "updateServer")
	ch, ok := changeFor(ev.Changes, "hostname")
	if !ok {
		t.Fatalf("no hostname entry in the event's changes: %+v", ev.Changes)
	}
	if got, _ := ch.Before.(string); got != "a-original" {
		t.Errorf("changes[hostname].before = %v, want a-original", ch.Before)
	}
	if got, _ := ch.After.(string); got != "edge-observed-name" {
		t.Errorf("changes[hostname].after = %v, want edge-observed-name", ch.After)
	}
}

// ── 11. an internal dispatch that supplies no before still diffs ────────────

// DispatchMutation used to document "callers that want a diff must supply
// before themselves". Merge forgot, and its audit rows dumped raw variables
// instead of a diff. The fetch now happens for internal callers too, so there is
// nothing left to forget.
func TestWritePath_InternalDispatchWithNoBeforeStillProducesADiff(t *testing.T) {
	f := newAcceptFixture(t)

	query := `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`
	vars := map[string]any{"orbId": crServerA, "set": map[string]any{"hostname": "dispatched-name"}}

	if _, err := f.gql.DispatchMutation(context.Background(), "dispatcher@test.com",
		callerRole{Role: user.RoleAdmin, Source: "user"}, gateEnforce, query, vars, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if got := readVersion(t, crServerA); got != 2 {
		t.Errorf("version = %d, want 2 — an internal dispatch was not stamped", got)
	}
	ev := waitForAuditEvent(t, f.crFixture, "updateServer")
	ch, ok := changeFor(ev.Changes, "hostname")
	if !ok {
		t.Fatalf("no field diff on a dispatch that passed before=nil: %+v", ev.Changes)
	}
	if got, _ := ch.Before.(string); got != "a-original" {
		t.Errorf("changes[hostname].before = %v, want a-original", ch.Before)
	}
}

// ── 12. a field outside the type's BeforeFields still diffs ─────────────────

// BeforeFields is a curated per-type selection; NetworkInterface's is just
// "id orbId name version". A divergence entry names its field at runtime, so
// the fetch cannot be relied on to have read it — which is why the caller's
// supplied before is merged UNDER the fetch rather than discarded by it.
func TestWritePath_AcceptOnAFieldOutsideBeforeFieldsStillDiffs(t *testing.T) {
	f := newAcceptFixture(t)
	const nicOrbID = crNS + ":network-interface-write-path"

	crGQL(t, `mutation($input:[AddNetworkInterfaceInput!]!){ addNetworkInterface(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": crNS, "orbId": nicOrbID, "name": "eth0", "version": 1, "mgmtOnly": false,
		}}})
	t.Cleanup(func() { deleteEntity(t, "NetworkInterface", nicOrbID) })

	entryID := f.seedEntry(t, nicOrbID, "mgmtOnly", "NetworkInterface", false, true)
	f.resolve(t, entryID, "accept", "nic-actor@test.com", user.RoleAdmin)

	ev := waitForAuditEvent(t, f.crFixture, "updateNetworkInterface")
	ch, ok := changeFor(ev.Changes, "mgmtOnly")
	if !ok {
		t.Fatalf("no mgmtOnly entry in changes — the fetch does not select it and the caller's before was discarded: %+v", ev.Changes)
	}
	if b, _ := ch.Before.(bool); b != false {
		t.Errorf("changes[mgmtOnly].before = %v, want false", ch.Before)
	}
	if a, _ := ch.After.(bool); a != true {
		t.Errorf("changes[mgmtOnly].after = %v, want true", ch.After)
	}
}

// ── 13. the audit event records the variables as SENT ───────────────────────

// Handle unmarshals the body for authz and the inline-selector check, then hands
// the BYTES to writeToDGraph, which parses its own copy and stamps that. The map
// Handle holds is therefore the unstamped one; auditing it would record a
// mutation orbital never sent. Silent, and invisible on every screen.
func TestWritePath_AuditVariablesCarryTheStamp(t *testing.T) {
	f := newAcceptFixture(t)
	const actor = "stamp-actor@test.com"

	c, rec := newGQLCtx(t, map[string]any{
		"query":         `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		"operationName": "UpdateServer",
		"variables":     map[string]any{"orbId": crServerA, "set": map[string]any{"hostname": "stamped-name"}},
	})
	c.Set("user_email", actor)
	c.Set("role", string(user.RoleAdmin))
	if err := f.gql.Handle(c); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	ev := waitForAuditEvent(t, f.crFixture, "updateServer")
	vars, _ := ev.Details["variables"].(map[string]any)
	set, _ := vars["set"].(map[string]any)
	if set == nil {
		t.Fatalf("no variables.set on the event: %v", ev.Details)
	}
	if v, _ := set["version"].(float64); int(v) != 2 {
		t.Errorf("details.variables.set.version = %v, want 2 — the audit row records an unstamped mutation", set["version"])
	}
	if got, _ := set["updatedBy"].(string); got != actor {
		t.Errorf("details.variables.set.updatedBy = %v, want %q", set["updatedBy"], actor)
	}
	if _, ok := set["updatedAt"].(string); !ok {
		t.Errorf("details.variables.set.updatedAt missing: %v", set)
	}
}

// ── 14. a bypassed policy is recorded from BOTH entry points ────────────────

// The gate returns the policy label through writeToDGraph and both callers stamp
// it on the audit event. A privileged write has to stay findable from the audit
// API long after the log stream has rotated — and it must NOT be marked when no
// policy was in play, or the field trains its reader to ignore it.
func TestWritePath_BypassedPolicyIsRecordedFromBothEntryPoints(t *testing.T) {
	t.Run("via /graphql", func(t *testing.T) {
		f := newAcceptFixture(t)
		f.bypassPolicy(t, "admin")

		c, rec := newGQLCtx(t, map[string]any{
			"query":         `mutation UpdateServer($orbId: String!, $set: ServerPatch!) { updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
			"operationName": "UpdateServer",
			"variables":     map[string]any{"orbId": crServerA, "set": map[string]any{"hostname": "bypassed-via-http"}},
		})
		c.Set("user_email", "admin@test.com")
		c.Set("role", string(user.RoleAdmin))
		if err := f.gql.Handle(c); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}

		ev := waitForAuditEvent(t, f.crFixture, "updateServer")
		assertBypassRecorded(t, ev, crNS)
	})

	t.Run("via DispatchMutation", func(t *testing.T) {
		f := newAcceptFixture(t)
		f.bypassPolicy(t, "admin")

		entryID := f.seedEntry(t, crServerA, "hostname", "Server", "a-original", "bypassed-via-accept")
		f.resolve(t, entryID, "accept", "admin@test.com", user.RoleAdmin)

		ev := waitForAuditEvent(t, f.crFixture, "updateServer")
		assertBypassRecorded(t, ev, crNS)
	})

	t.Run("the negative — no policy, no mark", func(t *testing.T) {
		f := newAcceptFixture(t) // deliberately no policy

		entryID := f.seedEntry(t, crServerA, "hostname", "Server", "a-original", "ungoverned")
		f.resolve(t, entryID, "accept", "admin@test.com", user.RoleAdmin)

		ev := waitForAuditEvent(t, f.crFixture, "updateServer")
		if _, marked := ev.Details["privileged"]; marked {
			t.Error("a write with no policy in play was marked privileged — the flag means nothing if it fires when nothing was bypassed")
		}
		if _, marked := ev.Details["bypassedPolicy"]; marked {
			t.Error("bypassedPolicy set with no policy in play")
		}
	})
}

func assertBypassRecorded(t *testing.T, ev auditEventView, wantPolicy string) {
	t.Helper()
	if p, _ := ev.Details["privileged"].(bool); !p {
		t.Errorf("details.privileged = %v, want true — the bypass is not queryable from the audit API", ev.Details["privileged"])
	}
	got, _ := ev.Details["bypassedPolicy"].(string)
	if !strings.Contains(got, wantPolicy) {
		t.Errorf("details.bypassedPolicy = %q, want it to name %q", got, wantPolicy)
	}
}

// ── 9. a merge still applies, stamps once, and diffs ────────────────────────

// Merge sets `version` itself. The pre-flight preserves a caller-set version, so
// moving stamping down must not turn one increment into two — and the diff it
// gained on 2026-09-01 must survive the before-fallback becoming a merge.
func TestWritePath_MergeStillAppliesAndBumpsVersionExactlyOnce(t *testing.T) {
	ctx := context.Background()
	f := newAcceptFixture(t)
	f.requireApproval(t, 1)

	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "merged-name"},
	})
	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.crh.Merge(ctx, cr.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge: %v", err)
	}

	if got := readHostname(t, crServerA); got != "merged-name" {
		t.Errorf("hostname = %q, want merged-name", got)
	}
	if got := readVersion(t, crServerA); got != 2 {
		t.Errorf("version = %d, want exactly 2 — the merge double-stamped", got)
	}

	ev := waitForAuditEvent(t, f.crFixture, "updateServer")
	ch, ok := changeFor(ev.Changes, "hostname")
	if !ok {
		t.Fatalf("merge event carries no field diff: %+v", ev.Changes)
	}
	if got, _ := ch.Before.(string); got != "a-original" {
		t.Errorf("changes[hostname].before = %v, want a-original", ch.Before)
	}
}
