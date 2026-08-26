//go:build integration

package handler_test

import (
	"archive/zip"
	"bytes"
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/armada/orbital/ent/exportjob"
)

// dataAndSchemaZip returns a valid export zip containing both data.json.gz
// and schema.gz with the given bytes. The bundler-aware Download path requires
// both files to be present.
func dataAndSchemaZip(t *testing.T, data, schema []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if data != nil {
		w, _ := zw.Create("data.json.gz")
		w.Write(data)
	}
	if schema != nil {
		w, _ := zw.Create("schema.gz")
		w.Write(schema)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// TestExportDownload_NoBundlers_StreamsRawZip pins the backward-compat path:
// when no bundlers are configured, Download streams the raw export zip
// byte-for-byte from disk. Regression class: a change to Download's default
// branch that would break local-dev / OCI-not-configured deployments.
func TestExportDownload_NoBundlers_StreamsRawZip(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	zipPath := dir + "/raw.zip"
	rawBytes := dataAndSchemaZip(t, []byte("fake-data"), []byte("fake-schema"))
	if err := os.WriteFile(zipPath, rawBytes, 0o600); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	dcOrbID := "test:no-bundlers"
	job := testDB.ExportJob.Create().
		SetDatacenterID("dc-no-bundlers").
		SetDatacenterName("no-bundlers-dc").
		SetDatacenterOrbID(dcOrbID).
		SetStatus(exportjob.StatusCompleted).
		SetArtifactPath(zipPath).
		SaveX(ctx)
	t.Cleanup(func() { testDB.ExportJob.DeleteOne(job).ExecX(ctx) })

	h := newExportListHandler(t)
	// Explicitly NO SetBundlers call — verify default state produces raw zip.

	c, rec := exportAPICtx(http.MethodGet, "/api/v1/export/jobs/"+job.ID.String()+"/download",
		map[string]string{"jobId": job.ID.String()})

	if err := h.Download(c); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, rawBytes) {
		t.Errorf("raw path must stream the on-disk zip byte-for-byte (got %d bytes, want %d)", len(got), len(rawBytes))
	}
}

// TestExportDownload_WithBundlers_ReturnsCourierZip pins the bundler-aware
// path: when bundlers are configured, Download calls them, packages layers
// alongside data.json.gz + schema.gz into a courier-ready zip that matches
// the shape orb's /api/v1/import/artifact accepts (data.json.gz + schema.gz +
// layers.json + one file per layer).
//
// Regression class: a change to buildCourierZip that would break the courier
// contract with orb — a shape mismatch means orb's importArtifact returns 400
// and the operator has nowhere to go.
func TestExportDownload_WithBundlers_ReturnsCourierZip(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	zipPath := dir + "/raw.zip"
	rawData := []byte("data-payload")
	rawSchema := []byte("schema-payload")
	if err := os.WriteFile(zipPath, dataAndSchemaZip(t, rawData, rawSchema), 0o600); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	dcOrbID := "test:with-bundlers"

	// Fake bundler that returns two layers with distinct media types + base64
	// bodies. Matches the bundler.Client wire shape: {mediaType, data:<base64>}.
	fakeBundler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		// Verify orbId propagates so publish/download share the same contract.
		var body struct {
			OrbID string `json:"orbId"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.OrbID != dcOrbID {
			http.Error(w, "wrong orbId: "+body.OrbID, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"layers": [
				{"mediaType": "application/vnd.armada.configbundle+yaml", "data": "` + base64Std("cb-yaml-bytes") + `"},
				{"mediaType": "application/vnd.armada.k8s-manifests+yaml", "data": "` + base64Std("k8s-bytes") + `"}
			]
		}`))
	}))
	t.Cleanup(fakeBundler.Close)

	job := testDB.ExportJob.Create().
		SetDatacenterID("dc-with-bundlers").
		SetDatacenterName("with-bundlers-dc").
		SetDatacenterOrbID(dcOrbID).
		SetStatus(exportjob.StatusCompleted).
		SetArtifactPath(zipPath).
		SaveX(ctx)
	t.Cleanup(func() { testDB.ExportJob.DeleteOne(job).ExecX(ctx) })

	h := newExportListHandler(t)
	h.SetBundlers([]string{"test-bundler=" + fakeBundler.URL}, 10*time.Second)

	c, rec := exportAPICtx(http.MethodGet, "/api/v1/export/jobs/"+job.ID.String()+"/download",
		map[string]string{"jobId": job.ID.String()})

	if err := h.Download(c); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type: got %q, want application/zip", ct)
	}

	// Inspect the returned zip against orb's /import/artifact expectations
	// (data.json.gz + schema.gz required; layers.json + layer blobs when
	// bundlers ran). See internal/orbserver/import_handlers.go:410 for the
	// consumer side.
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("returned body is not a valid zip: %v", err)
	}

	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = b
	}

	if !bytes.Equal(files["data.json.gz"], rawData) {
		t.Errorf("data.json.gz: got %q, want %q", files["data.json.gz"], rawData)
	}
	if !bytes.Equal(files["schema.gz"], rawSchema) {
		t.Errorf("schema.gz: got %q, want %q", files["schema.gz"], rawSchema)
	}

	manifestBytes, ok := files["layers.json"]
	if !ok {
		t.Fatal("layers.json missing — orb's importArtifact requires it to dispatch to consumers")
	}
	var manifest []struct {
		MediaType string `json:"mediaType"`
		Filename  string `json:"filename"`
		Producer  string `json:"producer"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("layers.json is not valid JSON: %v", err)
	}
	if len(manifest) != 2 {
		t.Fatalf("expected 2 layer entries, got %d", len(manifest))
	}
	for _, entry := range manifest {
		if entry.Producer != "test-bundler" {
			t.Errorf("layer entry producer: got %q, want test-bundler", entry.Producer)
		}
		// Filename extension is derived from the media type's structured-syntax
		// suffix (RFC 6838). Both fixture layers use "+yaml" → ".yaml".
		// Pins the operator-UX contract: unzipping the courier bundle should
		// produce inspectable files, not opaque .bin blobs.
		if !strings.HasSuffix(entry.Filename, ".yaml") {
			t.Errorf("layer filename should end in .yaml for +yaml media type: got %q", entry.Filename)
		}
		if _, ok := files[entry.Filename]; !ok {
			t.Errorf("layers.json references %q but that file is not in the zip", entry.Filename)
		}
	}
	// Filename numbering matches OCI Image Spec manifest positions: data.json.gz
	// is at position 0, schema.gz at 1, so bundler layers start at 2. This lets
	// operators cross-reference the layers modal (which shows Position) with
	// the zip filename without arithmetic. See docs/reference/OCI.md.
	if manifest[0].Filename != "layer-2-test-bundler.yaml" {
		t.Errorf("first bundler layer filename should be layer-2-... (OCI position 2), got %q", manifest[0].Filename)
	}
	if manifest[1].Filename != "layer-3-test-bundler.yaml" {
		t.Errorf("second bundler layer filename should be layer-3-... (OCI position 3), got %q", manifest[1].Filename)
	}
	if manifest[0].MediaType != "application/vnd.armada.configbundle+yaml" {
		t.Errorf("layer 0 mediaType: got %q, want configbundle+yaml", manifest[0].MediaType)
	}
	if !bytes.Equal(files[manifest[0].Filename], []byte("cb-yaml-bytes")) {
		t.Errorf("layer 0 body: got %q, want cb-yaml-bytes", files[manifest[0].Filename])
	}
}

// TestExportDownload_BundlerFails_Returns502 pins the error path: when a
// bundler HTTP call fails, Download returns 502 with an actionable message,
// NOT a partial or empty zip. Regression class: a change to the bundler
// error branch that would silently return the raw zip instead of an error,
// producing an incomplete courier artifact.
func TestExportDownload_BundlerFails_Returns502(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	zipPath := dir + "/raw.zip"
	if err := os.WriteFile(zipPath, dataAndSchemaZip(t, []byte("d"), []byte("s")), 0o600); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	failingBundler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "graphql: field not defined", http.StatusInternalServerError)
	}))
	t.Cleanup(failingBundler.Close)

	job := testDB.ExportJob.Create().
		SetDatacenterID("dc-bundler-fails").
		SetDatacenterName("bundler-fails-dc").
		SetDatacenterOrbID("test:bundler-fails").
		SetStatus(exportjob.StatusCompleted).
		SetArtifactPath(zipPath).
		SaveX(ctx)
	t.Cleanup(func() { testDB.ExportJob.DeleteOne(job).ExecX(ctx) })

	h := newExportListHandler(t)
	h.SetBundlers([]string{"failing=" + failingBundler.URL}, 5*time.Second)

	c, rec := exportAPICtx(http.MethodGet, "/api/v1/export/jobs/"+job.ID.String()+"/download",
		map[string]string{"jobId": job.ID.String()})

	renderErr(c, h.Download(c))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

// base64Std is a package-scope helper to keep the bundler-response test
// literal readable. Uses stdlib base64.StdEncoding indirectly.
func base64Std(s string) string {
	return b64.StdEncoding.EncodeToString([]byte(s))
}
