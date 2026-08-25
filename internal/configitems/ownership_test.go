package configitems

import (
	"slices"
	"testing"
)

// OwnerEdgePredicates is the single source graphdiff.ownerEdges derives from.
// This pins the full set so an accidental registry edit that changes the diff
// rollup is caught here.
func TestOwnerEdgePredicates_Set(t *testing.T) {
	want := []string{
		"IdracSettings.server",
		"ServerConfigurationProfile.server",
		"ServerMaintenance.server",
		"StorageController.server",
		"StorageDevice.storageController",
		"StorageVolume.storageController",
		"NetworkAdapter.server",
		"NetworkInterface.networkAdapter",
		"NetworkInterface.server",
		"NetworkInterface.networkDevice",
		"KubernetesNode.cluster",
		"ClusterBackup.cluster",
		"EtcdBackup.clusterBackupEtcd",
		"VeleroBackup.clusterBackupVelero",
		"S3Sync.clusterBackupS3Sync",
		"IPAddress.serverOobIP",
		"IPAddress.kubernetesNodeIpv4",
		"IPAddress.kubernetesClusterControlPlaneEndpoint",
		"IPAddress.eksaKubernetesClusterTinkerbellIP",
	}
	got := OwnerEdgePredicates()
	if len(got) != len(want) {
		t.Fatalf("edge count = %d, want %d\n got=%v", len(got), len(want), got)
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing owner edge %q", w)
		}
	}
}

// Precedence is load-bearing: for multi-parent types the first present edge on a
// node wins, so within-type order (most-specific first) must be preserved.
func TestOwnerEdgePredicates_Precedence(t *testing.T) {
	got := OwnerEdgePredicates()
	idx := func(s string) int { return slices.Index(got, s) }

	// NIC: adapter before server before device.
	if !(idx("NetworkInterface.networkAdapter") < idx("NetworkInterface.server") &&
		idx("NetworkInterface.server") < idx("NetworkInterface.networkDevice")) {
		t.Errorf("NetworkInterface owner-edge precedence wrong: %v", got)
	}
	// IPAddress: server (most common) before k8s node/cluster/eksa.
	if i := idx("IPAddress.serverOobIP"); i < 0 || i > idx("IPAddress.kubernetesNodeIpv4") {
		t.Errorf("IPAddress precedence: serverOobIP should come first, got %v", got)
	}
}

// OwnedChildren is the single source the audit collector derives from. These
// cases pin the drift bugs Spike 33 fixes: StorageDevice→StorageVolume,
// NetworkDevice→NetworkInterface, and Server's full child set.
func TestOwnedChildren_DriftFixes(t *testing.T) {
	has := func(parent, childType, childField string) bool {
		for _, c := range OwnedChildren(parent) {
			if c.ChildType == childType && c.ChildField == childField {
				return true
			}
		}
		return false
	}

	// The two drift bugs: these previously did not roll up in the audit tabs.
	if !has("StorageController", "StorageDevice", "storageDevices") {
		t.Error("StorageController should own StorageDevice via storageDevices")
	}
	if !has("StorageDevice", "StorageVolume", "storageVolumes") {
		t.Error("StorageDevice should own StorageVolume via storageVolumes (audit rollup drift)")
	}
	if !has("NetworkDevice", "NetworkInterface", "networkInterfaces") {
		t.Error("NetworkDevice should own switch NetworkInterfaces (audit rollup drift)")
	}

	// Server's full downward child set (incl. the oobIP address + NICs).
	for _, want := range []struct{ ct, cf string }{
		{"IdracSettings", "idracSettings"},
		{"ServerMaintenance", "serverMaintenance"},
		{"ServerConfigurationProfile", "serverConfigurationProfile"},
		{"StorageController", "storageControllers"},
		{"NetworkAdapter", "networkAdapters"},
		{"IPAddress", "oobIP"},
	} {
		if !has("Server", want.ct, want.cf) {
			t.Errorf("Server should own %s via %s", want.ct, want.cf)
		}
	}

	// Interface ownership: a concrete cluster gets the interface's children.
	if !has("EksaKubernetesCluster", "KubernetesNode", "nodes") {
		t.Error("EksaKubernetesCluster should own KubernetesNode via the KubernetesCluster interface")
	}
	if !has("ClusterBackup", "EtcdBackup", "etcd") {
		t.Error("ClusterBackup should own EtcdBackup via etcd")
	}
}
