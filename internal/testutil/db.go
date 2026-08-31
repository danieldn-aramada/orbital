//go:build integration

package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/enttest"
	"github.com/lib/pq"
)

// TestDatabaseURL returns the PostgreSQL DSN for the test stack.
// Defaults to the orbital_test database on port 5432.
func TestDatabaseURL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://orbital:orbital-local-dev-secret@localhost:5432/orbital_test?sslmode=disable"
}

// EnsureTestDatabase creates the test database if it does not exist.
//
// Called from the test harness rather than left to the Makefile because the
// harness is the code that NEEDS it: `make test-integration` creates it, but
// running `go test -tags=integration` directly, or from an IDE, skips that and
// dies with a bare `database "orbital_test" does not exist` several layers
// away from the cause. `make down` runs `-v`, which wipes the Postgres volume,
// so the normal down/up cycle destroys it.
//
// "Already exists" is not an error; anything else is, and is returned rather
// than swallowed — a permissions failure must not look like success.
func EnsureTestDatabase() error {
	dsn := TestDatabaseURL()
	admin := regexp.MustCompile(`/[^/?]+(\?|$)`).ReplaceAllString(dsn, "/postgres$1")

	db, err := sql.Open("postgres", admin)
	if err != nil {
		return fmt.Errorf("connect to postgres to create the test database: %w", err)
	}
	defer db.Close()

	name := "orbital_test"
	if m := regexp.MustCompile(`/([^/?]+)(\?|$)`).FindStringSubmatch(dsn); len(m) > 1 {
		name = m[1]
	}
	if _, err := db.Exec("CREATE DATABASE " + pq.QuoteIdentifier(name)); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("create database %q: %w", name, err)
	}
	return nil
}

// NewTestDB opens an ent client against the test PostgreSQL instance and runs
// auto-migration. All tables are truncated via t.Cleanup when the test ends.
func NewTestDB(t *testing.T) *ent.Client {
	t.Helper()

	if err := EnsureTestDatabase(); err != nil {
		t.Fatalf("ensure test database: %v", err)
	}
	client := enttest.Open(t, "postgres", TestDatabaseURL())

	t.Cleanup(func() {
		if err := truncateAll(TestDatabaseURL()); err != nil {
			t.Logf("truncateAll: %v (continuing)", err)
		}
	})

	return client
}

// TruncateAllE removes all rows from every operational table.
// Use in TestMain to ensure a clean slate before each test run.
func TruncateAllE() error {
	return truncateAll(TestDatabaseURL())
}

// truncateAll removes all rows from every operational table.
// Order respects foreign key constraints: child tables before parents.
func truncateAll(dsn string) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	tables := []string{
		"approvals",
		"merge_attempts",
		"approval_requests",
		"approval_policies",
		"registry_artifacts",
		"restore_jobs",
		"export_jobs",
		"backups",
		"divergence_resolutions",
		"divergence_entries",
		"divergence_ingest_cursors",
		"audit_event_resource_types",
		"audit_event_resources",
		"audit_events",
		"orbs",
		"users",
	}

	ctx := context.Background()
	for _, table := range tables {
		if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+table+" CASCADE"); err != nil {
			return err
		}
	}
	return nil
}
