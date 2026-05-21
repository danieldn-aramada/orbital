//go:build integration

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/testutil"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// newRestoreHandler creates a RestoreHandler with a fake Kubernetes client.
// The fake client returns no pods, so any triggered restore job will fail at the
// "find dgraph-live pod" step — which is the expected behaviour in a non-K8s environment.
func newRestoreHandler(t *testing.T) *handler.RestoreHandler {
	t.Helper()
	h, err := handler.NewRestoreHandler(
		context.Background(),
		testDB,
		handler.RestoreConfig{
			S3Endpoint:      testutil.MinIOEndpoint(),
			S3Region:        testutil.TestS3Region,
			S3Bucket:        testutil.TestS3Bucket,
			S3AccessKey:     testutil.TestS3AccessKey,
			S3SecretKey:     testutil.TestS3SecretKey,
			DGraphAdminURL:  testutil.DGraphAdminURL(),
			DGraphAlphaGRPC: "localhost:19080",
			DGraphZeroGRPC:  "localhost:5080",
			DGraphNamespace: "default",
			SchemaPath:      schemaPath(),
			RestoreDir:      t.TempDir(),
			RestoreTimeout:  30 * time.Second,
		},
		k8sfake.NewSimpleClientset(),
		&rest.Config{Host: "http://localhost"},
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("NewRestoreHandler: %v", err)
	}
	return h
}

// createCompletedBackup inserts a completed backup record directly into PostgreSQL.
// Used to bootstrap restore trigger tests without running a full backup pipeline.
func createCompletedBackup(t *testing.T) uuid.UUID {
	t.Helper()
	b, err := testDB.Backup.Create().
		SetStatus(backup.StatusCompleted).
		SetS3Key("test-backup.zip").
		SetS3Bucket(testutil.TestS3Bucket).
		SetS3Endpoint(testutil.MinIOEndpoint()).
		SetChecksum("deadbeef").
		SetCreatedBy("test").
		SetCompletedAt(time.Now()).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create completed backup: %v", err)
	}
	return b.ID
}

// triggerRestore calls the Trigger handler with the given backup ID.
func triggerRestore(t *testing.T, h *handler.RestoreHandler, backupID uuid.UUID) (int, []byte) {
	t.Helper()

	body, _ := json.Marshal(map[string]string{"backupId": backupID.String()})
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Trigger(c); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	return rec.Code, rec.Body.Bytes()
}

// ── Tests ──────────────────────────────────────────────────────────────────────

func TestRestoreTrigger_InvalidBackupID(t *testing.T) {
	h := newRestoreHandler(t)

	body := []byte(`{"backupId": "not-a-uuid"}`)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Trigger(c); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRestoreTrigger_BackupNotFound(t *testing.T) {
	h := newRestoreHandler(t)
	code, body := triggerRestore(t, h, uuid.New())
	if code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-existent backup, got %d: %s", code, body)
	}
}

func TestRestoreTrigger_BackupNotCompleted(t *testing.T) {
	h := newRestoreHandler(t)

	// Insert a backup in failed status — not completed, but also not "in progress"
	// (pending/running would trigger a 409 conflict from the restore handler).
	failedBackup, err := testDB.Backup.Create().
		SetStatus(backup.StatusFailed).
		SetCreatedBy("test").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create failed backup: %v", err)
	}

	code, body := triggerRestore(t, h, failedBackup.ID)
	if code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-completed backup, got %d: %s", code, body)
	}
}

func TestRestoreTrigger_FailsGracefullyWithoutK8s(t *testing.T) {
	backupID := createCompletedBackup(t)
	h := newRestoreHandler(t)

	code, respBody := triggerRestore(t, h, backupID)
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", code, respBody)
	}

	var resp struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	jobID, err := uuid.Parse(resp.JobID)
	if err != nil {
		t.Fatalf("parse job ID: %v", err)
	}

	// The restore goroutine will fail looking for a dgraph-live K8s pod.
	status := testutil.WaitForRestoreJob(t, testDB, jobID, 30*time.Second)
	if string(status) != "failed" {
		t.Errorf("expected restore to fail without K8s, got %q", status)
	}

	job, err := testDB.RestoreJob.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get restore job: %v", err)
	}
	if job.Error == nil || *job.Error == "" {
		t.Error("failed restore job has no error message")
	}
}
