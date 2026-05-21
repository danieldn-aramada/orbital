//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/internal/handler"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// newExportListHandler creates an Export handler wired to testDB.
// DGraph URLs are empty because List/Status don't use them.
func newExportListHandler(t *testing.T) *handler.Export {
	t.Helper()
	return handler.NewExport(
		testDB,
		"", "", "", "", // DGraph URLs — unused by List/Status
		t.TempDir(),    // exportDir
		t.TempDir(),    // scratchExportDir
		"",             // schemaPath
		slog.Default(),
	)
}

// exportAPICtx builds an Echo context for a GET request with optional path params.
func exportAPICtx(method, url string, params map[string]string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, url, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	for k, v := range params {
		c.SetParamNames(k)
		c.SetParamValues(v)
	}
	return c, rec
}

// ── Export.List ───────────────────────────────────────────────────────────────

func TestExportList_EmptyReturnsEmptyArray(t *testing.T) {
	ctx := context.Background()
	// Ensure no jobs exist from prior runs.
	testDB.ExportJob.Delete().ExecX(ctx)

	h := newExportListHandler(t)
	c, rec := exportAPICtx(http.MethodGet, "/api/v1/export/jobs", nil)

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("expected empty array, got %d items", len(body))
	}
}

func TestExportList_ReturnsJobsWithRequiredFields(t *testing.T) {
	ctx := context.Background()
	testDB.ExportJob.Delete().ExecX(ctx)

	// Create two jobs.
	j1 := testDB.ExportJob.Create().
		SetDatacenterID("dc-1").
		SetDatacenterName("alpha").
		SetStatus(exportjob.StatusPending).
		SaveX(ctx)
	j2 := testDB.ExportJob.Create().
		SetDatacenterID("dc-2").
		SetDatacenterName("beta").
		SetStatus(exportjob.StatusCompleted).
		SaveX(ctx)
	t.Cleanup(func() {
		testDB.ExportJob.DeleteOne(j1).ExecX(ctx)
		testDB.ExportJob.DeleteOne(j2).ExecX(ctx)
	})

	h := newExportListHandler(t)
	c, rec := exportAPICtx(http.MethodGet, "/api/v1/export/jobs", nil)

	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(body))
	}

	// Each item must have required fields.
	for _, item := range body {
		for _, field := range []string{"jobId", "dataCenter", "status", "createdAt"} {
			if _, ok := item[field]; !ok {
				t.Errorf("missing field %q in job response", field)
			}
		}
	}

	// Verify the DC names appear somewhere in the response.
	names := map[string]bool{}
	for _, item := range body {
		if dc, ok := item["dataCenter"].(string); ok {
			names[dc] = true
		}
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected dataCenter names alpha and beta, got %v", names)
	}
}

// ── Export.Status ─────────────────────────────────────────────────────────────

func TestExportStatus_ReturnsJobFields(t *testing.T) {
	ctx := context.Background()
	job := testDB.ExportJob.Create().
		SetDatacenterID("dc-status-test").
		SetDatacenterName("gamma").
		SetStatus(exportjob.StatusRunning).
		SaveX(ctx)
	t.Cleanup(func() { testDB.ExportJob.DeleteOne(job).ExecX(ctx) })

	h := newExportListHandler(t)
	c, rec := exportAPICtx(http.MethodGet, "/api/v1/export/jobs/"+job.ID.String(),
		map[string]string{"jobId": job.ID.String()})

	if err := h.Status(c); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["jobId"] != job.ID.String() {
		t.Errorf("jobId: got %v, want %q", body["jobId"], job.ID.String())
	}
	if body["status"] != "running" {
		t.Errorf("status: got %v, want running", body["status"])
	}
	if body["dataCenter"] != "gamma" {
		t.Errorf("dataCenter: got %v, want gamma", body["dataCenter"])
	}
}

func TestExportStatus_InvalidUUID_Returns400(t *testing.T) {
	h := newExportListHandler(t)
	c, _ := exportAPICtx(http.MethodGet, "/api/v1/export/jobs/not-a-uuid",
		map[string]string{"jobId": "not-a-uuid"})

	err := h.Status(c)
	if err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	}
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusBadRequest {
		t.Errorf("expected 400 HTTPError, got %v", err)
	}
}

func TestExportStatus_UnknownUUID_Returns404(t *testing.T) {
	h := newExportListHandler(t)
	missing := uuid.New().String()
	c, _ := exportAPICtx(http.MethodGet, "/api/v1/export/jobs/"+missing,
		map[string]string{"jobId": missing})

	err := h.Status(c)
	if err == nil {
		t.Fatal("expected error for unknown UUID, got nil")
	}
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusNotFound {
		t.Errorf("expected 404 HTTPError, got %v", err)
	}
}
