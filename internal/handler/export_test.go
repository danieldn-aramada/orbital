package handler

import (
	"archive/zip"
	"bytes"
	"path/filepath"
	"testing"
)

func TestWriteZip(t *testing.T) {
	dataGZ := []byte("fake-data-gz")
	dqlSchemaGZ := []byte("fake-dql-schema-gz")
	gqlSchemaGZ := []byte("fake-gql-schema-gz")

	t.Run("writes all three entries", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.zip")
		if err := writeZip(path, dataGZ, dqlSchemaGZ, gqlSchemaGZ); err != nil {
			t.Fatalf("writeZip: %v", err)
		}

		contents := readZipContents(t, path)
		assertZipEntry(t, contents, "data.json.gz", dataGZ)
		assertZipEntry(t, contents, "schema.gz", dqlSchemaGZ)
		assertZipEntry(t, contents, "gql_schema.gz", gqlSchemaGZ)
	})

	t.Run("nil entries are omitted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.zip")
		if err := writeZip(path, dataGZ, nil, nil); err != nil {
			t.Fatalf("writeZip: %v", err)
		}

		contents := readZipContents(t, path)
		if len(contents) != 1 {
			t.Errorf("expected 1 entry, got %d", len(contents))
		}
		assertZipEntry(t, contents, "data.json.gz", dataGZ)
	})

	t.Run("fails on unwritable path", func(t *testing.T) {
		err := writeZip("/nonexistent/path/out.zip", dataGZ, dqlSchemaGZ, gqlSchemaGZ)
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

