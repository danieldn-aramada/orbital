package configitems

import "testing"

// Acceptance item 1: the slug derives from the TYPE NAME, never from stored
// orbIds. The two legacy types are the reason — IPAddress ids are
// `<ns>:<address>` and Rack ids are `<ns>:<rackName>`, with no kind token, so
// id-derivation would fail on exactly them.
func TestDerivedSlug_EveryConfigItemType(t *testing.T) {
	cases := map[string]string{
		"Server":                     "servers",
		"DataCenter":                 "data-centers",
		"Rack":                       "racks",
		"EksaKubernetesCluster":      "eksa-kubernetes-clusters",
		"KubernetesNode":             "kubernetes-nodes",
		"NetworkDevice":              "network-devices",
		"NetworkAdapter":             "network-adapters",
		"NetworkInterface":           "network-interfaces",
		"StorageController":          "storage-controllers",
		"StorageDevice":              "storage-devices",
		"StorageVolume":              "storage-volumes",
		"ClusterBackup":              "cluster-backups",
		"EtcdBackup":                 "etcd-backups",
		"VeleroBackup":               "velero-backups",
		"ServerConfigurationProfile": "server-configuration-profiles",
		"ServerMaintenance":          "server-maintenances",

		// The three that break a naive implementation:
		"IPAddress":     "ip-addresses",   // acronym prefix, and -ss takes -es
		"S3Sync":        "s3-syncs",       // digit inside the name
		"IdracSettings": "idrac-settings", // ALREADY plural — must not become -settingss
	}
	for typeName, want := range cases {
		if got := DerivedSlug(typeName); got != want {
			t.Errorf("DerivedSlug(%q) = %q, want %q", typeName, got, want)
		}
	}
}

func TestKebabTypeName_AcronymsAndDigits(t *testing.T) {
	cases := map[string]string{
		"IPAddress":             "ip-address",
		"S3Sync":                "s3-sync",
		"EksaKubernetesCluster": "eksa-kubernetes-cluster",
		"Server":                "server",
		"DataCenter":            "data-center",
	}
	for in, want := range cases {
		if got := KebabTypeName(in); got != want {
			t.Errorf("KebabTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}
