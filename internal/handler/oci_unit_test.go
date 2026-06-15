package handler

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/internal/ocitype"
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
	if !strings.Contains(body, `hx-trigger="every 200ms"`) {
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

// TestArtifactsTbody_WithLayers verifies that artifacts-tbody renders a modal-
// trigger button (HTMX hx-get) with layer count tags when HasLayers is true.
// Layer details themselves are rendered by the layers-modal fragment fetched
// on demand — not by this fragment.
func TestArtifactsTbody_WithLayers(t *testing.T) {
	t.Chdir("../..")

	_, rec := newEchoCtx(http.MethodGet, "/")

	digest := "sha256:abc123"
	a := &ent.RegistryArtifact{
		ID:          5,
		Status:      registryartifact.StatusCompleted,
		InitiatedAt: time.Now(),
		Digest:      &digest,
		Enriched:    true,
		Layers: []ocitype.ArtifactLayer{
			{MediaType: "application/vnd.orbital.subgraph.data.v1+gzip", SizeBytes: 1024, IsOrbitalNative: true},
			{MediaType: "application/vnd.example.bundle.v1", SizeBytes: 2048, IsOrbitalNative: false},
		},
	}

	rows := []artifactFragRow{toArtifactFragRow(a, "")}
	tmpl, err := template.ParseFiles("web/templates/orbital/partials/artifacts-tbody.gohtml")
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	if err := tmpl.Execute(rec, rows); err != nil {
		t.Fatalf("execute template: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/api/v1/oci/artifacts/5/layers"`) {
		t.Errorf("expected declarative hx-get to fetch the layers modal; body:\n%s", body)
	}
	if !strings.Contains(body, `hx-target="#layers-modal-body"`) {
		t.Errorf("expected hx-target=#layers-modal-body; body:\n%s", body)
	}
	if !strings.Contains(body, "1 dgraph") {
		t.Errorf("expected dgraph layer count tag in trigger; body:\n%s", body)
	}
	if !strings.Contains(body, "1 bundler") {
		t.Errorf("expected bundler layer count tag in trigger; body:\n%s", body)
	}
}

// TestLayersModal_RendersLayers verifies the layers-modal fragment renders the
// per-layer detail table with source / media-type / size / digest columns.
func TestLayersModal_RendersLayers(t *testing.T) {
	t.Chdir("../..")

	_, rec := newEchoCtx(http.MethodGet, "/")

	row := artifactFragRow{
		Tag:       "v3",
		HasLayers: true,
		LayerRows: []artifactLayerRow{
			{MediaType: "application/vnd.orbital.subgraph.data.v1+gzip", SizeDisplay: "1.0 KB", DigestShort: "sha256:abc…", IsOrbitalNative: true, Producer: "orbital"},
			{MediaType: "application/vnd.example.bundle.v1", SizeDisplay: "2.0 KB", DigestShort: "sha256:xyz…", IsOrbitalNative: false, Producer: "configbundle-bundler"},
		},
	}
	tmpl, err := template.ParseFiles("web/templates/orbital/partials/layers-modal.gohtml")
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	if err := tmpl.ExecuteTemplate(rec, "layers-modal", row); err != nil {
		t.Fatalf("execute template: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"Layers — v3",
		"application/vnd.orbital.subgraph.data.v1&#43;gzip",
		"application/vnd.example.bundle.v1",
		"orbital",
		"configbundle-bundler",
		"1.0 KB",
		"2.0 KB",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in modal body; body:\n%s", want, body)
		}
	}
}

// TestArtifactsTbody_LegacyNoLayers verifies that artifacts-tbody renders "—"
// for legacy artifacts that have no layer metadata stored.
func TestArtifactsTbody_LegacyNoLayers(t *testing.T) {
	t.Chdir("../..")

	_, rec := newEchoCtx(http.MethodGet, "/")

	a := &ent.RegistryArtifact{
		ID:          3,
		Status:      registryartifact.StatusCompleted,
		InitiatedAt: time.Now(),
		Enriched:    false,
	}
	rows := []artifactFragRow{toArtifactFragRow(a, "")}
	tmpl, err := template.ParseFiles("web/templates/orbital/partials/artifacts-tbody.gohtml")
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	if err := tmpl.Execute(rec, rows); err != nil {
		t.Fatalf("execute template: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "hx-get=") {
		t.Errorf("did not expect hx-get for legacy artifact (no layers); body:\n%s", body)
	}
}


// TestToRow_LayerRowsReversedFromManifestOrder verifies that toArtifactFragRow
// returns LayerRows in reverse manifest order (bundler layers at top, dgraph
// layers at bottom — matches container-image visual convention).
func TestToRow_LayerRowsReversedFromManifestOrder(t *testing.T) {
	digest := "sha256:abc"
	a := &ent.RegistryArtifact{
		ID:       1,
		Status:   registryartifact.StatusCompleted,
		Digest:   &digest,
		Enriched: true,
		Layers: []ocitype.ArtifactLayer{
			{MediaType: "application/vnd.orbital.subgraph.data.v1+gzip", IsOrbitalNative: true, SizeBytes: 100},
			{MediaType: "application/vnd.orbital.subgraph.schema.v1+gzip", IsOrbitalNative: true, SizeBytes: 200},
			{MediaType: "application/vnd.armada.configbundle.manifest.v1+yaml", IsOrbitalNative: false, SizeBytes: 300},
			{MediaType: "application/vnd.armada.configbundle.mapping.v1+json", IsOrbitalNative: false, SizeBytes: 400},
		},
	}
	row := toArtifactFragRow(a, "")

	if len(row.LayerRows) != 4 {
		t.Fatalf("expected 4 LayerRows, got %d", len(row.LayerRows))
	}
	if !hasSuffix(row.LayerRows[0].MediaType, "mapping.v1+json") {
		t.Errorf("LayerRows[0] should be mapping (bundler top); got %q", row.LayerRows[0].MediaType)
	}
	if !hasSuffix(row.LayerRows[3].MediaType, "data.v1+gzip") {
		t.Errorf("LayerRows[3] should be data (dgraph bottom); got %q", row.LayerRows[3].MediaType)
	}
	if row.OrbitalLayers != 2 {
		t.Errorf("OrbitalLayers = %d, want 2", row.OrbitalLayers)
	}
	if row.BundlerLayers != 2 {
		t.Errorf("BundlerLayers = %d, want 2", row.BundlerLayers)
	}
}

func TestToRow_SingleLayer_NoVisibleChange(t *testing.T) {
	digest := "sha256:abc"
	a := &ent.RegistryArtifact{
		ID:     2,
		Status: registryartifact.StatusCompleted,
		Digest: &digest,
		Layers: []ocitype.ArtifactLayer{
			{MediaType: "application/vnd.orbital.subgraph.data.v1+gzip", IsOrbitalNative: true, SizeBytes: 100},
		},
	}
	row := toArtifactFragRow(a, "")
	if len(row.LayerRows) != 1 {
		t.Fatalf("expected 1 LayerRow, got %d", len(row.LayerRows))
	}
	if !hasSuffix(row.LayerRows[0].MediaType, "data.v1+gzip") {
		t.Errorf("unexpected MediaType %q", row.LayerRows[0].MediaType)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func newEchoCtx(method, path string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}
