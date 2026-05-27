package orb

import (
	"bytes"
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const dgraphLivePodSelector = "app.kubernetes.io/name=dgraph-live"

// DGraphBackend abstracts how orb stages data and runs dgraph live.
// DockerBackend is used for local dev (binary + Docker); K8sBackend for production K8s.
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

// K8sBackend finds an idle dgraph-live pod and execs dgraph live inside it.
// dataPath must be on a PVC shared between the orb pod and the dgraph-live pod —
// set ORB_DATA_DIR to the shared PVC mount path so the same path is visible on both sides.
type K8sBackend struct {
	Namespace string
	AlphaGRPC string
	ZeroGRPC  string
	k8sClient kubernetes.Interface
	restCfg   *rest.Config
}

// NewK8sBackend builds a K8sBackend using in-cluster config.
// namespace may be empty, in which case it is read from the pod's service account namespace file.
func NewK8sBackend(namespace, alphaGRPC, zeroGRPC string) (*K8sBackend, error) {
	if namespace == "" {
		data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if err != nil {
			return nil, fmt.Errorf("namespace not set and cannot read service account namespace: %w", err)
		}
		namespace = string(data)
	}
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("k8s client: %w", err)
	}
	return &K8sBackend{
		Namespace: namespace,
		AlphaGRPC: alphaGRPC,
		ZeroGRPC:  zeroGRPC,
		k8sClient: k8sClient,
		restCfg:   restCfg,
	}, nil
}

func (b *K8sBackend) RunLive(ctx context.Context, dataPath string) (string, error) {
	podName, err := b.findLivePod(ctx)
	if err != nil {
		return "", err
	}
	cmd := fmt.Sprintf("dgraph live -f %s -a %s -z %s", dataPath, b.AlphaGRPC, b.ZeroGRPC)
	return b.execInPod(ctx, podName, cmd)
}

// findLivePod returns the name of the first running dgraph-live pod in the namespace.
func (b *K8sBackend) findLivePod(ctx context.Context) (string, error) {
	pods, err := b.k8sClient.CoreV1().Pods(b.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: dgraphLivePodSelector,
	})
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}
	return "", fmt.Errorf("no running dgraph-live pod found in namespace %q (selector: %s)", b.Namespace, dgraphLivePodSelector)
}

func (b *K8sBackend) execInPod(ctx context.Context, podName, cmd string) (string, error) {
	req := b.k8sClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(b.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"/bin/sh", "-c", cmd},
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(b.restCfg, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		output := stdout.String()
		if stderr.Len() > 0 {
			output += "\nstderr: " + stderr.String()
		}
		return output, err
	}
	return stdout.String(), nil
}
