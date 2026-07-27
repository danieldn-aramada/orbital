package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

// orbId is declared on ConfigItem (the root interface), NOT on the
// KubernetesCluster sub-interface. DGraph's auto-generated KubernetesClusterFilter
// omits orbId, so `queryKubernetesCluster(filter:{orbId:{eq:}})` is rejected
// at schema-validation time. We query via the parent interface (queryConfigItem)
// where orbId IS filterable, and surface concrete cluster fields through
// fragments. Caller asserts __typename is a cluster type.
const getClusterQuery = `
  query GetKubernetesCluster($orbId: String!) {
    queryConfigItem(filter: { orbId: { eq: $orbId } }, first: 1) {
      __typename
      ... on ConfigItem {
        id orbId name
        namespace version
        createdBy createdAt updatedBy updatedAt
      }
      ... on KubernetesCluster {
        kubernetesVersion
        controlPlaneEndpoint { address }
        cni
        environment
        provider
        dataCenter { id orbId name }
        nodes {
          orbId
          name
          role
          server { id orbId hostname serviceTag }
        }
        backup {
          id orbId name namespace version
          createdBy createdAt updatedBy updatedAt
          etcd { id orbId name namespace version createdBy createdAt updatedBy updatedAt enabled schedule location retentionDays }
          velero { id orbId name namespace version createdBy createdAt updatedBy updatedAt enabled schedule location retentionDays }
          s3Sync { id orbId name namespace version createdBy createdAt updatedBy updatedAt enabled }
        }
      }
      ... on EksaKubernetesCluster {
        clusterType
        tinkerbellIP { address }
        managementCluster {
          orbId
          name
        }
        workloadClusters {
          orbId
          name
          environment
          kubernetesVersion
          nodesAggregate { count }
        }
      }
    }
  }`

type ClusterHandler struct {
	dev       bool
	dgraphURL string
	fragment  *template.Template
	logger    *slog.Logger
	basePath  string
	// actions resolves the per-request PageActions for this handler. orbital
	// passes a closure that reads `can_mutate` from the context (set by the
	// auth middleware); orb passes a const that returns layout.OrbActions.
	// This is the seam that lets one handler serve two apps with different
	// editability without duplicating the DGraph query + struct-builder.
	actions func(echo.Context) layout.PageActions
}

// NewClusterHandler builds a cluster Tab handler. `actions` is required:
// the same DGraph query + render path serves orbital (role-based actions)
// and orb (read-only OrbActions); the caller injects the policy.
func NewClusterHandler(dgraphURL string, dev bool, logger *slog.Logger, basePath string, actions func(echo.Context) layout.PageActions) *ClusterHandler {
	return &ClusterHandler{
		dgraphURL: dgraphURL,
		dev:       dev,
		fragment:  parseClusterFragment(),
		logger:    logger,
		basePath:  basePath,
		actions:   actions,
	}
}

func parseClusterFragment() *template.Template {
	return template.Must(template.ParseFiles(
		"web/templates/shared/partials/cluster-tab.gohtml",
		"web/templates/shared/partials/audit-tab.gohtml",
		"web/templates/shared/components/edit-modal-cluster.gohtml",
	))
}

type clusterQueryResponse struct {
	Typename             string `json:"__typename"`
	ID                   string `json:"id"`
	OrbID                string `json:"orbId"`
	Name                 string `json:"name"`
	Namespace            string `json:"namespace"`
	Version              int    `json:"version"`
	CreatedBy            string `json:"createdBy"`
	CreatedAt            string `json:"createdAt"`
	UpdatedBy            string `json:"updatedBy"`
	UpdatedAt            string `json:"updatedAt"`
	KubernetesVersion    string `json:"kubernetesVersion"`
	CNI                  string `json:"cni"`
	Environment          string `json:"environment"`
	Provider             string `json:"provider"`
	ControlPlaneEndpoint *struct {
		Address string `json:"address"`
	} `json:"controlPlaneEndpoint"`
	DataCenter struct {
		ID    string `json:"id"`
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"dataCenter"`
	Nodes []struct {
		OrbID  string `json:"orbId"`
		Name   string `json:"name"`
		Role   string `json:"role"`
		Server struct {
			ID         string `json:"id"`
			OrbID      string `json:"orbId"`
			Hostname   string `json:"hostname"`
			ServiceTag string `json:"serviceTag"`
		} `json:"server"`
	} `json:"nodes"`

	// EksaKubernetesCluster-specific fields. Nil/empty when the cluster is a
	// different concrete type.
	ClusterType  string `json:"clusterType,omitempty"`
	TinkerbellIP *struct {
		Address string `json:"address"`
	} `json:"tinkerbellIP,omitempty"`
	ManagementCluster *struct {
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"managementCluster,omitempty"`
	WorkloadClusters []struct {
		OrbID             string `json:"orbId"`
		Name              string `json:"name"`
		Environment       string `json:"environment"`
		KubernetesVersion string `json:"kubernetesVersion"`
		NodesAggregate    struct {
			Count int `json:"count"`
		} `json:"nodesAggregate"`
	} `json:"workloadClusters,omitempty"`

	Backup *struct {
		ID        string `json:"id"`
		OrbID     string `json:"orbId"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Version   int    `json:"version"`
		CreatedBy string `json:"createdBy"`
		CreatedAt string `json:"createdAt"`
		UpdatedBy string `json:"updatedBy"`
		UpdatedAt string `json:"updatedAt"`
		Etcd      *backupKindResponse   `json:"etcd"`
		Velero    *backupKindResponse   `json:"velero"`
		S3Sync    *backupS3SyncResponse `json:"s3Sync"`
	} `json:"backup,omitempty"`
}

// backupKindResponse mirrors EtcdBackup/VeleroBackup (identical shape).
type backupKindResponse struct {
	ID        string `json:"id"`
	OrbID     string `json:"orbId"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
	Enabled       bool   `json:"enabled"`
	Schedule      string `json:"schedule"`
	Location      string `json:"location"`
	RetentionDays *int   `json:"retentionDays"` // nullable — null = backend default (never 0)
}

type backupS3SyncResponse struct {
	ID        string `json:"id"`
	OrbID     string `json:"orbId"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
	Enabled   bool   `json:"enabled"`
}

// collectClusterRelatedOrbIDs returns the cluster's orbId followed by every
// nested ConfigItem orbId in the cluster's subgraph (nodes + backup tree).
// Empty / zero values are skipped so the result is ready for the
// data-related-orb-ids attribute. Order is stable: the cluster comes first,
// then nodes, then the backup wrapper, then each backup sub-kind. Mirrors the
// Server → collectRelatedOrbIDs pattern. See shared.js loadAuditPanel.
func collectClusterRelatedOrbIDs(raw *clusterQueryResponse) []string {
	out := make([]string, 0, 4+len(raw.Nodes))
	add := func(id string) {
		if id != "" {
			out = append(out, id)
		}
	}
	add(raw.OrbID)
	for _, n := range raw.Nodes {
		add(n.OrbID)
	}
	if raw.Backup != nil {
		add(raw.Backup.OrbID)
		if raw.Backup.Etcd != nil {
			add(raw.Backup.Etcd.OrbID)
		}
		if raw.Backup.Velero != nil {
			add(raw.Backup.Velero.OrbID)
		}
		if raw.Backup.S3Sync != nil {
			add(raw.Backup.S3Sync.OrbID)
		}
	}
	return out
}


type clusterNodeTabData struct {
	OrbID            string
	Name             string // the k8s node name (KubernetesNode.name), e.g. "dev-main-cp9-7"
	Role             string
	ServerOrbID      string
	ServerDomID      string
	ServerName       string // hostname → fallback to serviceTag
	ServerServiceTag string
}

// clusterEdgeRef is a lightweight link to another cluster — name for display,
// orbId for navigation. Used for both the workload→management single edge and
// the management→workloads list rows.
type clusterEdgeRef struct {
	OrbID string
	Name  string
}

type clusterWorkloadRef struct {
	OrbID             string
	Name              string
	Environment       string
	KubernetesVersion string
	NodeCount         int
}

// clusterBackupData is the cluster's ClusterBackup parent + its three nullable
// sub-kinds. When the cluster has no backup configured at all, the parent
// pointer (clusterTabData.Backup) is nil; the template renders an empty state.
// When the parent exists but a sub-kind hasn't been configured, that sub-kind
// pointer is nil and the table row shows "Not configured" + a Configure button.
type clusterBackupData struct {
	OrbID  string
	DomID  string
	Etcd   *backupKindTab
	Velero *backupKindTab
	S3Sync *backupS3SyncTab
}

type backupKindTab struct {
	OrbID         string
	DomID         string
	Enabled       bool
	Schedule      string
	Location      string
	RetentionDays *int // nullable — null renders as "default" (see cluster-tab.gohtml)
}

type backupS3SyncTab struct {
	OrbID   string
	DomID   string
	Enabled bool
}

type clusterTabData struct {
	ID                   string
	OrbID                string
	DomID                string
	Name                 string
	Typename             string // e.g. "EksaKubernetesCluster" — used internally for EKSA-specific field gating
	Provider             string // canonical short token from the schema field: "eksa", "maas", "metal3"
	Namespace            string
	Version              int
	CreatedBy            string
	CreatedAt            string
	UpdatedBy            string
	UpdatedAt            string
	KubernetesVersion    string
	CNI                  string
	Environment          string
	ControlPlaneEndpoint string
	DataCenterID         string
	DataCenterOrbID      string
	DataCenterDomID      string
	DataCenterName       string
	Nodes                []clusterNodeTabData

	// EKSA-specific (zero-valued when not EKSA).
	ClusterType  string
	TinkerbellIP string

	// Cross-cluster edges. ManagementCluster is non-nil only when this cluster
	// IS a workload. WorkloadClusters may be empty for management/standalone
	// clusters that don't (yet) own any children.
	ManagementCluster *clusterEdgeRef
	WorkloadClusters  []clusterWorkloadRef

	// Backup configuration. Nil when the cluster has no ClusterBackup yet —
	// template renders the empty state.
	Backup *clusterBackupData

	CurrentUser     string
	EditDataJSON    template.JS
	EditTargetsJSON template.JS // configitem-editor.js consumes this — see buildClusterEditTargets
	BasePath        string
	Actions         layout.PageActions

	// RelatedOrbIDsCSV is "<cluster-orbId>,<node-orbId>...,<backup-orbId>,<etcd-orbId>..."
	// — every ConfigItem in the rendered cluster subgraph. The Audit Log tab
	// reads this via data-related-orb-ids to pull events for the cluster AND
	// its owned children (nodes, backup tree) in one query. Same pattern as
	// Server → IdracSettings / StorageController / etc.
	RelatedOrbIDsCSV string
	// AuditPanelID matches data-panel on the audit <li> and the id of the
	// placeholder <div>. Consumed by the shared audit-tab partial.
	AuditPanelID string
}

func (h *ClusterHandler) Tab(c echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusFound, h.basePath+"/")
	}

	if h.dev {
		time.Sleep(150 * time.Millisecond)
	}

	orbID := c.Param("orbId")

	body, _ := json.Marshal(map[string]any{
		"query":     getClusterQuery,
		"variables": map[string]any{"orbId": orbID},
	})

	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dgraph query: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	h.logger.Debug("cluster query response", "body", string(rawBody))

	var result struct {
		Data struct {
			QueryConfigItem []clusterQueryResponse `json:"queryConfigItem"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(result.Data.QueryConfigItem) == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}
	raw := result.Data.QueryConfigItem[0]
	// Defend against the orbId belonging to a non-cluster ConfigItem (e.g. a
	// DC or Server with the same orbId — shouldn't happen given @id scope but
	// the polymorphic query returns whichever type matched).
	if raw.Typename != "EksaKubernetesCluster" {
		return echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	currentUser := actorFromContext(c)

	// Edit modal payload — universal fields always; EKSA-specific keys present
	// only when the concrete type is EKSA so the form renders the right block.
	editFields := map[string]any{
		"kubernetesVersion":    raw.KubernetesVersion,
		"controlPlaneEndpoint": "",
		"cni":                  raw.CNI,
		"environment":          raw.Environment,
	}
	if raw.ControlPlaneEndpoint != nil {
		editFields["controlPlaneEndpoint"] = raw.ControlPlaneEndpoint.Address
	}
	if raw.Typename == "EksaKubernetesCluster" {
		editFields["clusterType"] = raw.ClusterType
		if raw.TinkerbellIP != nil {
			editFields["tinkerbellIP"] = raw.TinkerbellIP.Address
		} else {
			editFields["tinkerbellIP"] = ""
		}
	}

	// Backup tree — owned children nested in the JSON editor, same shape as
	// Server → IdracSettings. Operator edits sub-kinds inline; a `null` key
	// means "remove this kind"; a missing key means "leave unchanged." The
	// orbital convention is: parent ConfigItem with owned children renders one
	// JSON editor whose tree is the full intent.
	backupEdit := map[string]any{}
	if raw.Backup != nil {
		if raw.Backup.Etcd != nil {
			backupEdit["etcd"] = map[string]any{
				"enabled":       raw.Backup.Etcd.Enabled,
				"schedule":      raw.Backup.Etcd.Schedule,
				"location":      raw.Backup.Etcd.Location,
				"retentionDays": raw.Backup.Etcd.RetentionDays,
			}
		}
		if raw.Backup.Velero != nil {
			backupEdit["velero"] = map[string]any{
				"enabled":       raw.Backup.Velero.Enabled,
				"schedule":      raw.Backup.Velero.Schedule,
				"location":      raw.Backup.Velero.Location,
				"retentionDays": raw.Backup.Velero.RetentionDays,
			}
		}
		if raw.Backup.S3Sync != nil {
			backupEdit["s3Sync"] = map[string]any{
				"enabled": raw.Backup.S3Sync.Enabled,
			}
		}
	}
	editFields["backup"] = backupEdit
	editJSON, _ := json.Marshal(editFields)

	// Build the targets list the configitem-editor JS module consumes — one
	// entry per editable entity in the JSON tree. Field lists, paths, and
	// owner-link metadata are derived from the configitems registry; adding a
	// new ConfigItem there auto-propagates here.
	targets := configitems.BuildEditTargets(raw.Typename, raw.OrbID, raw.Namespace, raw.Name)
	// Owned-child orbIds defaulted by BuildEditTargets follow the
	// `<namespace>:<name>-<suffix>` convention. Override with actual orbIds
	// when the parent's GraphQL response carries them (handles existing data
	// that may use legacy orbId formats).
	if raw.Backup != nil {
		if raw.Backup.Etcd != nil && raw.Backup.Etcd.OrbID != "" {
			targets = configitems.OverrideEditTargetOrbID(targets, "EtcdBackup", raw.Backup.Etcd.OrbID)
		}
		if raw.Backup.Velero != nil && raw.Backup.Velero.OrbID != "" {
			targets = configitems.OverrideEditTargetOrbID(targets, "VeleroBackup", raw.Backup.Velero.OrbID)
		}
		if raw.Backup.S3Sync != nil && raw.Backup.S3Sync.OrbID != "" {
			targets = configitems.OverrideEditTargetOrbID(targets, "S3Sync", raw.Backup.S3Sync.OrbID)
		}
	}
	targetsJSON, _ := json.Marshal(targets)

	tab := clusterTabData{
		ID:                raw.ID,
		OrbID:             raw.OrbID,
		DomID:             SafeDomID(raw.OrbID),
		Name:              raw.Name,
		Typename:          raw.Typename,
		Provider:          raw.Provider,
		Namespace:         raw.Namespace,
		Version:           raw.Version,
		CreatedBy:         raw.CreatedBy,
		CreatedAt:         raw.CreatedAt,
		UpdatedBy:         raw.UpdatedBy,
		UpdatedAt:         raw.UpdatedAt,
		KubernetesVersion: raw.KubernetesVersion,
		CNI:               raw.CNI,
		Environment:       raw.Environment,
		DataCenterID:      raw.DataCenter.ID,
		DataCenterOrbID:   raw.DataCenter.OrbID,
		DataCenterDomID:   SafeDomID(raw.DataCenter.OrbID),
		DataCenterName:    raw.DataCenter.Name,
		ClusterType:       raw.ClusterType,
		CurrentUser:       currentUser,
		EditDataJSON:      template.JS(editJSON),
		EditTargetsJSON:   template.JS(targetsJSON),
		BasePath:          h.basePath,
		Actions:           h.actions(c),
	}
	if raw.ControlPlaneEndpoint != nil {
		tab.ControlPlaneEndpoint = raw.ControlPlaneEndpoint.Address
	}
	if raw.TinkerbellIP != nil {
		tab.TinkerbellIP = raw.TinkerbellIP.Address
	}
	if raw.ManagementCluster != nil {
		tab.ManagementCluster = &clusterEdgeRef{
			OrbID: raw.ManagementCluster.OrbID,
			Name:  raw.ManagementCluster.Name,
		}
	}
	for _, w := range raw.WorkloadClusters {
		tab.WorkloadClusters = append(tab.WorkloadClusters, clusterWorkloadRef{
			OrbID:             w.OrbID,
			Name:              w.Name,
			Environment:       w.Environment,
			KubernetesVersion: w.KubernetesVersion,
			NodeCount:         w.NodesAggregate.Count,
		})
	}
	for _, n := range raw.Nodes {
		display := n.Server.Hostname
		if display == "" {
			display = n.Server.ServiceTag
		}
		tab.Nodes = append(tab.Nodes, clusterNodeTabData{
			OrbID:            n.OrbID,
			Name:             n.Name,
			Role:             n.Role,
			ServerOrbID:      n.Server.OrbID,
			ServerDomID:      SafeDomID(n.Server.OrbID),
			ServerName:       display,
			ServerServiceTag: n.Server.ServiceTag,
		})
	}

	if raw.Backup != nil {
		bd := &clusterBackupData{
			OrbID: raw.Backup.OrbID,
			DomID: SafeDomID(raw.Backup.OrbID),
		}
		if raw.Backup.Etcd != nil {
			bd.Etcd = &backupKindTab{
				OrbID:         raw.Backup.Etcd.OrbID,
				DomID:         SafeDomID(raw.Backup.Etcd.OrbID),
				Enabled:       raw.Backup.Etcd.Enabled,
				Schedule:      raw.Backup.Etcd.Schedule,
				Location:      raw.Backup.Etcd.Location,
				RetentionDays: raw.Backup.Etcd.RetentionDays,
			}
		}
		if raw.Backup.Velero != nil {
			bd.Velero = &backupKindTab{
				OrbID:         raw.Backup.Velero.OrbID,
				DomID:         SafeDomID(raw.Backup.Velero.OrbID),
				Enabled:       raw.Backup.Velero.Enabled,
				Schedule:      raw.Backup.Velero.Schedule,
				Location:      raw.Backup.Velero.Location,
				RetentionDays: raw.Backup.Velero.RetentionDays,
			}
		}
		if raw.Backup.S3Sync != nil {
			bd.S3Sync = &backupS3SyncTab{
				OrbID:   raw.Backup.S3Sync.OrbID,
				DomID:   SafeDomID(raw.Backup.S3Sync.OrbID),
				Enabled: raw.Backup.S3Sync.Enabled,
			}
		}
		tab.Backup = bd
	}
	tab.RelatedOrbIDsCSV = strings.Join(collectClusterRelatedOrbIDs(&raw), ",")
	tab.AuditPanelID = "cluster-panel-audit-" + tab.DomID

	tmpl := h.fragment
	if h.dev {
		tmpl = parseClusterFragment()
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return renderHTML(c, tmpl, "", tab)
}
