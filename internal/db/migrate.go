package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/migrate"
)

// MigrationAdvisoryLockKey serializes schema migration across replicas.
//
// Orbital's advisory-lock keys share one numbering. The others live in
// internal/handler (5555100001 backup scheduler, …002 job admission,
// …003 reaper); keep new keys unique across BOTH packages.
const MigrationAdvisoryLockKey int64 = 5555100004

// DefaultMigrationLockTimeout bounds how long a replica waits for another
// replica's migration to finish. Generous, because the wait is legitimate —
// the holder is migrating, and a large table can take minutes.
const DefaultMigrationLockTimeout = 5 * time.Minute

// ErrMigrationLockTimeout is returned when the lock could not be acquired in
// time. Distinct so a caller can tell "someone else is migrating / a previous
// migration died holding the lock" apart from "the migration itself failed".
var ErrMigrationLockTimeout = errors.New("timed out waiting for the schema migration lock")

// Migrate applies the ent schema under a PostgreSQL advisory lock.
//
// WHY THIS EXISTS. Migration runs in every replica at boot. Without a lock,
// replicas starting together race the same DDL, and the failure is fatal —
// the pod crashloops. This is not theoretical or rare: a FRESH install at two
// replicas fails deterministically, because an empty database means both pods
// try to create every table:
//
//	create "orbs" table: pq: duplicate key value violates unique
//	constraint "pg_type_typname_nsp_index" (23505)
//
// An adopter deploying orbital at `replicas: 2` — which deploy/README.md tells
// them is safe — hits that on their first install. They have one deploy; the
// "scale to 1, deploy, scale back up" workaround is not available to them.
//
// THE LOSER MUST STILL MIGRATE. A replica that gave up on the lock and carried
// on would serve requests against a schema that is not there yet. Losing means
// WAIT, then run Create anyway, where it is a no-op.
//
// WHY A RETRY LOOP RATHER THAN A BLOCKING pg_advisory_lock. If the holder dies
// mid-migration its connection can linger, and a blocking acquire would wait on
// it until TCP keepalives fire — 7200s by default on Linux. That presents as a
// silent hang, which is harder to diagnose than a crash. Bounded retries fail
// loudly instead. (Same keepalive trap as the job leases; see
// docs/reference/OCI.md § Job leases.)
//
// SESSION-SCOPED, not pg_try_advisory_xact_lock: ent's Schema.Create opens its
// own transactions, so it cannot run inside one of ours. The lock is held on a
// dedicated connection whose Close releases it even if the explicit unlock
// fails.
//
// TEMPORARY. When versioned migrations move out of the serving process — a
// pre-upgrade Job or init container, per the Atlas work — this whole function
// should be DELETED rather than carried forward.
func Migrate(ctx context.Context, rawDB *sql.DB, client *ent.Client, timeout time.Duration, logger *slog.Logger) error {
	if timeout <= 0 {
		timeout = DefaultMigrationLockTimeout
	}
	// No raw handle (DB-less and some test paths) — nothing to serialize against.
	if rawDB == nil {
		return client.Schema.Create(ctx, migrate.WithDropColumn(true))
	}

	conn, err := rawDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration lock: acquire connection: %w", err)
	}
	// Closing the connection releases any session lock it holds — the backstop
	// if the explicit unlock below never runs.
	defer conn.Close() //nolint:errcheck

	deadline := time.Now().Add(timeout)
	waited := false
	for attempt := 0; ; attempt++ {
		var acquired bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", MigrationAdvisoryLockKey).Scan(&acquired); err != nil {
			return fmt.Errorf("migration lock: %w", err)
		}
		if acquired {
			break
		}
		if !waited {
			logger.Info("another replica is migrating the schema — waiting", "timeout", timeout.String())
			waited = true
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w after %s: another replica may still be migrating, or a previous "+
				"migration died holding the lock. Check: SELECT * FROM pg_locks WHERE locktype='advisory'",
				ErrMigrationLockTimeout, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff(attempt)):
		}
	}

	defer func() {
		if _, err := conn.ExecContext(context.WithoutCancel(ctx),
			"SELECT pg_advisory_unlock($1)", MigrationAdvisoryLockKey); err != nil {
			// Not fatal: closing the connection releases it regardless.
			logger.Warn("migration lock: explicit unlock failed, releasing with the connection", "err", err)
		}
	}()

	if waited {
		logger.Info("schema migration lock acquired after waiting")
	}
	return client.Schema.Create(ctx, migrate.WithDropColumn(true))
}

// backoff grows the retry interval to a 1s ceiling. Short early so a fresh
// install at N replicas loses only milliseconds, not a whole interval.
func backoff(attempt int) time.Duration {
	d := time.Duration(1<<min(attempt, 6)) * 20 * time.Millisecond
	return min(d, time.Second)
}
