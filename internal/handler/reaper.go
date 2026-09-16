package handler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/restorejob"
)

// The reaper marks jobs failed when the runner that claimed them has died.
//
// It used to run once in server.New() and fail EVERY pending/running job,
// on the assumption that a process starting meant no job could be alive.
// That assumption holds at exactly one replica and is destructive at two:
// a second pod booting would mark the first pod's in-flight restore failed
// while it was still executing drop_all. Death is now decided by a stale
// heartbeat, which is true regardless of how many replicas exist.
//
// The reaper never touches terminal states. exportjob.StatusStale in
// particular is NOT a liveness state — it means the exported artifact no
// longer reflects current intent — and is excluded by the pending/running
// filter rather than by name.
//
// No audit event is written. AUDIT.md § Row admission: technical failures
// are not audited, and markFailed (the path this replaces) writes none
// either. The job row IS the durable record — status, error and locked_by,
// readable through GET /api/v1/export/jobs/:id.

// StartReaper sweeps for dead jobs every interval until ctx is cancelled.
// Call as a goroutine. Every replica runs it; the advisory lock inside the
// sweep means only one actually does the work on any given tick.
func StartReaper(ctx context.Context, db *ent.Client, rawDB *sql.DB, cfg JobLeaseConfig, logger *slog.Logger) {
	if db == nil {
		return
	}
	cfg = cfg.withDefaults()
	logger.Info("job reaper started",
		"interval", cfg.HeartbeatInterval.String(),
		"stale_after", cfg.StaleAfter.String(),
		"orphan_grace", cfg.OrphanGrace.String(),
		"runner", runnerID)

	ticker := time.NewTicker(cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("job reaper stopped")
			return
		case <-ticker.C:
			ReapStaleJobs(ctx, db, rawDB, cfg, logger)
		}
	}
}

// ReapStaleJobs runs one sweep. Exported so tests can drive a single pass
// without a ticker.
func ReapStaleJobs(ctx context.Context, db *ent.Client, rawDB *sql.DB, cfg JobLeaseConfig, logger *slog.Logger) {
	if db == nil {
		return
	}
	cfg = cfg.withDefaults()

	acquired, release, err := tryAdvisoryLockKey(ctx, rawDB, reaperAdvisoryLockKey)
	if err != nil {
		logger.Warn("reaper: advisory lock error", "err", err)
		return
	}
	if !acquired {
		return // another replica is sweeping this tick
	}
	defer release()

	now := time.Now()
	staleBefore := now.Add(-cfg.StaleAfter)
	orphanBefore := now.Add(-cfg.OrphanGrace)

	reapExportJobs(ctx, db, staleBefore, orphanBefore, logger)
	reapBackups(ctx, db, staleBefore, orphanBefore, logger)
	reapRestoreJobs(ctx, db, staleBefore, orphanBefore, logger)
}

// reapReason explains a reaping in the job's error field. It names the runner
// that held the job so an operator reading the row knows which pod to look at
// — the whole point of storing locked_by rather than a bare token.
func reapReason(lockedBy *string, orphan bool) string {
	if orphan {
		if lockedBy == nil || *lockedBy == "" {
			return "interrupted: no runner ever claimed this job (orphaned by a restart)"
		}
		return fmt.Sprintf("interrupted: runner %s left no heartbeat (orphaned by a restart)", *lockedBy)
	}
	if lockedBy == nil || *lockedBy == "" {
		return "interrupted: runner stopped heartbeating"
	}
	return fmt.Sprintf("interrupted: runner %s stopped heartbeating", *lockedBy)
}

func reapExportJobs(ctx context.Context, db *ent.Client, staleBefore, orphanBefore time.Time, logger *slog.Logger) {
	jobs, err := db.ExportJob.Query().
		Where(
			exportjob.StatusIn(exportjob.StatusPending, exportjob.StatusRunning),
			exportjob.Or(
				// Claimed, then stopped proving it was alive.
				exportjob.And(exportjob.HeartbeatAtNotNil(), exportjob.HeartbeatAtLT(staleBefore)),
				// Never heartbeated at all — predates leases, or died between
				// row creation and claim. Longer grace: under a rolling update
				// one of these may still be running in the outgoing pod.
				exportjob.And(exportjob.HeartbeatAtIsNil(), exportjob.CreatedAtLT(orphanBefore)),
			),
		).All(ctx)
	if err != nil {
		logger.Error("reaper: query stale export jobs", "err", err)
		return
	}
	for _, j := range jobs {
		reason := reapReason(j.LockedBy, j.HeartbeatAt == nil)
		n, err := db.ExportJob.Update().
			Where(exportjob.ID(j.ID), exportjob.StatusIn(exportjob.StatusPending, exportjob.StatusRunning)).
			SetStatus(exportjob.StatusFailed).
			SetError(reason).
			Save(ctx)
		if err != nil {
			logger.Error("reaper: fail export job", "job_id", j.ID, "err", err)
			continue
		}
		if n > 0 {
			logger.Warn("reaper: marked export job failed", "job_id", j.ID, "locked_by", derefStr(j.LockedBy), "reason", reason)
		}
	}
}

func reapBackups(ctx context.Context, db *ent.Client, staleBefore, orphanBefore time.Time, logger *slog.Logger) {
	jobs, err := db.Backup.Query().
		Where(
			backup.StatusIn(backup.StatusPending, backup.StatusRunning),
			backup.Or(
				backup.And(backup.HeartbeatAtNotNil(), backup.HeartbeatAtLT(staleBefore)),
				backup.And(backup.HeartbeatAtIsNil(), backup.CreatedAtLT(orphanBefore)),
			),
		).All(ctx)
	if err != nil {
		logger.Error("reaper: query stale backups", "err", err)
		return
	}
	for _, j := range jobs {
		reason := reapReason(j.LockedBy, j.HeartbeatAt == nil)
		n, err := db.Backup.Update().
			Where(backup.ID(j.ID), backup.StatusIn(backup.StatusPending, backup.StatusRunning)).
			SetStatus(backup.StatusFailed).
			SetError(reason).
			Save(ctx)
		if err != nil {
			logger.Error("reaper: fail backup", "job_id", j.ID, "err", err)
			continue
		}
		if n > 0 {
			logger.Warn("reaper: marked backup failed", "job_id", j.ID, "locked_by", derefStr(j.LockedBy), "reason", reason)
		}
	}
}

func reapRestoreJobs(ctx context.Context, db *ent.Client, staleBefore, orphanBefore time.Time, logger *slog.Logger) {
	jobs, err := db.RestoreJob.Query().
		Where(
			restorejob.StatusIn(restorejob.StatusPending, restorejob.StatusRunning),
			restorejob.Or(
				restorejob.And(restorejob.HeartbeatAtNotNil(), restorejob.HeartbeatAtLT(staleBefore)),
				restorejob.And(restorejob.HeartbeatAtIsNil(), restorejob.CreatedAtLT(orphanBefore)),
			),
		).All(ctx)
	if err != nil {
		logger.Error("reaper: query stale restore jobs", "err", err)
		return
	}
	for _, j := range jobs {
		reason := reapReason(j.LockedBy, j.HeartbeatAt == nil)
		n, err := db.RestoreJob.Update().
			Where(restorejob.ID(j.ID), restorejob.StatusIn(restorejob.StatusPending, restorejob.StatusRunning)).
			SetStatus(restorejob.StatusFailed).
			SetError(reason).
			Save(ctx)
		if err != nil {
			logger.Error("reaper: fail restore job", "job_id", j.ID, "err", err)
			continue
		}
		if n > 0 {
			logger.Warn("reaper: marked restore job failed", "job_id", j.ID, "locked_by", derefStr(j.LockedBy), "reason", reason)
		}
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
