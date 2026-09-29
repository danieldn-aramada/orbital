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

// A slug is a contract, so the override lives in the SCHEMA (version-controlled,
// reviewed, a visible breaking act) and never in a runtime preference store.
func TestSlugFor_AnnotationOverridesDerivation(t *testing.T) {
	if got := SlugFor("EksaKubernetesCluster", "slug: clusters"); got != "clusters" {
		t.Errorf("annotation should win, got %q", got)
	}
	if got := SlugFor("EksaKubernetesCluster", "Human prose.\nslug: clusters"); got != "clusters" {
		t.Errorf("annotation on its own line among prose should win, got %q", got)
	}
	if got := SlugFor("EksaKubernetesCluster", ""); got != "eksa-kubernetes-clusters" {
		t.Errorf("no annotation should derive, got %q", got)
	}
	if got := SlugFor("Server", "this type has no slug: directive as such"); got != "servers" {
		t.Errorf("prose mentioning the word must not be read as an annotation, got %q", got)
	}
	if got := SlugFor("Server", "slug:"); got != "servers" {
		t.Errorf("an EMPTY annotation must fall back to derivation, not yield an empty slug, got %q", got)
	}
}
