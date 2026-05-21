//go:build integration

package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/ent/restorejob"
	"github.com/google/uuid"
)

// WaitForExportJob polls an ExportJob until it reaches a terminal status
// (completed, failed, stale) or the timeout elapses. Returns the final status.
func WaitForExportJob(t *testing.T, db *ent.Client, jobID uuid.UUID, timeout time.Duration) exportjob.Status {
	t.Helper()
	status := pollUntilDone(t, timeout, func() (string, bool) {
		job, err := db.ExportJob.Get(context.Background(), jobID)
		if err != nil {
			return "", false
		}
		switch job.Status {
		case exportjob.StatusCompleted, exportjob.StatusFailed, exportjob.StatusStale:
			return string(job.Status), true
		}
		return string(job.Status), false
	})
	return exportjob.Status(status)
}

// WaitForRestoreJob polls a RestoreJob until it reaches a terminal status
// (completed, failed) or the timeout elapses. Returns the final status.
func WaitForRestoreJob(t *testing.T, db *ent.Client, jobID uuid.UUID, timeout time.Duration) restorejob.Status {
	t.Helper()
	status := pollUntilDone(t, timeout, func() (string, bool) {
		job, err := db.RestoreJob.Get(context.Background(), jobID)
		if err != nil {
			return "", false
		}
		switch job.Status {
		case restorejob.StatusCompleted, restorejob.StatusFailed:
			return string(job.Status), true
		}
		return string(job.Status), false
	})
	return restorejob.Status(status)
}

// WaitForBackupJob polls a Backup job until it reaches a terminal status
// (completed, failed, skipped) or the timeout elapses. Returns the final status.
func WaitForBackupJob(t *testing.T, db *ent.Client, jobID uuid.UUID, timeout time.Duration) backup.Status {
	t.Helper()
	status := pollUntilDone(t, timeout, func() (string, bool) {
		job, err := db.Backup.Get(context.Background(), jobID)
		if err != nil {
			return "", false
		}
		switch job.Status {
		case backup.StatusCompleted, backup.StatusFailed, backup.StatusSkipped:
			return string(job.Status), true
		}
		return string(job.Status), false
	})
	return backup.Status(status)
}

// WaitForOCIArtifact polls a RegistryArtifact until it reaches a terminal status
// (completed, failed) or the timeout elapses. Returns the final status.
func WaitForOCIArtifact(t *testing.T, db *ent.Client, artifactID int, timeout time.Duration) registryartifact.Status {
	t.Helper()
	status := pollUntilDone(t, timeout, func() (string, bool) {
		a, err := db.RegistryArtifact.Get(context.Background(), artifactID)
		if err != nil {
			return "", false
		}
		switch a.Status {
		case registryartifact.StatusCompleted, registryartifact.StatusFailed:
			return string(a.Status), true
		}
		return string(a.Status), false
	})
	return registryartifact.Status(status)
}

// pollUntilDone polls fn every 500ms until fn signals done or timeout elapses.
// Returns the terminal status string. Calls t.Fatalf on timeout.
func pollUntilDone(t *testing.T, timeout time.Duration, fn func() (status string, done bool)) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, done := fn()
		if done {
			return status
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("job did not reach terminal state within %s", timeout)
	return ""
}
