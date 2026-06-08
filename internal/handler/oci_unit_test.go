package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/labstack/echo/v4"
)

// renderArtifactFragment writes the progress fragment (with polling div) while
// the artifact is in a non-terminal state, and the result fragment with
// HX-Trigger=refreshExportJobs once the artifact reaches a terminal state.

func TestRenderArtifactFragment_InProgress_KeepsPolling(t *testing.T) {
	t.Chdir("../..") // template paths are repo-relative

	h := &OCI{basePath: "/orbital"}
	c, rec := newEchoCtx(http.MethodGet, "/")

	a := &ent.RegistryArtifact{
		ID:     42,
		Status: registryartifact.StatusPushing,
	}
	if err := h.renderArtifactFragment(c, a); err != nil {
		t.Fatalf("renderArtifactFragment: %v", err)
	}

	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Errorf("non-terminal artifact must not set HX-Trigger; got %q", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Errorf("expected polling div with hx-trigger; body:\n%s", body)
	}
	if !strings.Contains(body, "/orbital/api/v1/oci/artifacts/42") {
		t.Errorf("expected basePath + artifact ID in polling URL; body:\n%s", body)
	}
}

func TestRenderArtifactFragment_Completed_StopsPollingAndTriggers(t *testing.T) {
	t.Chdir("../..")

	h := &OCI{basePath: "/orbital"}
	c, rec := newEchoCtx(http.MethodGet, "/")

	digest := "sha256:abc123"
	a := &ent.RegistryArtifact{
		ID:     7,
		Status: registryartifact.StatusCompleted,
		Tag:    "v5",
		Digest: &digest,
		Signed: true,
	}
	if err := h.renderArtifactFragment(c, a); err != nil {
		t.Fatalf("renderArtifactFragment: %v", err)
	}

	if got := rec.Header().Get("HX-Trigger"); got != "refreshExportJobs" {
		t.Errorf("expected HX-Trigger=refreshExportJobs, got %q", got)
	}
	body := rec.Body.String()
	if strings.Contains(body, "hx-trigger") {
		t.Errorf("terminal fragment must not contain polling div; body:\n%s", body)
	}
	for _, want := range []string{"v5", digest, "signed"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in body; body:\n%s", want, body)
		}
	}
}

func TestRenderArtifactFragment_Failed_ShowsErrorAndTriggers(t *testing.T) {
	t.Chdir("../..")

	h := &OCI{basePath: "/orbital"}
	c, rec := newEchoCtx(http.MethodGet, "/")

	errMsg := "ORAS push: unauthorized"
	a := &ent.RegistryArtifact{
		ID:     9,
		Status: registryartifact.StatusFailed,
		Error:  &errMsg,
	}
	if err := h.renderArtifactFragment(c, a); err != nil {
		t.Fatalf("renderArtifactFragment: %v", err)
	}

	if got := rec.Header().Get("HX-Trigger"); got != "refreshExportJobs" {
		t.Errorf("expected HX-Trigger=refreshExportJobs, got %q", got)
	}
	body := rec.Body.String()
	if strings.Contains(body, "hx-trigger") {
		t.Errorf("terminal fragment must not contain polling div; body:\n%s", body)
	}
	if !strings.Contains(body, errMsg) {
		t.Errorf("expected error message in body; body:\n%s", body)
	}
}

func newEchoCtx(method, path string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}
