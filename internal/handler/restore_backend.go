package handler

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// RestoreBackend executes the dgraph live command in the target environment.
// dataDir is the local path containing data.json.gz and schema.gz.
type RestoreBackend interface {
	RunLive(ctx context.Context, dataDir, alphaGRPC, zeroGRPC string) (string, error)
}

// SubprocessRestoreBackend runs dgraph live as a subprocess. The dgraph binary
// must be present in PATH — it is included in the orbital Docker image via a
// multi-stage build (copied from dgraph/dgraph:v25.3.1).
type SubprocessRestoreBackend struct{}

func NewSubprocessRestoreBackend() *SubprocessRestoreBackend {
	return &SubprocessRestoreBackend{}
}

func (s *SubprocessRestoreBackend) RunLive(ctx context.Context, dataDir, alphaGRPC, zeroGRPC string) (string, error) {
	cmd := exec.CommandContext(ctx, "dgraph", "live",
		"--files", dataDir+"/data.json.gz",
		"--schema", dataDir+"/schema.gz",
		"--alpha", alphaGRPC,
		"--zero", zeroGRPC,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("dgraph live: %w", err)
	}
	return out.String(), nil
}
