//go:build integration

package handler_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/oci"
	"github.com/armada/orbital/internal/testutil"
	cosign "github.com/sigstore/cosign/v2/pkg/cosign"
	"github.com/labstack/echo/v4"
)

// generateTestCosignKey generates an ephemeral cosign key pair and writes the
// private key to a temp file. Returns the path to the private key file.
func generateTestCosignKey(t *testing.T) string {
	t.Helper()
	keys, err := cosign.GenerateKeyPair(func(bool) ([]byte, error) { return []byte{}, nil })
	if err != nil {
		t.Fatalf("generate cosign key pair: %v", err)
	}
	path := filepath.Join(t.TempDir(), "test.key")
	if err := os.WriteFile(path, keys.PrivateBytes, 0o600); err != nil {
		t.Fatalf("write cosign key: %v", err)
	}
	return path
}

// newOCIHandler creates an OCI handler wired to the test registry.
func newOCIHandler(t *testing.T, keyPath string) *handler.OCI {
	t.Helper()
	cfg := oci.Config{
		Registry:      testutil.TestOCIRegistry,
		Repo:          "orbital",
		SigningKeyPath: keyPath,
		AllowHTTP:     true,
	}
	return handler.NewOCI(testDB, cfg, scratchExportDir, slog.Default())
}

// ── Tests ──────────────────────────────────────────────────────────────────────

func TestOCIPublish_EndToEnd(t *testing.T) {
	// Run a full export to obtain a completed export job.
	exportHandler := newExportHandler(t)
	jobID := triggerExport(t, exportHandler, testDcID)
	exportStatus := testutil.WaitForExportJob(t, testDB, jobID, 90*time.Second)
	if string(exportStatus) != "completed" {
		t.Fatalf("prerequisite export ended with %q", exportStatus)
	}

	keyPath := generateTestCosignKey(t)
	h := newOCIHandler(t, keyPath)

	// Trigger publish.
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("jobId")
	c.SetParamValues(jobID.String())

	if err := h.Publish(c); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ArtifactID int    `json:"artifactId"`
		Status     string `json:"status"`
		Tag        string `json:"tag"`
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse publish response: %v", err)
	}
	if resp.ArtifactID == 0 {
		t.Fatal("publish response missing artifactId")
	}
	if resp.Tag == "" {
		t.Error("publish response missing tag")
	}

	// Wait for artifact to reach terminal state.
	artifactStatus := testutil.WaitForOCIArtifact(t, testDB, resp.ArtifactID, 120*time.Second)
	if string(artifactStatus) != "completed" {
		artifact, _ := testDB.RegistryArtifact.Get(t.Context(), resp.ArtifactID)
		errMsg := ""
		if artifact != nil && artifact.Error != nil {
			errMsg = *artifact.Error
		}
		t.Fatalf("OCI artifact ended with %q: %s", artifactStatus, errMsg)
	}

	artifact, err := testDB.RegistryArtifact.Get(t.Context(), resp.ArtifactID)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	if artifact.Digest == nil || *artifact.Digest == "" {
		t.Error("completed artifact has no digest")
	}
	if !artifact.Signed {
		t.Error("completed artifact is not marked signed")
	}
}

func TestOCIPublish_UnconfiguredReturns503(t *testing.T) {
	// OCI handler with no registry configured — publisher will be nil.
	h := handler.NewOCI(testDB, oci.Config{}, scratchExportDir, slog.Default())

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("jobId")
	c.SetParamValues("00000000-0000-0000-0000-000000000001")

	if err := h.Publish(c); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestOCIPublish_UnknownJobReturns404(t *testing.T) {
	keyPath := generateTestCosignKey(t)
	h := newOCIHandler(t, keyPath)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("jobId")
	c.SetParamValues("00000000-0000-0000-0000-000000000099")

	err := h.Publish(c)
	// Publish returns echo.ErrNotFound as an error (not via c.JSON).
	// Verify it's a 404 Echo HTTP error.
	if err == nil {
		t.Fatalf("expected error for unknown job, got nil (status %d)", rec.Code)
	}
	echoErr, ok := err.(*echo.HTTPError)
	if !ok || echoErr.Code != http.StatusNotFound {
		t.Errorf("expected echo 404 error, got: %v", err)
	}
}

