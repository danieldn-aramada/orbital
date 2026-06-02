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

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: cbManifestMediaType, URL: srv.URL}})
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

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: cbManifestMediaType, URL: srv.URL}})
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

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: cbManifestMediaType, URL: srv.URL}})
	d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: payload}, "v1", "", "")

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
	results := d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("x")}, "v1", "", "")

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

	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: cbManifestMediaType, URL: srv.URL}})
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
	d := orb.NewDispatcher([]orbconfig.ConsumerConfig{{MediaType: cbManifestMediaType, URL: "http://127.0.0.1:1"}})
	results := d.Dispatch(context.Background(), map[string][]byte{cbManifestMediaType: []byte("x")}, "v1", "", "")

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == "" {
		t.Error("expected Error to be set for unreachable consumer")
	}
}
