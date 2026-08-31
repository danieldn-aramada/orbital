package db

import (
	"context"
	dbsql "database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// DefaultMaxConns caps the pool. Orbital is a single replica serving a UI and a
// GraphQL proxy; the ent client and the backup advisory lock share this pool.
const DefaultMaxConns = 10

// NewPool opens a connection pool that resolves its password through cred on every
// new connection, so a rotated Entra token is picked up without a restart, and
// verifies it with a ping.
//
// The DSN carries host, port, database and sslmode. Under managed identity its
// password field is empty and beforeConnect fills it; under a static credential the
// password is already there and beforeConnect is a no-op re-read.
func NewPool(ctx context.Context, dsn string, cred Credential, maxConns int32) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing pool config: %w", err)
	}
	poolCfg.BeforeConnect = beforeConnect(cred)
	poolCfg.MaxConns = maxConns
	poolCfg.MaxConnLifetime = cred.MaxConnLifetime()
	poolCfg.MaxConnLifetimeJitter = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	return pool, nil
}

// SQLDB adapts the pool to database/sql for the two consumers that need it: ent's
// driver and the backup advisory lock. Both must come from this pool rather than a
// second sql.Open, or they bypass BeforeConnect and connect with no password.
func SQLDB(pool *pgxpool.Pool) *dbsql.DB {
	return stdlib.OpenDBFromPool(pool)
}
