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

// fixtureViewSet is a miniature of config/views.yaml — the members the handler
// unit tests reach for, and nothing else.
func fixtureViewSet() configitems.ViewSet {
	m := func(field, typeName string, isList, editable bool) configitems.ViewTab {
		return configitems.ViewTab{Field: field, Type: typeName, IsList: isList, Editable: editable}
	}
	// Contains is CONTAINMENT — derived from the schema in production, spelled
	// out here. Tabs is what the page shows. They are different questions now:
	// a page may show something it does not contain, and contains things no page
	// shows.
	c := func(field, typeName string, isList bool) configitems.EditableMember {
		return configitems.EditableMember{ChildType: typeName, ChildField: field, IsList: isList, ParentEdge: "server"}
	}
	return configitems.ViewSet{
		{Type: "Server", Slug: "servers", Contains: []configitems.EditableMember{
			c("idracSettings", "IdracSettings", false),
			c("serverMaintenance", "ServerMaintenance", false),
			c("storageControllers", "StorageController", true),
		}, Tabs: []configitems.ViewTab{
			m("dataCenter", "DataCenter", false, false),
			m("idracSettings", "IdracSettings", false, true),
			m("serverMaintenance", "ServerMaintenance", false, true),
			m("storageControllers", "StorageController", true, true),
		}},
		{Type: "DataCenter", Slug: "data-centers", Contains: []configitems.EditableMember{
			c("servers", "Server", true), c("racks", "Rack", true),
		}, Tabs: []configitems.ViewTab{
			m("servers", "Server", true, false),
			m("racks", "Rack", true, true),
		}},
		{Type: "StorageController", Slug: "storage-controllers", Contains: []configitems.EditableMember{
			c("storageDevices", "StorageDevice", true),
		}, Tabs: []configitems.ViewTab{
			m("storageDevices", "StorageDevice", true, true),
		}},
		{Type: "IdracSettings", Slug: "idrac-settings"},
		{Type: "ServerMaintenance", Slug: "server-maintenances"},
		{Type: "StorageDevice", Slug: "storage-devices"},
		{Type: "Rack", Slug: "racks"},
		// The interface view, so Implements() can answer — it is what tells a
		// concrete EksaKubernetesCluster it is deletable AS a KubernetesCluster,
		// and what stops a cluster's back-reference column being kept.
		{Type: "KubernetesCluster", Slug: "clusters", IsInterface: true,
			Implementations: []string{"EksaKubernetesCluster"},
			Contains: []configitems.EditableMember{
				c("nodes", "KubernetesNode", true), c("backup", "ClusterBackup", false),
			}, Tabs: []configitems.ViewTab{
				m("nodes", "KubernetesNode", true, true),
				m("backup", "ClusterBackup", false, true),
			}},
		{Type: "EksaKubernetesCluster", Slug: "eksa-kubernetes-clusters", Contains: []configitems.EditableMember{
			c("nodes", "KubernetesNode", true), c("backup", "ClusterBackup", false),
		}, Tabs: []configitems.ViewTab{
			m("nodes", "KubernetesNode", true, true),
			m("backup", "ClusterBackup", false, true),
		}},
		{Type: "ClusterBackup", Slug: "cluster-backups", Contains: []configitems.EditableMember{
			c("etcd", "EtcdBackup", false), c("velero", "VeleroBackup", false), c("s3Sync", "S3Sync", false),
		}, Tabs: []configitems.ViewTab{
			m("etcd", "EtcdBackup", false, true),
			m("velero", "VeleroBackup", false, true),
			m("s3Sync", "S3Sync", false, true),
		}},
		{Type: "KubernetesNode", Slug: "kubernetes-nodes"},
		{Type: "EtcdBackup", Slug: "etcd-backups"},
		{Type: "VeleroBackup", Slug: "velero-backups"},
		{Type: "S3Sync", Slug: "s3-syncs"},
	}
}
