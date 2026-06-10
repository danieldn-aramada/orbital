package orb

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// DGraphBackend abstracts how orb runs dgraph live to apply imported data.
type DGraphBackend interface {
	RunLive(ctx context.Context, dataPath string) (string, error)
}

// SubprocessBackend runs dgraph live as a subprocess inside the orb process.
// Requires the dgraph binary to be on PATH — included in orb's container image
// via a multi-stage build copying from dgraph/dgraph:v25.3.1.
type SubprocessBackend struct {
	AlphaGRPC string
	ZeroGRPC  string
}

func (b *SubprocessBackend) RunLive(ctx context.Context, dataPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "dgraph", "live",
		"-f", dataPath,
		"-a", b.AlphaGRPC,
		"-z", b.ZeroGRPC,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("dgraph live: %w", err)
	}
	return out.String(), nil
}
