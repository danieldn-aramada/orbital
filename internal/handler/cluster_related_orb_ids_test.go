package handler

import (
	"strings"
	"testing"
)

// Pins the parent-audit-aggregation convention for clusters: the cluster's
// orbId comes first, followed by every nested ConfigItem orbId present on the
// cluster's GraphQL response (nodes + backup tree). Empty / zero values must
// be skipped so a "" never lands in the audit endpoint's orbId filter.
//
// Regression class this catches: silently dropping an owned-child orbId when
// a new sub-kind is added to ClusterBackup (or any other owned ConfigItem
// added to the cluster subgraph). See docs/reference/AUDIT.md "Every parent
// ConfigItem with owned children" rule.
func TestCollectClusterRelatedOrbIDs(t *testing.T) {
	t.Run("full subgraph", func(t *testing.T) {
		raw := &clusterQueryResponse{}
		raw.OrbID = "colo:dev-main"
		raw.Nodes = append(raw.Nodes, struct {
			OrbID  string `json:"orbId"`
			Name   string `json:"name"`
			Role   string `json:"role"`
			Server struct {
				ID         string `json:"id"`
				OrbID      string `json:"orbId"`
				Hostname   string `json:"hostname"`
				ServiceTag string `json:"serviceTag"`
			} `json:"server"`
		}{OrbID: "colo:dev-main-cp9-7"})
		raw.Nodes = append(raw.Nodes, struct {
			OrbID  string `json:"orbId"`
			Name   string `json:"name"`
			Role   string `json:"role"`
			Server struct {
				ID         string `json:"id"`
				OrbID      string `json:"orbId"`
				Hostname   string `json:"hostname"`
				ServiceTag string `json:"serviceTag"`
			} `json:"server"`
		}{OrbID: "colo:dev-main-wk9-3"})

		raw.Backup = &struct {
			ID        string                `json:"id"`
			OrbID     string                `json:"orbId"`
			Name      string                `json:"name"`
			Namespace string                `json:"namespace"`
			Version   int                   `json:"version"`
			CreatedBy string                `json:"createdBy"`
			CreatedAt string                `json:"createdAt"`
			UpdatedBy string                `json:"updatedBy"`
			UpdatedAt string                `json:"updatedAt"`
			Etcd      *backupKindResponse   `json:"etcd"`
			Velero    *backupKindResponse   `json:"velero"`
			S3Sync    *backupS3SyncResponse `json:"s3Sync"`
		}{
			OrbID:  "colo:dev-main-backup",
			Etcd:   &backupKindResponse{OrbID: "colo:dev-main-etcd-backup"},
			Velero: &backupKindResponse{OrbID: "colo:dev-main-velero-backup"},
			S3Sync: &backupS3SyncResponse{OrbID: "colo:dev-main-s3sync"},
		}

		got := collectClusterRelatedOrbIDs(raw)
		want := []string{
			"colo:dev-main",
			"colo:dev-main-cp9-7",
			"colo:dev-main-wk9-3",
			"colo:dev-main-backup",
			"colo:dev-main-etcd-backup",
			"colo:dev-main-velero-backup",
			"colo:dev-main-s3sync",
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("ordered orbId list mismatch\n  got:  %v\n  want: %v", got, want)
		}
	})

	t.Run("partial backup (only s3sync configured)", func(t *testing.T) {
		raw := &clusterQueryResponse{}
		raw.OrbID = "colo:dev-main"
		raw.Backup = &struct {
			ID        string                `json:"id"`
			OrbID     string                `json:"orbId"`
			Name      string                `json:"name"`
			Namespace string                `json:"namespace"`
			Version   int                   `json:"version"`
			CreatedBy string                `json:"createdBy"`
			CreatedAt string                `json:"createdAt"`
			UpdatedBy string                `json:"updatedBy"`
			UpdatedAt string                `json:"updatedAt"`
			Etcd      *backupKindResponse   `json:"etcd"`
			Velero    *backupKindResponse   `json:"velero"`
			S3Sync    *backupS3SyncResponse `json:"s3Sync"`
		}{
			OrbID:  "colo:dev-main-backup",
			S3Sync: &backupS3SyncResponse{OrbID: "colo:dev-main-s3sync"},
		}
		got := collectClusterRelatedOrbIDs(raw)
		want := []string{"colo:dev-main", "colo:dev-main-backup", "colo:dev-main-s3sync"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("ordered orbId list mismatch\n  got:  %v\n  want: %v", got, want)
		}
	})

	t.Run("no backup configured", func(t *testing.T) {
		raw := &clusterQueryResponse{}
		raw.OrbID = "colo:dev-main"
		got := collectClusterRelatedOrbIDs(raw)
		want := []string{"colo:dev-main"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("ordered orbId list mismatch\n  got:  %v\n  want: %v", got, want)
		}
	})

	t.Run("zero-value orbIds skipped", func(t *testing.T) {
		raw := &clusterQueryResponse{}
		raw.OrbID = "colo:dev-main"
		raw.Backup = &struct {
			ID        string                `json:"id"`
			OrbID     string                `json:"orbId"`
			Name      string                `json:"name"`
			Namespace string                `json:"namespace"`
			Version   int                   `json:"version"`
			CreatedBy string                `json:"createdBy"`
			CreatedAt string                `json:"createdAt"`
			UpdatedBy string                `json:"updatedBy"`
			UpdatedAt string                `json:"updatedAt"`
			Etcd      *backupKindResponse   `json:"etcd"`
			Velero    *backupKindResponse   `json:"velero"`
			S3Sync    *backupS3SyncResponse `json:"s3Sync"`
		}{
			OrbID:  "colo:dev-main-backup",
			Etcd:   &backupKindResponse{OrbID: ""}, // sub-kind with empty orbId
			S3Sync: &backupS3SyncResponse{OrbID: "colo:dev-main-s3sync"},
		}
		got := collectClusterRelatedOrbIDs(raw)
		for _, id := range got {
			if id == "" {
				t.Fatalf("empty orbId leaked into related list: %v", got)
			}
		}
	})
}
