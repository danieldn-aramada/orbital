//go:build integration

package orbserver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/armada/orbital/internal/orb"
	"github.com/armada/orbital/internal/orbconfig"
)

// makeGZ returns a gzip-compressed version of data.
func makeGZ(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write(data)
	gz.Close()
	return buf.Bytes()
}

// buildArtifactZipWithLayer builds a zip containing graph layers + an extra named layer
// with the given mediaType registered in layers.json.
func buildArtifactZipWithLayer(t *testing.T, layerData []byte, mediaType, filename string) []byte {
	t.Helper()
	layersJSON, _ := json.Marshal([]map[string]string{{"mediaType": mediaType, "filename": filename}})
	return buildZip(t, map[string][]byte{
		"data.json.gz": makeGZ(t, []byte("")),                              // empty N-Quads — valid for dgraph live
		"schema.gz":    makeGZ(t, []byte("type Query { _dummy: String }")), // minimal valid schema
		"layers.json":  layersJSON,
		filename:       layerData,
	})
}

// newServerWithMockBackend creates a Server wired to a mock DGraph backend
// and the given consumer configuration. Uses t.TempDir() for DataDir.
func newServerWithMockBackend(t *testing.T, consumers orbconfig.ConsumersConfig) *Server {
	t.Helper()
	t.Chdir("../..")

	// Minimal httptest DGraph (handles /alter and /admin/schema).
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(dgraph.Close)

	cfg := &orbconfig.Config{
		Port:                "0",
		DGraphURL:           dgraph.URL + "/graphql",
		DGraphAdminURL:      dgraph.URL + "/admin",
		DGraphAlphaGRPC:     "localhost:9082",
		DataDir:             t.TempDir(),
		Backend:             "docker",
		DGraphContainerName: "local-dgraph-orb-alpha-1",
		PollInterval:        60 * time.Second,
		LogLevel:            "error",
		Consumers:           consumers,
	}

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Replace the importer's backend with a mock that always succeeds.
	srv.imp = orb.NewImporter(*cfg, srv.logger, &mockDGraphBackend{})

	return srv
}

// mockDGraphBackend is a DGraphBackend that always succeeds without running Docker.
type mockDGraphBackend struct{}

func (m *mockDGraphBackend) RunLive(_ context.Context, _ string) (string, error) { return "", nil }

func TestImportArtifact_FullPipeline(t *testing.T) {
	const testMediaType = "application/vnd.armada.configbundle.manifest.v1+yaml"
	manifestData := []byte("manifest:\n  version: test-1")

	// Consumer records what it receives. mu protects all fields except callCount.
	var (
		mu                sync.Mutex
		receivedBody      []byte
		receivedMediaType string
		receivedTag       string
		receivedDigest    string
		receivedImportID  string
		callCount         atomic.Int32
	)
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedMediaType = r.Header.Get("Content-Type")
		receivedTag = r.Header.Get("X-Orb-Tag")
		receivedDigest = r.Header.Get("X-Orb-Digest")
		receivedImportID = r.Header.Get("X-Orb-Import-ID")
		receivedBody = body
		mu.Unlock()
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer consumer.Close()

	srv := newServerWithMockBackend(t, orbconfig.ConsumersConfig{
		{MediaType: testMediaType, URL: consumer.URL},
	})

	// Build and POST the artifact zip.
	artifactZip := buildArtifactZipWithLayer(t, manifestData, testMediaType, "cb-manifest.yaml")
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("bundle", "artifact.zip")
	fw.Write(artifactZip)
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&resp)
	importID := resp["importId"]
	tag := resp["tag"]

	// Wait for the async goroutine to complete and dispatch to fire.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if callCount.Load() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if callCount.Load() == 0 {
		t.Fatal("consumer was not called within 10 seconds")
	}

	// Assert dispatch headers and body (read under lock).
	mu.Lock()
	gotMediaType := receivedMediaType
	gotTag := receivedTag
	gotDigest := receivedDigest
	gotImportID := receivedImportID
	gotBody := receivedBody
	mu.Unlock()

	_ = gotDigest // empty for artifact imports (no OCI pull)

	if gotMediaType != testMediaType {
		t.Errorf("Content-Type: got %q, want %q", gotMediaType, testMediaType)
	}
	if string(gotBody) != string(manifestData) {
		t.Errorf("body: got %q, want %q", gotBody, manifestData)
	}
	if !strings.HasPrefix(gotTag, "artifact-") {
		t.Errorf("X-Orb-Tag: got %q, expected prefix %q", gotTag, "artifact-")
	}
	if gotTag != tag {
		t.Errorf("X-Orb-Tag %q does not match response tag %q", gotTag, tag)
	}
	if gotImportID == "" {
		t.Error("X-Orb-Import-ID should be set")
	}
	if gotImportID != importID {
		t.Errorf("X-Orb-Import-ID %q does not match response importId %q", gotImportID, importID)
	}

	// Wait for history to be patched with dispatch layer records.
	deadline = time.Now().Add(5 * time.Second)
	var history []orb.ImportRecord
	for time.Now().Before(deadline) {
		histReq := httptest.NewRequest(http.MethodGet, "/api/v1/import/history", nil)
		histRec := httptest.NewRecorder()
		srv.echo.ServeHTTP(histRec, histReq)
		if err := json.NewDecoder(histRec.Body).Decode(&history); err == nil {
			if len(history) > 0 {
				last := history[len(history)-1]
				for _, l := range last.Layers {
					if l.Role == orb.LayerRoleDispatched {
						goto found
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
		continue
	found:
		break
	}

	if len(history) == 0 {
		t.Fatal("import history should have at least one record")
	}
	last := history[len(history)-1]
	if last.Status != "done" {
		t.Errorf("last history status: got %q, want %q", last.Status, "done")
	}
	var dispatchedLayer *orb.LayerRecord
	for i := range last.Layers {
		if last.Layers[i].Role == orb.LayerRoleDispatched {
			dispatchedLayer = &last.Layers[i]
			break
		}
	}
	if dispatchedLayer == nil {
		t.Error("last history record should have a dispatched layer")
	} else {
		if dispatchedLayer.MediaType != testMediaType {
			t.Errorf("dispatch layer mediaType: got %q, want %q", dispatchedLayer.MediaType, testMediaType)
		}
		if dispatchedLayer.Dispatch == nil {
			t.Error("dispatched layer should have Dispatch result")
		} else {
			if dispatchedLayer.Dispatch.StatusCode != http.StatusOK {
				t.Errorf("dispatch result statusCode: got %d, want 200", dispatchedLayer.Dispatch.StatusCode)
			}
			if dispatchedLayer.Dispatch.Error != "" {
				t.Errorf("dispatch result error: got %q, want empty", dispatchedLayer.Dispatch.Error)
			}
		}
	}
}

func TestImportArtifact_ConsumerFails_StillDone(t *testing.T) {
	// DGraph import succeeds; consumer returns 500 — import should still be "done",
	// dispatch result should record the error.
	const testMediaType = "application/vnd.armada.configbundle.manifest.v1+yaml"

	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer consumer.Close()

	srv := newServerWithMockBackend(t, orbconfig.ConsumersConfig{
		{MediaType: testMediaType, URL: consumer.URL},
	})

	artifactZip := buildArtifactZipWithLayer(t, []byte("manifest: fail"), testMediaType, "cb-manifest.yaml")
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("bundle", "artifact.zip")
	fw.Write(artifactZip)
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	// Wait for history to be updated with dispatch layer records.
	deadline := time.Now().Add(10 * time.Second)
	var history []orb.ImportRecord
	for time.Now().Before(deadline) {
		histReq := httptest.NewRequest(http.MethodGet, "/api/v1/import/history", nil)
		histRec := httptest.NewRecorder()
		srv.echo.ServeHTTP(histRec, histReq)
		if err := json.NewDecoder(histRec.Body).Decode(&history); err == nil {
			if len(history) > 0 && history[len(history)-1].Status != "" {
				last := history[len(history)-1]
				for _, l := range last.Layers {
					if l.Role == orb.LayerRoleDispatched {
						goto found2
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
		continue
	found2:
		break
	}

	if len(history) == 0 {
		t.Fatal("expected history record")
	}
	last := history[len(history)-1]
	if last.Status != "done" {
		t.Errorf("import should be done even when consumer fails; got %q", last.Status)
	}
	var dispatchedLayer *orb.LayerRecord
	for i := range last.Layers {
		if last.Layers[i].Role == orb.LayerRoleDispatched {
			dispatchedLayer = &last.Layers[i]
			break
		}
	}
	if dispatchedLayer == nil {
		t.Fatal("expected a dispatched layer in history")
	}
	if dispatchedLayer.Dispatch == nil {
		t.Fatal("dispatched layer should have Dispatch result")
	}
	if dispatchedLayer.Dispatch.StatusCode != http.StatusInternalServerError {
		t.Errorf("dispatch result statusCode: got %d, want 500", dispatchedLayer.Dispatch.StatusCode)
	}
	if dispatchedLayer.Dispatch.Error == "" {
		t.Error("dispatch result error should be set for 500 consumer response")
	}
}

func TestImportArtifact_NoExtraLayers_NoDispatch(t *testing.T) {
	// Zip with only graph layers and no layers.json → no dispatch, import succeeds.
	var consumerCalled atomic.Bool
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		consumerCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer consumer.Close()

	srv := newServerWithMockBackend(t, orbconfig.ConsumersConfig{
		{MediaType: "application/vnd.armada.configbundle.manifest.v1+yaml", URL: consumer.URL},
	})

	// Zip with only graph layers — no layers.json, no extra blobs.
	artifactZip := validArtifactZip(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("bundle", "artifact.zip")
	fw.Write(artifactZip)
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	// Give the goroutine time to complete; consumer must NOT be called.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := srv.state.snapshot()
		if snap.Status != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if consumerCalled.Load() {
		t.Error("consumer should not be called when there are no extra layers in the zip")
	}
}
