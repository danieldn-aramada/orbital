//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/testutil"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// newBackupHandler creates a BackupHandler wired to the test stack.
func newBackupHandler(t *testing.T) *handler.BackupHandler {
	t.Helper()
	h, err := handler.NewBackupHandler(context.Background(), testDB, handler.BackupConfig{
		DGraphAdminURL:  testutil.DGraphAdminURL(),
		DGraphExportDir: blueExportDir,
		SchemaPath:      schemaPath(),
		S3Endpoint:      testutil.MinIOEndpoint(),
		S3Region:        testutil.TestS3Region,
		S3Bucket:        testutil.TestS3Bucket,
		S3AccessKey:     testutil.TestS3AccessKey,
		S3SecretKey:     testutil.TestS3SecretKey,
		RetentionCount:  3,
		Version:         "test",
	}, slog.Default())
	if err != nil {
		t.Fatalf("NewBackupHandler: %v", err)
	}
	return h
}

// triggerBackup calls the Trigger handler and returns the job ID.
func triggerBackup(t *testing.T, h *handler.BackupHandler) uuid.UUID {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Trigger(c); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse trigger response: %v", err)
	}
	jobID, err := uuid.Parse(resp.JobID)
	if err != nil {
		t.Fatalf("parse job ID %q: %v", resp.JobID, err)
	}
	return jobID
}

// ── Tests ──────────────────────────────────────────────────────────────────────

func TestBackupPipeline_EndToEnd(t *testing.T) {
	h := newBackupHandler(t)
	jobID := triggerBackup(t, h)

	status := testutil.WaitForBackupJob(t, testDB, jobID, 90*time.Second)

	if string(status) != "completed" && string(status) != "skipped" {
		job, _ := testDB.Backup.Get(context.Background(), jobID)
		errMsg := ""
		if job != nil && job.Error != nil {
			errMsg = *job.Error
		}
		t.Fatalf("backup job ended with status %q: %s", status, errMsg)
	}

	job, err := testDB.Backup.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get completed job: %v", err)
	}
	if string(status) == "completed" && job.S3Key == "" {
		t.Error("completed backup has no S3 key")
	}
}

func TestBackupPipeline_SkipsWhenUnchanged(t *testing.T) {
	h := newBackupHandler(t)

	// First backup: may be "completed" or "skipped" depending on whether a prior
	// test already uploaded a backup with the same checksum. Both are acceptable.
	jobID1 := triggerBackup(t, h)
	status1 := testutil.WaitForBackupJob(t, testDB, jobID1, 90*time.Second)
	if string(status1) != "completed" && string(status1) != "skipped" {
		t.Fatalf("first backup ended with %q, want completed or skipped", status1)
	}

	// Second backup against unchanged graph must be skipped.
	jobID2 := triggerBackup(t, h)
	status2 := testutil.WaitForBackupJob(t, testDB, jobID2, 90*time.Second)
	if string(status2) != "skipped" {
		t.Errorf("second backup: expected skipped, got %q", status2)
	}
}

func TestBackupTrigger_ConflictWhenInProgress(t *testing.T) {
	h := newBackupHandler(t)

	e := echo.New()

	req1 := httptest.NewRequest(http.MethodPost, "/", nil)
	rec1 := httptest.NewRecorder()
	c1 := e.NewContext(req1, rec1)
	if err := h.Trigger(c1); err != nil {
		t.Fatalf("first Trigger: %v", err)
	}
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first trigger: expected 202, got %d", rec1.Code)
	}

	// Immediately trigger a second — should 409.
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)
	if err := h.Trigger(c2); err != nil {
		t.Fatalf("second Trigger: %v", err)
	}
	if rec2.Code != http.StatusConflict {
		t.Errorf("second trigger: expected 409, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Wait for first job to finish so the DB is clean for subsequent tests.
	var resp struct {
		JobID string `json:"jobId"`
	}
	json.Unmarshal(rec1.Body.Bytes(), &resp) //nolint:errcheck
	if jobID, err := uuid.Parse(resp.JobID); err == nil {
		testutil.WaitForBackupJob(t, testDB, jobID, 90*time.Second)
	}
}
