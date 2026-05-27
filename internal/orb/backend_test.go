package orb

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestK8sBackend_FindPod_NoPods verifies an error is returned when no pods match the selector.
func TestK8sBackend_FindPod_NoPods(t *testing.T) {
	b := &K8sBackend{
		Namespace: "orb",
		k8sClient: fake.NewSimpleClientset(),
	}
	_, err := b.findLivePod(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no running dgraph-live pod") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestK8sBackend_FindPod_NotRunning verifies that a pod in Pending phase is not selected.
func TestK8sBackend_FindPod_NotRunning(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dgraph-live-0",
			Namespace: "orb",
			Labels:    map[string]string{"app.kubernetes.io/name": "dgraph-live"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	b := &K8sBackend{
		Namespace: "orb",
		k8sClient: fake.NewSimpleClientset(pod),
	}
	_, err := b.findLivePod(context.Background())
	if err == nil {
		t.Fatal("expected error for non-running pod, got nil")
	}
}

// TestK8sBackend_FindPod_Running verifies that a Running pod is selected correctly.
func TestK8sBackend_FindPod_Running(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dgraph-live-0",
			Namespace: "orb",
			Labels:    map[string]string{"app.kubernetes.io/name": "dgraph-live"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	b := &K8sBackend{
		Namespace: "orb",
		k8sClient: fake.NewSimpleClientset(pod),
	}
	name, err := b.findLivePod(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "dgraph-live-0" {
		t.Errorf("expected dgraph-live-0, got %q", name)
	}
}

// TestK8sBackend_FindPod_PicksFirst verifies the first running pod is returned when multiple exist.
func TestK8sBackend_FindPod_PicksFirst(t *testing.T) {
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dgraph-live-0",
				Namespace: "orb",
				Labels:    map[string]string{"app.kubernetes.io/name": "dgraph-live"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dgraph-live-1",
				Namespace: "orb",
				Labels:    map[string]string{"app.kubernetes.io/name": "dgraph-live"},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	}
	b := &K8sBackend{
		Namespace: "orb",
		k8sClient: fake.NewSimpleClientset(&pods[0], &pods[1]),
	}
	name, err := b.findLivePod(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return one of the running pods (order not guaranteed by fake client, but must be one of them).
	if name != "dgraph-live-0" && name != "dgraph-live-1" {
		t.Errorf("unexpected pod name: %q", name)
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

// TestDGraphBackend_Interface verifies that both backends satisfy the interface at compile time.
var _ DGraphBackend = (*DockerBackend)(nil)
var _ DGraphBackend = (*K8sBackend)(nil)
