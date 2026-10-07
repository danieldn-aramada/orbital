package handler

import (
	dbsql "database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/internal/metrics"
	"github.com/labstack/echo/v4"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// A dropped audit event is the only failure in orbital that changes nothing
// observable at the time — the mutation committed, the caller got its 200, and
// the sole trace was a log line nobody reads until someone asks a question that
// can no longer be answered. writeAuditEvent swallows its errors deliberately
// (an audit write must never fail a request), so the counter is the ONLY thing
// standing between that design choice and silent provenance loss.
//
// The regression this guards is concrete: someone refactors writeAuditEvent,
// moves or merges an error branch, and drops the metrics call. The behaviour is
// identical in every test that does not look at the counter, and audit loss
// goes silent again.
func TestAuditWriteFailure_IsCounted(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	before := scrapeAuditFailures(t)

	writeAuditEvent(closedClient(t), logger, "data", "someone@example.com", "updateServer",
		[]string{"updateServer"}, []string{"Server"}, []string{"ns:srv-1"},
		map[string]any{"operationName": "updateServer"}, auditOrigin{Source: "graphql"})

	after := scrapeAuditFailures(t)

	// The write could not even begin a transaction, so that is the stage that
	// must be named. A generic "audit_failed" counter would not tell an operator
	// whether the database was unreachable or the row itself was rejected.
	if got, want := after["begin_tx"]-before["begin_tx"], 1.0; got != want {
		t.Errorf("begin_tx counter rose by %v, want %v — a swallowed audit failure is unalertable without it", got, want)
	}

	// The negative, and the reason it matters: a signal that fires for stages
	// that did not fail trains whoever reads it to ignore the label, which is
	// worse than having no label. Only the stage that actually failed may move.
	for _, stage := range []string{"event", "resources", "resource_types", "commit"} {
		if d := after[stage] - before[stage]; d != 0 {
			t.Errorf("stage %q rose by %v on a begin_tx failure, want 0", stage, d)
		}
	}
}

// scrapeAuditFailures reads the counter back through /metrics — the surface an
// operator's alert actually queries — rather than through a getter on the
// metrics package. A helper that reached into the registry would prove the
// counter exists; this proves it is published.
func scrapeAuditFailures(t *testing.T) map[string]float64 {
	t.Helper()

	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/metrics", nil), rec)
	if err := metrics.Handler()(c); err != nil {
		t.Fatalf("scraping /metrics: %v", err)
	}

	out := map[string]float64{
		"begin_tx": 0, "event": 0, "resources": 0, "resource_types": 0, "commit": 0,
	}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "orbital_audit_write_failures_total{") {
			continue
		}
		open, close := strings.Index(line, `stage="`), strings.Index(line, `"}`)
		if open < 0 || close < 0 {
			continue
		}
		stage := line[open+len(`stage="`) : close]
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("parsing %q: %v", line, err)
		}
		out[stage] = v
	}
	return out
}

// closedClient is an ent client over a closed database handle, so every
// operation fails at the first step. No PostgreSQL required — this is a unit
// test, and the behaviour under test is orbital's reaction to a failed write,
// not the database's.
func closedClient(t *testing.T) *ent.Client {
	t.Helper()

	sqlDB, err := dbsql.Open("pgx", "postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatalf("opening handle: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("closing handle: %v", err)
	}
	return ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
}
