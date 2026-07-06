package store

import (
	"context"
	"database/sql"
	"fmt"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	// modernc.org/sqlite is a pure-Go SQLite driver — respects the project's
	// CGO_ENABLED=0 invariant. Do NOT swap for mattn/go-sqlite3 (needs CGO).
	_ "modernc.org/sqlite"
)

// New opens the orb SQLite database at dbPath and runs schema migrations.
// Pass ":memory:" for in-memory tests. WAL mode is enabled for concurrent
// read + single-writer safety; busy_timeout absorbs brief writer contention.
func New(ctx context.Context, dbPath string) (*Client, error) {
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dbPath, err)
	}
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := NewClient(Driver(drv))
	if err := client.Schema.Create(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return client, nil
}
