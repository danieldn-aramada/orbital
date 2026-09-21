//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/approvalpolicy"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/divergenceresolution"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// seedDivergenceEntry inserts one DivergenceEntry row with the given typeName
// (empty string is allowed — exercises the legacy-fallback path) and returns
// the entry's UUID as a string.
func seedDivergenceEntry(t *testing.T, dcID, orbID, field, typeName string, overrideValue any) string {
	t.Helper()
	ctx := context.Background()
	intended, _ := json.Marshal(false)
	override, _ := json.Marshal(overrideValue)
	e, err := testDB.DivergenceEntry.Create().
		SetDcOrbID(dcID).
		SetEntryOrbID(orbID).
		SetField(field).
		SetTypeName(typeName).
		SetIntendedValue(intended).
		SetOverrideValue(override).
		SetWho("local:admin").
		SetFirstSeenAt(time.Now().UTC().Add(-2 * time.Hour)).
		SetLastSeenAt(time.Now().UTC()).
		SetLastReportPublishedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed divergence entry: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.DivergenceResolution.Delete().
			Where(divergenceresolution.EntryOrbID(orbID), divergenceresolution.Field(field)).
			Exec(ctx)
		_ = testDB.DivergenceEntry.DeleteOneID(e.ID).Exec(ctx)
	})
	return e.ID.String()
}

// newPutResolutionRequest builds the echo.Context for
// PUT /api/v1/divergences/:id/resolution with {"action": action} as the body,
// authenticated as the given admin user.
func newPutResolutionRequest(t *testing.T, entryID, action string, adminID int, actor string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	body := strings.NewReader(`{"action":"` + action + `"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/divergences/"+entryID+"/resolution", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(entryID)
	c.Set("user_id", adminID)
	c.Set("user_email", actor)
	return c, rec
}

// An entry with no type_name is no longer a dead end. The report's type_name is
// a hint; orbId is @id on the ConfigItem interface, so orbital looks the type up
// instead of telling the admin to "update intent manually" — advice the approval
// gate makes circular for exactly the classes someone bothered to protect.
//
// What remains a hard failure is an orbId orbital has never heard of. That is a
// genuinely different problem (the edge is reporting a resource orbital does not
// model) with a different remedy, and the message has to say so rather than
// blaming missing type info.
func TestAccept_UnknownOrbIDIsRejectedWithItsOwnReason(t *testing.T) {
	adminID := createTestUser(t, "accept-empty-type@test.com", user.RoleAdmin)
	entryID := seedDivergenceEntry(t, "colo:colo-galleon", "colo:not-in-the-graph", "sshEnabled", "", true)

	// A real DGraph: the type lookup must actually run and come back empty.
	gql := handler.NewGraphQL(testutil.DGraphURL(), testDB, slog.Default(), false)
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	c, _ := newPutResolutionRequest(t, entryID, "accept", adminID, "accept-empty-type@test.com")
	err := h.PutResolution(c)
	if err == nil {
		t.Fatal("expected an error for an orbId orbital does not know")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %v", err)
	}
	msg, _ := httpErr.Message.(string)
	if !strings.Contains(msg, "colo:not-in-the-graph") {
		t.Errorf("error does not name the orbId: %q", msg)
	}
	if strings.Contains(msg, "type info") {
		t.Errorf("error still blames missing type info: %q", msg)
	}
	// Resolution must NOT have been recorded.
	count := testDB.DivergenceResolution.Query().
		Where(divergenceresolution.EntryOrbID("colo:not-in-the-graph"), divergenceresolution.Field("sshEnabled")).
		CountX(context.Background())
	if count != 0 {
		t.Errorf("expected 0 resolutions, got %d", count)
	}
}

func TestAccept_DispatchesMutationAndRecordsResolution(t *testing.T) {
	adminID := createTestUser(t, "accept-success@test.com", user.RoleAdmin)
	entryID := seedDivergenceEntry(t, "colo:colo-galleon", "colo:srv-001", "sshEnabled", "Server", true)

	// Mock DGraph returning a successful updateServer response.
	var receivedBody struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"updateServer":{"numUids":1}}}`)) //nolint:errcheck
	}))
	defer dgraph.Close()
	gql := handler.NewGraphQL(dgraph.URL, testDB, slog.Default(), false)
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	c, _ := newPutResolutionRequest(t, entryID, "accept", adminID, "accept-success@test.com")
	if err := h.PutResolution(c); err != nil {
		t.Fatalf("PutResolution(accept) failed: %v", err)
	}

	// The dispatched mutation must hit the right type AND use the canonical
	// update{Kind}($orbId, $set) shape. The shape is not cosmetic: writeToDGraph
	// resolves the row to stamp from the `orbId` VARIABLE, so the $filter-object
	// form this used to send produced an unstamped write — no version bump, and
	// therefore invisible to change-request staleness. Asserting the shape is
	// asserting the stamp is reachable.
	if receivedBody.Query == "" {
		t.Fatal("expected DGraph to be called with a mutation, got nothing")
	}
	for _, want := range []string{"updateServer", "$orbId: String!", "ServerPatch!"} {
		if !strings.Contains(receivedBody.Query, want) {
			t.Errorf("mutation missing %q; got: %s", want, receivedBody.Query)
		}
	}
	if strings.Contains(receivedBody.Query, "ServerFilter") {
		t.Errorf("mutation still declares a $filter object — the row cannot be resolved for stamping: %s", receivedBody.Query)
	}
	if got, _ := receivedBody.Variables["orbId"].(string); got != "colo:srv-001" {
		t.Errorf("variables.orbId: got %q, want %q", got, "colo:srv-001")
	}
	set, _ := receivedBody.Variables["set"].(map[string]any)
	if got, _ := set["sshEnabled"].(bool); got != true {
		t.Errorf("variables.set.sshEnabled: got %v, want true", set["sshEnabled"])
	}

	// Resolution must be recorded with action=accept.
	res := testDB.DivergenceResolution.Query().
		Where(divergenceresolution.EntryOrbID("colo:srv-001"), divergenceresolution.Field("sshEnabled")).
		OnlyX(context.Background())
	if res.Action != divergenceresolution.ActionAccept {
		t.Errorf("resolution action: got %v, want accept", res.Action)
	}
	if res.Actor != "accept-success@test.com" {
		t.Errorf("resolution actor: got %q, want accept-success@test.com", res.Actor)
	}
}

func TestAccept_MutationFailureLeavesNoResolution(t *testing.T) {
	adminID := createTestUser(t, "accept-fail@test.com", user.RoleAdmin)
	entryID := seedDivergenceEntry(t, "colo:colo-galleon", "colo:srv-002", "sshEnabled", "Server", true)

	// DGraph returns an error in the GraphQL `errors` array.
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"errors":[{"message":"resolver error: server not found"}]}`)) //nolint:errcheck
	}))
	defer dgraph.Close()
	gql := handler.NewGraphQL(dgraph.URL, testDB, slog.Default(), false)
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	c, _ := newPutResolutionRequest(t, entryID, "accept", adminID, "accept-fail@test.com")
	err := h.PutResolution(c)
	if err == nil {
		t.Fatal("expected error when DGraph returns gql errors, got nil")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %v", err)
	}
	// Resolution row must NOT exist.
	count := testDB.DivergenceResolution.Query().
		Where(divergenceresolution.EntryOrbID("colo:srv-002"), divergenceresolution.Field("sshEnabled")).
		CountX(context.Background())
	if count != 0 {
		t.Errorf("expected no resolution after mutation failure, got %d", count)
	}
	// The DivergenceEntry itself stays put (not deleted) so the admin can retry.
	if !testDB.DivergenceEntry.Query().Where(divergenceentry.EntryOrbID("colo:srv-002")).ExistX(context.Background()) {
		t.Error("expected entry to still exist after failed Accept")
	}
}

// TestList_ActionFilter_PartitionsIgnoreFromAcceptReject pins the contract that
// cb-bundler relies on for the local:admin-ownership semantic:
//
//   - GET /api/v1/divergences?action=accept&action=reject returns ONLY accept
//     and reject rows (these become spec.takeover[] → cb-controller releases
//     local:admin's claim).
//   - GET /api/v1/divergences?action=ignore returns ONLY ignore rows (these
//     become Omissions → bundler nils the field → cb-controller does not
//     re-claim → local:admin retains ownership).
//
// Regression class: if a refactor of the action-filter logic let an Ignore row
// leak into the accept|reject result, cb-controller would release local:admin's
// claim on an Ignored field — silently violating "Ignore retains ownership."
// Not caught by clicking; only visible after a full bundle/apply cycle.
func TestList_ActionFilter_PartitionsIgnoreFromAcceptReject(t *testing.T) {
	ctx := context.Background()
	dc := "colo:colo-list-filter"

	// Seed one entry per action with a resolution row attached.
	type seed struct {
		orbID, field string
		action       divergenceresolution.Action
	}
	seeds := []seed{
		{"colo:srv-list-accept", "sshEnabled", divergenceresolution.ActionAccept},
		{"colo:srv-list-reject", "ipmiEnabled", divergenceresolution.ActionReject},
		{"colo:srv-list-ignore", "dhcpEnabled", divergenceresolution.ActionIgnore},
	}
	for _, s := range seeds {
		seedDivergenceEntry(t, dc, s.orbID, s.field, "IdracSettings", true)
		_, err := testDB.DivergenceResolution.Create().
			SetEntryOrbID(s.orbID).
			SetField(s.field).
			SetAction(s.action).
			SetActor("list-filter@test.com").
			SetDecidedAt(time.Now().UTC()).
			Save(ctx)
		if err != nil {
			t.Fatalf("seed %s resolution: %v", s.action, err)
		}
	}

	gql := handler.NewGraphQL("http://unused", testDB, slog.Default(), false)
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	listForActions := func(t *testing.T, query string) map[string]bool {
		t.Helper()
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/divergences?"+query+"&dc="+dc, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if err := h.List(c); err != nil {
			t.Fatalf("List(%s): %v", query, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("List(%s): status %d", query, rec.Code)
		}
		var items []struct {
			EntryOrbID string `json:"entryOrbId"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatalf("decode (%s): %v", query, err)
		}
		got := map[string]bool{}
		for _, it := range items {
			got[it.EntryOrbID] = true
		}
		return got
	}

	// action=accept|reject — must include accept+reject, must exclude ignore.
	got := listForActions(t, "action=accept&action=reject")
	for _, want := range []string{"colo:srv-list-accept", "colo:srv-list-reject"} {
		if !got[want] {
			t.Errorf("action=accept|reject: missing %s in response %v", want, got)
		}
	}
	if got["colo:srv-list-ignore"] {
		t.Errorf("action=accept|reject MUST NOT include the Ignore row — would silently strip local:admin ownership. got %v", got)
	}

	// action=ignore — must include ignore, must exclude accept+reject.
	got = listForActions(t, "action=ignore")
	if !got["colo:srv-list-ignore"] {
		t.Errorf("action=ignore: missing the Ignore row, got %v", got)
	}
	for _, mustNot := range []string{"colo:srv-list-accept", "colo:srv-list-reject"} {
		if got[mustNot] {
			t.Errorf("action=ignore MUST NOT include accept/reject row %s, got %v", mustNot, got)
		}
	}
}

// TestList_ActionFilter_BatchAcceptAndRejectOnSameConfigItem pins that the
// List handler returns batched decisions on sibling fields of the same
// ConfigItem correctly. Under ADR 012, resolutions are not subject to any
// post-decision staleness check — anything in the divergence_resolutions table
// is by construction current (the ingester wipes resolutions on supersede).
// The test exists to lock the action-filter contract: each row appears with
// its own decision; sibling rows don't shadow each other.
func TestList_ActionFilter_BatchAcceptAndRejectOnSameConfigItem(t *testing.T) {
	ctx := context.Background()
	dc := "colo:colo-batch-mvcc"
	orbID := "colo:srv-batch-mvcc-idrac"

	// Reject sshEnabled (intent=false stays); Accept ipmiEnabled (intent → true).
	falseV, _ := json.Marshal(false)
	trueV, _ := json.Marshal(true)
	for _, s := range []struct {
		field  string
		action divergenceresolution.Action
	}{
		{"sshEnabled", divergenceresolution.ActionReject},
		{"ipmiEnabled", divergenceresolution.ActionAccept},
	} {
		e2, err := testDB.DivergenceEntry.Create().
			SetDcOrbID(dc).
			SetEntryOrbID(orbID).
			SetField(s.field).
			SetTypeName("IdracSettings").
			SetIntendedValue(falseV).
			SetOverrideValue(trueV).
			SetWho("local:admin").
			SetFirstSeenAt(time.Now().UTC().Add(-1 * time.Hour)).
			SetLastSeenAt(time.Now().UTC()).
			SetLastReportPublishedAt(time.Now().UTC()).
			Save(ctx)
		if err != nil {
			t.Fatalf("seed %s entry: %v", s.field, err)
		}
		t.Cleanup(func() {
			_, _ = testDB.DivergenceResolution.Delete().
				Where(divergenceresolution.EntryOrbID(orbID), divergenceresolution.Field(s.field)).
				Exec(ctx)
			_ = testDB.DivergenceEntry.DeleteOneID(e2.ID).Exec(ctx)
		})
		if _, err := testDB.DivergenceResolution.Create().
			SetEntryOrbID(orbID).
			SetField(s.field).
			SetAction(s.action).
			SetActor("admin@test.com").
			SetDecidedAt(time.Now().UTC()).
			Save(ctx); err != nil {
			t.Fatalf("seed %s resolution: %v", s.field, err)
		}
	}

	// Mock DGraph: ipmiEnabled=true (Accept worked), sshEnabled=false (Reject's
	// expected intent unchanged). Both should pass the per-field check.
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "sshEnabled"):
			w.Write([]byte(`{"data":{"getIdracSettings":{"sshEnabled":false}}}`)) //nolint:errcheck
		case strings.Contains(string(body), "ipmiEnabled"):
			w.Write([]byte(`{"data":{"getIdracSettings":{"ipmiEnabled":true}}}`)) //nolint:errcheck
		default:
			w.Write([]byte(`{"data":{"getIdracSettings":{}}}`)) //nolint:errcheck
		}
	}))
	defer dgraph.Close()

	gql := handler.NewGraphQL(dgraph.URL, testDB, slog.Default(), false)
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/divergences?action=accept&action=reject&dc="+dc, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	var items []struct {
		Field      string `json:"field"`
		Resolution *struct {
			Action string `json:"action"`
		} `json:"resolution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Accept + Reject on sibling fields must both survive; got %d items: %v",
			len(items), items)
	}
	gotFields := map[string]string{}
	for _, it := range items {
		if it.Resolution == nil {
			t.Fatalf("item %s has no resolution", it.Field)
		}
		gotFields[it.Field] = it.Resolution.Action
	}
	if gotFields["sshEnabled"] != "reject" {
		t.Errorf("sshEnabled should be reject, got %q (would re-introduce the user-reported bug — sshEnabled lost when batched with sibling Accept)", gotFields["sshEnabled"])
	}
	if gotFields["ipmiEnabled"] != "accept" {
		t.Errorf("ipmiEnabled should be accept, got %q", gotFields["ipmiEnabled"])
	}
}

// ── the approval gate meets divergence resolution ──────────────────────────

// Reject and Ignore never touch intent, so gating them would be friction with
// no control value: it would make an operator open a change request to decline
// a change.
func TestResolve_RejectAndIgnoreAreNeverGated(t *testing.T) {
	ctx := context.Background()
	adminID := createTestUser(t, "gate-reject@test.com", user.RoleAdmin)
	seedGatePolicy(t, "colo")

	gql := handler.NewGraphQL(testutil.DGraphURL(), testDB, slog.Default(), false)
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)
	h.SetChangeRequests(handler.NewChangeRequest(testDB, gql, testutil.DGraphURL(), slog.Default()))

	for _, action := range []string{"reject", "ignore"} {
		orbID := "colo:gate-" + action
		entryID := seedDivergenceEntry(t, "colo:colo-galleon", orbID, "sshEnabled", "IdracSettings", true)
		c, rec := newPutResolutionRequest(t, entryID, action, adminID, "gate-reject@test.com")
		if err := h.PutResolution(c); err != nil {
			t.Fatalf("%s was gated: %v", action, err)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("%s returned %d, want 200", action, rec.Code)
		}
		n := testDB.DivergenceResolution.Query().
			Where(divergenceresolution.EntryOrbID(orbID), divergenceresolution.Field("sshEnabled")).
			CountX(ctx)
		if n != 1 {
			t.Errorf("%s recorded %d resolutions, want 1", action, n)
		}
	}
}

// A gated Accept must NOT mutate and must NOT record a resolution — intent has
// not moved, and claiming otherwise would leave an entry marked resolved while
// the edge keeps diverging. It opens a change request instead and says so.
func TestAccept_GatedOpensChangeRequestAndLeavesEntryPending(t *testing.T) {
	ctx := context.Background()
	devID := createTestUser(t, "gate-accept@test.com", user.RoleDev)
	seedGatePolicy(t, "colo")

	orbID := seedGateTargetServer(t)
	entryID := seedDivergenceEntry(t, "colo:colo-galleon", orbID, "hostname", "Server", "edge-set-name")

	gql := handler.NewGraphQL(testutil.DGraphURL(), testDB, slog.Default(), false)
	crh := handler.NewChangeRequest(testDB, gql, testutil.DGraphURL(), slog.Default())
	h := handler.NewDivergenceHandler(testDB, slog.Default(), gql)
	h.SetChangeRequests(crh)

	c, rec := newPutResolutionRequest(t, entryID, "accept", devID, "gate-accept@test.com")
	if err := h.PutResolution(c); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 Accepted", rec.Code)
	}

	var body struct {
		Status          string `json:"status"`
		ChangeRequestID string `json:"changeRequestId"`
		Message         string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "pending_approval" || body.ChangeRequestID == "" {
		t.Fatalf("body = %+v, want pending_approval with a change request id", body)
	}

	// No resolution: the divergence is still open.
	if n := testDB.DivergenceResolution.Query().
		Where(divergenceresolution.EntryOrbID(orbID)).CountX(ctx); n != 0 {
		t.Errorf("recorded %d resolutions for a gated accept, want 0", n)
	}

	// The change request is real, and carries the edge's value as its proposal.
	// The API hands back the human id (namespace-number). This test is in the
	// _test package, so it resolves it the way an external client would rather
	// than reaching for the unexported parser.
	i := strings.LastIndex(body.ChangeRequestID, "-")
	if i <= 0 {
		t.Fatalf("bad change request id %q", body.ChangeRequestID)
	}
	num, err := strconv.Atoi(body.ChangeRequestID[i+1:])
	if err != nil {
		t.Fatalf("bad change request id %q: %v", body.ChangeRequestID, err)
	}
	row, err := testDB.ApprovalRequest.Query().
		Where(approvalrequest.NamespaceEQ(body.ChangeRequestID[:i]),
			approvalrequest.NumberEQ(num)).Only(ctx)
	if err != nil {
		t.Fatalf("resolve %q: %v", body.ChangeRequestID, err)
	}
	cr, err := crh.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("load change request: %v", err)
	}
	var payload struct {
		Namespace string `json:"namespace"`
		Changes   []struct {
			OrbID string         `json:"orbId"`
			Type  string         `json:"type"`
			Op    string         `json:"op"`
			Set   map[string]any `json:"set"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(cr.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(payload.Changes))
	}
	ch := payload.Changes[0]
	if ch.OrbID != orbID || ch.Type != "Server" || ch.Op != "update" {
		t.Errorf("change = %+v, want an update of %s", ch, orbID)
	}
	if ch.Set["hostname"] != "edge-set-name" {
		t.Errorf("proposed value = %v, want the edge's value", ch.Set["hostname"])
	}
	if cr.Author != "gate-accept@test.com" {
		t.Errorf("author = %q, want the operator who clicked accept", cr.Author)
	}
}

// seedGatePolicy makes a namespace protected. Idempotent and self-cleaning:
// this package shares one testDB across the whole run (TestMain, not a
// per-test truncate), so a policy left behind would leak into every later test
// and gate mutations they never asked to have gated.
func seedGatePolicy(t *testing.T, namespace string) {
	t.Helper()
	ctx := context.Background()
	clear := func() {
		if _, err := testDB.ApprovalPolicy.Delete().
			Where(approvalpolicy.NamespaceEQ(namespace)).Exec(ctx); err != nil {
			t.Logf("clear approval policies: %v", err)
		}
	}
	clear()
	if _, err := testDB.ApprovalPolicy.Create().
		SetActionType("config.mutation").
		SetNamespace(namespace).
		SetRequiredApprovals(1).
		Save(ctx); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	t.Cleanup(clear)
}

// seedGateTargetServer creates a real Server so the accept path has something to
// resolve and diff against.
func seedGateTargetServer(t *testing.T) string {
	t.Helper()
	const dc = "colo:gate-dc"
	const srv = "colo:gate-server"
	// Inline literals are fine here: this posts straight to DGraph, never
	// through orbital, so the gate's variable-form requirement does not apply.
	dgraphMutate(t, `mutation { addDataCenter(input: [{namespace: "colo", orbId: "`+dc+`", name: "gate dc", version: 1}], upsert: true) { numUids } }`)
	dgraphMutate(t, `mutation { addServer(input: [{namespace: "colo", orbId: "`+srv+`", version: 1, hostname: "orbital-intended-name", dataCenter: {orbId: "`+dc+`"}}], upsert: true) { numUids } }`)
	t.Cleanup(func() {
		dgraphMutate(t, `mutation { deleteServer(filter: {orbId: {eq: "`+srv+`"}}) { numUids } }`)
		dgraphMutate(t, `mutation { deleteDataCenter(filter: {orbId: {eq: "`+dc+`"}}) { numUids } }`)
	})
	return srv
}
