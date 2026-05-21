//go:build integration

package testutil

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/enttest"
	_ "github.com/lib/pq"
)

// TestDatabaseURL returns the PostgreSQL DSN for the test stack.
// Defaults to the orbital_test database on port 5432.
func TestDatabaseURL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://orbital:orbital@localhost:5432/orbital_test?sslmode=disable"
}

// NewTestDB opens an ent client against the test PostgreSQL instance and runs
// auto-migration. All tables are truncated via t.Cleanup when the test ends.
func NewTestDB(t *testing.T) *ent.Client {
	t.Helper()

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
		"registry_artifacts",
		"restore_jobs",
		"export_jobs",
		"backups",
		"events",
		"orbs",
		"users",
		"namespaces",
	}

	ctx := context.Background()
	for _, table := range tables {
		if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+table+" CASCADE"); err != nil {
			return err
		}
	}
	return nil
}
