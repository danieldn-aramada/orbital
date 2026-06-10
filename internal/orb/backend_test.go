package orb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var _ DGraphBackend = (*SubprocessBackend)(nil)

// TestSubprocessBackend_RunLive_CommandNotFound verifies an error is returned and
// wrapped when dgraph is not on PATH.
func TestSubprocessBackend_RunLive_CommandNotFound(t *testing.T) {
	b := &SubprocessBackend{AlphaGRPC: "localhost:9080", ZeroGRPC: "localhost:5080"}
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	_, err := b.RunLive(context.Background(), "/tmp/nonexistent.json.gz")
	if err == nil {
		t.Fatal("expected error when dgraph not on PATH, got nil")
	}
	if !strings.Contains(err.Error(), "dgraph live") {
		t.Errorf("expected error to mention dgraph live, got: %v", err)
	}
}

// TestSubprocessBackend_RunLive_CapturesOutput verifies that stderr/stdout from
// a failing subprocess is included in the returned output string.
func TestSubprocessBackend_RunLive_CapturesOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dgraph")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'dgraph-output-marker'; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin))

	b := &SubprocessBackend{AlphaGRPC: "localhost:9080", ZeroGRPC: "localhost:5080"}
	out, err := b.RunLive(context.Background(), "/tmp/data.json.gz")
	if err == nil {
		t.Fatal("expected error from failing subprocess, got nil")
	}
	if !strings.Contains(out, "dgraph-output-marker") {
		t.Errorf("expected output to contain marker string, got: %q", out)
	}
}
