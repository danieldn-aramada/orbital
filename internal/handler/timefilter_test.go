package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/internal/configitems"
	"github.com/labstack/echo/v4"
)

// whereSQL applies the filters to a bare SELECT and returns its WHERE clause,
// so a test asserts the column, operator and argument each one produces.
func whereSQL(t *testing.T, raw string, fields TimeFields) (string, []any, error) {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	preds, err := TimeFilters[func(*sql.Selector)](q, fields)
	if err != nil {
		return "", nil, err
	}
	s := sql.Dialect(dialect.Postgres).Select("*").From(sql.Table("t"))
	for _, p := range preds {
		p(s)
	}
	query, args := s.Query()
	_, where, _ := strings.Cut(query, " WHERE ")
	return where, args, nil
}

// Each operator maps to its own comparison, on the column behind the JSON
// name. Catches a swapped operator (gte rendering as >) or a filter landing on
// the JSON name rather than the column — both return a plausible list.
func TestTimeFilters_EachOperatorBoundsTheWindowOnItsColumn(t *testing.T) {
	fields := TimeFields{"createdAt": "created_at"}
	for op, want := range map[string]string{
		"gte": `"t"."created_at" >= $1`,
		"gt":  `"t"."created_at" > $1`,
		"lte": `"t"."created_at" <= $1`,
		"lt":  `"t"."created_at" < $1`,
	} {
		t.Run(op, func(t *testing.T) {
			where, args, err := whereSQL(t, "createdAt_"+op+"=2026-10-01T00:00:00Z", fields)
			if err != nil {
				t.Fatal(err)
			}
			if where != want || len(args) != 1 {
				t.Errorf("got %s %v, want %s with one arg", where, args, want)
			}
		})
	}

	// A lower and an upper bound together select a window: both apply.
	where, _, err := whereSQL(t, "createdAt_gte=2026-10-01T00:00:00Z&createdAt_lte=2026-10-07T00:00:00Z", fields)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, ">=") || !strings.Contains(where, "<=") || !strings.Contains(where, " AND ") {
		t.Errorf("a two-sided window should AND both bounds, got %s", where)
	}
}

func TestTimeFilters_MalformedTimeIsRefused(t *testing.T) {
	fields := TimeFields{"createdAt": "created_at"}
	for name, raw := range map[string]string{
		"not a time": "createdAt_gte=yesterday",
		"date only":  "createdAt_gte=2026-10-01",
		// The classic: an unencoded + offset decodes to a space.
		"plus decoded to space": "createdAt_gte=2026-10-01T00:00:00+05:30",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := whereSQL(t, raw, fields)
			var qe *QueryError
			if err == nil || !asQueryError(err, &qe) {
				t.Fatalf("got %v, want a QueryError", err)
			}
			if name == "plus decoded to space" && !strings.Contains(qe.Hint, "%2B") {
				t.Errorf("hint %q should say to encode the +", qe.Hint)
			}
		})
	}
}

// A typo'd or unsupported field must not be ignored: ignoring it returns the
// whole list, which reads exactly like a correct, filtered answer.
func TestTimeFilters_UnknownFieldIsRefusedNotIgnored(t *testing.T) {
	_, _, err := whereSQL(t, "createAt_gte=2026-10-01T00:00:00Z", TimeFields{"createdAt": "created_at", "updatedAt": "updated_at"})
	var qe *QueryError
	if err == nil || !asQueryError(err, &qe) {
		t.Fatalf("got %v, want a QueryError", err)
	}
	if !strings.Contains(qe.Hint, "createdAt, updatedAt") {
		t.Errorf("hint %q should list the fields this endpoint filters", qe.Hint)
	}
}

// An empty bound is no bound — a template with no previous export renders
// `createdAt_gt=` and must not 400. Other params must never be read as filters.
func TestTimeFilters_IgnoresEmptyBoundsAndOtherParams(t *testing.T) {
	where, _, err := whereSQL(t, "createdAt_gt=&status=open&awaiting_review=true&limit=5", TimeFields{"createdAt": "created_at"})
	if err != nil {
		t.Fatal(err)
	}
	if where != "" {
		t.Errorf("expected no predicate, got %s", where)
	}
}

// WriteQueryError is what every endpoint returns through, so the 400 must be
// the standard envelope with a hint, not Echo's bare {"message"}.
func TestWriteQueryError_RendersTheEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
	if err := WriteQueryError(c, &QueryError{Msg: "bad", Hint: "fix it"}); err != nil {
		t.Fatal(err)
	}
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest || body.Code != CodeBadUserInput || body.Hint != "fix it" {
		t.Errorf("got %d %+v", rec.Code, body)
	}
}

func asQueryError(err error, target **QueryError) bool {
	qe, ok := err.(*QueryError)
	*target = qe
	return ok
}

// Every list endpoint declares the time fields its response carries — AC 4.
// Asked with a field nobody filters, each must 400 and NAME its own fields.
// Catches an endpoint that drops a field, filters the wrong set, or forgets
// to call TimeFilters at all (then the bogus param is ignored and it 200s).
// Runs before any database access, so the handlers need no DB here.
func TestListEndpoints_DeclareTheirTimeFields(t *testing.T) {
	for name, tc := range map[string]struct {
		list func(echo.Context) error
		want string
	}{
		"change-requests": {(&ChangeRequest{}).ListChangeRequests, "createdAt, executedAt, updatedAt"},
		"audit-log":       {(&AuditHandler{}).List, "createdAt"},
		"backup/jobs":     {(&BackupHandler{}).List, "completedAt, initiatedAt"},
		"export/jobs":     {(&Export{}).List, "completedAt, createdAt"},
		"restore/jobs":    {(&RestoreHandler{}).List, "completedAt, createdAt, startedAt"},
		"oci/artifacts":   {(&OCI{}).ListArtifacts, "completedAt, initiatedAt"},
		"divergences":     {(&DivergenceHandler{}).List, "firstSeenAt, lastSeenAt"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/?nope_gte=2026-10-01T00:00:00Z", nil), rec)
			c.Set(callerRoleKey, callerRole{NoAuthz: true})
			if err := tc.list(c); err != nil {
				t.Fatalf("got error %v, want a written 400", err)
			}
			var body errorResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(body.Hint, "Time filters here: "+tc.want+" —") {
				t.Errorf("got %d hint %q, want 400 naming %s", rec.Code, body.Hint, tc.want)
			}
		})
	}
}

// Stored item types are always concrete, so an interface name taken literally
// matches nothing and reads as "no requests touch clusters" — AC 9.
func TestConcreteTypes_InterfaceExpandsToItsImplementations(t *testing.T) {
	h := &ChangeRequest{views: func(context.Context) (configitems.ViewSet, error) {
		return configitems.ViewSet{
			{Type: "KubernetesCluster", IsInterface: true, Implementations: []string{"EksaKubernetesCluster"}},
			{Type: "Server"},
		}, nil
	}}
	got, err := h.concreteTypes(context.Background(), []string{"KubernetesCluster", "Server", "NoSuchType"})
	if err != nil {
		t.Fatal(err)
	}
	want := "KubernetesCluster,EksaKubernetesCluster,Server,NoSuchType"
	if strings.Join(got, ",") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}
