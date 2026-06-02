package oci

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// newOCIServer returns an httptest.Server that speaks OCI Distribution Spec v1.
// It serves a tags list from the provided tags slice, and a minimal manifest
// for each tag containing one layer of the given size.
func newOCIServer(t *testing.T, tags []string, layerSize int64) *httptest.Server {
	t.Helper()

	// Build a minimal OCI manifest with one layer.
	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Layers: []ocispec.Descriptor{
			{
				MediaType: mediaTypeDataGZ,
				Digest:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Size:      layerSize,
			},
		},
	}
	manifestBytes, _ := json.Marshal(manifest)
	sum := sha256.Sum256(manifestBytes)
	manifestDigest := fmt.Sprintf("sha256:%x", sum)

	mux := http.NewServeMux()

	// OCI Distribution Spec: GET /v2/{name}/tags/list
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Tags list: /v2/{name}/tags/list
		if strings.HasSuffix(path, "/tags/list") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"name": "test/repo",
				"tags": tags,
			})
			return
		}

		// Manifest by tag: /v2/{name}/manifests/{reference}
		if strings.Contains(path, "/manifests/") {
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			w.WriteHeader(http.StatusOK)
			w.Write(manifestBytes)
			return
		}

		http.NotFound(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func pullCfgForServer(srv *httptest.Server) PullConfig {
	// Strip http:// from URL — oras-go expects host:port
	addr := strings.TrimPrefix(srv.URL, "http://")
	return PullConfig{
		Registry:  addr,
		Repo:      "test/repo",
		AllowHTTP: true,
	}
}

func TestListTags_Returns(t *testing.T) {
	srv := newOCIServer(t, []string{"v1", "v2", "v3"}, 1024)
	cfg := pullCfgForServer(srv)

	tags, err := ListTags(t.Context(), cfg)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 3 {
		t.Fatalf("expected 3 tags, got %d: %v", len(tags), tags)
	}
}

func TestListTags_Empty(t *testing.T) {
	srv := newOCIServer(t, nil, 0)
	cfg := pullCfgForServer(srv)

	tags, err := ListTags(t.Context(), cfg)
	if err != nil {
		t.Fatalf("ListTags empty: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected empty, got %v", tags)
	}
}

func TestListTags_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	addr := strings.TrimPrefix(srv.URL, "http://")
	cfg := PullConfig{Registry: addr, Repo: "test/repo", AllowHTTP: true}

	_, err := ListTags(t.Context(), cfg)
	if err == nil {
		t.Error("expected error from server 500, got nil")
	}
}

func TestResolveTag_ReturnsDigestAndSize(t *testing.T) {
	const layerSize = int64(2048)
	srv := newOCIServer(t, []string{"v1"}, layerSize)
	cfg := pullCfgForServer(srv)

	meta, err := ResolveTag(t.Context(), cfg, "v1")
	if err != nil {
		t.Fatalf("ResolveTag: %v", err)
	}
	if meta.Digest == "" {
		t.Error("Digest should be non-empty")
	}
	if meta.TotalSize != layerSize {
		t.Errorf("TotalSize: got %d, want %d", meta.TotalSize, layerSize)
	}
}

func TestResolveTag_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintln(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	addr := strings.TrimPrefix(srv.URL, "http://")
	cfg := PullConfig{Registry: addr, Repo: "test/repo", AllowHTTP: true}

	_, err := ResolveTag(t.Context(), cfg, "v99")
	if err == nil {
		t.Error("expected error for non-existent tag, got nil")
	}
}
