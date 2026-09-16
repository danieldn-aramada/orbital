package handler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/restorejob"
	"github.com/google/uuid"
)

// Job leases — how orbital stays correct at any replica count.
//
// Every long-running job (export, backup, restore) is CLAIMED by exactly one
// runner before it executes, and that runner proves it is still alive by
// refreshing a heartbeat. A runner that loses its claim aborts instead of
// racing its replacement. Nothing here depends on how many replicas exist.
//
// Two primitives, deliberately not interchangeable:
//
//   - An advisory lock answers "may I act right now" — short-lived,
//     transaction-scoped, released when the transaction ends.
//   - A lease (locked_by + heartbeat_at) answers "is the runner that claimed
//     this still alive". A lock cannot answer that: one held by a partitioned
//     node is indistinguishable from one held by a working node, and
//     PostgreSQL will not notice the dead connection until TCP keepalives
//     fire — 7200s by default on Linux. A lease that must be renewed fails
//     CLOSED on a partition; a held connection fails OPEN.
//
// See docs/reference/OCI.md for the settled design and its precedents
// (controller-runtime lease holders, River's rescuer, delayed_job's
// locked_by).

// Advisory lock keys. One per concern so admission never blocks the reaper
// sweep. 5555100001 is the backup scheduler, which predates the rest.
const (
	jobAdmissionAdvisoryLockKey int64 = 5555100002
	reaperAdvisoryLockKey       int64 = 5555100003
)

// runnerID identifies this process as a job runner, as <hostname>_<uuid> —
// controller-runtime's lease-holder format.
//
// NEVER a bare hostname: a pod that restarts in place reuses its name, so a
// zombie from the previous process would match locked_by and the fence would
// silently stop fencing. Never a bare UUID either — an operator reading
// locked_by needs to know which pod to look at.
var runnerID = newRunnerID()

func newRunnerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return host + "_" + uuid.NewString()
}

// tryAdvisoryLockKey attempts a PostgreSQL transaction-scoped advisory lock on
// key. Returns (acquired, release, err); release rolls the transaction back,
// which is what frees the lock. pg_try_advisory_xact_lock is used rather than
// the session-scoped variant so a crashed process cannot hold the lock.
//
// A nil rawDB skips locking and reports acquired — the DB-less and unit-test
// paths have no concurrent replica to exclude.
func tryAdvisoryLockKey(ctx context.Context, rawDB *sql.DB, key int64) (bool, func(), error) {
	if rawDB == nil {
		return true, func() {}, nil
	}
	tx, err := rawDB.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, fmt.Errorf("advisory lock begin tx: %w", err)
	}
	var acquired bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", key).Scan(&acquired); err != nil {
		tx.Rollback() //nolint:errcheck
		return false, nil, fmt.Errorf("advisory lock query: %w", err)
	}
	if !acquired {
		tx.Rollback() //nolint:errcheck
		return false, nil, nil
	}
	return true, func() { tx.Rollback() }, nil //nolint:errcheck
}

// acquireAdmissionLock blocks until the job-admission lock is held, or ctx
// expires. Blocking (pg_advisory_xact_lock, not the try- variant) is the right
// choice here: the critical section is a couple of queries, and a caller that
// gave up would answer "already in progress" without having looked — a lie.
// The lock is transaction-scoped, so a crashed admitter cannot hold it.
//
// Returns a release func that is always safe to call.
func acquireAdmissionLock(ctx context.Context, rawDB *sql.DB) (func(), error) {
	if rawDB == nil {
		return func() {}, nil
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	tx, err := rawDB.BeginTx(lockCtx, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("admission lock begin tx: %w", err)
	}
	if _, err := tx.ExecContext(lockCtx, "SELECT pg_advisory_xact_lock($1)", jobAdmissionAdvisoryLockKey); err != nil {
		tx.Rollback() //nolint:errcheck
		cancel()
		return nil, fmt.Errorf("admission lock: %w", err)
	}
	return func() {
		tx.Rollback() //nolint:errcheck
		cancel()
	}, nil
}

// JobLeaseConfig carries the three durations that govern job liveness.
//
// The staleness threshold does NOT need to exceed job duration — that is the
// difference between a refreshed heartbeat and a static claim timestamp. It
// only needs to exceed one heartbeat interval plus the worst plausible pause.
type JobLeaseConfig struct {
	// HeartbeatInterval is how often a running job refreshes heartbeat_at.
	HeartbeatInterval time.Duration
	// StaleAfter is how long a heartbeat may go unrefreshed before the reaper
	// declares the runner dead. Wider than controller-runtime's 15s lease
	// because a false positive here kills a live restore mid-drop_all, where
	// a controller losing leadership merely re-elects.
	StaleAfter time.Duration
	// OrphanGrace applies only to jobs with NO heartbeat at all — those left
	// running by the deploy that introduced leases. Under a rolling update a
	// job started by the old pod is genuinely still running while the new
	// pod's reaper is live, so these cannot share StaleAfter. One-time: after
	// the first deploy no job is ever created without a heartbeat.
	OrphanGrace time.Duration
}

// DefaultJobLeaseConfig is used when configuration supplies no values.
func DefaultJobLeaseConfig() JobLeaseConfig {
	return JobLeaseConfig{
		HeartbeatInterval: 10 * time.Second,
		StaleAfter:        60 * time.Second,
		OrphanGrace:       time.Hour,
	}
}

func (c JobLeaseConfig) withDefaults() JobLeaseConfig {
	d := DefaultJobLeaseConfig()
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = d.HeartbeatInterval
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = d.StaleAfter
	}
	if c.OrphanGrace <= 0 {
		c.OrphanGrace = d.OrphanGrace
	}
	return c
}

// keepLease refreshes a claimed job's heartbeat until ctx ends.
//
// beat reports whether this runner STILL holds the lease — it is a conditional
// update filtered on locked_by, so a runner whose job was reaped and re-claimed
// updates zero rows. When that happens the job is being executed by someone
// else, and continuing would mean two processes writing the same scratch
// DGraph, so lost() is called to cancel the runner's context and the loop
// stops. This is the fence; without it the heartbeat would keep a superseded
// job looking healthy.
//
// Call as a goroutine. A beat that errors is logged and retried — a transient
// database blip must not abort a live job, and a genuinely dead database will
// stop the job by other means.
func keepLease(ctx context.Context, interval time.Duration, logger *slog.Logger, jobID string,
	beat func(context.Context) (bool, error), lost func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Deliberately not ctx: a cancelled job context must still be able
			// to report, and this write is bounded anyway.
			beatCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interval)
			held, err := beat(beatCtx)
			cancel()
			if err != nil {
				logger.Warn("job lease: heartbeat failed, will retry", "job_id", jobID, "runner", runnerID, "err", err)
				continue
			}
			if !held {
				logger.Warn("job lease LOST — another runner owns this job; aborting to avoid concurrent execution",
					"job_id", jobID, "runner", runnerID)
				lost()
				return
			}
		}
	}
}

// ── Per-type lease operations ────────────────────────────────────────────────
//
// ent's builders are typed, so claim and beat exist once per job type. The
// logic is identical in all three and deliberately kept side by side rather
// than spread across the handler files: this is where "exactly one runner"
// is enforced, and it should be readable in one place.
//
// runner is threaded as a parameter rather than read from the package-level
// runnerID so a test can simulate two replicas in one process — the thing
// these functions exist to make safe. Production always passes runnerID.
//
// claim* transitions pending → running for THIS runner. The WHERE clause on
// status is what makes it atomic — PostgreSQL row-locks, so exactly one
// caller changes a row and every other gets zero back. A claim that returns
// false means someone else already has the job; the caller must not run it.
//
// beat* refreshes the heartbeat ONLY while this runner still owns the row.
// Filtering on locked_by is the fence: a runner whose job was reaped and
// re-claimed updates zero rows and learns it has been superseded.

func claimExportJob(ctx context.Context, db *ent.Client, jobID uuid.UUID, runner string) (bool, error) {
	now := time.Now()
	n, err := db.ExportJob.Update().
		Where(exportjob.ID(jobID), exportjob.StatusEQ(exportjob.StatusPending)).
		SetStatus(exportjob.StatusRunning).
		SetLockedBy(runner).
		SetHeartbeatAt(now).
		SetStartedAt(now).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("claim export job: %w", err)
	}
	return n == 1, nil
}

func beatExportJob(ctx context.Context, db *ent.Client, jobID uuid.UUID, runner string) (bool, error) {
	n, err := db.ExportJob.Update().
		Where(exportjob.ID(jobID), exportjob.LockedByEQ(runner)).
		SetHeartbeatAt(time.Now()).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("heartbeat export job: %w", err)
	}
	return n == 1, nil
}

func claimBackup(ctx context.Context, db *ent.Client, jobID uuid.UUID, runner string) (bool, error) {
	now := time.Now()
	n, err := db.Backup.Update().
		Where(backup.ID(jobID), backup.StatusEQ(backup.StatusPending)).
		SetStatus(backup.StatusRunning).
		SetLockedBy(runner).
		SetHeartbeatAt(now).
		SetStartedAt(now).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("claim backup: %w", err)
	}
	return n == 1, nil
}

func beatBackup(ctx context.Context, db *ent.Client, jobID uuid.UUID, runner string) (bool, error) {
	n, err := db.Backup.Update().
		Where(backup.ID(jobID), backup.LockedByEQ(runner)).
		SetHeartbeatAt(time.Now()).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("heartbeat backup: %w", err)
	}
	return n == 1, nil
}

func claimRestoreJob(ctx context.Context, db *ent.Client, jobID uuid.UUID, runner string) (bool, error) {
	now := time.Now()
	n, err := db.RestoreJob.Update().
		Where(restorejob.ID(jobID), restorejob.StatusEQ(restorejob.StatusPending)).
		SetStatus(restorejob.StatusRunning).
		SetLockedBy(runner).
		SetHeartbeatAt(now).
		SetStartedAt(now).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("claim restore job: %w", err)
	}
	return n == 1, nil
}

func beatRestoreJob(ctx context.Context, db *ent.Client, jobID uuid.UUID, runner string) (bool, error) {
	n, err := db.RestoreJob.Update().
		Where(restorejob.ID(jobID), restorejob.LockedByEQ(runner)).
		SetHeartbeatAt(time.Now()).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("heartbeat restore job: %w", err)
	}
	return n == 1, nil
}
