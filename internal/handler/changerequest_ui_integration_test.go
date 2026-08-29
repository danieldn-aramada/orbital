//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"strings"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
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

	for _, role := range []string{"readonly", "dev"} {
		ok, items := find(role)
		if !ok {
			t.Fatalf("%s cannot see the Change Control section at all", role)
		}
		if len(items) != 1 || items[0] != "Change Requests" {
			t.Errorf("%s sees %v — the queue is readonly+, but policy admin is not", role, items)
		}
	}

	ok, items := find("admin")
	if !ok || len(items) != 2 {
		t.Fatalf("admin sees %v, want the queue and Approval Policies", items)
	}
	if items[1] != "Approval Policies" {
		t.Errorf("admin's second item is %q, want Approval Policies", items[1])
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

// AC 13 — the page's admin gate. CRUD itself is the API's, covered by the
// endpoints; what this pins is that the UI gate matches the API's minimum, so a
// dev cannot be shown controls the API would refuse.
func TestApprovalPoliciesPage_AdminCanCRUDAndDevGetsNoWriteControls(t *testing.T) {
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
			hasTable := strings.Contains(html, `id="ap-tbody"`)
			hasAdd := strings.Contains(html, `id="ap-add"`)
			if tc.wantControls && !(hasTable && hasAdd) {
				t.Errorf("admin sees no policy controls: table=%v add=%v", hasTable, hasAdd)
			}
			if !tc.wantControls {
				if hasTable || hasAdd {
					t.Errorf("%s is shown policy controls the API would refuse", tc.role)
				}
				if !strings.Contains(html, "Admin access required") {
					t.Errorf("%s is not told why the page is empty", tc.role)
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
	if len(got) != 2 || !got[openCR.ID.String()] || !got[approvedCR.ID.String()] {
		t.Fatalf("status=active returned %v, want the open and the approved request", keysOf(got))
	}
	if got[closedCR.ID.String()] {
		t.Error("a closed request came back as active")
	}

	// The narrower filters must still split the two, or `active` would be the
	// only usable one.
	if ids := listIDs(t, f, "?status=open"); !ids[openCR.ID.String()] || ids[approvedCR.ID.String()] {
		t.Errorf("status=open returned %v", keysOf(ids))
	}
	if ids := listIDs(t, f, "?status=approved"); ids[openCR.ID.String()] || !ids[approvedCR.ID.String()] {
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

	if ids := listIDs(t, f, "?orbId="+crServerA); !ids[a.ID.String()] || ids[b.ID.String()] {
		t.Errorf("orbId=%s returned %v, want only the request touching it", crServerA, keysOf(ids))
	}
	if ids := listIDs(t, f, "?namespace="+crNS); !ids[a.ID.String()] || !ids[b.ID.String()] {
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
		if ids := listIDs(t, f, "?orbId="+orbID); !ids[cr.ID.String()] {
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
