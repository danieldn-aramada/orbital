//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/labstack/echo/v4"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/enttest"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/testutil"
)

// countingDriver counts SELECTs per table, so a test can assert how many times
// one request asked PostgreSQL the same question.
//
// Query counts are otherwise invisible: an N+1 changes no output, no status
// code and no test assertion — it only shows up as latency on someone else's
// deployment. Pinning the count is the only thing that stops a helper added
// later from quietly reintroducing one.
type countingDriver struct {
	dialect.Driver

	mu     sync.Mutex
	counts map[string]int
}

func newCountingDriver(d dialect.Driver) *countingDriver {
	return &countingDriver{Driver: d, counts: map[string]int{}}
}

func (d *countingDriver) Query(ctx context.Context, query string, args, v any) error {
	d.record(query)
	return d.Driver.Query(ctx, query, args, v)
}

// record attributes a statement to a table by looking for the quoted table name
// ent emits. Matching on the quoted form avoids counting a statement that
// merely mentions the word in a column alias.
func (d *countingDriver) record(query string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range []string{"users", "approval_policies", "approval_requests"} {
		if strings.Contains(query, `"`+t+`"`) {
			d.counts[t]++
		}
	}
}

func (d *countingDriver) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.counts = map[string]int{}
}

func (d *countingDriver) count(table string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.counts[table]
}

// newCountingCRFixture is newCRFixture over a driver that counts SELECTs.
func newCountingCRFixture(t *testing.T) (*crFixture, *countingDriver) {
	t.Helper()
	if err := testutil.EnsureTestDatabase(); err != nil {
		t.Fatalf("ensure test database: %v", err)
	}
	drv, err := entsql.Open(dialect.Postgres, testutil.TestDatabaseURL())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cd := newCountingDriver(drv)
	db := enttest.NewClient(t, enttest.WithOptions(ent.Driver(cd)))
	t.Cleanup(func() {
		if err := testutil.TruncateAllE(); err != nil {
			t.Logf("truncate: %v (continuing)", err)
		}
	})

	gql := NewGraphQL(testutil.DGraphURL(), db, slog.Default(), false)
	crh := NewChangeRequest(db, gql, testutil.DGraphURL(), slog.Default())
	seedCREngineFixture(t)
	return &crFixture{db: db, crh: crh, auditH: &AuditHandler{db: db, logger: slog.Default()}}, cd
}

// AC 1/2 — the caller's role is fetched once per request, not once per asker.
//
// RequireRole looks the user up, then the handler asks again, and on a
// /graphql mutation authorizeMutation and writeToDGraph each ask again after
// that. Nothing about the extra queries is observable except latency, so only
// a count catches a fifth asker being added later.
func TestCallerRole_IsFetchedOncePerRequest(t *testing.T) {
	ctx := context.Background()
	f, cd := newCountingCRFixture(t)

	u, err := f.db.User.Create().
		SetEmail("counter@test.com").SetName("Counter").
		SetPreferredUsername("counter@test.com").SetVerified(true).
		SetRole(user.RoleDev).Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	c.Set("user_id", u.ID)

	cd.reset()
	for i := 0; i < 4; i++ {
		if got := resolveCallerRole(c, f.db); got.Role != user.RoleDev {
			t.Fatalf("resolve %d: role = %q, want dev", i, got.Role)
		}
	}
	if n := cd.count("users"); n != 1 {
		t.Errorf("users SELECTs = %d, want 1 — the role is being re-fetched within one request", n)
	}

	// A DIFFERENT request must re-read: PostgreSQL stays the authority, so an
	// admin changing this role takes effect on the caller's next request rather
	// than whenever something expires.
	if _, err := f.db.User.UpdateOneID(u.ID).SetRole(user.RoleReadonly).Save(ctx); err != nil {
		t.Fatalf("demote: %v", err)
	}
	c2 := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	c2.Set("user_id", u.ID)
	if got := resolveCallerRole(c2, f.db); got.Role != user.RoleReadonly {
		t.Errorf("role on the next request = %q, want readonly — the memo outlived its request", got.Role)
	}
}

// AC 4 — an external-JWT caller carries a pre-mapped role and has no users-table
// row, so memoising must not turn that into a lookup.
func TestCallerRole_ContextRoleStillWinsAndQueriesNothing(t *testing.T) {
	f, cd := newCountingCRFixture(t)

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	c.Set("role", string(user.RoleAdmin))
	c.Set("user_id", 99999) // would fail if it were ever looked up

	cd.reset()
	for i := 0; i < 3; i++ {
		if got := resolveCallerRole(c, f.db); got.Role != user.RoleAdmin || got.Source != "context" {
			t.Fatalf("resolve %d: %+v, want admin from context", i, got)
		}
	}
	if n := cd.count("users"); n != 0 {
		t.Errorf("users SELECTs = %d, want 0 — a context role must never hit the table", n)
	}
}

// AC 5 — listing N change requests queries the policy once, not N times.
//
// Every rendered request derives its status from the policy, so the lookup sat
// inside the render loop. On the queue page those rows are overwhelmingly one
// namespace, and the badge in the nav runs this on every page load.
func TestListChangeRequests_ResolvesThePolicyOncePerNamespace(t *testing.T) {
	ctx := context.Background()
	f, cd := newCountingCRFixture(t)

	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetRequiredApprovals(1).
		Save(ctx); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	const n = 5
	for i := 0; i < n; i++ {
		f.open(t, approval.ChangeItem{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": fmt.Sprintf("count-%d", i)},
		})
	}

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/change-requests", nil), rec)
	c.Set("user_email", reviewer)
	c.Set("role", string(user.RoleDev))

	cd.reset()
	if err := f.crh.ListChangeRequests(c); err != nil {
		t.Fatalf("list: %v", err)
	}
	var out changeRequestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != n {
		t.Fatalf("rendered %d requests, want %d — the count below would be meaningless", out.Total, n)
	}
	if q := cd.count("approval_policies"); q != 1 {
		t.Errorf("approval_policies SELECTs = %d for %d rendered requests, want 1", q, n)
	}

	// AC 6 — and the answer is unchanged: every row still reports the policy's
	// requirement, which is the thing the memo could silently have dropped.
	for i, item := range out.Items {
		if item.Required != 1 {
			t.Errorf("item %d: required = %d, want 1", i, item.Required)
		}
	}
}

// AC 7 — a readonly caller short-circuits before any query.
//
// readonly can never approve, so every row would be rendered and then
// discarded. This runs on every page load, so "render then discard" is the
// expensive way to return zero.
func TestAwaitingReview_ReadonlyShortCircuitsWithoutQuerying(t *testing.T) {
	f, cd := newCountingCRFixture(t)
	f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "x"},
	})

	cd.reset()
	total, _ := awaitingFor(t, f, "someone@test.com", user.RoleReadonly)
	if total != 0 {
		t.Errorf("readonly badge total = %d, want 0", total)
	}
	if n := cd.count("approval_requests"); n != 0 {
		t.Errorf("approval_requests SELECTs = %d, want 0 — rows were fetched only to be discarded", n)
	}
}

// AC 8 — your own requests are excluded in SQL, not after rendering.
func TestAwaitingReview_ExcludesYourOwnRequestsInSQL(t *testing.T) {
	ctx := context.Background()
	f, _ := newCountingCRFixture(t)

	// Without a policy `required` is 0, so a request reads as approved the
	// moment it opens and awaits nobody — the opt-in property. A policy is what
	// makes "awaiting review" a state that exists at all.
	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetRequiredApprovals(1).
		SetBypassRoles([]string{}).
		Save(ctx); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	for i := 0; i < 3; i++ {
		f.open(t, approval.ChangeItem{
			OrbID: crServerA, Op: approval.OpUpdate,
			Set: map[string]any{"hostname": fmt.Sprintf("mine-%d", i)},
		})
	}

	// f.open authors as `author`, so nothing awaits them and everything awaits
	// a peer.
	if total, _ := awaitingFor(t, f, author, user.RoleDev); total != 0 {
		t.Errorf("author's own badge total = %d, want 0", total)
	}
	if total, _ := awaitingFor(t, f, reviewer, user.RoleDev); total != 3 {
		t.Errorf("peer's badge total = %d, want 3", total)
	}
}

// AC 9 — the SQL exclusion must NOT apply when a policy lets the caller bypass,
// because bypass makes approve available on your own request.
//
// This is the exactness guard. The prefilter is a performance change that must
// not alter what the badge counts, and this is the single case where a naive
// "always exclude your own" would silently start under-counting.
func TestAwaitingReview_BypassRoleStillSeesTheirOwnRequests(t *testing.T) {
	ctx := context.Background()
	f, _ := newCountingCRFixture(t)

	if _, err := f.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(crNS).
		SetRequiredApprovals(2).
		SetBypassRoles([]string{"admin"}).
		Save(ctx); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	f.open(t, approval.ChangeItem{
		OrbID: crServerA, Op: approval.OpUpdate, Set: map[string]any{"hostname": "x"},
	})

	// `author` opened it. As a dev they cannot approve their own.
	if total, _ := awaitingFor(t, f, author, user.RoleDev); total != 0 {
		t.Errorf("dev author badge = %d, want 0", total)
	}
	// The same author acting as admin CAN, because the policy says so — and the
	// SQL filter must not have removed the row before anyone asked.
	if total, _ := awaitingFor(t, f, author, user.RoleAdmin); total != 1 {
		t.Errorf("admin author badge = %d, want 1 — the SQL prefilter dropped a row bypass makes actionable", total)
	}
}

func awaitingFor(t *testing.T, f *crFixture, actor string, role user.Role) (int, changeRequestListResponse) {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/change-requests?awaiting_review=true", nil), rec)
	c.Set("user_email", actor)
	c.Set("role", string(role))
	if err := f.crh.ListChangeRequests(c); err != nil {
		t.Fatalf("list: %v", err)
	}
	var out changeRequestListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Total, out
}
