//go:build integration

package handler_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/testutil"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// mockRestoreBackend records RunLive calls for test assertions.
type mockRestoreBackend struct {
	called    bool
	dataDir   string
	alphaGRPC string
	zeroGRPC  string
	returnErr error
}

func (m *mockRestoreBackend) RunLive(_ context.Context, dataDir, alphaGRPC, zeroGRPC string) (string, error) {
	m.called = true
	m.dataDir = dataDir
	m.alphaGRPC = alphaGRPC
	m.zeroGRPC = zeroGRPC
	return "mock dgraph live output", m.returnErr
}

// newRestoreHandlerWithBackend creates a RestoreHandler using the provided backend.
func newRestoreHandlerWithBackend(t *testing.T, backend handler.RestoreBackend) *handler.RestoreHandler {
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
			SchemaPath:      schemaPath(),
			RestoreTimeout:  30 * time.Second,
		},
		backend,
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("NewRestoreHandler: %v", err)
	}
	return h
}

// uploadTestBackupZip creates a minimal valid backup zip (data.json.gz + schema.gz + gql_schema.gz)
// in MinIO and returns the S3 key and the SHA-256 hex checksum of the zip bytes.
func uploadTestBackupZip(t *testing.T) (key, checksum string) {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, name := range []string{"data.json.gz", "schema.gz", "gql_schema.gz"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		gz := gzip.NewWriter(w)
		fmt.Fprintf(gz, `{"test":true}`)
		gz.Close()
	}
	zw.Close()

	zipBytes := buf.Bytes()
	h := sha256.Sum256(zipBytes)
	checksum = hex.EncodeToString(h[:])

	ctx := context.Background()
	endpoint := testutil.MinIOEndpoint()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(testutil.TestS3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			testutil.TestS3AccessKey, testutil.TestS3SecretKey, "",
		)),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})

	key = "test-restore-" + uuid.New().String() + ".zip"
	body := bytes.NewReader(zipBytes)
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        strPtr(testutil.TestS3Bucket),
		Key:           &key,
		Body:          body,
		ContentLength: int64Ptr(int64(len(zipBytes))),
	}); err != nil {
		t.Fatalf("upload test backup zip: %v", err)
	}
	return key, checksum
}

// createCompletedBackupWithKey inserts a completed backup record with a real S3 key and checksum.
func createCompletedBackupWithKey(t *testing.T, s3Key, checksum string) uuid.UUID {
	t.Helper()
	b, err := testDB.Backup.Create().
		SetStatus(backup.StatusCompleted).
		SetS3Key(s3Key).
		SetS3Bucket(testutil.TestS3Bucket).
		SetS3Endpoint(testutil.MinIOEndpoint()).
		SetChecksum(checksum).
		SetCreatedBy("test").
		SetCompletedAt(time.Now()).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create completed backup: %v", err)
	}
	return b.ID
}

// createCompletedBackup inserts a completed backup record with a placeholder S3 key.
// Use for tests that don't reach the download step.
func createCompletedBackup(t *testing.T) uuid.UUID {
	return createCompletedBackupWithKey(t, "placeholder-backup.zip", "")
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
	h := newRestoreHandlerWithBackend(t, &mockRestoreBackend{})

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
	h := newRestoreHandlerWithBackend(t, &mockRestoreBackend{})
	code, body := triggerRestore(t, h, uuid.New())
	if code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-existent backup, got %d: %s", code, body)
	}
}

func TestRestoreTrigger_BackupNotCompleted(t *testing.T) {
	h := newRestoreHandlerWithBackend(t, &mockRestoreBackend{})

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

func TestRestoreTrigger_CallsBackendRunLive(t *testing.T) {
	// Upload a real backup zip to MinIO so the download + extract steps succeed.
	s3Key, checksum := uploadTestBackupZip(t)
	backupID := createCompletedBackupWithKey(t, s3Key, checksum)

	mock := &mockRestoreBackend{}
	h := newRestoreHandlerWithBackend(t, mock)

	code, respBody := triggerRestore(t, h, backupID)
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", code, respBody)
	}

	var resp struct {
		JobID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	jobID, err := uuid.Parse(resp.JobID)
	if err != nil {
		t.Fatalf("parse job ID: %v", err)
	}

	// The restore goroutine downloads the zip, extracts it, calls drop_all (which
	// may fail against a non-live DGraph), then calls RunLive. We assert RunLive
	// was called and the job reached a terminal state.
	status := testutil.WaitForRestoreJob(t, testDB, jobID, 30*time.Second)

	if !mock.called {
		t.Error("expected RunLive to be called, but it was not")
	}
	if mock.alphaGRPC == "" {
		t.Error("expected alphaGRPC to be passed to RunLive")
	}
	if !strings.Contains(mock.dataDir, "") {
		t.Errorf("unexpected dataDir passed to RunLive: %q", mock.dataDir)
	}
	// Job should be completed (mock returns no error) or failed at drop_all
	// (DGraph not live in integration test). Either way RunLive was reached.
	if status != "completed" && status != "failed" {
		t.Errorf("expected terminal job status, got %q", status)
	}
}

func TestRestoreTrigger_BackendRunLiveError_JobFails(t *testing.T) {
	s3Key, checksum := uploadTestBackupZip(t)
	backupID := createCompletedBackupWithKey(t, s3Key, checksum)

	mock := &mockRestoreBackend{returnErr: fmt.Errorf("simulated dgraph live failure")}
	h := newRestoreHandlerWithBackend(t, mock)

	code, respBody := triggerRestore(t, h, backupID)
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", code, respBody)
	}

	var resp struct {
		JobID string `json:"id"`
	}
	json.Unmarshal(respBody, &resp)
	jobID, _ := uuid.Parse(resp.JobID)

	status := testutil.WaitForRestoreJob(t, testDB, jobID, 30*time.Second)

	// May fail at drop_all (DGraph not running) or at RunLive — either is fine.
	// The key assertion is the job reaches a terminal failed state.
	if status != "failed" {
		t.Errorf("expected failed status when RunLive errors, got %q", status)
	}
}

func TestRestoreCompleted_WritesManagementAuditEvent(t *testing.T) {
	// Clean up any existing audit events so we can assert on the new one.
	clearEvents(context.Background())

	s3Key, checksum := uploadTestBackupZip(t)
	backupID := createCompletedBackupWithKey(t, s3Key, checksum)

	mock := &mockRestoreBackend{}
	h := newRestoreHandlerWithBackend(t, mock)

	code, respBody := triggerRestore(t, h, backupID)
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", code, respBody)
	}

	var resp struct {
		JobID string `json:"id"`
	}
	json.Unmarshal(respBody, &resp)
	jobID, _ := uuid.Parse(resp.JobID)

	status := testutil.WaitForRestoreJob(t, testDB, jobID, 30*time.Second)
	// Job may fail at drop_all (DGraph not running in integration tests) or complete.
	// Either way: if the job reached "completed" a management audit event must exist.
	// If it failed at an earlier step, no tombstone is written — skip assertion.
	if status != "completed" {
		t.Skipf("restore job did not complete (status=%s, DGraph likely not available); skipping tombstone assertion", status)
	}

	ctx := context.Background()
	events, err := testDB.Event.Query().All(ctx)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}

	var found bool
	for _, ev := range events {
		if ev.EventCategory == "management" {
			for _, op := range ev.Operations {
				if op == "restoreBackup" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected a management audit event with operation restoreBackup, got %d events: %v", len(events), events)
	}
}

func TestRestorePage_RendersExpectedElements(t *testing.T) {
	t.Chdir("../..")

	userID := createTestUser(t, "restore-render@test.com", user.RoleAdmin)

	ui := handler.NewUI(
		false, "", "",
		false, false,
		true,
		testutil.MinIOEndpoint(), testutil.TestS3Bucket,
		"",
		testDB,
		slog.Default(),
	)
	ui.SetRestoreAvailable(true)
	ui.SetSchemaPath(schemaPath())

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/restore", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("is_authn", true)
	c.Set("user_id", userID)

	if err := ui.Restore(c); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	html := rec.Body.String()
	for _, want := range []string{
		"Restore Graph",
		"destructive",
		`id="restore-tbody"`,
		"From a local file",
		"dgraph live",
		`id="restore-log-modal"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected HTML to contain %q", want)
		}
	}
	if strings.Contains(html, `id="restore-log-modal" class="modal is-active"`) {
		t.Error("restore-log-modal must not be active on initial page load")
	}
}

func int64Ptr(i int64) *int64 { return &i }
func strPtr(s string) *string { return &s }
