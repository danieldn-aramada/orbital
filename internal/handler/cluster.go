package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"time"

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
}

func NewClusterHandler(dgraphURL string, dev bool, logger *slog.Logger, basePath string) *ClusterHandler {
	return &ClusterHandler{
		dgraphURL: dgraphURL,
		dev:       dev,
		fragment:  parseClusterFragment(),
		logger:    logger,
		basePath:  basePath,
	}
}

func parseClusterFragment() *template.Template {
	return template.Must(template.ParseFiles(
		"web/templates/shared/partials/cluster-tab.gohtml",
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

	CurrentUser  string
	EditDataJSON template.JS
	BasePath     string
	Actions      layout.PageActions
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
	canMutate, _ := c.Get("can_mutate").(bool)

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
	editJSON, _ := json.Marshal(editFields)

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
		BasePath:          h.basePath,
		Actions:           layout.OrbitalActions(canMutate),
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

	tmpl := h.fragment
	if h.dev {
		tmpl = parseClusterFragment()
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.Execute(c.Response(), tab)
}
