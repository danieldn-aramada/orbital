package handler

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/api/types/container"
)

// RestoreBackend executes the dgraph live command in the target environment.
// dataDir is the path to the directory containing data.json.gz and schema.gz
// as seen from inside the execution environment (container or pod).
type RestoreBackend interface {
	RunLive(ctx context.Context, dataDir, alphaGRPC, zeroGRPC string) (string, error)
}

// ── Docker backend ────────────────────────────────────────────────────────────

// DockerRestoreBackend runs dgraph live via docker exec into the blue alpha
// container. Used for local development.
type DockerRestoreBackend struct {
	containerName string
}

func NewDockerRestoreBackend(containerName string) *DockerRestoreBackend {
	return &DockerRestoreBackend{containerName: containerName}
}

func (d *DockerRestoreBackend) RunLive(ctx context.Context, dataDir, alphaGRPC, zeroGRPC string) (string, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	cmd := []string{
		"dgraph", "live",
		"--files", dataDir + "/data.json.gz",
		"--schema", dataDir + "/schema.gz",
		"--alpha", alphaGRPC,
		"--zero", zeroGRPC,
	}

	exec, err := cli.ContainerExecCreate(ctx, d.containerName, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}

	resp, err := cli.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Reader.Read(buf)
		if n > 0 {
			// Docker multiplexes stdout/stderr with an 8-byte header; strip it.
			raw := buf[:n]
			if len(raw) > 8 {
				raw = raw[8:]
			}
			out.Write(raw)
		}
		if err != nil {
			break
		}
	}

	inspect, err := cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return out.String(), fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return out.String(), fmt.Errorf("dgraph live exited with code %d", inspect.ExitCode)
	}
	return out.String(), nil
}

// ── K8s backend ───────────────────────────────────────────────────────────────

// K8sRestoreBackend runs dgraph live via kubectl exec into an idle dgraph-live
// pod. Used for Kubernetes deployments.
type K8sRestoreBackend struct {
	k8sClient kubernetes.Interface
	restCfg   *rest.Config
	namespace string
}

func NewK8sRestoreBackend(k8sClient kubernetes.Interface, restCfg *rest.Config, namespace string) *K8sRestoreBackend {
	return &K8sRestoreBackend{
		k8sClient: k8sClient,
		restCfg:   restCfg,
		namespace: namespace,
	}
}

func (k *K8sRestoreBackend) RunLive(ctx context.Context, dataDir, alphaGRPC, zeroGRPC string) (string, error) {
	podName, err := k.findDgraphLivePod(ctx)
	if err != nil {
		return "", fmt.Errorf("find dgraph-live pod: %w", err)
	}

	cmd := fmt.Sprintf(
		"dgraph live --files %s/data.json.gz --schema %s/schema.gz --alpha %s --zero %s",
		dataDir, dataDir, alphaGRPC, zeroGRPC,
	)
	return k.execInPod(ctx, podName, cmd)
}

func (k *K8sRestoreBackend) findDgraphLivePod(ctx context.Context) (string, error) {
	pods, err := k.k8sClient.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: dgraphLiveLabelSelector,
	})
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}
	return "", fmt.Errorf("no running dgraph-live pod found in namespace %q (selector: %s)", k.namespace, dgraphLiveLabelSelector)
}

func (k *K8sRestoreBackend) execInPod(ctx context.Context, podName, cmd string) (string, error) {
	req := k.k8sClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(k.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"/bin/sh", "-c", cmd},
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(k.restCfg, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\nstderr: " + stderr.String()
	}
	return output, err
}
