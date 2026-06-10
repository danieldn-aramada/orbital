package orb

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// DGraphBackend abstracts how orb stages data and runs dgraph live.
// DockerBackend is used for local dev (binary + Docker); SubprocessBackend for production K8s.
type DGraphBackend interface {
	RunLive(ctx context.Context, dataPath string) (string, error)
}

// DockerBackend uses docker cp + docker exec to load data into the DGraph alpha container.
// dataPath is the host-side path to data.json.gz; it is copied into the container before exec.
type DockerBackend struct {
	ContainerName string
}

func (b *DockerBackend) RunLive(ctx context.Context, dataPath string) (string, error) {
	if _, err := dockerExec(ctx, b.ContainerName, []string{"mkdir", "-p", "/tmp/orb-import"}); err != nil {
		return "", fmt.Errorf("mkdir /tmp/orb-import: %w", err)
	}
	if err := dockerCopy(ctx, b.ContainerName, dataPath, "/tmp/orb-import/"); err != nil {
		return "", fmt.Errorf("docker cp: %w", err)
	}
	cmd := []string{
		"dgraph", "live",
		"-f", "/tmp/orb-import/" + scratchFile,
		"-a", "localhost:9080",
		"-z", "localhost:5080",
	}
	return dockerExec(ctx, b.ContainerName, cmd)
}

// SubprocessBackend runs dgraph live as a subprocess inside the orb process.
// Requires the dgraph binary to be on PATH (e.g. included in orb's container image).
// Used in production K8s — no idle dgraph-live pod or shared PVC needed.
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
