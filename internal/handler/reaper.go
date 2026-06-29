package handler

import (
	"context"
	"log/slog"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/restorejob"
)

// ReconcileStaleJobs marks any pending/running jobs as failed on startup.
// When the server crashes mid-job, the row stays in its in-progress state
// permanently — no goroutine is alive to advance it. This runs once in
// server.New() before serving any requests.
func ReconcileStaleJobs(ctx context.Context, db *ent.Client, logger *slog.Logger) {
	if db == nil {
		return
	}

	staleExport, err := db.ExportJob.Update().
		Where(exportjob.StatusIn(exportjob.StatusPending, exportjob.StatusRunning)).
		SetStatus(exportjob.StatusFailed).
		SetError("interrupted: server restarted").
		Save(ctx)
	if err != nil {
		logger.Error("reaper: failed to sweep stale export jobs", "err", err)
	} else if staleExport > 0 {
		logger.Warn("reaper: marked stale export jobs failed", "count", staleExport)
	}

	staleBackup, err := db.Backup.Update().
		Where(backup.StatusIn(backup.StatusPending, backup.StatusRunning)).
		SetStatus(backup.StatusFailed).
		SetError("interrupted: server restarted").
		Save(ctx)
	if err != nil {
		logger.Error("reaper: failed to sweep stale backup jobs", "err", err)
	} else if staleBackup > 0 {
		logger.Warn("reaper: marked stale backup jobs failed", "count", staleBackup)
	}

	staleRestore, err := db.RestoreJob.Update().
		Where(restorejob.StatusIn(restorejob.StatusPending, restorejob.StatusRunning)).
		SetStatus(restorejob.StatusFailed).
		SetError("interrupted: server restarted").
		Save(ctx)
	if err != nil {
		logger.Error("reaper: failed to sweep stale restore jobs", "err", err)
	} else if staleRestore > 0 {
		logger.Warn("reaper: marked stale restore jobs failed", "count", staleRestore)
	}
}
