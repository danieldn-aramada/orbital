package configitems

import (
	"strings"
	"testing"
)

func TestBuildEditTargets_Cluster(t *testing.T) {
	got := BuildEditTargets(fixtureViews(), fixtureFields, fixtureMeta, "EksaKubernetesCluster", "colo:dev-main", "colo", "dev-main", nil)
	if len(got) != 4 {
		t.Fatalf("BuildEditTargets returned %d targets, want 4 (root + 3 backup kinds)", len(got))
	}

	checks := []struct {
		path               []string
		kind               string
		orbID              string
		parentInverseField string
		wrapperKind        string
	}{
		{path: []string{}, kind: "EksaKubernetesCluster", orbID: "colo:dev-main"},
		{path: []string{"backup", "etcd"}, kind: "EtcdBackup", orbID: "colo:dev-main-etcd-backup", parentInverseField: "clusterBackupEtcd", wrapperKind: "ClusterBackup"},
		{path: []string{"backup", "velero"}, kind: "VeleroBackup", orbID: "colo:dev-main-velero-backup", parentInverseField: "clusterBackupVelero", wrapperKind: "ClusterBackup"},
		{path: []string{"backup", "s3Sync"}, kind: "S3Sync", orbID: "colo:dev-main-s3sync", parentInverseField: "clusterBackupS3Sync", wrapperKind: "ClusterBackup"},
	}
	for i, want := range checks {
		g := got[i]
		if strings.Join(g.Path, ".") != strings.Join(want.path, ".") {
			t.Errorf("target[%d].Path = %v, want %v", i, g.Path, want.path)
		}
		if g.Kind != want.kind {
			t.Errorf("target[%d].Kind = %q, want %q", i, g.Kind, want.kind)
		}
		if g.OrbID != want.orbID {
			t.Errorf("target[%d].OrbID = %q, want %q", i, g.OrbID, want.orbID)
		}
		if g.ParentInverseField != want.parentInverseField {
			t.Errorf("target[%d].ParentInverseField = %q, want %q", i, g.ParentInverseField, want.parentInverseField)
		}
		if want.wrapperKind != "" && (g.ParentWrapper == nil || g.ParentWrapper.Kind != want.wrapperKind) {
			t.Errorf("target[%d].ParentWrapper.Kind = %v, want %q", i, g.ParentWrapper, want.wrapperKind)
		}
	}
}

// TestBuildEditTargets_Server asserts Server gets its IdracSettings as a
// direct (non-wrapper) child. The path is a single segment ["idracSettings"]
// and the orbId follows the `<ns>:<name>-idrac` convention.
func TestBuildEditTargets_Server(t *testing.T) {
	got := BuildEditTargets(fixtureViews(), fixtureFields, fixtureMeta, "Server", "colo:5L4P7Y3", "colo", "5L4P7Y3", nil)
	// Server has multiple children registered (IdracSettings,
	// ServerConfigurationProfile, StorageController). All show up; the
	// FormFields-empty ones still produce targets but with no editable fields.
	var idrac *EditTarget
	for i := range got {
		if got[i].Kind == "IdracSettings" {
			idrac = &got[i]
			break
		}
	}
	if idrac == nil {
		t.Fatalf("IdracSettings target missing; got types: %v", kindList(got))
	}
	if strings.Join(idrac.Path, ".") != "idracSettings" {
		t.Errorf("IdracSettings path = %v, want [idracSettings]", idrac.Path)
	}
	if idrac.OrbID != "colo:5L4P7Y3-idrac" {
		t.Errorf("IdracSettings OrbID = %q, want %q", idrac.OrbID, "colo:5L4P7Y3-idrac")
	}
	if idrac.ParentInverseField != "server" {
		t.Errorf("IdracSettings ParentInverseField = %q, want %q", idrac.ParentInverseField, "server")
	}
	// IdracSettings is a direct child, so no wrapper.
	if idrac.ParentWrapper != nil {
		t.Errorf("IdracSettings should have no ParentWrapper (direct child); got %+v", idrac.ParentWrapper)
	}
}

func kindList(targets []EditTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Kind)
	}
	return out
}
func fixtureFields(typeName string) []string {
	m := map[string][]string{
		"ClusterBackup":              nil,
		"DataCenter":                 {"name", "assetDataV2", "model"},
		"EksaKubernetesCluster":      {"kubernetesVersion", "cni", "environment", "clusterType"},
		"EtcdBackup":                 {"enabled", "schedule", "location", "retentionDays"},
		"IPAddress":                  nil,
		"IdracSettings":              {"firmwareVersion", "sshEnabled", "ipmiEnabled", "lockdownModeEnabled", "osToIdracPassThroughEnabled", "usbManagementPortEnabled", "dhcpEnabled", "racadmEnabled"},
		"KubernetesNode":             nil,
		"NetworkAdapter":             nil,
		"NetworkDevice":              {"manufacturer", "model", "serial", "role", "macAddress", "rackPosition", "platform", "face", "vcPosition", "vcPriority"},
		"NetworkInterface":           nil,
		"Rack":                       {"name", "uHeight"},
		"S3Sync":                     {"enabled"},
		"Server":                     {"hostname", "manufacturer", "model", "oobMAC", "rackPosition", "uHeight", "serviceTag", "serialNumber"},
		"ServerConfigurationProfile": nil,
		"ServerMaintenance":          {"enabled", "windowStart", "windowEnd", "reason"},
		"StorageController":          nil,
		"StorageDevice":              nil,
		"StorageVolume":              nil,
		"VeleroBackup":               {"enabled", "schedule", "location", "retentionDays"},
	}
	return m[typeName]
}
func fixtureMeta(typeName string) TypeInfo {
	suffix := map[string]string{
		"IdracSettings": "idrac",
		"EtcdBackup":    "etcd-backup",
		"VeleroBackup":  "velero-backup",
		"S3Sync":        "s3sync",
		"ClusterBackup": "backup",
	}[typeName]
	if suffix == "" {
		suffix = strings.ToLower(typeName)
	}
	// No DerivesIDFrom: the create edge comes from CONTAINMENT now, because for
	// a multi-parent type it depends on which parent you create from.
	info := TypeInfo{OrbIDSuffix: suffix}
	if typeName == "DataCenter" {
		info.Fields = []DerivedField{{Name: "assetDataV2", Editable: true, Doc: "jsonString"}}
	}
	return info
}

// fixtureViews stands in for the resolved views in tests that assert edit-tree
// STRUCTURE. Only the subgraph matters here, so the fields, columns and labels
// are left out — a view's shape, not its content, is what BuildEditTargets reads.
//
// It mirrors config/views.yaml for the two roots these tests exercise. The live
// file is asserted against the deployed schema separately; this exists so the
// structural assertions need no services.
func fixtureViews() ViewSet {
	member := func(field, typeName, parentEdge string, isList, editable bool) ViewTab {
		return ViewTab{Field: field, Type: typeName, ParentEdge: parentEdge, IsList: isList, Editable: editable}
	}
	return ViewSet{
		{Type: "Server", Slug: "servers", Relations: map[string]string{
			"idracSettings": "IdracSettings", "serverMaintenance": "ServerMaintenance", "kubernetesNode": "KubernetesNode",
			"storageControllers": "StorageController", "networkAdapters": "NetworkAdapter",
		}, Subgraph: []ViewTab{
			member("idracSettings", "IdracSettings", "server", false, true),
			member("serverMaintenance", "ServerMaintenance", "server", false, true),
			member("kubernetesNode", "KubernetesNode", "server", false, false),
			member("storageControllers", "StorageController", "server", true, false),
			member("networkAdapters", "NetworkAdapter", "server", true, false),
			member("storageControllers.storageDevices", "StorageDevice", "storageController", true, false),
		}},
		{Type: "EksaKubernetesCluster", Slug: "eksa-kubernetes-clusters", Relations: map[string]string{
			"nodes": "KubernetesNode", "backup": "ClusterBackup",
		}, Subgraph: []ViewTab{
			member("backup", "ClusterBackup", "cluster", false, false),
			member("backup.etcd", "EtcdBackup", "clusterBackupEtcd", false, true),
			member("backup.velero", "VeleroBackup", "clusterBackupVelero", false, true),
			member("backup.s3Sync", "S3Sync", "clusterBackupS3Sync", false, true),
			member("nodes", "KubernetesNode", "cluster", true, false),
		}},
	}
}

// fixtureTypeNames lists the types the editable-field snapshot covers, so
// callers can iterate it without reading the deployed schema.
func fixtureTypeNames() []string {
	return []string{
		"ClusterBackup", "DataCenter", "EksaKubernetesCluster", "EtcdBackup", "IPAddress",
		"IdracSettings", "KubernetesNode", "NetworkAdapter", "NetworkDevice", "NetworkInterface",
		"Rack", "S3Sync", "Server", "ServerConfigurationProfile", "ServerMaintenance",
		"StorageController", "StorageDevice", "StorageVolume", "VeleroBackup",
	}
}
