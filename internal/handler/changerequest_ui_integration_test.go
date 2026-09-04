//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"

	"strings"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/armada/orbital/internal/web/data/page"
	webtemplates "github.com/armada/orbital/web/templates/orbital"
	"github.com/labstack/echo/v4"
)

// Session 3 acceptance criteria, transcribed from
// the session-3 acceptance list (see docs/reference/CHANGE-CONTROL.md) — one item, one test name, written
// BEFORE any body. A dropped item shows up here as a failing stub rather than
// as a quietly weaker assertion inside something that passes.
//
// Go-level items only. e2e items live in e2e/change-requests.spec.ts; AC 20 is
// the curl sweep at the end of the session.

// AC 1
func TestUINav_ChangeControlSectionVisibilityByRole(t *testing.T) {
	ui := &UI{basePath: ""}

	find := func(role string) (section bool, items []string) {
		for _, s := range ui.buildMenuSections("/", role, 0) {
			if s.Title != "Change Control" {
				continue
			}
			section = true
			for _, it := range s.Items {
				items = append(items, it.Label)
			}
		}
		return
	}

	// Both items are readonly+, matching the API: the queue and the policy list
	// are served to anyone authenticated. Only MUTATING policies is admin, and
	// that gate lives on the controls, not on visibility — hiding the page from
	// a dev would hide the answer to "why was my change gated".
	for _, role := range []string{"readonly", "dev", "admin"} {
		ok, items := find(role)
		if !ok {
			t.Fatalf("%s cannot see the Change Control section at all", role)
		}
		if len(items) != 2 || items[0] != "Change Requests" || items[1] != "Approval Policies" {
			t.Errorf("%s sees %v, want both items", role, items)
		}
	}
}

// AC 2 — the badge is asynchronous, so the Go half is that the menu points at
// the RIGHT endpoint (the same one the item links to), and that the endpoint
// returns the count for THIS caller. The JS reads `total` from that response,
// which is why the badge and the page it opens cannot disagree.
func TestUINav_ChangeRequestsBadgeMatchesAwaitingReviewTotal(t *testing.T) {
	ctx := context.Background()
	ui := &UI{basePath: ""}

	var src string
	for _, s := range ui.buildMenuSections("/", "dev", 0) {
		for _, it := range s.Items {
			if it.Label == "Change Requests" {
				src = it.BadgeSrc
				if it.Badge != 0 {
					t.Error("the badge is computed synchronously — that makes every page load pay for a change-request scan")
				}
			}
		}
	}
	if src != "/api/v1/change-requests?awaiting_review=true" {
		t.Fatalf("BadgeSrc = %q, want the awaiting-review query", src)
	}

	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "needs-a-reviewer"}})

	// The author is not a reviewer of their own request, so their badge is 0
	// while a peer's is 1. A count that ignored that would send everyone to an
	// empty queue.
	if n := awaitingTotal(t, f, author); n != 0 {
		t.Errorf("the author's awaiting-review count is %d, want 0", n)
	}
	if n := awaitingTotal(t, f, reviewer); n != 1 {
		t.Errorf("a peer's awaiting-review count is %d, want 1", n)
	}

	if _, err := f.crh.Approve(ctx, cr.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// Once they have reviewed it, it is no longer awaiting THEIR review.
	if n := awaitingTotal(t, f, reviewer); n != 0 {
		t.Errorf("count is %d after reviewing, want 0", n)
	}
}

// The UI gate must match the API's, in BOTH directions. Stricter hides
// information the API would serve; looser offers controls the API refuses. The
// read is readonly+ (apiReadonly), the writes are admin (adminAPI).
func TestApprovalPoliciesPage_EveryoneReadsAdminWrites(t *testing.T) {
	for _, tc := range []struct {
		role         string
		wantControls bool
	}{
		{"admin", true},
		{"dev", false},
		{"readonly", false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			html := renderPageAs(t, "approval-policies", tc.role)

			// Everyone authenticated can SEE the policies: the API serves them to
			// readonly+, and a UI gate stricter than the API's hides from a dev
			// the answer to "why was my change gated" — the question this page
			// exists to answer.
			if !strings.Contains(html, `id="ap-tbody"`) {
				t.Errorf("%s cannot see the policy table, but the API would serve it", tc.role)
			}

			hasAdd := strings.Contains(html, `id="ap-add"`)
			hasModal := strings.Contains(html, `id="ap-modal"`)
			if tc.wantControls && !(hasAdd && hasModal) {
				t.Errorf("admin sees no write controls: add=%v modal=%v", hasAdd, hasModal)
			}
			if !tc.wantControls {
				if hasAdd || hasModal {
					t.Errorf("%s is shown write controls the API would refuse", tc.role)
				}
				if !strings.Contains(html, "Read-only") {
					t.Errorf("%s is not told why they have no controls", tc.role)
				}
			}
		})
	}
}

func awaitingTotal(t *testing.T, f *crFixture, actor string) int {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/change-requests?awaiting_review=true", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_email", actor)
	c.Set("role", string(user.RoleDev))
	if err := f.crh.ListChangeRequests(c); err != nil {
		t.Fatalf("list: %v", err)
	}
	var out changeRequestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Total
}

// AC 16
func TestListFilter_StatusActiveReturnsOpenAndApprovedOnly(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	openCR := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "still-open"}})

	approvedCR := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "approved-not-merged"}})
	if _, err := f.crh.Approve(ctx, approvedCR.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	closedCR := f.open(t, approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate,
		Set: map[string]any{"firmwareVersion": "9.9.9"}})
	if _, err := f.crh.Close(ctx, closedCR.ID, author, user.RoleDev); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := listIDs(t, f, "?status=active")
	if len(got) != 2 || !got[crHumanID(openCR)] || !got[crHumanID(approvedCR)] {
		t.Fatalf("status=active returned %v, want the open and the approved request", keysOf(got))
	}
	if got[crHumanID(closedCR)] {
		t.Error("a closed request came back as active")
	}

	// The narrower filters must still split the two, or `active` would be the
	// only usable one.
	if ids := listIDs(t, f, "?status=open"); !ids[crHumanID(openCR)] || ids[crHumanID(approvedCR)] {
		t.Errorf("status=open returned %v", keysOf(ids))
	}
	if ids := listIDs(t, f, "?status=approved"); ids[crHumanID(openCR)] || !ids[crHumanID(approvedCR)] {
		t.Errorf("status=approved returned %v", keysOf(ids))
	}
}

// AC 17 — the filters moved from a post-render loop into SQL. The claim being
// preserved is WHICH rows come back, so that is what is asserted; the ordering
// change is only observable in cost (AC 18).
func TestListFilter_SQLPushdownReturnsTheSameResults(t *testing.T) {
	f := newCRFixture(t)

	a := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "a"}})
	b := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "b"}})

	if ids := listIDs(t, f, "?orbId="+crServerA); !ids[crHumanID(a)] || ids[crHumanID(b)] {
		t.Errorf("orbId=%s returned %v, want only the request touching it", crServerA, keysOf(ids))
	}
	if ids := listIDs(t, f, "?namespace="+crNS); !ids[crHumanID(a)] || !ids[crHumanID(b)] {
		t.Errorf("namespace=%s returned %v, want both", crNS, keysOf(ids))
	}
	if ids := listIDs(t, f, "?namespace=some-other-namespace"); len(ids) != 0 {
		t.Errorf("a foreign namespace returned %v, want none", keysOf(ids))
	}
	if ids := listIDs(t, f, "?orbId="+crNS+":does-not-exist"); len(ids) != 0 {
		t.Errorf("an unmatched orbId returned %v, want none", keysOf(ids))
	}
}

// AC 18 — the whole reason the filters moved into SQL.
//
// Rendering a request derives staleness, which costs a subtree query and a hash
// per request. The pending-change badge fires on every detail view and almost
// always matches nothing, so the common case has to touch DGraph zero times.
// Asserted on a counting stub rather than by reading the code, because the
// regression is invisible: move the filter back after render() and every result
// stays correct while the badge quietly becomes the most expensive call in the
// product.
func TestListFilter_NonMatchingOrbIDQueryMakesZeroDGraphCalls(t *testing.T) {
	f := newCRFixture(t)
	f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "in-flight"}})

	var calls atomic.Int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{}}`)) //nolint:errcheck
	}))
	defer stub.Close()

	// Pointed at the stub, so any render at all both increments the counter and
	// fails the request — two independent ways for a regression to show.
	badge := NewChangeRequest(f.db, NewGraphQL(stub.URL, f.db, f.crh.logger, false), stub.URL, f.crh.logger)

	rec := callList(t, badge, "?orbId="+crNS+":server-nothing-pending&status=active")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out changeRequestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 0 {
		t.Fatalf("total = %d, want 0", out.Total)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the badge query made %d DGraph calls, want 0 — the filter is running after render()", n)
	}
}

// AC 19 — jsonb containment descends into arrays, so a match on the SECOND item
// of a changeset must work. A naive equality on the first element would pass
// every single-item test and silently hide multi-item requests from the badge.
func TestListFilter_OrbIDMatchesAnyItemInTheChangeset(t *testing.T) {
	f := newCRFixture(t)

	cr := f.open(t,
		approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "first"}},
		approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "second"}},
	)

	for _, orbID := range []string{crServerA, crServerB} {
		if ids := listIDs(t, f, "?orbId="+orbID); !ids[crHumanID(cr)] {
			t.Errorf("orbId=%s did not match the changeset that names it at position %d", orbID, len(ids))
		}
	}
	if ids := listIDs(t, f, "?orbId="+crIdracA); len(ids) != 0 {
		t.Errorf("orbId=%s matched a changeset that does not name it: %v", crIdracA, keysOf(ids))
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func callList(t *testing.T, h *ChangeRequest, query string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/change-requests"+query, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_email", author)
	c.Set("role", string(user.RoleDev))
	if err := h.ListChangeRequests(c); err != nil {
		t.Fatalf("list%s: %v", query, err)
	}
	return rec
}

func listIDs(t *testing.T, f *crFixture, query string) map[string]bool {
	t.Helper()
	rec := callList(t, f.crh, query)
	var out changeRequestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != len(out.Items) {
		t.Errorf("total = %d but %d items — the badge reads total, so they must agree", out.Total, len(out.Items))
	}
	ids := make(map[string]bool, len(out.Items))
	for _, it := range out.Items {
		ids[it.ID] = true
	}
	return ids
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// renderPageAs executes a page template with a caller of the given role, and
// returns the HTML. Used to assert the UI's own role gate, which must match the
// API's minimum — a page that shows controls the API refuses is a worse
// experience than one that shows nothing.
func renderPageAs(t *testing.T, name, role string) string {
	t.Helper()
	// webtemplates.Map() resolves template paths relative to the repo root, and
	// `go test` runs in the package directory. Same reason schemaPath() exists.
	_, file, _, _ := runtime.Caller(0)
	t.Chdir(filepath.Join(filepath.Dir(file), "..", ".."))

	tmpl, ok := webtemplates.Map()[name]
	if !ok {
		t.Fatalf("template %q is not registered", name)
	}
	data := page.ApprovalPolicies{
		Base: layout.Base{
			IsAuthn: true,
			User:    layout.User{Role: role},
		},
		PageTitle: "Approval Policies",
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "page", data); err != nil {
		t.Fatalf("render %s as %s: %v", name, role, err)
	}
	return buf.String()
}

// ── approval-policy selector validation ────────────────────────────────────
//
// A policy naming a namespace or type that does not exist is accepted by the
// database, renders in the table, and reports itself ENFORCED — while gating
// zero writes. One transposed character produces it. Checked in the API rather
// than only in the form, because the form is one client and an integrator
// POSTing JSON has no dropdown.

func TestApprovalPolicy_RefusesANamespaceThatHoldsNothing(t *testing.T) {
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{"namespace": "cr-engien", "requiredApprovals": 1}) // transposed
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — a policy for a nonexistent namespace was accepted", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "cr-engien") {
		t.Errorf("the error does not name the namespace, so the typo is not obvious: %s", body)
	}
	if n := f.db.ApprovalPolicy.Query().CountX(context.Background()); n != 0 {
		t.Errorf("%d policies were stored despite the refusal", n)
	}
}

func TestApprovalPolicy_RefusesATypeThatIsNotAConfigItem(t *testing.T) {
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{
		"namespace": crNS, "allTypes": false, "types": []string{"Server", "Serverr"}, "requiredApprovals": 1,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Serverr") {
		t.Errorf("the error does not name the bad type: %s", rec.Body.String())
	}
}

func TestApprovalPolicy_AcceptsARealNamespaceAndType(t *testing.T) {
	f := newCRFixture(t)

	if rec := postPolicy(t, f, map[string]any{"namespace": crNS, "requiredApprovals": 1}); rec.Code != http.StatusCreated {
		t.Fatalf("a valid namespace-wide policy was refused: %d %s", rec.Code, rec.Body.String())
	}
	// Same namespace, so this one collides — one policy per namespace is the
	// model, and changing which types it covers is an edit to that policy.
	rec := postPolicy(t, f, map[string]any{
		"namespace": crNS, "allTypes": false, "types": []string{"Server", "Rack"}, "requiredApprovals": 1,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a second policy for the same namespace was accepted: %s", rec.Code, rec.Body.String())
	}
}

// all_types and types are an either/or. Both contradictory shapes are refused
// rather than one silently winning, because a stored row that says two things
// leaves nobody able to answer "what does this policy protect?" — and the
// operator who wrote it believes whichever half they typed last.
//
// The database enforces this too (see the CHECK constraint test in
// internal/approval). This asserts the API says WHICH rule was broken, which a
// constraint violation cannot.
func TestApprovalPolicy_RefusesContradictoryScopes(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{
		"namespace": crNS, "allTypes": true, "types": []string{"Server"}, "requiredApprovals": 1,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("all-types-plus-a-list: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	rec = postPolicy(t, f, map[string]any{
		"namespace": crNS, "allTypes": false, "types": []string{}, "requiredApprovals": 1,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("nothing-selected: status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	if n := f.db.ApprovalPolicy.Query().CountX(ctx); n != 0 {
		t.Errorf("%d policies stored despite both being refused", n)
	}
}

// An EMPTY bypassRoles means "nobody bypasses, including admins" and must not
// collapse into the default. Testing len() > 0 instead of nil silently restores
// the admin bypass an operator just unchecked — a control that looks stricter
// than it is, which is the same class of lie as one that looks enforced and
// is not.
func TestApprovalPolicy_EmptyBypassRolesIsDistinctFromOmitted(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{"namespace": crNS, "requiredApprovals": 1, "bypassRoles": []string{}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	p := f.db.ApprovalPolicy.Query().FirstX(ctx)
	if len(p.BypassRoles) != 0 {
		t.Fatalf("bypass_roles = %v, want empty — an explicit 'nobody' was overwritten with the default", p.BypassRoles)
	}

	// And the gate must honour it: an admin is now NOT exempt.
	gql := NewGraphQL(testutil.DGraphURL(), f.db, f.crh.logger, false)
	q, v := updateHostname(crServerA, "admin-should-be-gated")
	if err := mutate(t, gql, adminCaller(), q, v); err == nil {
		t.Fatal("an admin bypassed a policy whose bypass_roles is explicitly empty")
	}
}

func postPolicy(t *testing.T, f *crFixture, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approval-policies", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_email", author)
	c.Set("role", string(user.RoleAdmin))
	if err := f.crh.CreateApprovalPolicy(c); err != nil {
		t.Fatalf("CreateApprovalPolicy: %v", err)
	}
	return rec
}

func callResolve(t *testing.T, f *crFixture, namespace string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/approval-policies/resolve?namespace="+namespace, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_email", author)
	c.Set("role", string(user.RoleDev))
	if err := f.crh.ResolveApprovalPolicy(c); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return rec
}

// ORBITAL_CHANGE_CONTROL_ENABLED is the "not for me" toggle: an adopter running
// their own change management (ServiceNow, an internal process) would otherwise
// have two systems answering "was this approved", and anyone using orbital's
// flow makes that change invisible to their org's audit.
//
// What must hold when it is off: the surface disappears, the gate does not run,
// and NOTHING is deleted. The last one is the dangerous half — someone turning
// it off to "reset" the feature must not lose an audit trail.
func TestChangeControlToggle_HidesTheSurfaceAndKeepsTheData(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)
	cr := f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "written-before-disabling"},
	})

	t.Cleanup(func() { SetChangeControlEnabled(true) })

	// Server startup resolves the hierarchy: change control off means the gate
	// is off whatever ORBITAL_APPROVAL_GATE_ENABLED says. Enforcing writes while
	// offering no way to propose a change is a state with no coherent meaning.
	SetChangeControlEnabled(false)

	// 1. Nothing is gated.
	gql := NewGraphQL(testutil.DGraphURL(), f.db, f.crh.logger, false)
	q, v := updateHostname(crServerA, "ungated-while-off")
	if err := mutate(t, gql, devCaller(), q, v); err != nil {
		t.Fatalf("a write was gated with change control disabled: %v", err)
	}

	// 2. The nav offers no second place to ask "was this approved".
	ui := &UI{basePath: ""}
	for _, role := range []string{"readonly", "dev", "admin"} {
		for _, sec := range ui.buildMenuSections("/", role, 0) {
			if sec.Title == "Change Control" {
				t.Errorf("%s is still shown the Change Control section", role)
			}
		}
	}

	// 3. And nothing was deleted. This is the half that would hurt: the request
	//    and its policy are still there, and come back when it is switched on.
	if n := f.db.ApprovalRequest.Query().CountX(ctx); n != 1 {
		t.Errorf("change requests = %d, want 1 — disabling the feature deleted data", n)
	}
	if n := f.db.ApprovalPolicy.Query().CountX(ctx); n != 1 {
		t.Errorf("policies = %d, want 1 — disabling the feature deleted data", n)
	}

	SetChangeControlEnabled(true)
	back, err := f.crh.Get(ctx, cr.ID)
	if err != nil {
		t.Fatalf("the change request did not survive a disable/enable cycle: %v", err)
	}
	if back.Title != "test change" {
		t.Errorf("title = %q after the cycle", back.Title)
	}
	if err := mutate(t, gql, devCaller(), q, v); err == nil {
		t.Error("enforcement did not resume when change control was re-enabled")
	}
}

// ── Policy administration is audited ────────────────────────────────────────
//
// Policy administration decides what needs review at all, so it is the most
// consequential act in change control — and it was the one part of the feature
// leaving no trace. A write that BYPASSED a policy was audited; deleting the
// policy that would have gated it was not.
//
// Every assertion below reads back through GET /api/v1/audit-log, not the
// table: the guarantee is that an operator can answer "who changed the gate"
// from the API, and grepping the process log or the rows would prove neither.

// AC 1 — creating a policy is recorded, with the actor.
func TestApprovalPolicyAudit_CreateIsRecordedAndReadableFromTheAPI(t *testing.T) {
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{"namespace": crNS, "requiredApprovals": 2})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	ev := findAuditEvent(t, f, "createApprovalPolicy")
	if ev.Actor == "" {
		t.Error("the event names no actor — 'who changed the gate' is the question it exists to answer")
	}
	after, _ := ev.Details["after"].(map[string]any)
	if after == nil {
		t.Fatalf("no 'after' in details: %v", ev.Details)
	}
	if got := after["requiredApprovals"]; got != float64(2) {
		t.Errorf("after.requiredApprovals = %v, want 2", got)
	}
	if got := ev.Details["namespace"]; got != crNS {
		t.Errorf("namespace = %v, want %q", got, crNS)
	}
}

// AC 2 + AC 4 — an update records before AND after, and says outright when
// enforcement stopped.
func TestApprovalPolicyAudit_UpdateRecordsBeforeAndAfterAndFlagsDisabling(t *testing.T) {
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{"namespace": crNS, "requiredApprovals": 2})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	id := f.db.ApprovalPolicy.Query().FirstX(context.Background()).ID.String()

	if rec := patchPolicy(t, f, id, map[string]any{"enabled": false}); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}

	ev := findAuditEvent(t, f, "updateApprovalPolicy")
	before, _ := ev.Details["before"].(map[string]any)
	after, _ := ev.Details["after"].(map[string]any)
	if before == nil || after == nil {
		t.Fatalf("before/after missing: %v", ev.Details)
	}
	if before["enabled"] != true || after["enabled"] != false {
		t.Errorf("enabled before=%v after=%v, want true→false", before["enabled"], after["enabled"])
	}
	if ev.Details["enforcementStopped"] != true {
		t.Error("enforcementStopped is not set — the one change that stops the gate applying must not need spotting in a five-field diff")
	}
	// AC 4's negative: the flag must not fire when enforcement did not stop.
	if rec := patchPolicy(t, f, id, map[string]any{"requiredApprovals": 3}); rec.Code != http.StatusOK {
		t.Fatalf("bump: %d", rec.Code)
	}
	ev = findAuditEvent(t, f, "updateApprovalPolicy")
	if ev.Details["enforcementStopped"] == true {
		t.Error("enforcementStopped fired on a change that did not stop enforcement — a flag that cries wolf trains readers to ignore it")
	}
}

// AC 3 — after a delete the audit event is the ONLY record the policy existed,
// so it must be reconstructable from it.
func TestApprovalPolicyAudit_DeleteRecordsEnoughToReconstructThePolicy(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)

	rec := postPolicy(t, f, map[string]any{
		"namespace": crNS, "allTypes": false, "types": []string{"Server", "Rack"},
		"requiredApprovals": 3, "bypassRoles": []string{},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	id := f.db.ApprovalPolicy.Query().FirstX(ctx).ID.String()

	if rec := deletePolicy(t, f, id); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.db.ApprovalPolicy.Query().CountX(ctx); n != 0 {
		t.Fatalf("policy still present after delete: %d", n)
	}

	ev := findAuditEvent(t, f, "deleteApprovalPolicy")
	before, _ := ev.Details["before"].(map[string]any)
	if before == nil {
		t.Fatalf("no 'before' in details: %v", ev.Details)
	}
	types, _ := before["types"].([]any)
	if len(types) != 2 || types[0] != "Server" || types[1] != "Rack" {
		t.Errorf("before.types = %v, want [Server Rack]", before["types"])
	}
	if before["requiredApprovals"] != float64(3) {
		t.Errorf("before.requiredApprovals = %v, want 3", before["requiredApprovals"])
	}
	if roles, ok := before["bypassRoles"].([]any); !ok || len(roles) != 0 {
		t.Errorf("before.bypassRoles = %v, want [] — an explicit 'nobody bypasses' must survive as itself, not as null", before["bypassRoles"])
	}
	if before["namespace"] != crNS || before["allTypes"] != false {
		t.Errorf("before namespace=%v allTypes=%v", before["namespace"], before["allTypes"])
	}
}

// AC 5 — a REFUSED write records nothing. A record of a change that never took
// effect is worse than none: whoever reads it believes the gate moved.
func TestApprovalPolicyAudit_RefusedWritesLeaveNoTrail(t *testing.T) {
	f := newCRFixture(t)

	// Contradictory scope → 400.
	if rec := postPolicy(t, f, map[string]any{
		"namespace": crNS, "allTypes": true, "types": []string{"Server"},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	// Nonexistent namespace → 400.
	if rec := postPolicy(t, f, map[string]any{"namespace": "no-such-ns"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	// A real one, then a colliding second → 409.
	if rec := postPolicy(t, f, map[string]any{"namespace": crNS}); rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	if rec := postPolicy(t, f, map[string]any{"namespace": crNS}); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}

	events := auditEvents(t, f, "createApprovalPolicy")
	if len(events) != 1 {
		t.Errorf("createApprovalPolicy events = %d, want 1 — three refusals were recorded as if they had taken effect", len(events))
	}
}

// AC 6 — findable by namespace, which is how someone asks "what happened to the
// gate protecting THIS namespace".
func TestApprovalPolicyAudit_IsFindableByNamespace(t *testing.T) {
	f := newCRFixture(t)
	if rec := postPolicy(t, f, map[string]any{"namespace": crNS}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/audit-log?resource_id="+crNS, nil), rec)
	c.Set("user_email", "admin@test.com")
	c.Set("role", string(user.RoleAdmin))
	if err := f.auditH.List(c); err != nil {
		t.Fatalf("audit list: %v", err)
	}
	var out struct {
		Events []auditEventView `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, ev := range out.Events {
		if len(ev.Operations) > 0 && ev.Operations[0] == "createApprovalPolicy" {
			found = true
		}
	}
	if !found {
		t.Errorf("no policy event when filtering by resource_id=%s — %d events returned", crNS, len(out.Events))
	}
}

// auditEventView is eventItem with details decoded, which is what a test asks
// questions of. Read through the same JSON the API returns rather than the ent
// row, so the assertions fail if the response shape stops carrying the field.
type auditEventView struct {
	Operations    []string       `json:"operations"`
	ResourceIDs   []string       `json:"resourceIds"`
	Actor         string         `json:"actor"`
	EventCategory string         `json:"eventCategory"`
	Details       map[string]any `json:"details"`
	// Changes is the pre-computed field diff. Present only for a clean
	// single-entity update, which is exactly what makes its ABSENCE worth
	// asserting on: a write that lost its before-state still returns 200.
	Changes []fieldChange `json:"changes"`
}

// auditEvents returns every event for an operation, newest first, via
// GET /api/v1/audit-log — the path an operator actually uses.
func auditEvents(t *testing.T, f *crFixture, operation string) []auditEventView {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit-log?operation_name="+operation, nil)
	c := e.NewContext(req, rec)
	c.Set("user_email", "admin@test.com")
	c.Set("role", string(user.RoleAdmin))
	if err := f.auditH.List(c); err != nil {
		t.Fatalf("audit list: %v", err)
	}
	var out struct {
		Events []auditEventView `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode audit log: %v (body: %s)", err, rec.Body.String())
	}
	return out.Events
}

func findAuditEvent(t *testing.T, f *crFixture, operation string) auditEventView {
	t.Helper()
	events := auditEvents(t, f, operation)
	if len(events) == 0 {
		t.Fatalf("no %s event in GET /api/v1/audit-log", operation)
	}
	return events[0]
}

func patchPolicy(t *testing.T, f *crFixture, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/approval-policies/"+id, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(id)
	c.Set("user_email", author)
	c.Set("role", string(user.RoleAdmin))
	if err := f.crh.UpdateApprovalPolicy(c); err != nil {
		t.Fatalf("UpdateApprovalPolicy: %v", err)
	}
	return rec
}

func deletePolicy(t *testing.T, f *crFixture, id string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/api/v1/approval-policies/"+id, nil), rec)
	c.SetParamNames("id")
	c.SetParamValues(id)
	c.Set("user_email", author)
	c.Set("role", string(user.RoleAdmin))
	if err := f.crh.DeleteApprovalPolicy(c); err != nil {
		t.Fatalf("DeleteApprovalPolicy: %v", err)
	}
	return rec
}

// ── Repeatable ?orbId= (subtree pending-change lookup) ──────────────────────
//
// Transcribed from the acceptance list before any body was written. The bug
// these pin: the filter read c.QueryParam("orbId"), which returns the FIRST
// value only, so ?orbId=a&orbId=b silently answered about `a` alone. A page
// asking "is anything in flight for this server and the children it owns" got
// an answer about the server, and a pending edit to its ServerMaintenance or
// IdracSettings child read as "nothing in flight".

// Item 1 — repeated orbId is OR'd, matching /api/v1/audit-log's semantics.
func TestListFilter_RepeatedOrbIDMatchesAnyOfThem(t *testing.T) {
	f := newCRFixture(t)

	a := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "a"}})
	b := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "b"}})

	ids := listIDs(t, f, "?orbId="+crServerA+"&orbId="+crServerB)
	if !ids[crHumanID(a)] || !ids[crHumanID(b)] {
		t.Errorf("two orbIds returned %v, want the request touching each", keysOf(ids))
	}

	// The exact shape of the bug: a value in any position but the first has to
	// count. Reading only the first turns this into an empty answer while a
	// change on crServerB sits open.
	ids = listIDs(t, f, "?orbId="+crNS+":not-a-real-item&orbId="+crServerB)
	if !ids[crHumanID(b)] {
		t.Errorf("a match in the second orbId was missed: %v — only the first value is being read", keysOf(ids))
	}
	if ids[crHumanID(a)] {
		t.Errorf("orbId list matched a request naming none of its values: %v", keysOf(ids))
	}

	// OR, not AND: a request naming one of the two still counts.
	if ids := listIDs(t, f, "?orbId="+crServerA+"&orbId="+crIdracA); !ids[crHumanID(a)] {
		t.Errorf("orbId list behaved as AND: %v", keysOf(ids))
	}
}

// Item 3 — a single ?orbId= still means exactly what it meant before. The OR
// must not widen the one-value case into a prefix or namespace match.
func TestListFilter_SingleOrbIDIsUnchangedByTheRepeatableForm(t *testing.T) {
	f := newCRFixture(t)

	a := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "a"}})
	b := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "b"}})

	if ids := listIDs(t, f, "?orbId="+crServerA); !ids[crHumanID(a)] || ids[crHumanID(b)] {
		t.Errorf("orbId=%s returned %v, want only the request touching it", crServerA, keysOf(ids))
	}
	if ids := listIDs(t, f, "?orbId="+crNS+":does-not-exist"); len(ids) != 0 {
		t.Errorf("an unmatched orbId returned %v, want none", keysOf(ids))
	}
	// An empty value is dropped rather than treated as a filter for "" — the
	// page hands this over as data-related-orb-ids="" when the subtree is
	// unknown, and an unfiltered list would be a wildly wrong answer.
	if ids := listIDs(t, f, "?orbId="); len(ids) != 2 {
		t.Errorf("an empty orbId returned %v, want the unfiltered list", keysOf(ids))
	}
}

// Item 2 — over the cap is refused, never truncated. A truncated filter answers
// a different question than the one asked and is indistinguishable from a
// correct answer, which is the same silent-wrong-answer failure the repeatable
// form exists to remove.
func TestListFilter_OverTheOrbIDCapIsRefused(t *testing.T) {
	f := newCRFixture(t)
	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "a"}})

	// Fill up to the cap with filler and put the real match LAST, so a filter
	// that quietly kept only the first 32 would return nothing and be caught.
	query := func(n int) string {
		q := ""
		for i := 0; i < n-1; i++ {
			q += "&orbId=" + crNS + ":filler-" + strconv.Itoa(i)
		}
		return "?" + strings.TrimPrefix(q+"&orbId="+crServerA, "&")
	}

	if ids := listIDs(t, f, query(maxOrbIDFilter)); !ids[crHumanID(cr)] {
		t.Errorf("%d orbIds returned %v, want the request named by the last one", maxOrbIDFilter, keysOf(ids))
	}

	rec := callList(t, f.crh, query(maxOrbIDFilter+1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — over the cap was accepted and silently truncated: %s", rec.Code, rec.Body.String())
	}
	var errBody errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rec.Body.String())
	}
	if errBody.Code != CodeBadUserInput || errBody.HTTPStatus != http.StatusBadRequest {
		t.Errorf("error envelope = %+v, want code %s and httpStatus 400", errBody, CodeBadUserInput)
	}
	if errBody.Error == "" || errBody.Hint == "" {
		t.Errorf("refusal must say what is wrong and what to do instead, got %+v", errBody)
	}
	// A refusal is not a result set: nothing may come back that a client could
	// mistake for an answer.
	if strings.Contains(rec.Body.String(), `"items"`) {
		t.Errorf("the refusal carried a result set: %s", rec.Body.String())
	}
}

// Item 4 — the reported bug. A change to an owned child names the CHILD's orbId
// and never the parent's, so the parent's own orbId finds nothing; the subtree
// list is what makes the pending notice appear on the parent's editor.
func TestPendingChanges_SubtreeQueryFindsAChangeOnAChildOnly(t *testing.T) {
	f := newCRFixture(t)

	cr := f.open(t, approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate,
		Set: map[string]any{"firmwareVersion": "9.9.9"}})

	// What the editor used to ask, and why the notice never showed.
	if ids := listIDs(t, f, "?status=active&orbId="+crServerA); len(ids) != 0 {
		t.Fatalf("the server's own orbId matched a child-only changeset (%v) — the fixture no longer reproduces the bug", keysOf(ids))
	}

	// What it asks now: the parent plus everything it owns, the same list the
	// audit tab already carries as data-related-orb-ids.
	subtree := collectRelatedOrbIDs(context.Background(), testutil.DGraphURL(), "Server", crServerA)
	if !containsStr(subtree, crIdracA) {
		t.Fatalf("the server's subtree %v does not include its IdracSettings child", subtree)
	}
	q := "?status=active"
	for _, id := range subtree {
		q += "&orbId=" + id
	}
	if ids := listIDs(t, f, q); !ids[crHumanID(cr)] {
		t.Errorf("the subtree query returned %v, want the change request naming only the child", keysOf(ids))
	}
}

// Item 9 — only non-terminal requests count. A merged, rejected or closed
// request is history; surfacing it as "in flight" would send someone to review
// a decision that has already been made.
func TestListFilter_SubtreeQueryCountsOnlyActiveRequests(t *testing.T) {
	ctx := context.Background()
	f := newCRFixture(t)
	f.requireApproval(t, 1)

	inFlight := f.open(t, approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate,
		Set: map[string]any{"firmwareVersion": "1.1.1"}})

	closed := f.open(t, approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate,
		Set: map[string]any{"firmwareVersion": "2.2.2"}})
	if _, err := f.crh.Close(ctx, closed.ID, author, user.RoleDev); err != nil {
		t.Fatalf("close: %v", err)
	}

	rejected := f.open(t, approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate,
		Set: map[string]any{"firmwareVersion": "3.3.3"}})
	if _, err := f.crh.Reject(ctx, rejected.ID, reviewer, user.RoleDev, "no"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	merged := f.open(t, approval.ChangeItem{OrbID: crIdracA, Op: approval.OpUpdate,
		Set: map[string]any{"firmwareVersion": "4.4.4"}})
	if _, err := f.crh.Approve(ctx, merged.ID, reviewer, user.RoleDev, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.crh.Merge(ctx, merged.ID, author, user.RoleDev, false); err != nil {
		t.Fatalf("merge: %v", err)
	}

	q := "?status=active&orbId=" + crServerA + "&orbId=" + crIdracA
	ids := listIDs(t, f, q)
	if !ids[crHumanID(inFlight)] {
		t.Errorf("status=active dropped the open request: %v", keysOf(ids))
	}
	for name, id := range map[string]string{
		"closed": crHumanID(closed), "rejected": crHumanID(rejected), "merged": crHumanID(merged),
	} {
		if ids[id] {
			t.Errorf("a %s request came back as active: %v", name, keysOf(ids))
		}
	}
}

// ── The human identifier ────────────────────────────────────────────────────

// Numbers are per-namespace, so each data center counts from 1 and an id says
// which one it belongs to. A global sequence would make colo-1 and houston-1
// impossible and the number meaningless.
func TestCRHumanID_NumbersRestartPerNamespace(t *testing.T) {
	f := newCRFixture(t)

	a := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "a"}})
	b := f.open(t, approval.ChangeItem{OrbID: crServerB, Op: approval.OpUpdate, Set: map[string]any{"hostname": "b"}})
	if a.Number != 1 || b.Number != 2 {
		t.Fatalf("numbers = %d, %d; want 1, 2 within one namespace", a.Number, b.Number)
	}
	if got := crHumanID(a); got != crNS+"-1" {
		t.Errorf("human id = %q, want %q", got, crNS+"-1")
	}

	// A different namespace starts its own count at 1. Allocated directly rather
	// than through Create, because Create validates every orbId against DGraph
	// and the point here is the numbering, not the changeset.
	other, err := f.crh.createNumbered(context.Background(), "houston",
		func(b *ent.ApprovalRequestCreate) *ent.ApprovalRequestCreate {
			return b.SetActionType(approval.ActionTypeConfigMutation).
				SetTitle("elsewhere").SetAuthor(author).SetCreatedBy(author).
				SetBaseHash("sha256:x").SetPayload([]byte(`{"namespace":"houston","changes":[]}`))
		})
	if err != nil {
		t.Fatalf("create in another namespace: %v", err)
	}
	if other.Number != 1 {
		t.Errorf("first request in a second namespace got number %d, want 1 — numbering is not per-namespace", other.Number)
	}
	if got := crHumanID(other); got != "houston-1" {
		t.Errorf("human id = %q, want houston-1", got)
	}

	// And the original namespace keeps counting where it left off — the two
	// sequences are independent, not a shared counter with a prefix.
	third := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "c"}})
	if third.Number != 3 {
		t.Errorf("next number in the original namespace = %d, want 3", third.Number)
	}
}

// Namespaces contain hyphens, so the id has to survive them. The number is
// always the final segment, which is what makes the split unambiguous — this
// pins the rule rather than trusting the comment above it.
func TestCRHumanID_ParsesNamespacesContainingHyphens(t *testing.T) {
	cases := []struct {
		raw       string
		namespace string
		number    int
		ok        bool
	}{
		{"colo-42", "colo", 42, true},
		{"alaska-dot-cruiser-42", "alaska-dot-cruiser", 42, true},
		{"dc-2-42", "dc-2", 42, true}, // a namespace whose own last segment is numeric
		{"colo-1", "colo", 1, true},
		{"colo", "", 0, false},          // no number
		{"colo-", "", 0, false},         // empty number
		{"colo-abc", "", 0, false},      // non-numeric
		{"-42", "", 0, false},           // no namespace
		{"colo-0", "", 0, false},        // numbers start at 1
		{"colo-42-extra", "", 0, false}, // trailing junk is not a number
	}
	for _, tc := range cases {
		ns, num, ok := splitCRID(tc.raw)
		if ok != tc.ok || ns != tc.namespace || num != tc.number {
			t.Errorf("splitCRID(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.raw, ns, num, ok, tc.namespace, tc.number, tc.ok)
		}
	}
}

// The API speaks the human id end to end: it is what comes back from a create
// and what a subsequent fetch accepts. A client never sees the surrogate key.
func TestCRHumanID_IsWhatTheAPIAcceptsAndReturns(t *testing.T) {
	f := newCRFixture(t)
	cr := f.open(t, approval.ChangeItem{OrbID: crServerA, Op: approval.OpUpdate,
		Set: map[string]any{"hostname": "round-trip"}})

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/change-requests/"+crHumanID(cr), nil), rec)
	c.SetParamNames("id")
	c.SetParamValues(crHumanID(cr))
	c.Set("user_email", reviewer)
	c.Set("role", string(user.RoleDev))
	if err := f.crh.GetChangeRequest(c); err != nil {
		t.Fatalf("get by human id: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out changeRequestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != crHumanID(cr) {
		t.Errorf("id = %q, want %q — the response must echo the identifier the caller used", out.ID, crHumanID(cr))
	}
	if strings.Contains(out.ID, "-4") && len(out.ID) == 36 {
		t.Errorf("id %q looks like a UUID", out.ID)
	}
}

// An unknown or malformed id must not 500. Malformed is a client error;
// well-formed but absent is a 404.
func TestCRHumanID_UnknownAndMalformedAreRefusedCleanly(t *testing.T) {
	f := newCRFixture(t)
	for _, tc := range []struct {
		id   string
		want int
	}{
		{crNS + "-999", http.StatusNotFound},
		{"not-an-id", http.StatusBadRequest},
		{"7c2e1f88-1a2b-4c3d-8e9f-0a1b2c3d4e5f", http.StatusBadRequest}, // an old UUID
	} {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/change-requests/"+tc.id, nil), rec)
		c.SetParamNames("id")
		c.SetParamValues(tc.id)
		c.Set("user_email", reviewer)
		c.Set("role", string(user.RoleDev))
		// A refusal is written by the handler; the error it returns afterwards
		// only signals "already responded" (see errResponseWritten). Assert on
		// what the client sees.
		err := f.crh.GetChangeRequest(c)
		if err != nil && !errors.Is(err, errResponseWritten) {
			t.Fatalf("%s: unexpected handler error %v", tc.id, err)
		}
		if rec.Code != tc.want {
			t.Errorf("GET %s: status = %d, want %d (%s)", tc.id, rec.Code, tc.want, rec.Body.String())
		}
		var envelope errorResponse
		if e := json.Unmarshal(rec.Body.Bytes(), &envelope); e != nil || envelope.Code == "" {
			t.Errorf("GET %s: body is not an error envelope: %s", tc.id, rec.Body.String())
		}
	}
}
