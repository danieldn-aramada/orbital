package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteZip(t *testing.T) {
	dataGZ := []byte("fake-data-gz")
	dqlSchemaGZ := []byte("fake-dql-schema-gz")
	gqlSchemaGZ := []byte("fake-gql-schema-gz")

	t.Run("writes all three entries", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.zip")
		if err := writeZip(path, dataGZ, dqlSchemaGZ, gqlSchemaGZ, nil); err != nil {
			t.Fatalf("writeZip: %v", err)
		}

		contents := readZipContents(t, path)
		assertZipEntry(t, contents, "data.json.gz", dataGZ)
		assertZipEntry(t, contents, "schema.gz", dqlSchemaGZ)
		assertZipEntry(t, contents, "gql_schema.gz", gqlSchemaGZ)
	})

	t.Run("nil entries are omitted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.zip")
		if err := writeZip(path, dataGZ, nil, nil, nil); err != nil {
			t.Fatalf("writeZip: %v", err)
		}

		contents := readZipContents(t, path)
		if len(contents) != 1 {
			t.Errorf("expected 1 entry, got %d", len(contents))
		}
		assertZipEntry(t, contents, "data.json.gz", dataGZ)
	})

	t.Run("fails on unwritable path", func(t *testing.T) {
		err := writeZip("/nonexistent/path/out.zip", dataGZ, dqlSchemaGZ, gqlSchemaGZ, nil)
		if err == nil {
			t.Fatal("expected error for bad path, got nil")
		}
	})
}

func TestGzipBytes(t *testing.T) {
	t.Run("produces non-empty output", func(t *testing.T) {
		input := []byte("hello world")
		out, err := gzipBytes(input)
		if err != nil {
			t.Fatalf("gzipBytes: %v", err)
		}
		if len(out) == 0 {
			t.Error("expected non-empty gzip output")
		}
	})

	t.Run("empty input produces valid gzip", func(t *testing.T) {
		out, err := gzipBytes([]byte{})
		if err != nil {
			t.Fatalf("gzipBytes empty: %v", err)
		}
		if len(out) == 0 {
			t.Error("expected non-empty gzip header even for empty input")
		}
	})

	t.Run("output differs from input", func(t *testing.T) {
		input := []byte("hello world")
		out, err := gzipBytes(input)
		if err != nil {
			t.Fatalf("gzipBytes: %v", err)
		}
		if bytes.Equal(out, input) {
			t.Error("gzip output should not equal input")
		}
	})
}

// readZipContents opens a zip file and returns a map of name → contents.
func readZipContents(t *testing.T, path string) map[string][]byte {
	t.Helper()

	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer r.Close()

	out := make(map[string][]byte, len(r.File))
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			rc.Close()
			t.Fatalf("read zip entry %s: %v", f.Name, err)
		}
		rc.Close()
		out[f.Name] = buf.Bytes()
	}
	return out
}

func assertZipEntry(t *testing.T, contents map[string][]byte, name string, want []byte) {
	t.Helper()
	got, ok := contents[name]
	if !ok {
		t.Errorf("zip entry %q not found", name)
		return
	}
	if !bytes.Equal(got, want) {
		t.Errorf("zip entry %q: got %q, want %q", name, got, want)
	}
}

// newTestExport creates an Export handler pointing at the given mock DGraph URL.
func newTestExport(dgraphURL string) *Export {
	return NewExport(
		nil, // db not needed for DQL/fetch tests
		dgraphURL, dgraphURL, dgraphURL, dgraphURL,
		"/tmp", "/tmp", "",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func TestFetchDCInfo_ReadsNamespaceScalar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"getDataCenter": map[string]any{
					"name":      "colo-galleon",
					"orbId":     "colo:colo-galleon",
					"namespace": "colo",
				},
			},
		})
	}))
	defer srv.Close()

	h := newTestExport(srv.URL + "/graphql")
	name, orbID, ns, err := h.fetchDCInfo(context.Background(), "colo:colo-galleon")
	if err != nil {
		t.Fatalf("fetchDCInfo: %v", err)
	}
	if name != "colo-galleon" {
		t.Errorf("name: got %q, want %q", name, "colo-galleon")
	}
	if orbID != "colo:colo-galleon" {
		t.Errorf("orbID: got %q, want %q", orbID, "colo:colo-galleon")
	}
	if ns != "colo" {
		t.Errorf("namespace: got %q, want %q", ns, "colo")
	}
}

func TestFetchDCInfo_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"getDataCenter": nil},
		})
	}))
	defer srv.Close()

	h := newTestExport(srv.URL + "/graphql")
	_, _, _, err := h.fetchDCInfo(context.Background(), "missing:dc")
	if err == nil {
		t.Fatal("expected error for missing DC, got nil")
	}
}

func TestFetchDCInfo_QueryUsesNamespaceScalarField(t *testing.T) {
	var capturedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capturedBody = string(b)
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"getDataCenter": map[string]any{
					"name": "dc", "orbId": "ns:dc", "namespace": "ns",
				},
			},
		})
	}))
	defer srv.Close()

	h := newTestExport(srv.URL + "/graphql")
	h.fetchDCInfo(context.Background(), "ns:dc") //nolint:errcheck

	if strings.Contains(capturedBody, "namespace {") {
		t.Error("fetchDCInfo query must not use namespace edge traversal — use namespace scalar")
	}
	if !strings.Contains(capturedBody, "namespace") {
		t.Error("fetchDCInfo query must request namespace scalar field")
	}
}

func TestFetchNamespaceSubgraph_DQLUsesNamespaceScalar(t *testing.T) {
	var capturedDQL string
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		callCount++

		if callCount == 1 {
			// First call: fetchUIDPredicates — return empty schema
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"schema": []any{}},
			})
			return
		}
		// Second call: the DQL subgraph query
		capturedDQL = body
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"ns":    []any{map[string]any{"uid": "0x1", "dgraph.type": []string{"Namespace"}}},
				"items": []any{},
				"edges": []any{},
			},
		})
	}))
	defer srv.Close()

	h := newTestExport(srv.URL + "/graphql")
	h.fetchNamespaceSubgraph(context.Background(), "colo") //nolint:errcheck

	if strings.Contains(capturedDQL, "uid_in") {
		t.Error("subgraph DQL must not use uid_in (edge-based) — use eq(ConfigItem.namespace, ...) scalar filter")
	}
	if !strings.Contains(capturedDQL, "eq(ConfigItem.namespace") {
		t.Error("subgraph DQL must use eq(ConfigItem.namespace, ...) scalar filter")
	}
}
