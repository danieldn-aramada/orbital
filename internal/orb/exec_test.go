package orb

import (
	"context"
	"strings"
	"testing"
)

// TestDockerExec_Hello is the Docker exec proof-of-concept.
// Requires dgraph-orb-alpha container to be running: make up
// Run with: go test -v -run TestDockerExec_Hello -tags integration ./internal/orb/
func TestDockerExec_Hello(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker exec PoC in short mode")
	}

	ctx := context.Background()
	out, err := dockerExec(ctx, "orbital-dgraph-orb-alpha-1", []string{"echo", "hello from dgraph-orb-alpha"})
	if err != nil {
		t.Fatalf("dockerExec: %v", err)
	}
	// Docker multiplexes stdout/stderr — output may have control bytes; check contains
	if !strings.Contains(out, "hello from dgraph-orb-alpha") {
		t.Errorf("unexpected output: %q", out)
	}
	t.Logf("exec output: %q", out)
}
