package handler

import (
	"github.com/armada/orbital/internal/configitems"
)

// Two ways to get a view list in a test, for the two kinds of test.
//
// Unit tests take `fixtureViewSet`, a hand-written slice — no services, and the
// shape is visible beside the assertion that depends on it.
//
// Integration tests take `liveViewSet`, which resolves the SHIPPED
// config/views.yaml against the cluster's deployed schema. That is deliberately
// the real file rather than a fixture: a test asserting review scope or audit
// roll-up is asserting something about what orbital actually ships, and a
// fixture that drifted from it would pass while production was wrong.

// fixtureViewSet is a miniature of config/views.yaml — the subgraphs the
// handler unit tests reach for, and nothing else.
func fixtureViewSet() configitems.ViewSet {
	m := func(field, typeName string, isList, editable bool) configitems.ViewTab {
		return configitems.ViewTab{Field: field, Type: typeName, IsList: isList, Editable: editable}
	}
	clusterSubgraph := []configitems.ViewTab{
		m("backup", "ClusterBackup", false, false),
		m("backup.etcd", "EtcdBackup", false, true),
		m("backup.velero", "VeleroBackup", false, true),
		m("backup.s3Sync", "S3Sync", false, true),
		m("nodes", "KubernetesNode", true, false),
	}
	clusterRelations := map[string]string{"backup": "ClusterBackup", "nodes": "KubernetesNode", "dataCenter": "DataCenter"}
	return configitems.ViewSet{
		{Type: "Server", Slug: "servers", Relations: map[string]string{
			"dataCenter": "DataCenter", "idracSettings": "IdracSettings",
			"serverMaintenance": "ServerMaintenance", "storageControllers": "StorageController",
		}, Subgraph: []configitems.ViewTab{
			m("idracSettings", "IdracSettings", false, true),
			m("serverMaintenance", "ServerMaintenance", false, true),
			m("storageControllers", "StorageController", true, false),
			m("storageControllers.storageDevices", "StorageDevice", true, false),
		}},
		{Type: "DataCenter", Slug: "data-centers", Relations: map[string]string{
			"servers": "Server", "racks": "Rack",
		}, Subgraph: []configitems.ViewTab{
			m("servers", "Server", true, false),
			m("racks", "Rack", true, false),
		}},
		{Type: "StorageController", Relations: map[string]string{"storageDevices": "StorageDevice", "server": "Server"}},
		{Type: "IdracSettings"},
		{Type: "ServerMaintenance"},
		{Type: "StorageDevice", Relations: map[string]string{"storageController": "StorageController"}},
		{Type: "Rack"},
		// The interface view, so Implements() can answer — it is what gives a
		// concrete EksaKubernetesCluster its page's subgraph, and what stops a
		// cluster's back-reference column being kept.
		{Type: "KubernetesCluster", Slug: "clusters", IsInterface: true,
			Implementations: []string{"EksaKubernetesCluster"},
			Relations:       clusterRelations, Subgraph: clusterSubgraph},
		{Type: "EksaKubernetesCluster", Relations: clusterRelations},
		{Type: "ClusterBackup", Relations: map[string]string{"etcd": "EtcdBackup", "velero": "VeleroBackup", "s3Sync": "S3Sync"}},
		{Type: "KubernetesNode"},
		{Type: "EtcdBackup"},
		{Type: "VeleroBackup"},
		{Type: "S3Sync"},
	}
}
