package orb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSubprocessBackend_Interface verifies compile-time interface compliance.
var _ DGraphBackend = (*DockerBackend)(nil)
var _ DGraphBackend = (*SubprocessBackend)(nil)

// TestSubprocessBackend_RunLive_CommandNotFound verifies an error is returned and
// wrapped when dgraph is not on PATH.
func TestSubprocessBackend_RunLive_CommandNotFound(t *testing.T) {
	b := &SubprocessBackend{AlphaGRPC: "localhost:9080", ZeroGRPC: "localhost:5080"}
	// Point PATH to an empty dir so dgraph is not found.
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
	// Write a tiny shell script that prints a known string and exits 1.
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

// TestDockerBackend_RunLive is an integration test that requires make up.
// Run with: go test -v -run TestDockerBackend_RunLive -tags integration ./internal/orb/
func TestDockerBackend_RunLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker backend integration test in short mode")
	}

	b := &DockerBackend{ContainerName: "local-dgraph-orb-alpha-1"}

	// Verify exec works by checking dgraph version instead of running a full live import.
	out, err := dockerExec(context.Background(), b.ContainerName, []string{"dgraph", "version"})
	if err != nil {
		t.Fatalf("docker exec: %v", err)
	}
	if !strings.Contains(out, "Dgraph") && !strings.Contains(out, "dgraph") {
		t.Errorf("unexpected dgraph version output: %q", out)
	}
	t.Logf("dgraph version output: %q", out)
}
