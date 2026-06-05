//go:build integration

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// newScheduleHandler creates a BackupHandler without S3 or RawDB — sufficient
// for schedule-only integration tests that don't actually run backups.
func newScheduleHandler(t *testing.T) *handler.BackupHandler {
	t.Helper()
	h, err := handler.NewBackupHandler(context.Background(), testDB, handler.BackupConfig{
		DGraphAdminURL:    testutil.DGraphAdminURL(),
		DGraphExportDir:   blueExportDir,
		SchemaPath:        schemaPath(),
		S3Endpoint:        testutil.MinIOEndpoint(),
		S3Region:          testutil.TestS3Region,
		S3Bucket:          testutil.TestS3Bucket,
		S3AccessKey:       testutil.TestS3AccessKey,
		S3SecretKey:       testutil.TestS3SecretKey,
		RetentionDays:     30,
		RetentionMinCount: 3,
		Version:           "test",
		// RawDB intentionally nil — advisory lock skipped in tests
	}, slogDiscard())
	if err != nil {
		t.Fatalf("NewBackupHandler: %v", err)
	}
	return h
}

func TestBootstrapSchedule_CreatesRow(t *testing.T) {
	testDB.BackupSchedule.Delete().ExecX(context.Background())

	h, err := handler.NewBackupHandler(context.Background(), testDB, handler.BackupConfig{
		DGraphAdminURL:    testutil.DGraphAdminURL(),
		DGraphExportDir:   blueExportDir,
		SchemaPath:        schemaPath(),
		S3Endpoint:        testutil.MinIOEndpoint(),
		S3Region:          testutil.TestS3Region,
		S3Bucket:          testutil.TestS3Bucket,
		S3AccessKey:       testutil.TestS3AccessKey,
		S3SecretKey:       testutil.TestS3SecretKey,
		RetentionDays:     14,
		RetentionMinCount: 3,
		Version:           "test",
		BootstrapCronSpec: "0 0 * * *",
	}, slogDiscard())
	if err != nil {
		t.Fatalf("NewBackupHandler: %v", err)
	}

	if err := h.BootstrapSchedule(context.Background()); err != nil {
		t.Fatalf("BootstrapSchedule: %v", err)
	}

	sched, err := testDB.BackupSchedule.Query().First(context.Background())
	if err != nil {
		t.Fatalf("query schedule: %v", err)
	}
	if sched.CronSpec != "0 0 * * *" {
		t.Errorf("cronSpec: got %q, want %q", sched.CronSpec, "0 0 * * *")
	}
	if !sched.Enabled {
		t.Error("expected enabled=true by default")
	}
}

func TestBootstrapSchedule_IgnoresEnvWhenRowExists(t *testing.T) {
	testDB.BackupSchedule.Delete().ExecX(context.Background())
	existing, err := testDB.BackupSchedule.Create().
		SetCronSpec("0 * * * *").
		SetEnabled(true).
		SetCreatedBy("test").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create seed schedule: %v", err)
	}

	h, err := handler.NewBackupHandler(context.Background(), testDB, handler.BackupConfig{
		DGraphAdminURL:    testutil.DGraphAdminURL(),
		DGraphExportDir:   blueExportDir,
		SchemaPath:        schemaPath(),
		S3Endpoint:        testutil.MinIOEndpoint(),
		S3Region:          testutil.TestS3Region,
		S3Bucket:          testutil.TestS3Bucket,
		S3AccessKey:       testutil.TestS3AccessKey,
		S3SecretKey:       testutil.TestS3SecretKey,
		RetentionDays:     14,
		RetentionMinCount: 3,
		Version:           "test",
		BootstrapCronSpec: "0 0 * * *", // different spec — should be ignored
	}, slogDiscard())
	if err != nil {
		t.Fatalf("NewBackupHandler: %v", err)
	}

	if err := h.BootstrapSchedule(context.Background()); err != nil {
		t.Fatalf("BootstrapSchedule: %v", err)
	}

	after, err := testDB.BackupSchedule.Get(context.Background(), existing.ID)
	if err != nil {
		t.Fatalf("get schedule after bootstrap: %v", err)
	}
	if after.CronSpec != existing.CronSpec {
		t.Errorf("bootstrap should not overwrite existing row: got %q, want %q",
			after.CronSpec, existing.CronSpec)
	}
}

func TestBootstrapSchedule_NoopWhenEmpty(t *testing.T) {
	h, err := handler.NewBackupHandler(context.Background(), testDB, handler.BackupConfig{
		DGraphAdminURL:    testutil.DGraphAdminURL(),
		DGraphExportDir:   blueExportDir,
		SchemaPath:        schemaPath(),
		S3Endpoint:        testutil.MinIOEndpoint(),
		S3Region:          testutil.TestS3Region,
		S3Bucket:          testutil.TestS3Bucket,
		S3AccessKey:       testutil.TestS3AccessKey,
		S3SecretKey:       testutil.TestS3SecretKey,
		RetentionDays:     14,
		RetentionMinCount: 3,
		Version:           "test",
		BootstrapCronSpec: "", // empty → no-op
	}, slogDiscard())
	if err != nil {
		t.Fatalf("NewBackupHandler: %v", err)
	}

	// No error should occur even when no row exists.
	if err := h.BootstrapSchedule(context.Background()); err != nil {
		t.Fatalf("BootstrapSchedule with empty interval: %v", err)
	}
}

func TestGetSchedule_NoSchedule(t *testing.T) {
	h := newScheduleHandler(t)
	// Ensure no schedule exists.
	testDB.BackupSchedule.Delete().ExecX(context.Background())

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/backup/schedule", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.GetSchedule(c); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if body != nil {
		// expect null
		t.Errorf("expected null body when no schedule exists, got %v", body)
	}
}

func TestGetSchedule_WithSchedule(t *testing.T) {
	h := newScheduleHandler(t)
	testDB.BackupSchedule.Delete().ExecX(context.Background())
	_, err := testDB.BackupSchedule.Create().
		SetCronSpec("0 0 * * *").
		SetEnabled(true).
		SetCreatedBy("test").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/backup/schedule", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.GetSchedule(c); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		CronSpec      string `json:"cronSpec"`
		Enabled       bool   `json:"enabled"`
		NextRunApprox string `json:"nextRunApprox"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse schedule body: %v", err)
	}
	if body.CronSpec != "0 0 * * *" {
		t.Errorf("cronSpec: got %q, want %q", body.CronSpec, "0 0 * * *")
	}
	if !body.Enabled {
		t.Error("expected enabled=true")
	}
	if body.NextRunApprox == "" {
		t.Error("nextRunApprox should be set")
	}
}

func TestUpdateSchedule_Create(t *testing.T) {
	h := newScheduleHandler(t)
	testDB.BackupSchedule.Delete().ExecX(context.Background())

	payload := `{"cronSpec":"0 * * * *","enabled":true}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/backup/schedule",
		bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.UpdateSchedule(c); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	sched, err := testDB.BackupSchedule.Query().First(context.Background())
	if err != nil {
		t.Fatalf("query schedule: %v", err)
	}
	if sched.CronSpec != "0 * * * *" {
		t.Errorf("cronSpec: got %q, want %q", sched.CronSpec, "0 * * * *")
	}
}

func TestUpdateSchedule_MissingCronSpec_Returns400(t *testing.T) {
	h := newScheduleHandler(t)
	testDB.BackupSchedule.Delete().ExecX(context.Background())

	payload := `{"enabled":true}` // no cronSpec → 400 on create
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/backup/schedule",
		bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.UpdateSchedule(c); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateSchedule_InvalidCronSpec_Returns400(t *testing.T) {
	h := newScheduleHandler(t)
	testDB.BackupSchedule.Delete().ExecX(context.Background())

	payload := `{"cronSpec":"not valid cron"}`
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/backup/schedule",
		bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.UpdateSchedule(c); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateSchedule_ToggleEnabled(t *testing.T) {
	h := newScheduleHandler(t)
	testDB.BackupSchedule.Delete().ExecX(context.Background())
	_, err := testDB.BackupSchedule.Create().
		SetCronSpec("0 0 * * *").
		SetEnabled(true).
		SetCreatedBy("test").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	// Disable it.
	disabled := false
	payload, _ := json.Marshal(map[string]any{"enabled": disabled})
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/backup/schedule",
		bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.UpdateSchedule(c); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	sched, err := testDB.BackupSchedule.Query().First(context.Background())
	if err != nil {
		t.Fatalf("query schedule: %v", err)
	}
	if sched.Enabled {
		t.Error("expected enabled=false after toggle")
	}

	// Re-enable it.
	enablePayload, _ := json.Marshal(map[string]any{"enabled": true})
	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/backup/schedule",
		bytes.NewBuffer(enablePayload))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(req2, rec2)

	if err := h.UpdateSchedule(c2); err != nil {
		t.Fatalf("UpdateSchedule re-enable: %v", err)
	}
	sched2, err := testDB.BackupSchedule.Query().First(context.Background())
	if err != nil {
		t.Fatalf("query schedule after re-enable: %v", err)
	}
	if !sched2.Enabled {
		t.Error("expected enabled=true after re-enable")
	}
}

func TestBackupList_IncludesTriggerField(t *testing.T) {
	h := newBackupHandler(t)
	jobID := triggerBackup(t, h)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/backup/jobs", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var jobs []struct {
		ID      string `json:"id"`
		Trigger string `json:"trigger"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("parse jobs: %v", err)
	}

	found := false
	for _, j := range jobs {
		if j.ID == jobID.String() {
			found = true
			if j.Trigger != "manual" {
				t.Errorf("expected trigger=manual, got %q", j.Trigger)
			}
		}
	}
	if !found {
		t.Errorf("job %s not found in list", jobID)
	}
}

// slogDiscard returns a no-op slog logger.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
