# POST /import/artifact — Consumer Dispatch Implementation Plan

This plan implements `POST /import/artifact` on orb: the full multi-layer artifact import pipeline with best-effort consumer dispatch. It is the symmetric reverse of orbital's enricher pipeline.

**Source of truth for the architecture:** `docs/configbundle-integration.md`

---

## What Changes

| File | Change |
|------|--------|
| `internal/orbconfig/config.go` | Add `ConsumerConfig`, `ConsumersConfig`, `ORB_CONSUMERS` field |
| `internal/oci/puller.go` | Add `ExtraLayers map[string][]byte` to `PulledArtifact`, collect non-graph layers |
| `internal/orb/dispatch.go` | **New file** — `Dispatcher`, `DispatchResult`, `NewDispatcher` |
| `internal/orb/importer.go` | Add `DispatchResults []DispatchResult` to `ImportRecord`; add `PatchLastHistoryDispatch()` |
| `internal/orbserver/server.go` | Add `dispatcher *orb.Dispatcher` field; initialize from config; register `/import/artifact` |
| `internal/orbserver/import_handlers.go` | Add `importArtifact` handler |
| `internal/orbconfig/config_test.go` | Consumer config parsing tests |
| `internal/oci/puller_test.go` | ExtraLayers extraction tests |
| `internal/orb/dispatch_test.go` | **New file** — Dispatcher unit tests |
| `internal/orb/importer_test.go` | DispatchResults round-trip + PatchLastHistoryDispatch tests |
| `internal/orbserver/import_handlers_test.go` | `importArtifact` request validation + conflict tests |
| `internal/orbserver/server_routes_test.go` | `/import/artifact` route registration test |
| `internal/orbserver/import_artifact_integration_test.go` | **New file** — full pipeline integration test |

---

## DAG

```
Step 1: orbconfig.ConsumerConfig        Step 2: oci.PulledArtifact.ExtraLayers
        (ORB_CONSUMERS parsing)                 (collect non-graph layers)
              │                                          │
              └─────────────────┐                        │
                                ↓                        │
                        Step 3: orb.Dispatcher           │
                        (internal/orb/dispatch.go)       │
                                │                        │
                        Step 4: ImportRecord.DispatchResults
                                + PatchLastHistoryDispatch
                                │
                                ↓
                        Step 5: importArtifact handler
                        (orbserver/import_handlers.go)
                                │
                                ↓
                        Step 6: Route + Server field
                        (server.go)
                                │
                                ↓
                        Step 7: Integration test
```

Steps 1 and 2 have no dependencies and can be done in parallel. Step 4 has no dependencies on Steps 1–3 (it's pure file I/O on `ImportRecord`) — it can be done in parallel with Step 3.

---

## Zip Format for `/import/artifact`

Same multipart form field `bundle` as `/import/subgraph`. The zip may contain:

```
artifact.zip
├── data.json.gz              (required)
├── schema.gz                 (required)
├── layers.json               (optional — array of {mediaType, filename})
└── <filename>                (one blob per entry in layers.json)
```

`layers.json` example:
```json
[
  {"mediaType": "application/vnd.armada.configbundle.manifest.v1+yaml", "filename": "cb-manifest.yaml"}
]
```

If `layers.json` is absent, behavior is identical to `/import/subgraph`: only graph layers are imported, no dispatch occurs.

---

## Step 1 — `orbconfig`: `ORB_CONSUMERS`

**File:** `internal/orbconfig/config.go`

Add before the `Config` struct:

```go
// ConsumerConfig registers an external layer consumer for orb dispatch.
type ConsumerConfig struct {
    MediaType string `json:"mediaType"`
    URL       string `json:"url"`
}

// ConsumersConfig is a []ConsumerConfig that decodes from a JSON string env var.
type ConsumersConfig []ConsumerConfig

// Decode implements envconfig.Decoder for JSON array env vars.
func (c *ConsumersConfig) Decode(value string) error {
    if value == "" {
        *c = nil
        return nil
    }
    return json.Unmarshal([]byte(value), c)
}
```

Add to `Config` struct (after the `EnableOCIRegistry` field):

```go
// Consumers holds registered layer consumers for dispatch.
// Set via: ORB_CONSUMERS='[{"mediaType":"...","url":"..."}]'
// Multiple consumers: ORB_CONSUMERS='[{"mediaType":"a","url":"http://x"},{"mediaType":"b","url":"http://y"}]'
Consumers ConsumersConfig `envconfig:"ORB_CONSUMERS" default:""`
```

Add `"encoding/json"` to the import block.

### Tests for Step 1

**File:** `internal/orbconfig/config_test.go` (create if it does not exist)

```go
package orbconfig_test

import (
    "testing"

    "github.com/armada/orbital/internal/orbconfig"
)

func TestConsumersConfig_ParsesJSON(t *testing.T) {
    var c orbconfig.ConsumersConfig
    if err := c.Decode(`[{"mediaType":"application/vnd.armada.configbundle.manifest.v1+yaml","url":"http://cb:8080/consume"}]`); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(c) != 1 {
        t.Fatalf("expected 1 consumer, got %d", len(c))
    }
    if c[0].MediaType != "application/vnd.armada.configbundle.manifest.v1+yaml" {
        t.Errorf("mediaType mismatch: %q", c[0].MediaType)
    }
    if c[0].URL != "http://cb:8080/consume" {
        t.Errorf("url mismatch: %q", c[0].URL)
    }
}

func TestConsumersConfig_Empty(t *testing.T) {
    var c orbconfig.ConsumersConfig
    if err := c.Decode(""); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if c != nil {
        t.Errorf("expected nil slice for empty string, got %v", c)
    }
}

func TestConsumersConfig_Invalid(t *testing.T) {
    var c orbconfig.ConsumersConfig
    if err := c.Decode("not json"); err == nil {
        t.Error("expected error for invalid JSON, got nil")
    }
}

func TestConsumersConfig_Multiple(t *testing.T) {
    var c orbconfig.ConsumersConfig
    if err := c.Decode(`[{"mediaType":"a/b","url":"http://x"},{"mediaType":"c/d","url":"http://y"}]`); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(c) != 2 {
        t.Fatalf("expected 2 consumers, got %d", len(c))
    }
}
```

**Run:** `go test -short ./internal/orbconfig/...`

---

## Step 2 — `oci.PulledArtifact`: `ExtraLayers`

**File:** `internal/oci/puller.go`

Update `PulledArtifact`:

```go
// PulledArtifact contains the data extracted from a pulled OCI artifact.
type PulledArtifact struct {
    DataGZ      []byte
    SchemaGZ    []byte
    ExtraLayers map[string][]byte // mediaType → bytes; non-graph layers for consumer dispatch
    Annotations map[string]string
    Digest      string
    Tag         string
}
```

Update the layer switch in `Pull()` to capture unknown layers instead of silently ignoring them:

```go
switch layer.MediaType {
case mediaTypeDataGZ:
    artifact.DataGZ = data
case mediaTypeSchemaGZ:
    artifact.SchemaGZ = data
default:
    if artifact.ExtraLayers == nil {
        artifact.ExtraLayers = make(map[string][]byte)
    }
    artifact.ExtraLayers[layer.MediaType] = data
}
```

### Tests for Step 2

**File:** `internal/oci/puller_test.go` (create if it does not exist; this step can use a minimal httptest OCI server — see T.26 for the full helper)

Add these two test cases. They require an `httptest.NewServer` that speaks OCI Distribution Spec v1. A minimal helper is provided below; consolidate with T.26 when that step is implemented.

```go
package oci_test

import (
    "archive/tar"
    "compress/gzip"
    "bytes"
    "crypto/sha256"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "testing"

    ocispec "github.com/opencontainers/image-spec/specs-go/v1"
    "github.com/armada/orbital/internal/oci"
)

// buildLayer returns (digest, size, bytes) for an OCI layer blob.
func buildLayer(data []byte) (string, int64, []byte) {
    sum := sha256.Sum256(data)
    return fmt.Sprintf("sha256:%x", sum), int64(len(data)), data
}

// newOCITestServer returns an httptest.Server serving a minimal OCI registry
// with a single manifest that has the specified layers. tagName is the OCI tag.
// layers is a map of mediaType → raw bytes.
func newOCITestServer(t *testing.T, tagName string, layers map[string][]byte) *httptest.Server {
    t.Helper()
    // ... (implementation details: build manifest, serve /v2/{name}/manifests/{tag}, /v2/{name}/blobs/{digest})
    // Full implementation in T.26. For now, stub or skip these tests if T.26 is not done.
}

func TestPull_ExtraLayersCaptured(t *testing.T) {
    extraMediaType := "application/vnd.armada.configbundle.manifest.v1+yaml"
    extraData := []byte("cb-manifest: foo")
    dataGZ := []byte("fake-data-gz")
    schemaGZ := []byte("fake-schema-gz")

    srv := newOCITestServer(t, "v1", map[string][]byte{
        mediaTypeDataGZ:   dataGZ,
        mediaTypeSchemaGZ: schemaGZ,
        extraMediaType:    extraData,
    })
    defer srv.Close()

    // ... configure PullConfig pointing at srv.URL, call Pull("v1")
    // assert artifact.ExtraLayers[extraMediaType] == extraData
}

func TestPull_ExtraLayersNilWhenNone(t *testing.T) {
    // artifact has only graph layers
    // assert artifact.ExtraLayers == nil
}
```

**Note:** If the OCI test server helper is not yet in place (T.26 not done), mark these tests with `t.Skip("OCI test server not yet implemented (T.26)")` and remove the skip when T.26 lands.

**Run:** `go test -short ./internal/oci/...`

---

## Step 3 — `orb.Dispatcher`

**File:** `internal/orb/dispatch.go` (new)

```go
package orb

import (
    "bytes"
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/armada/orbital/internal/orbconfig"
)

// DispatchResult records the outcome of dispatching one layer to one consumer.
type DispatchResult struct {
    MediaType  string `json:"mediaType"`
    URL        string `json:"url"`
    StatusCode int    `json:"statusCode,omitempty"`
    Error      string `json:"error,omitempty"`
}

// Dispatcher sends artifact layers to registered consumers.
type Dispatcher struct {
    consumers []orbconfig.ConsumerConfig
    client    *http.Client
}

// NewDispatcher creates a Dispatcher from the given consumer registrations.
func NewDispatcher(consumers []orbconfig.ConsumerConfig) *Dispatcher {
    return &Dispatcher{
        consumers: consumers,
        client:    &http.Client{Timeout: 30 * time.Second},
    }
}

// Dispatch sends each extra layer to its registered consumer.
// Dispatch is best-effort: each consumer result is recorded individually.
// A failed dispatch does not stop other consumers from receiving their layer.
// Layers with no registered consumer are silently skipped.
func (d *Dispatcher) Dispatch(ctx context.Context, layers map[string][]byte, tag, digest, importID string) []DispatchResult {
    var results []DispatchResult
    for _, c := range d.consumers {
        data, ok := layers[c.MediaType]
        if !ok {
            continue
        }
        results = append(results, d.dispatchOne(ctx, c, data, tag, digest, importID))
    }
    return results
}

func (d *Dispatcher) dispatchOne(ctx context.Context, c orbconfig.ConsumerConfig, data []byte, tag, digest, importID string) DispatchResult {
    result := DispatchResult{MediaType: c.MediaType, URL: c.URL}
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(data))
    if err != nil {
        result.Error = fmt.Sprintf("create request: %s", err)
        return result
    }
    req.Header.Set("Content-Type", c.MediaType)
    req.Header.Set("X-Orb-Tag", tag)
    req.Header.Set("X-Orb-Digest", digest)
    req.Header.Set("X-Orb-Import-ID", importID)

    resp, err := d.client.Do(req)
    if err != nil {
        result.Error = fmt.Sprintf("dispatch: %s", err)
        return result
    }
    defer resp.Body.Close()
    result.StatusCode = resp.StatusCode
    if resp.StatusCode >= 400 {
        result.Error = fmt.Sprintf("consumer returned %d", resp.StatusCode)
    }
    return result
}
```

### Tests for Step 3

**File:** `internal/orb/dispatch_test.go` (new)

All tests use `httptest.NewServer` as the fake consumer. Table-driven with `t.Run`.

```go
package orb_test

import (
    "context"
    "io"
    "net/http"
    "net/http/httptest"
    "sync/atomic"
    "testing"

    "github.com/armada/orbital/internal/orb"
    "github.com/armada/orbital/internal/orbconfig"
)

const testMediaType = "application/vnd.armada.configbundle.manifest.v1+yaml"

func TestDispatcher_DispatchesLayer(t *testing.T) {
    var called atomic.Bool
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called.Store(true)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: testMediaType, URL: srv.URL}})
    results := d.Dispatch(context.Background(), map[string][]byte{testMediaType: []byte("payload")}, "v1", "sha256:abc", "id-1")

    if !called.Load() {
        t.Error("expected consumer to be called")
    }
    if len(results) != 1 {
        t.Fatalf("expected 1 result, got %d", len(results))
    }
    if results[0].StatusCode != http.StatusOK {
        t.Errorf("expected 200, got %d", results[0].StatusCode)
    }
    if results[0].Error != "" {
        t.Errorf("unexpected error: %s", results[0].Error)
    }
}

func TestDispatcher_CorrectHeaders(t *testing.T) {
    var gotContentType, gotTag, gotDigest, gotImportID string
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotContentType = r.Header.Get("Content-Type")
        gotTag = r.Header.Get("X-Orb-Tag")
        gotDigest = r.Header.Get("X-Orb-Digest")
        gotImportID = r.Header.Get("X-Orb-Import-ID")
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: testMediaType, URL: srv.URL}})
    d.Dispatch(context.Background(), map[string][]byte{testMediaType: []byte("x")}, "v3", "sha256:digest", "import-uuid")

    if gotContentType != testMediaType {
        t.Errorf("Content-Type: got %q, want %q", gotContentType, testMediaType)
    }
    if gotTag != "v3" {
        t.Errorf("X-Orb-Tag: got %q, want %q", gotTag, "v3")
    }
    if gotDigest != "sha256:digest" {
        t.Errorf("X-Orb-Digest: got %q, want %q", gotDigest, "sha256:digest")
    }
    if gotImportID != "import-uuid" {
        t.Errorf("X-Orb-Import-ID: got %q, want %q", gotImportID, "import-uuid")
    }
}

func TestDispatcher_CorrectBody(t *testing.T) {
    payload := []byte("manifest: data here")
    var gotBody []byte
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotBody, _ = io.ReadAll(r.Body)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: testMediaType, URL: srv.URL}})
    d.Dispatch(context.Background(), map[string][]byte{testMediaType: payload}, "v1", "", "")

    if string(gotBody) != string(payload) {
        t.Errorf("body mismatch: got %q, want %q", gotBody, payload)
    }
}

func TestDispatcher_SkipsNonMatchingMediaType(t *testing.T) {
    var called atomic.Bool
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called.Store(true)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: "application/vnd.other", URL: srv.URL}})
    results := d.Dispatch(context.Background(), map[string][]byte{testMediaType: []byte("x")}, "v1", "", "")

    if called.Load() {
        t.Error("consumer should not be called for non-matching media type")
    }
    if len(results) != 0 {
        t.Errorf("expected 0 results, got %d", len(results))
    }
}

func TestDispatcher_ConsumerFails(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer srv.Close()

    d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: testMediaType, URL: srv.URL}})
    results := d.Dispatch(context.Background(), map[string][]byte{testMediaType: []byte("x")}, "v1", "", "")

    if len(results) != 1 {
        t.Fatalf("expected 1 result, got %d", len(results))
    }
    if results[0].StatusCode != http.StatusInternalServerError {
        t.Errorf("expected 500, got %d", results[0].StatusCode)
    }
    if results[0].Error == "" {
        t.Error("expected Error to be set for 500 response")
    }
}

func TestDispatcher_NoConsumers(t *testing.T) {
    d := orb.NewDispatcher(nil)
    results := d.Dispatch(context.Background(), map[string][]byte{testMediaType: []byte("x")}, "v1", "", "")
    if len(results) != 0 {
        t.Errorf("expected 0 results with no consumers, got %d", len(results))
    }
}

func TestDispatcher_ConsumerUnreachable(t *testing.T) {
    d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: testMediaType, URL: "http://127.0.0.1:1"}})
    results := d.Dispatch(context.Background(), map[string][]byte{testMediaType: []byte("x")}, "v1", "", "")

    if len(results) != 1 {
        t.Fatalf("expected 1 result, got %d", len(results))
    }
    if results[0].Error == "" {
        t.Error("expected Error to be set for unreachable consumer")
    }
}
```

**Run:** `go test -short ./internal/orb/...`

---

## Step 4 — `ImportRecord.DispatchResults` + `PatchLastHistoryDispatch`

**File:** `internal/orb/importer.go`

1. Add `DispatchResults` to `ImportRecord`:

```go
// ImportRecord is one entry in the import history log.
type ImportRecord struct {
    Tag             string           `json:"tag"`
    Digest          string           `json:"digest"`
    DCOrbID         string           `json:"dcOrbId"`
    ExportJobID     string           `json:"exportJobId"`
    ImportedAt      time.Time        `json:"importedAt"`
    Status          string           `json:"status"` // "done" | "failed"
    Verified        bool             `json:"verified"`
    Error           string           `json:"error,omitempty"`
    DispatchResults []DispatchResult `json:"dispatchResults,omitempty"`
}
```

2. Add `PatchLastHistoryDispatch` as a **package-level exported function** (not a method):

```go
// PatchLastHistoryDispatch updates the most recent history record with dispatch results.
// Called by the importArtifact handler after Import() writes the base record and
// Dispatcher.Dispatch() completes. Safe to call because the import state machine
// prevents concurrent imports (409 if one is already running).
func PatchLastHistoryDispatch(dataDir string, results []DispatchResult) error {
    if len(results) == 0 {
        return nil
    }
    path := filepath.Join(dataDir, importHistoryFile)
    data, err := os.ReadFile(path)
    if err != nil {
        return fmt.Errorf("read history: %w", err)
    }
    var records []ImportRecord
    if err := json.Unmarshal(data, &records); err != nil {
        return fmt.Errorf("unmarshal history: %w", err)
    }
    if len(records) == 0 {
        return nil
    }
    records[len(records)-1].DispatchResults = results
    out, err := json.MarshalIndent(records, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(path, out, 0o644)
}
```

### Tests for Step 4

**File:** `internal/orb/importer_test.go` (create if not exists)

```go
package orb_test

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/armada/orbital/internal/orb"
)

// writeHistory is a test helper that writes a history file with the given records.
func writeHistory(t *testing.T, dataDir string, records []orb.ImportRecord) {
    t.Helper()
    data, err := json.MarshalIndent(records, "", "  ")
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    if err := os.WriteFile(filepath.Join(dataDir, "import-history.json"), data, 0o644); err != nil {
        t.Fatalf("write: %v", err)
    }
}

func TestImportRecord_DispatchResults_RoundTrip(t *testing.T) {
    dataDir := t.TempDir()
    results := []orb.DispatchResult{
        {MediaType: "application/vnd.test", URL: "http://x", StatusCode: 200},
        {MediaType: "application/vnd.other", URL: "http://y", StatusCode: 500, Error: "consumer returned 500"},
    }
    records := []orb.ImportRecord{
        {Tag: "v1", Status: "done", ImportedAt: time.Now().UTC(), DispatchResults: results},
    }
    writeHistory(t, dataDir, records)

    got, err := orb.LoadHistory(dataDir)
    if err != nil {
        t.Fatalf("LoadHistory: %v", err)
    }
    if len(got) != 1 {
        t.Fatalf("expected 1 record, got %d", len(got))
    }
    if len(got[0].DispatchResults) != 2 {
        t.Fatalf("expected 2 dispatch results, got %d", len(got[0].DispatchResults))
    }
    if got[0].DispatchResults[0].StatusCode != 200 {
        t.Errorf("first result StatusCode: got %d, want 200", got[0].DispatchResults[0].StatusCode)
    }
    if got[0].DispatchResults[1].Error != "consumer returned 500" {
        t.Errorf("second result Error: got %q", got[0].DispatchResults[1].Error)
    }
}

func TestPatchLastHistoryDispatch_UpdatesLast(t *testing.T) {
    dataDir := t.TempDir()
    records := []orb.ImportRecord{
        {Tag: "v1", Status: "done", ImportedAt: time.Now().UTC()},
        {Tag: "v2", Status: "done", ImportedAt: time.Now().UTC()},
    }
    writeHistory(t, dataDir, records)

    results := []orb.DispatchResult{{MediaType: "a/b", URL: "http://x", StatusCode: 200}}
    if err := orb.PatchLastHistoryDispatch(dataDir, results); err != nil {
        t.Fatalf("PatchLastHistoryDispatch: %v", err)
    }

    got, _ := orb.LoadHistory(dataDir)
    if len(got[0].DispatchResults) != 0 {
        t.Error("first record should not have dispatch results")
    }
    if len(got[1].DispatchResults) != 1 {
        t.Errorf("last record: expected 1 dispatch result, got %d", len(got[1].DispatchResults))
    }
}

func TestPatchLastHistoryDispatch_EmptyResults_NoOp(t *testing.T) {
    dataDir := t.TempDir()
    records := []orb.ImportRecord{{Tag: "v1", Status: "done", ImportedAt: time.Now().UTC()}}
    writeHistory(t, dataDir, records)

    // empty results — should be a no-op
    if err := orb.PatchLastHistoryDispatch(dataDir, nil); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    got, _ := orb.LoadHistory(dataDir)
    if got[0].DispatchResults != nil {
        t.Error("expected nil dispatch results after no-op patch")
    }
}

func TestPatchLastHistoryDispatch_PreservesOtherFields(t *testing.T) {
    dataDir := t.TempDir()
    orig := orb.ImportRecord{Tag: "v5", Digest: "sha256:abc", Status: "done", Verified: true, ImportedAt: time.Now().UTC()}
    writeHistory(t, dataDir, []orb.ImportRecord{orig})

    results := []orb.DispatchResult{{MediaType: "a/b", URL: "http://x", StatusCode: 200}}
    orb.PatchLastHistoryDispatch(dataDir, results)

    got, _ := orb.LoadHistory(dataDir)
    if got[0].Tag != "v5" {
        t.Errorf("Tag changed: got %q", got[0].Tag)
    }
    if got[0].Digest != "sha256:abc" {
        t.Errorf("Digest changed: got %q", got[0].Digest)
    }
    if !got[0].Verified {
        t.Error("Verified changed")
    }
}
```

**Run:** `go test -short ./internal/orb/...`

---

## Step 5 — `importArtifact` Handler

**File:** `internal/orbserver/import_handlers.go`

Add to imports: `"crypto/rand"`, `"encoding/json"`, `"fmt"` (if not already present).

Add helper at top of file (or in a small private helpers block):

```go
// newImportID generates a random UUID-format string for import correlation.
func newImportID() string {
    b := make([]byte, 16)
    _, _ = rand.Read(b)
    return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
```

Add the handler:

```go
// @Summary     Import OCI artifact bundle
// @Description Accepts a zip bundle (data.json.gz + schema.gz + optional layers.json + layer blobs)
//   and runs the full import pipeline: DGraph import followed by best-effort consumer dispatch.
//   Always registered regardless of ORB_ENABLE_OCI_REGISTRY. Consumer dispatch only occurs
//   if ORB_CONSUMERS is configured and layers.json is present in the zip.
// @Tags        import
// @Accept      multipart/form-data
// @Produce     json
// @Param       bundle formData file true "Zip archive"
// @Success     202 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /import/artifact [post]
func (s *Server) importArtifact(c echo.Context) error {
    if snap := s.state.snapshot(); snap.Status == "running" {
        return c.JSON(http.StatusConflict, map[string]string{"error": "import already running"})
    }

    fh, err := c.FormFile("bundle")
    if err != nil {
        return c.JSON(http.StatusBadRequest, map[string]string{"error": "bundle file is required"})
    }
    f, err := fh.Open()
    if err != nil {
        return c.JSON(http.StatusInternalServerError, map[string]string{"error": "could not open upload"})
    }
    defer f.Close()
    raw, err := io.ReadAll(f)
    if err != nil {
        return c.JSON(http.StatusInternalServerError, map[string]string{"error": "could not read upload"})
    }
    zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
    if err != nil {
        return c.JSON(http.StatusBadRequest, map[string]string{"error": "not a valid zip archive"})
    }

    type layerEntry struct {
        MediaType string `json:"mediaType"`
        Filename  string `json:"filename"`
    }
    var layerManifest []layerEntry

    // First pass: parse layers.json if present.
    for _, zf := range zr.File {
        if zf.Name != "layers.json" {
            continue
        }
        rc, err := zf.Open()
        if err != nil {
            return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read layers.json"})
        }
        b, _ := io.ReadAll(rc)
        rc.Close()
        if err := json.Unmarshal(b, &layerManifest); err != nil {
            return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid layers.json: " + err.Error()})
        }
        break
    }

    // Build filename → mediaType lookup.
    filenameToMediaType := make(map[string]string, len(layerManifest))
    for _, e := range layerManifest {
        filenameToMediaType[e.Filename] = e.MediaType
    }

    // Second pass: extract all files.
    var dataGZ, schemaGZ []byte
    extraLayers := make(map[string][]byte)
    for _, zf := range zr.File {
        rc, err := zf.Open()
        if err != nil {
            return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read " + zf.Name})
        }
        data, _ := io.ReadAll(rc)
        rc.Close()
        switch zf.Name {
        case "data.json.gz":
            dataGZ = data
        case "schema.gz":
            schemaGZ = data
        case "layers.json":
            // already processed
        default:
            if mt, ok := filenameToMediaType[zf.Name]; ok {
                extraLayers[mt] = data
            }
        }
    }

    if len(dataGZ) == 0 {
        return c.JSON(http.StatusBadRequest, map[string]string{"error": "zip must contain data.json.gz"})
    }
    if len(schemaGZ) == 0 {
        return c.JSON(http.StatusBadRequest, map[string]string{"error": "zip must contain schema.gz"})
    }

    tag := fmt.Sprintf("artifact-%s", time.Now().UTC().Format("20060102-150405"))
    importID := newImportID()
    s.state.setRunning()

    go func() {
        ctx := context.Background()
        meta := orb.ImportMeta{
            Tag:       tag,
            CreatedAt: time.Now().UTC(),
        }
        if err := s.imp.Import(ctx, dataGZ, schemaGZ, meta); err != nil {
            s.state.setFailed("artifact import: " + err.Error())
            return
        }

        // Best-effort consumer dispatch for extra layers.
        var results []orb.DispatchResult
        if len(extraLayers) > 0 && s.dispatcher != nil {
            results = s.dispatcher.Dispatch(ctx, extraLayers, tag, "", importID)
            if err := orb.PatchLastHistoryDispatch(s.cfg.DataDir, results); err != nil {
                s.logger.Warn("failed to patch dispatch results in history", "err", err)
            }
        }

        s.state.setDone(orb.ImportRecord{
            Tag:             tag,
            ImportedAt:      time.Now().UTC(),
            Status:          "done",
            DispatchResults: results,
        })
    }()

    return c.JSON(http.StatusAccepted, map[string]string{"status": "started", "tag": tag, "importId": importID})
}
```

### Tests for Step 5 — Synchronous path only (unit, no DGraph)

**File:** `internal/orbserver/import_handlers_test.go` (add to existing file)

These tests cover the synchronous validation path. The async goroutine is tested in the integration test (Step 7).

All tests use `t.Chdir("../..") ` (required for template loading) and the existing `testCfg` helper.

```go
func TestImportArtifact_AlreadyRunning(t *testing.T) {
    t.Chdir("../..")
    cfg := testCfg(t)
    srv, _ := New(cfg)
    srv.state.setRunning()

    body, contentType := makeArtifactZipForm(t, validArtifactZip(t))
    req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
    req.Header.Set("Content-Type", contentType)
    rec := httptest.NewRecorder()
    srv.echo.ServeHTTP(rec, req)

    if rec.Code != http.StatusConflict {
        t.Errorf("expected 409, got %d", rec.Code)
    }
}

func TestImportArtifact_MissingBundle(t *testing.T) {
    t.Chdir("../..")
    cfg := testCfg(t)
    srv, _ := New(cfg)

    req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", nil)
    req.Header.Set("Content-Type", "application/octet-stream")
    rec := httptest.NewRecorder()
    srv.echo.ServeHTTP(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Errorf("expected 400, got %d", rec.Code)
    }
}

func TestImportArtifact_InvalidZip(t *testing.T) {
    t.Chdir("../..")
    cfg := testCfg(t)
    srv, _ := New(cfg)

    body, contentType := makeArtifactZipForm(t, []byte("not a zip"))
    req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
    req.Header.Set("Content-Type", contentType)
    rec := httptest.NewRecorder()
    srv.echo.ServeHTTP(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Errorf("expected 400, got %d", rec.Code)
    }
}

func TestImportArtifact_MissingDataLayer(t *testing.T) {
    t.Chdir("../..")
    cfg := testCfg(t)
    srv, _ := New(cfg)

    z := buildZip(t, map[string][]byte{"schema.gz": []byte("schema")})
    body, contentType := makeArtifactZipForm(t, z)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
    req.Header.Set("Content-Type", contentType)
    rec := httptest.NewRecorder()
    srv.echo.ServeHTTP(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Errorf("expected 400, got %d", rec.Code)
    }
}

func TestImportArtifact_MissingSchemaLayer(t *testing.T) {
    t.Chdir("../..")
    cfg := testCfg(t)
    srv, _ := New(cfg)

    z := buildZip(t, map[string][]byte{"data.json.gz": []byte("data")})
    body, contentType := makeArtifactZipForm(t, z)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
    req.Header.Set("Content-Type", contentType)
    rec := httptest.NewRecorder()
    srv.echo.ServeHTTP(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Errorf("expected 400, got %d", rec.Code)
    }
}

func TestImportArtifact_InvalidLayersJSON(t *testing.T) {
    t.Chdir("../..")
    cfg := testCfg(t)
    srv, _ := New(cfg)

    z := buildZip(t, map[string][]byte{
        "data.json.gz": []byte("data"),
        "schema.gz":    []byte("schema"),
        "layers.json":  []byte("not json"),
    })
    body, contentType := makeArtifactZipForm(t, z)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
    req.Header.Set("Content-Type", contentType)
    rec := httptest.NewRecorder()
    srv.echo.ServeHTTP(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Errorf("expected 400, got %d", rec.Code)
    }
}

func TestImportArtifact_ValidZipReturns202(t *testing.T) {
    // Only asserts that the handler accepts the request and returns 202.
    // The async goroutine will fail (no real DGraph) — that is acceptable for this unit test.
    t.Chdir("../..")
    cfg := testCfg(t)
    srv, _ := New(cfg)

    body, contentType := makeArtifactZipForm(t, validArtifactZip(t))
    req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", body)
    req.Header.Set("Content-Type", contentType)
    rec := httptest.NewRecorder()
    srv.echo.ServeHTTP(rec, req)

    if rec.Code != http.StatusAccepted {
        t.Errorf("expected 202, got %d: %s", rec.Code, rec.Body.String())
    }
    // Verify response body has expected keys.
    var resp map[string]string
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if resp["status"] != "started" {
        t.Errorf("status: got %q, want %q", resp["status"], "started")
    }
    if resp["importId"] == "" {
        t.Error("importId should be set in response")
    }
}

// --- Helpers ---

// buildZip creates an in-memory zip archive from the given files.
func buildZip(t *testing.T, files map[string][]byte) []byte {
    t.Helper()
    var buf bytes.Buffer
    w := zip.NewWriter(&buf)
    for name, data := range files {
        f, err := w.Create(name)
        if err != nil {
            t.Fatalf("zip create %s: %v", name, err)
        }
        f.Write(data)
    }
    w.Close()
    return buf.Bytes()
}

// validArtifactZip returns a minimal valid artifact zip.
func validArtifactZip(t *testing.T) []byte {
    return buildZip(t, map[string][]byte{
        "data.json.gz": []byte("fake-data"),
        "schema.gz":    []byte("fake-schema"),
    })
}

// makeArtifactZipForm wraps zip bytes in a multipart form (field: "bundle").
func makeArtifactZipForm(t *testing.T, zipBytes []byte) (io.Reader, string) {
    t.Helper()
    var body bytes.Buffer
    w := multipart.NewWriter(&body)
    fw, err := w.CreateFormFile("bundle", "artifact.zip")
    if err != nil {
        t.Fatalf("create form file: %v", err)
    }
    fw.Write(zipBytes)
    w.Close()
    return &body, w.FormDataContentType()
}
```

Add `"archive/zip"`, `"mime/multipart"` to imports.

**Run:** `go test -short -race ./internal/orbserver/...`

---

## Step 6 — Route Registration + Server Field

**File:** `internal/orbserver/server.go`

1. Add `dispatcher` field to `Server`:

```go
type Server struct {
    cfg          *orbconfig.Config
    echo         *echo.Echo
    logger       *slog.Logger
    state        *importState
    imp          *orb.Importer
    dispatcher   *orb.Dispatcher        // nil if no consumers configured
    divStore     *divergence.Store
    divPublisher *divergence.Publisher
    templates    map[string]*template.Template
    devMode      bool
}
```

2. Initialize in `New()` (after `imp` initialization):

```go
dispatcher := orb.NewDispatcher(cfg.Consumers)
```

3. Set on the struct:

```go
s := &Server{
    ...
    dispatcher:   dispatcher,
    ...
}
```

4. Register the route (in the `api` group, always registered — place next to `importSubgraph`):

```go
api.POST("/import/subgraph", s.importSubgraph)
api.POST("/import/artifact", s.importArtifact)   // ← add this line
api.GET("/import/status", s.importStatus)
```

### Tests for Step 6

**File:** `internal/orbserver/server_routes_test.go` (add to existing file)

```go
func TestRoutes_ImportArtifactAlwaysRegistered(t *testing.T) {
    t.Chdir("../..")

    for _, ociEnabled := range []bool{false, true} {
        cfg := testCfg(t)
        cfg.EnableOCIRegistry = ociEnabled

        code := routeStatus(t, cfg, http.MethodPost, "/api/v1/import/artifact")
        if code == http.StatusNotFound {
            t.Errorf("POST /api/v1/import/artifact (OCI=%v): got 404, expected route to be registered", ociEnabled)
        }
    }
}
```

**Run:** `go test -short -race ./internal/orbserver/...`

---

## Step 7 — Integration Test

**File:** `internal/orbserver/import_artifact_integration_test.go` (new)

```go
//go:build integration

package orbserver_test

import (
    "archive/zip"
    "bytes"
    "io"
    "mime/multipart"
    "net/http"
    "net/http/httptest"
    "sync/atomic"
    "testing"
    "time"

    "github.com/armada/orbital/internal/orbconfig"
    "github.com/armada/orbital/internal/orbserver"
)

// TestImportArtifact_FullPipeline verifies the complete artifact import pipeline:
// zip upload → DGraph import → consumer dispatch → history updated.
//
// Requires: DGraph running on test ports (make up), consumer is an httptest.Server.
func TestImportArtifact_FullPipeline(t *testing.T) {
    t.Chdir("../..")

    // Start a fake consumer that records what it receives.
    var receivedBody []byte
    var receivedMediaType string
    var receivedTag string
    var callCount atomic.Int32
    consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        callCount.Add(1)
        receivedMediaType = r.Header.Get("Content-Type")
        receivedTag = r.Header.Get("X-Orb-Tag")
        receivedBody, _ = io.ReadAll(r.Body)
        w.WriteHeader(http.StatusOK)
    }))
    defer consumer.Close()

    manifestData := []byte("manifest: test-value")
    const testMediaType = "application/vnd.armada.configbundle.manifest.v1+yaml"

    cfg := &orbconfig.Config{
        Port:                "0",
        DGraphURL:           "http://localhost:18082/graphql",  // test DGraph
        DGraphAdminURL:      "http://localhost:18082/admin",
        DGraphAlphaGRPC:     "localhost:19082",
        DataDir:             t.TempDir(),
        Backend:             "docker",
        DGraphContainerName: "local-dgraph-orb-alpha-1",
        PollInterval:        60 * time.Second,
        LogLevel:            "error",
        Consumers: orbconfig.ConsumersConfig{
            {MediaType: testMediaType, URL: consumer.URL},
        },
    }

    srv, err := orbserver.New(cfg)
    if err != nil {
        t.Fatalf("New: %v", err)
    }

    // Build artifact zip with a real (minimal) data.json.gz and schema.gz,
    // plus a CB manifest extra layer.
    artifactZip := buildArtifactZipWithExtra(t, manifestData, testMediaType, "cb-manifest.yaml")

    var body bytes.Buffer
    mw := multipart.NewWriter(&body)
    fw, _ := mw.CreateFormFile("bundle", "artifact.zip")
    fw.Write(artifactZip)
    mw.Close()

    req := httptest.NewRequest(http.MethodPost, "/api/v1/import/artifact", &body)
    req.Header.Set("Content-Type", mw.FormDataContentType())
    rec := httptest.NewRecorder()
    srv.ServeHTTP(rec, req)  // or use srv.echo.ServeHTTP

    if rec.Code != http.StatusAccepted {
        t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
    }

    // Wait for the async goroutine to dispatch to the consumer.
    deadline := time.Now().Add(15 * time.Second)
    for time.Now().Before(deadline) {
        if callCount.Load() > 0 {
            break
        }
        time.Sleep(100 * time.Millisecond)
    }
    if callCount.Load() == 0 {
        t.Fatal("consumer was not called within 15 seconds")
    }

    // Assert dispatch headers and body.
    if receivedMediaType != testMediaType {
        t.Errorf("Content-Type: got %q, want %q", receivedMediaType, testMediaType)
    }
    if string(receivedBody) != string(manifestData) {
        t.Errorf("body: got %q, want %q", receivedBody, manifestData)
    }
    if receivedTag == "" {
        t.Error("X-Orb-Tag should be set")
    }

    // Assert dispatch results appear in history.
    deadline = time.Now().Add(5 * time.Second)
    var history []map[string]interface{}
    for time.Now().Before(deadline) {
        histReq := httptest.NewRequest(http.MethodGet, "/api/v1/import/history", nil)
        histRec := httptest.NewRecorder()
        srv.ServeHTTP(histRec, histReq)
        json.NewDecoder(histRec.Body).Decode(&history)
        if len(history) > 0 && history[len(history)-1]["dispatchResults"] != nil {
            break
        }
        time.Sleep(100 * time.Millisecond)
    }
    if len(history) == 0 {
        t.Fatal("history should have at least one record")
    }
    last := history[len(history)-1]
    if last["dispatchResults"] == nil {
        t.Error("last history record should have dispatchResults")
    }
}
```

**Note on the integration test:** This test requires a real DGraph running on test ports (18082/19082). It also needs a real subgraph zip (actual valid DGraph data). For the integration test, use a pre-built minimal subgraph fixture or generate one with `testutil.SeedMinimal`. The fixture can be stored in `internal/orbserver/testdata/minimal-subgraph.zip` and checked into the repo — it is a small static file.

**Build the testdata fixture once:**
```bash
# After make up and make run-orbital is seeded:
curl -s -X POST http://localhost:8001/api/v1/datacenters/<dcId>/export | jq .jobId
# Wait for complete, then download the zip
curl -s http://localhost:8001/api/v1/export/jobs/<jobId>/download -o internal/orbserver/testdata/minimal-subgraph.zip
```

**Run:** `go test -tags integration -race ./internal/orbserver/...`

---

## CB Controller Testing (update `docs/cb-controller-consumer-plan.md`)

The CB Controller `POST /consume` handler must be tested at all three layers. See the updated Step 7 below.

---

## Testing Summary

### Unit tests (Step 1–6, `go test -short -race`)

| Layer | File | Count | Step |
|-------|------|-------|------|
| Config parsing | `internal/orbconfig/config_test.go` | 4 | 1 |
| OCI extra layers | `internal/oci/puller_test.go` | 2 | 2 |
| Dispatcher | `internal/orb/dispatch_test.go` | 7 | 3 |
| History round-trip | `internal/orb/importer_test.go` | 4 | 4 |
| Handler validation | `internal/orbserver/import_handlers_test.go` | 7 | 5 |
| Route registration | `internal/orbserver/server_routes_test.go` | 1 | 6 |

### Integration test (Step 7, `go test -tags integration -race`)

| File | Test | Requires |
|------|------|---------|
| `internal/orbserver/import_artifact_integration_test.go` | `TestImportArtifact_FullPipeline` | DGraph on test ports + httptest consumer |

### E2E

No new E2E test — this is an API endpoint. The import history UI will show `dispatchResults` when they are added to the history page template (future work).

---

## Post-implementation: Update `triggerImport` (follow-up, not in scope here)

Once `/import/artifact` is working, update `triggerImport` (OCI poller path) to also dispatch extra layers from `PulledArtifact.ExtraLayers`. The pattern is identical:

```go
// After s.imp.Import(ctx, artifact.DataGZ, artifact.SchemaGZ, meta) succeeds:
if len(artifact.ExtraLayers) > 0 && s.dispatcher != nil {
    results := s.dispatcher.Dispatch(ctx, artifact.ExtraLayers, meta.Tag, meta.Digest, importID)
    orb.PatchLastHistoryDispatch(s.cfg.DataDir, results)
    // update setDone record with results
}
```

This follow-up completes the symmetric pipeline and is tracked as a separate task.
