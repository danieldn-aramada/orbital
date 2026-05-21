package orb

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// dockerExec runs a command inside a named container and returns its combined stdout+stderr.
// The container must be running. Used to exec `dgraph live` inside dgraph-orb-alpha.
func dockerExec(ctx context.Context, containerName string, cmd []string) (string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	execResp, err := cli.ContainerExecCreate(ctx, containerName, dockercontainer.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}

	attach, err := cli.ContainerExecAttach(ctx, execResp.ID, dockercontainer.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, attach.Reader); err != nil {
		return "", fmt.Errorf("read exec output: %w", err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return buf.String(), fmt.Errorf("command exited %d: %s", inspect.ExitCode, buf.String())
	}

	return buf.String(), nil
}

// dockerCopy copies a file from the host into a running container at destDir.
// Used to stage data.json.gz into dgraph-orb-alpha before running dgraph live,
// so orb works correctly when running outside Docker (no shared volume needed).
func dockerCopy(ctx context.Context, containerName, srcPath, destDir string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read src file: %w", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: filepath.Base(srcPath),
		Mode: 0o644,
		Size: int64(len(data)),
	}); err != nil {
		return fmt.Errorf("tar header: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("tar write: %w", err)
	}
	tw.Close()

	return cli.CopyToContainer(ctx, containerName, destDir, &buf, dockercontainer.CopyToContainerOptions{})
}
