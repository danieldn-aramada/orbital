package metrics

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// scrape reads one series back through the REAL /metrics endpoint — the same
// path Prometheus uses. Asserting on the counter variable would prove only that
// the variable moved, not that anything is exposed to scrape.
func scrape(t *testing.T, series string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/metrics", nil), rec)
	if err := Handler()(c); err != nil {
		t.Fatalf("scrape /metrics: %v", err)
	}
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(series) + ` ([0-9.e+]+)$`)
	m := re.FindStringSubmatch(rec.Body.String())
	if m == nil {
		return 0 // not yet present — a counter at zero is not exported
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("parse %q: %v", m[1], err)
	}
	return v
}

// Acceptance 1 and 4: a failed publish is visible through /metrics, labelled
// with the operation and a classified reason.
func TestJobFailureIsScrapable(t *testing.T) {
	const fails = `orbital_job_failures_total{operation="publish",reason="dependency_rejected"}`
	const total = `orbital_jobs_total{operation="publish",outcome="failed"}`
	beforeF, beforeT := scrape(t, fails), scrape(t, total)

	RecordJobFailure(JobPublish, ReasonDependencyRejected)

	if got := scrape(t, fails); got != beforeF+1 {
		t.Errorf("%s = %v, want %v — a failure nobody can scrape is the bug this exists to fix", fails, got, beforeF+1)
	}
	if got := scrape(t, total); got != beforeT+1 {
		t.Errorf("%s = %v, want %v", total, got, beforeT+1)
	}
}

// Acceptance 2 and 3: success counts as success and advances the freshness
// gauge.
func TestJobSuccessAdvancesFreshnessGauge(t *testing.T) {
	const gauge = `orbital_job_last_success_timestamp_seconds{operation="export"}`
	const ok = `orbital_jobs_total{operation="export",outcome="succeeded"}`
	beforeOK := scrape(t, ok)

	at := time.Unix(1_800_000_000, 0)
	RecordJobSuccess(JobExport, at)

	if got := scrape(t, gauge); got != float64(at.Unix()) {
		t.Errorf("%s = %v, want %v", gauge, got, at.Unix())
	}
	if got := scrape(t, ok); got != beforeOK+1 {
		t.Errorf("%s = %v, want %v", ok, got, beforeOK+1)
	}
}

// THE NEGATIVE, and the reason the gauge is trustworthy: a failure must not
// advance the freshness clock. If it did, "time since last success" would reset
// on every failed attempt and would read healthiest exactly when publishing is
// most broken — training whoever watches it to ignore the panel.
func TestJobFailureDoesNotAdvanceFreshnessGauge(t *testing.T) {
	const gauge = `orbital_job_last_success_timestamp_seconds{operation="restore"}`
	const ok = `orbital_jobs_total{operation="restore",outcome="succeeded"}`

	RecordJobSuccess(JobRestore, time.Unix(1_700_000_000, 0))
	pinned, okBefore := scrape(t, gauge), scrape(t, ok)

	RecordJobFailure(JobRestore, ReasonLeaseLost)

	if got := scrape(t, gauge); got != pinned {
		t.Errorf("%s moved to %v on a FAILURE; it must stay at %v", gauge, got, pinned)
	}
	if got := scrape(t, ok); got != okBefore {
		t.Errorf("%s = %v, want %v — a failure counted as a success", ok, got, okBefore)
	}
}
