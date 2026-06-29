//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/user"
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
		RetentionDays:     30,
		RetentionMinCount: 3,
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
		JobID string `json:"id"`
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

	if string(status) != "completed" {
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
	if job.S3Key == "" {
		t.Error("completed backup has no S3 key")
	}
}

func TestBackupPipeline_MultipleBackupsEachUpload(t *testing.T) {
	h := newBackupHandler(t)

	jobID1 := triggerBackup(t, h)
	status1 := testutil.WaitForBackupJob(t, testDB, jobID1, 90*time.Second)
	if string(status1) != "completed" {
		t.Fatalf("first backup ended with %q, want completed", status1)
	}

	jobID2 := triggerBackup(t, h)
	status2 := testutil.WaitForBackupJob(t, testDB, jobID2, 90*time.Second)
	if string(status2) != "completed" {
		t.Fatalf("second backup ended with %q, want completed", status2)
	}

	job2, err := testDB.Backup.Get(context.Background(), jobID2)
	if err != nil {
		t.Fatalf("get second backup: %v", err)
	}
	if job2.S3Key == "" {
		t.Error("second backup has no S3 key")
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
		JobID string `json:"id"`
	}
	json.Unmarshal(rec1.Body.Bytes(), &resp) //nolint:errcheck
	if jobID, err := uuid.Parse(resp.JobID); err == nil {
		testutil.WaitForBackupJob(t, testDB, jobID, 90*time.Second)
	}
}

func TestBackupsPage_RendersExpectedElements(t *testing.T) {
	t.Chdir("../..")

	userID := createTestUser(t, "backups-render@test.com", user.RoleAdmin)

	ui := handler.NewUI(
		false, "", "",
		false, false,
		true,
		testutil.MinIOEndpoint(), testutil.TestS3Bucket,
		"",
		testDB,
		slog.Default(),
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/backups", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("is_authn", true)
	c.Set("user_id", userID)

	if err := ui.Backups(c); err != nil {
		t.Fatalf("Backups: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	html := rec.Body.String()
	for _, want := range []string{
		"Backup Graph",
		"object storage",
		`id="backup-tbody"`,
		`id="btn-backup"`,
		`id="btn-test-backup-connection"`,
		`id="delete-modal"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected HTML to contain %q", want)
		}
	}
	if strings.Contains(html, `id="delete-modal" class="modal is-active"`) {
		t.Error("delete-modal must not be active on initial page load")
	}
	// Storage location input must show endpoint/bucket, not the unconfigured placeholder
	if !strings.Contains(html, testutil.MinIOEndpoint()) {
		t.Errorf("expected storage location to contain MinIO endpoint %q", testutil.MinIOEndpoint())
	}
}
