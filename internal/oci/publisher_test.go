package oci

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/armada/orbital/ent"
)

func TestRepoForDC(t *testing.T) {
	tests := []struct {
		name     string
		registry string
		repo     string
		dcName   string
		want     string
	}{
		{
			name:     "simple lowercase",
			registry: "myregistry.azurecr.io",
			repo:     "orbital",
			dcName:   "alaska",
			want:     "myregistry.azurecr.io/orbital/alaska",
		},
		{
			name:     "uppercase becomes lowercase",
			registry: "myregistry.azurecr.io",
			repo:     "orbital",
			dcName:   "Alaska",
			want:     "myregistry.azurecr.io/orbital/alaska",
		},
		{
			name:     "spaces become hyphens",
			registry: "myregistry.azurecr.io",
			repo:     "orbital",
			dcName:   "Alaska DOT",
			want:     "myregistry.azurecr.io/orbital/alaska-dot",
		},
		{
			name:     "special chars stripped (underscore is removed, not hyphenated)",
			registry: "myregistry.azurecr.io",
			repo:     "orbital",
			dcName:   "Alaska_DOT!",
			want:     "myregistry.azurecr.io/orbital/alaskadot",
		},
		{
			name:     "leading and trailing hyphens trimmed",
			registry: "myregistry.azurecr.io",
			repo:     "orbital",
			dcName:   "---alaska---",
			want:     "myregistry.azurecr.io/orbital/alaska",
		},
		{
			name:     "empty name falls back to dc",
			registry: "myregistry.azurecr.io",
			repo:     "orbital",
			dcName:   "",
			want:     "myregistry.azurecr.io/orbital/dc",
		},
		{
			name:     "only special chars falls back to dc",
			registry: "myregistry.azurecr.io",
			repo:     "orbital",
			dcName:   "!!!",
			want:     "myregistry.azurecr.io/orbital/dc",
		},
		{
			name:     "real-world example",
			registry: "armadaeksatest.azurecr.io",
			repo:     "orbital",
			dcName:   "Alaska DOT GRTLY24",
			want:     "armadaeksatest.azurecr.io/orbital/alaska-dot-grtly24",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RepoForDC(tt.registry, tt.repo, tt.dcName)
			if got != tt.want {
				t.Errorf("RepoForDC(%q, %q, %q) = %q, want %q", tt.registry, tt.repo, tt.dcName, got, tt.want)
			}
		})
	}
}

func TestNextTag(t *testing.T) {
	tests := []struct {
		count int
		want  string
	}{
		{count: 0, want: "v1"},
		{count: 1, want: "v2"},
		{count: 5, want: "v6"},
		{count: 99, want: "v100"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := NextTag(tt.count)
			if got != tt.want {
				t.Errorf("NextTag(%d) = %q, want %q", tt.count, got, tt.want)
			}
		})
	}
}

func TestNextTagAfter(t *testing.T) {
	makeArtifacts := func(tags ...string) []*ent.RegistryArtifact {
		out := make([]*ent.RegistryArtifact, len(tags))
		for i, t := range tags {
			out[i] = &ent.RegistryArtifact{Tag: t}
		}
		return out
	}

	tests := []struct {
		desc string
		tags []*ent.RegistryArtifact
		want string
	}{
		{"empty — first publish", makeArtifacts(), "v1"},
		{"only v1", makeArtifacts("v1"), "v2"},
		{"v1 and v2", makeArtifacts("v1", "v2"), "v3"},
		{"duplicate v1 entries (old DatacenterID bug)", makeArtifacts("v1", "v1", "v1", "v2"), "v3"},
		{"gap in sequence", makeArtifacts("v1", "v3"), "v4"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := NextTagAfter(tt.tags)
			if got != tt.want {
				t.Errorf("NextTagAfter(%v) = %q, want %q", tt.tags, got, tt.want)
			}
		})
	}
}

func TestExtractZip(t *testing.T) {
	dataGZ := []byte("fake-data-gz-content")
	schemaGZ := []byte("fake-schema-gz-content")

	t.Run("happy path returns both files", func(t *testing.T) {
		path := makeTestZip(t, map[string][]byte{
			"data.json.gz": dataGZ,
			"schema.gz":    schemaGZ,
		})

		gotData, gotSchema, err := extractZip(path)
		if err != nil {
			t.Fatalf("extractZip: %v", err)
		}
		if !bytes.Equal(gotData, dataGZ) {
			t.Errorf("data mismatch: got %q, want %q", gotData, dataGZ)
		}
		if !bytes.Equal(gotSchema, schemaGZ) {
			t.Errorf("schema mismatch: got %q, want %q", gotSchema, schemaGZ)
		}
	})

	t.Run("extra files in zip are ignored", func(t *testing.T) {
		path := makeTestZip(t, map[string][]byte{
			"data.json.gz": dataGZ,
			"schema.gz":    schemaGZ,
			"extra.txt":    []byte("ignored"),
		})

		_, _, err := extractZip(path)
		if err != nil {
			t.Fatalf("extractZip: %v", err)
		}
	})

	t.Run("missing data.json.gz returns error", func(t *testing.T) {
		path := makeTestZip(t, map[string][]byte{
			"schema.gz": schemaGZ,
		})

		_, _, err := extractZip(path)
		if err == nil {
			t.Fatal("expected error for missing data.json.gz, got nil")
		}
	})

	t.Run("missing schema.gz returns error", func(t *testing.T) {
		path := makeTestZip(t, map[string][]byte{
			"data.json.gz": dataGZ,
		})

		_, _, err := extractZip(path)
		if err == nil {
			t.Fatal("expected error for missing schema.gz, got nil")
		}
	})

	t.Run("non-existent file returns error", func(t *testing.T) {
		_, _, err := extractZip("/tmp/does-not-exist-orbital-test.zip")
		if err == nil {
			t.Fatal("expected error for non-existent file, got nil")
		}
	})
}

func TestPublicKeyFingerprint(t *testing.T) {
	key1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	key2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key 2: %v", err)
	}

	t.Run("returns hex string", func(t *testing.T) {
		fp, err := PublicKeyFingerprint(key1.Public())
		if err != nil {
			t.Fatalf("PublicKeyFingerprint: %v", err)
		}
		if len(fp) != 64 {
			t.Errorf("expected 64-char hex (sha256), got %d: %s", len(fp), fp)
		}
	})

	t.Run("same key produces same fingerprint", func(t *testing.T) {
		fp1, err := PublicKeyFingerprint(key1.Public())
		if err != nil {
			t.Fatalf("first call: %v", err)
		}
		fp2, err := PublicKeyFingerprint(key1.Public())
		if err != nil {
			t.Fatalf("second call: %v", err)
		}
		if fp1 != fp2 {
			t.Errorf("fingerprint not deterministic: %q != %q", fp1, fp2)
		}
	})

	t.Run("different keys produce different fingerprints", func(t *testing.T) {
		fp1, err := PublicKeyFingerprint(key1.Public())
		if err != nil {
			t.Fatalf("key1: %v", err)
		}
		fp2, err := PublicKeyFingerprint(key2.Public())
		if err != nil {
			t.Fatalf("key2: %v", err)
		}
		if fp1 == fp2 {
			t.Error("different keys produced identical fingerprints")
		}
	})
}

// TestBuildLayerMeta verifies that pushArtifact builds layer metadata with correct
// IsOrbitalNative flags — orbital-native layers are true, bundler layers are false.
// We test via buildLayerMeta rather than calling doPush (which requires a live registry).
func TestBuildLayerMeta_OrbitalNativeAndBundler(t *testing.T) {
	dataGZ := []byte("fake-data-gz")
	schemaGZ := []byte("fake-schema-gz")
	bundlerData1 := []byte("bundler-layer-one")
	bundlerData2 := []byte("bundler-layer-two")

	layers := buildLayerMeta(dataGZ, schemaGZ, []bundlerLayerInput{
		{mediaType: "application/vnd.example.config.v1+json", data: bundlerData1},
		{mediaType: "application/vnd.example.manifest.v1+json", data: bundlerData2},
	})

	if len(layers) != 4 {
		t.Fatalf("expected 4 layers (2 orbital + 2 bundler), got %d", len(layers))
	}

	// First two are always orbital-native.
	for i := 0; i < 2; i++ {
		if !layers[i].IsOrbitalNative {
			t.Errorf("layers[%d].IsOrbitalNative = false, want true (orbital-native)", i)
		}
		if layers[i].SizeBytes == 0 {
			t.Errorf("layers[%d].SizeBytes = 0", i)
		}
		if layers[i].Digest == "" {
			t.Errorf("layers[%d].Digest is empty", i)
		}
	}

	// Last two are bundler-added.
	for i := 2; i < 4; i++ {
		if layers[i].IsOrbitalNative {
			t.Errorf("layers[%d].IsOrbitalNative = true, want false (bundler-added)", i)
		}
	}

	// Check correct media types.
	if layers[0].MediaType != mediaTypeDataGZ {
		t.Errorf("layers[0].MediaType = %q, want %q", layers[0].MediaType, mediaTypeDataGZ)
	}
	if layers[1].MediaType != mediaTypeSchemaGZ {
		t.Errorf("layers[1].MediaType = %q, want %q", layers[1].MediaType, mediaTypeSchemaGZ)
	}
	if layers[2].MediaType != "application/vnd.example.config.v1+json" {
		t.Errorf("layers[2].MediaType = %q, want bundler media type", layers[2].MediaType)
	}
}

// makeTestZip creates a temporary zip file with the given entries and returns its path.
func makeTestZip(t *testing.T, entries map[string][]byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create entry %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return path
}
