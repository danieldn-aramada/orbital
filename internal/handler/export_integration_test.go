//go:build integration

package handler_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/testutil"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	_ "github.com/lib/pq"
)

// scratchExportDir is the host-side path mounted to /dgraph/export inside the
// test scratch DGraph container. Must match the volume mount in deploy/test/docker-compose.yml.
const scratchExportDir = "/tmp/orbital-test-scratch"

// blueExportDir is the host-side path mounted to /dgraph/export inside the
// test blue DGraph container. Used by backup tests.
const blueExportDir = "/tmp/orbital-test-blue"

var (
	testDB   *ent.Client
	testDcID string
)

func TestMain(m *testing.M) {
	if err := setupExportSuite(); err != nil {
		log.Fatalf("export integration test setup: %v", err)
	}
	os.Exit(m.Run())
}

func setupExportSuite() error {
	// Open ent client.
	var err error
	testDB, err = ent.Open("postgres", testutil.TestDatabaseURL())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	if err := testDB.Schema.Create(context.Background()); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}

	// Truncate all operational tables so stale records from previous runs don't interfere.
	if err := testutil.TruncateAllE(); err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}

	// Wipe DGraph and apply the GraphQL schema.
	if err := testutil.ResetDGraphE(testutil.DGraphAdminURL(), schemaPath()); err != nil {
		return fmt.Errorf("reset dgraph: %w", err)
	}

	// Brief pause — DGraph may take a moment to activate the new schema.
	time.Sleep(2 * time.Second)

	// Seed one Namespace + DataCenter so the export pipeline has data to work with.
	_, testDcID, err = testutil.SeedMinimalE(testutil.DGraphURL())
	if err != nil {
		return fmt.Errorf("seed dgraph: %w", err)
	}

	// Ensure export host dirs exist.
	if err := os.MkdirAll(scratchExportDir, 0o755); err != nil {
		return fmt.Errorf("create scratch export dir: %w", err)
	}
	if err := os.MkdirAll(blueExportDir, 0o755); err != nil {
		return fmt.Errorf("create blue export dir: %w", err)
	}

	// Ensure the MinIO test bucket exists.
	if err := testutil.EnsureTestBucketE(); err != nil {
		return fmt.Errorf("ensure test bucket: %w", err)
	}

	return nil
}

// schemaPath returns the path to schema-demo.graphql relative to this package.
// go test sets the working directory to the package directory (internal/handler/).
func schemaPath() string {
	if v := os.Getenv("TEST_SCHEMA_PATH"); v != "" {
		return v
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(root, "schema", "schema-demo.graphql")
}

// newExportHandler creates an Export handler wired to the test services.
func newExportHandler(t *testing.T) *handler.Export {
	t.Helper()
	exportDir := t.TempDir()
	return handler.NewExport(
		testDB,
		testutil.DGraphURL(),
		testutil.DGraphScratchURL(),
		testutil.DGraphScratchAdminURL(),
		"http://localhost:6081", // scratch Zero HTTP
		exportDir,
		scratchExportDir,
		schemaPath(),
		slog.Default(),
	)
}

// triggerExport calls the Trigger handler with the given DC ID and returns the job ID.
func triggerExport(t *testing.T, h *handler.Export, dcID string) uuid.UUID {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(dcID)

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

func TestExportPipeline_EndToEnd(t *testing.T) {
	h := newExportHandler(t)
	jobID := triggerExport(t, h, testDcID)

	status := testutil.WaitForExportJob(t, testDB, jobID, 90*time.Second)

	if string(status) != "completed" {
		job, _ := testDB.ExportJob.Get(context.Background(), jobID)
		errMsg := ""
		if job != nil && job.Error != nil {
			errMsg = *job.Error
		}
		t.Fatalf("export job ended with status %q: %s", status, errMsg)
	}

	job, err := testDB.ExportJob.Get(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get completed job: %v", err)
	}
	if job.ArtifactPath == nil {
		t.Fatal("completed job has no artifact path")
	}

	assertExportZip(t, *job.ArtifactPath)
}

func TestExportTrigger_ConflictWhenJobInProgress(t *testing.T) {
	h := newExportHandler(t)

	// Trigger first export.
	e := echo.New()
	req1 := httptest.NewRequest(http.MethodPost, "/", nil)
	rec1 := httptest.NewRecorder()
	c1 := e.NewContext(req1, rec1)
	c1.SetParamNames("id")
	c1.SetParamValues(testDcID)
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
	c2.SetParamNames("id")
	c2.SetParamValues(testDcID)
	if err := h.Trigger(c2); err != nil {
		t.Fatalf("second Trigger: %v", err)
	}
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second trigger: expected 409, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Wait for the first job to finish to leave a clean state for subsequent tests.
	var resp struct {
		JobID string `json:"jobId"`
	}
	json.Unmarshal(rec1.Body.Bytes(), &resp)
	if jobID, err := uuid.Parse(resp.JobID); err == nil {
		testutil.WaitForExportJob(t, testDB, jobID, 90*time.Second)
	}
}

func TestExportPipeline_ScratchWipedBetweenRuns(t *testing.T) {
	h := newExportHandler(t)

	// First export.
	jobID1 := triggerExport(t, h, testDcID)
	status1 := testutil.WaitForExportJob(t, testDB, jobID1, 90*time.Second)
	if string(status1) != "completed" {
		t.Fatalf("first export ended with %q", status1)
	}

	job1, _ := testDB.ExportJob.Get(context.Background(), jobID1)
	if job1.ArtifactPath == nil {
		t.Fatal("first job has no artifact path")
	}
	zip1 := readZipFiles(t, *job1.ArtifactPath)

	// Second export.
	jobID2 := triggerExport(t, h, testDcID)
	status2 := testutil.WaitForExportJob(t, testDB, jobID2, 90*time.Second)
	if string(status2) != "completed" {
		t.Fatalf("second export ended with %q", status2)
	}

	job2, _ := testDB.ExportJob.Get(context.Background(), jobID2)
	if job2.ArtifactPath == nil {
		t.Fatal("second job has no artifact path")
	}
	zip2 := readZipFiles(t, *job2.ArtifactPath)

	// Both should contain the same files (data.json.gz and schema.gz).
	// The content size from a clean scratch should be consistent.
	if len(zip1) != len(zip2) {
		t.Errorf("zip entry counts differ: first=%d second=%d", len(zip1), len(zip2))
	}
}

func TestExportPipeline_FailsWhenScratchUnreachable(t *testing.T) {
	exportDir := t.TempDir()
	h := handler.NewExport(
		testDB,
		testutil.DGraphURL(),
		"http://localhost:19999/graphql", // unreachable scratch
		"http://localhost:19999/admin",
		"http://localhost:19999",
		exportDir,
		scratchExportDir,
		schemaPath(),
		slog.Default(),
	)

	jobID := triggerExport(t, h, testDcID)
	status := testutil.WaitForExportJob(t, testDB, jobID, 30*time.Second)

	if string(status) != "failed" {
		t.Errorf("expected export to fail with unreachable scratch, got %q", status)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────────

// assertExportZip verifies that the artifact zip at path contains non-empty
// data.json.gz and schema.gz entries.
func assertExportZip(t *testing.T, path string) {
	t.Helper()

	files := readZipFiles(t, path)

	data, ok := files["data.json.gz"]
	if !ok {
		t.Error("artifact zip missing data.json.gz")
	} else if len(data) == 0 {
		t.Error("data.json.gz is empty")
	}

	schema, ok := files["schema.gz"]
	if !ok {
		t.Error("artifact zip missing schema.gz")
	} else if len(schema) == 0 {
		t.Error("schema.gz is empty")
	}
}

// readZipFiles opens a zip archive and returns a map of filename → contents.
func readZipFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()

	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open artifact zip %s: %v", path, err)
	}
	defer r.Close()

	out := make(map[string][]byte, len(r.File))
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", f.Name, err)
		}
		var buf bytes.Buffer
		buf.ReadFrom(rc) //nolint:errcheck
		rc.Close()
		out[f.Name] = buf.Bytes()
	}
	return out
}
