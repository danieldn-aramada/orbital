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

const cbManifestMediaType = "application/vnd.armada.configbundle.manifest.v1+yaml"

func TestDispatcher_DispatchesLayer(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{Name: "test", URL: srv.URL}})
	results := d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("payload")}, "v1", "sha256:abc", "id-1")

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
	if results[0].ConsumerName != "test" {
		t.Errorf("expected ConsumerName=test, got %q", results[0].ConsumerName)
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

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{Name: "test", URL: srv.URL}})
	d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("x")}, "v3", "sha256:digest", "import-uuid")

	if gotContentType != cbManifestMediaType {
		t.Errorf("Content-Type: got %q, want %q", gotContentType, cbManifestMediaType)
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

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{Name: "test", URL: srv.URL}})
	d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: payload}, "v1", "", "")

	if string(gotBody) != string(payload) {
		t.Errorf("body mismatch: got %q, want %q", gotBody, payload)
	}
}

// Broadcast model: a consumer that 415s for a media type doesn't surface
// in DispatchResult.Error — that's expected behavior, not a failure. Only
// real errors (non-415) propagate to the operator. If NO consumer 2xxs and
// only 415s came back, the result has no error (consumer simply didn't care).
func TestDispatcher_BroadcastCollapses415(t *testing.T) {
	// Consumer A 415s; consumer B 2xxs the same layer.
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnsupportedMediaType)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srvB.Close()

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{
		{Name: "A", URL: srvA.URL},
		{Name: "B", URL: srvB.URL},
	})
	results := d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("x")}, "v1", "", "")

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].StatusCode != http.StatusOK {
		t.Errorf("expected the first 2xx to win; got %d", results[0].StatusCode)
	}
	if results[0].ConsumerName != "B" {
		t.Errorf("expected ConsumerName=B (the accepting consumer); got %q", results[0].ConsumerName)
	}
}

func TestDispatcher_ConsumerFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{Name: "test", URL: srv.URL}})
	results := d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("x")}, "v1", "", "")

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
	results := d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("x")}, "v1", "", "")
	if len(results) != 0 {
		t.Errorf("expected 0 results with no consumers, got %d", len(results))
	}
}

func TestDispatcher_ConsumerUnreachable(t *testing.T) {
	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{Name: "test", URL: "http://127.0.0.1:1"}})
	results := d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("x")}, "v1", "", "")

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == "" {
		t.Error("expected Error to be set for unreachable consumer")
	}
}

// Layer ordering: dispatch sorts by media type so cb-controller's manifest
// (lexicographically before "mapping") arrives first. Without this, the
// mapping layer's OwnerReference write would race against the manifest's
// async CR creation.
func TestDispatcher_LayerOrderingDeterministic(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{Name: "cb", URL: srv.URL}})
	d.Dispatch(context.Background(), map[string][]byte{
		"application/vnd.armada.configbundle.mapping.v1+json":  []byte("map"),
		"application/vnd.armada.configbundle.manifest.v1+yaml": []byte("man"),
	}, "v1", "", "")

	if len(seen) != 2 {
		t.Fatalf("expected 2 dispatches, got %d", len(seen))
	}
	if seen[0] != "application/vnd.armada.configbundle.manifest.v1+yaml" {
		t.Errorf("expected manifest first; got %q first", seen[0])
	}
}

// 409 retry: cb-controller returns 409 on the mapping layer while the
// manifest's ConfigBundle CR hasn't propagated to its informer yet. The
// dispatcher must retry with exponential backoff and surface success once
// the CR settles. Without this retry the user-visible flake is "import
// failed with 409 first time, worked second time" — the bug class this
// test pins. Pre-fix the budget was 4×500ms (~1.5s); current budget is
// 5 attempts with exponential backoff capped at 2s → ~5.5s total.
func TestDispatcher_RetriesOn409(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{Name: "cb", URL: srv.URL}})
	results := d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("x")}, "v1", "", "")

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].StatusCode != http.StatusOK {
		t.Errorf("expected final 200 after 409 retries, got %d (error: %s)", results[0].StatusCode, results[0].Error)
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts (2 409s + 1 200), got %d", attempts)
	}
}
