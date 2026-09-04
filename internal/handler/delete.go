package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/armada/orbital/ent"
	"github.com/labstack/echo/v4"
)

const maxDeleteListItems = 5

type DeleteGroup struct {
	Label string   `json:"label"`
	Items []string `json:"items,omitempty"` // named items, truncated to maxDeleteListItems
	Extra int      `json:"extra,omitempty"` // count beyond Items
	Count int      `json:"count,omitempty"` // for groups with no named items
}

type DeletePreview struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	TotalCount int    `json:"totalCount"`
	// Version is the root entity's OCC counter as the preview read it. The
	// modal echoes it back on confirm as ?ifVersion=, so a delete is refused if
	// the entity moved while the confirmation dialog sat open — which is
	// precisely the window a confirmation dialog creates.
	Version   int           `json:"version,omitempty"`
	Groups    []DeleteGroup `json:"groups"`
	Preserved []DeleteGroup `json:"preserved,omitempty"`
}

type DeleteHandler struct {
	dgraphURL     string
	dgraphDQLBase string // dgraphURL with /graphql stripped
	db            *ent.Client
	logger        *slog.Logger
	// gql is here for ONE reason: the approval gate. This endpoint writes via
	// DQL, so it cannot reuse writeToDGraph's chokepoint and has to ask the
	// policy question directly. See guardDelete.
	gql         *GraphQL
	previewTmpl *template.Template
}

func NewDeleteHandler(dgraphURL string, db *ent.Client, logger *slog.Logger, gql *GraphQL) *DeleteHandler {
	return &DeleteHandler{
		dgraphURL:     dgraphURL,
		dgraphDQLBase: strings.TrimSuffix(dgraphURL, "/graphql"),
		db:            db,
		logger:        logger,
		gql:           gql,
		previewTmpl:   parseDeletePreviewTmpl(),
	}
}

func parseDeletePreviewTmpl() *template.Template {
	return template.Must(template.ParseFiles("web/templates/orbital/partials/config-item-delete-preview.gohtml"))
}

// Preview returns an HTML fragment describing the impact of the delete without modifying anything.
func (h *DeleteHandler) Preview(c echo.Context) error {
	id := c.QueryParam("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id required")
	}
	ctx := c.Request().Context()
	var preview DeletePreview
	switch c.QueryParam("type") {
	case "DataCenter":
		plan, err := h.planDCDelete(ctx, id)
		if err != nil {
			return err
		}
		preview = plan.preview
	case "Server":
		plan, err := h.planServerDelete(ctx, id)
		if err != nil {
			return err
		}
		preview = plan.preview
	case "KubernetesCluster":
		plan, err := h.planClusterDelete(ctx, id)
		if err != nil {
			return err
		}
		preview = plan.preview
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported type")
	}
	tmpl := h.previewTmpl
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return renderHTML(c, tmpl, "", preview)
}

// Execute performs the cascade delete for the given config item.
//
// This REST endpoint exists specifically to back the UI's cascade-delete flow:
// a DataCenter or Server delete must (a) gather a single before-state and
// audit record, (b) remove the node together with its dependent children in
// one transaction, and (c) pair with `GET /config-items/delete-preview` for
// the impact summary. None of that fits a single auto-generated GraphQL
// mutation cleanly.
//
// Single-entity, non-cascading mutations (CRUD on individual ConfigItems) go
// through GraphQL at `/graphql`. This endpoint is not a general-purpose REST
// CRUD surface — see ADR 002.
//
// @Summary     Cascade-delete a config item (UI flow)
// @Description Deletes a DataCenter or Server together with its dependent
// @Description children. Bound to the UI delete modal's confirm action.
// @Description Single-entity (non-cascading) deletes go through GraphQL.
// @Tags        config-items
// @Produce     json
// @Param       type path string true "Config item type" Enums(DataCenter, Server)
// @Param       id   path string true "DGraph node id"
// @Success     200 {object} map[string]int "{ \"deleted\": N }"
// @Failure     400 {object} errorResponse
// @Failure     500 {object} errorResponse
// @Router      /api/v1/config-items/{type}/{id} [delete]
func (h *DeleteHandler) Execute(c echo.Context) error {
	// Path-param decoding is handled by middleware.DecodePathParams.
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id required")
	}
	ctx := c.Request().Context()
	actor := actorFromContext(c)
	typeName := c.Param("type")
	caller := resolveCallerRole(c, h.db)

	// Before planning: a caller holding a stale view should be told to reload,
	// not have a cascade computed on their behalf.
	if err := h.checkDeleteVersion(ctx, typeName, id, c.QueryParam("ifVersion")); err != nil {
		return h.refuse(c, err)
	}

	switch typeName {
	case "DataCenter":
		plan, err := h.planDCDelete(ctx, id)
		if err != nil {
			return err
		}
		if err := h.guardDelete(ctx, caller, plan.orbID, plan.uids); err != nil {
			return h.refuse(c, err)
		}
		if err := h.bulkDelete(ctx, plan.uids); err != nil {
			h.logger.Error("dc delete failed", "orbId", plan.orbID, "err", err)
			return fmt.Errorf("delete data center: %w", err)
		}
		writeAuditEvent(h.db, h.logger, "data", actor, "deleteDataCenter",
			[]string{"deleteDataCenter"}, []string{"DataCenter"}, []string{plan.orbID},
			map[string]any{
				"input":  map[string]any{"orbId": plan.orbID},
				"before": plan.before,
				"result": map[string]any{"totalDeleted": len(plan.uids), "breakdown": plan.preview.Groups},
			},
		)
		return c.JSON(http.StatusOK, map[string]any{"deleted": len(plan.uids)})

	case "Server":
		plan, err := h.planServerDelete(ctx, id)
		if err != nil {
			return err
		}
		if err := h.guardDelete(ctx, caller, plan.orbID, plan.uids); err != nil {
			return h.refuse(c, err)
		}
		if err := h.bulkDelete(ctx, plan.uids); err != nil {
			h.logger.Error("server delete failed", "orbId", plan.orbID, "err", err)
			return fmt.Errorf("delete server: %w", err)
		}
		writeAuditEvent(h.db, h.logger, "data", actor, "deleteServer",
			[]string{"deleteServer"}, []string{"Server"}, []string{plan.orbID},
			map[string]any{
				"input":  map[string]any{"orbId": plan.orbID},
				"before": plan.before,
				"result": map[string]any{"totalDeleted": len(plan.uids), "breakdown": plan.preview.Groups},
			},
		)
		return c.JSON(http.StatusOK, map[string]any{"deleted": len(plan.uids)})

	case "KubernetesCluster":
		plan, err := h.planClusterDelete(ctx, id)
		if err != nil {
			return err
		}
		if err := h.guardDelete(ctx, caller, plan.orbID, plan.uids); err != nil {
			return h.refuse(c, err)
		}
		if err := h.bulkDelete(ctx, plan.uids); err != nil {
			h.logger.Error("cluster delete failed", "orbId", plan.orbID, "err", err)
			return fmt.Errorf("delete cluster: %w", err)
		}
		writeAuditEvent(h.db, h.logger, "data", actor, "deleteKubernetesCluster",
			[]string{"deleteKubernetesCluster"}, []string{"KubernetesCluster"}, []string{plan.orbID},
			map[string]any{
				"input":  map[string]any{"orbId": plan.orbID},
				"before": plan.before,
				"result": map[string]any{"totalDeleted": len(plan.uids), "breakdown": plan.preview.Groups},
			},
		)
		return c.JSON(http.StatusOK, map[string]any{"deleted": len(plan.uids)})

	default:
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported type")
	}
}

// ── plan types ────────────────────────────────────────────────────────────────

type dcDeletePlan struct {
	preview DeletePreview
	uids    []string
	orbID   string
	name    string
	before  map[string]any
}

type serverDeletePlan struct {
	preview DeletePreview
	uids    []string
	orbID   string
	name    string
	before  map[string]any
}

// ── DataCenter ────────────────────────────────────────────────────────────────

const dcDeleteGQL = `
  query GetDCForDelete($orbId: String!) {
    getDataCenter(orbId: $orbId) {
      id name orbId namespace version
      racks { id name }
      servers {
        id name hostname
        idracSettings { id }
        serverConfigurationProfile { id }
        storageControllers {
          id
          storageDevices {
            id
            storageVolumes { id }
          }
        }
        oobIP { id address }
      }
      kubernetesClusters {
        __typename
        controlPlaneEndpoint { id }
        nodes { id }
        ... on ConfigItem { id name }
        ... on EksaKubernetesCluster {
          tinkerbellIP { id }
        }
      }
    }
  }`

type dcDeleteRaw struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OrbID     string `json:"orbId"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	Racks     []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"racks"`
	Servers []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Hostname      string `json:"hostname"`
		IdracSettings *struct {
			ID string `json:"id"`
		} `json:"idracSettings"`
		ServerConfigurationProfile *struct {
			ID string `json:"id"`
		} `json:"serverConfigurationProfile"`
		StorageControllers []struct {
			ID             string `json:"id"`
			StorageDevices []struct {
				ID             string `json:"id"`
				StorageVolumes []struct {
					ID string `json:"id"`
				} `json:"storageVolumes"`
			} `json:"storageDevices"`
		} `json:"storageControllers"`
		OobIP *struct {
			ID      string `json:"id"`
			Address string `json:"address"`
		} `json:"oobIP"`
	} `json:"servers"`
	KubernetesClusters []struct {
		Typename             string `json:"__typename"`
		ID                   string `json:"id"`
		Name                 string `json:"name"`
		ControlPlaneEndpoint *struct {
			ID string `json:"id"`
		} `json:"controlPlaneEndpoint"`
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
		TinkerbellIP *struct {
			ID string `json:"id"`
		} `json:"tinkerbellIP,omitempty"`
	} `json:"kubernetesClusters"`
}

func (h *DeleteHandler) planDCDelete(ctx context.Context, orbID string) (*dcDeletePlan, error) {
	data, err := h.gqlQuery(ctx, dcDeleteGQL, map[string]any{"orbId": orbID})
	if err != nil {
		return nil, err
	}
	var resp struct {
		GetDataCenter dcDeleteRaw `json:"getDataCenter"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode dc: %w", err)
	}
	dc := resp.GetDataCenter
	if dc.ID == "" {
		return nil, echo.NewHTTPError(http.StatusNotFound, "data center not found")
	}

	var uids []string
	var groups []DeleteGroup

	uids = append(uids, dc.ID)

	// Racks.
	rackCount := len(dc.Racks)
	for _, r := range dc.Racks {
		uids = append(uids, r.ID)
	}

	// Servers and owned children.
	var idracCount, scpCount, ctrlCount, devCount, volCount int
	serverCount := len(dc.Servers)
	for _, s := range dc.Servers {
		uids = append(uids, s.ID)
		if s.IdracSettings != nil && s.IdracSettings.ID != "" {
			idracCount++
			uids = append(uids, s.IdracSettings.ID)
		}
		if s.ServerConfigurationProfile != nil && s.ServerConfigurationProfile.ID != "" {
			scpCount++
			uids = append(uids, s.ServerConfigurationProfile.ID)
		}
		for _, ctrl := range s.StorageControllers {
			ctrlCount++
			uids = append(uids, ctrl.ID)
			for _, dev := range ctrl.StorageDevices {
				devCount++
				uids = append(uids, dev.ID)
				for _, vol := range dev.StorageVolumes {
					volCount++
					uids = append(uids, vol.ID)
				}
			}
		}
		if s.OobIP != nil && s.OobIP.ID != "" {
			uids = append(uids, s.OobIP.ID)
		}
	}
	if rackCount > 0 {
		groups = append(groups, countGroup("Racks", rackCount))
	}
	if serverCount > 0 {
		groups = append(groups, countGroup("Servers", serverCount))
	}
	if idracCount > 0 {
		groups = append(groups, countGroup("iDRAC Settings", idracCount))
	}
	if scpCount > 0 {
		groups = append(groups, countGroup("Server Config Profiles", scpCount))
	}
	if ctrlCount > 0 {
		groups = append(groups, countGroup("Storage Controllers", ctrlCount))
	}
	if devCount > 0 {
		groups = append(groups, countGroup("Storage Devices", devCount))
	}
	if volCount > 0 {
		groups = append(groups, countGroup("Storage Volumes", volCount))
	}

	// Kubernetes clusters, their nodes, and provider-owned IP addresses.
	k8sCount := len(dc.KubernetesClusters)
	var k8sNodeCount int
	for _, kc := range dc.KubernetesClusters {
		uids = append(uids, kc.ID)
		if kc.ControlPlaneEndpoint != nil && kc.ControlPlaneEndpoint.ID != "" {
			uids = append(uids, kc.ControlPlaneEndpoint.ID)
		}
		for _, n := range kc.Nodes {
			if n.ID != "" {
				k8sNodeCount++
				uids = append(uids, n.ID)
			}
		}
		if kc.TinkerbellIP != nil && kc.TinkerbellIP.ID != "" {
			uids = append(uids, kc.TinkerbellIP.ID)
		}
	}
	if k8sCount > 0 {
		groups = append(groups, countGroup("Kubernetes Clusters", k8sCount))
	}
	if k8sNodeCount > 0 {
		groups = append(groups, countGroup("Kubernetes Nodes", k8sNodeCount))
	}

	return &dcDeletePlan{
		preview: DeletePreview{
			Name:       dc.Name,
			Type:       "DataCenter",
			TotalCount: len(uids),
			Version:    dc.Version,
			Groups:     groups,
		},
		uids:  uids,
		orbID: dc.OrbID,
		name:  dc.Name,
		before: map[string]any{
			"name":            dc.Name,
			"orbId":           dc.OrbID,
			"namespace":       dc.Namespace,
			"rackCount":       len(dc.Racks),
			"serverCount":     len(dc.Servers),
			"k8sClusterCount": len(dc.KubernetesClusters),
		},
	}, nil
}

// ── Server ────────────────────────────────────────────────────────────────────

const srvDeleteGQL = `
  query GetServerForDelete($orbId: String!) {
    getServer(orbId: $orbId) {
      id name orbId hostname version
      idracSettings { id }
      serverConfigurationProfile { id }
      storageControllers {
        id name
        storageDevices {
          id
          storageVolumes { id }
        }
      }
      oobIP { id address }
    }
  }`

type srvDeleteRaw struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	OrbID         string `json:"orbId"`
	Hostname      string `json:"hostname"`
	Version       int    `json:"version"`
	IdracSettings *struct {
		ID string `json:"id"`
	} `json:"idracSettings"`
	ServerConfigurationProfile *struct {
		ID string `json:"id"`
	} `json:"serverConfigurationProfile"`
	StorageControllers []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		StorageDevices []struct {
			ID             string `json:"id"`
			StorageVolumes []struct {
				ID string `json:"id"`
			} `json:"storageVolumes"`
		} `json:"storageDevices"`
	} `json:"storageControllers"`
	OobIP *struct {
		ID      string `json:"id"`
		Address string `json:"address"`
	} `json:"oobIP"`
}

func (h *DeleteHandler) planServerDelete(ctx context.Context, orbID string) (*serverDeletePlan, error) {
	data, err := h.gqlQuery(ctx, srvDeleteGQL, map[string]any{"orbId": orbID})
	if err != nil {
		return nil, err
	}
	var resp struct {
		GetServer srvDeleteRaw `json:"getServer"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode server: %w", err)
	}
	s := resp.GetServer
	if s.ID == "" {
		return nil, echo.NewHTTPError(http.StatusNotFound, "server not found")
	}

	var uids []string
	var groups []DeleteGroup
	var preserved []DeleteGroup

	uids = append(uids, s.ID)

	if s.IdracSettings != nil && s.IdracSettings.ID != "" {
		uids = append(uids, s.IdracSettings.ID)
		groups = append(groups, countGroup("iDRAC Settings", 1))
	}
	if s.ServerConfigurationProfile != nil && s.ServerConfigurationProfile.ID != "" {
		uids = append(uids, s.ServerConfigurationProfile.ID)
		groups = append(groups, countGroup("Server Config Profile", 1))
	}

	var ctrlCount, devCount, volCount int
	for _, ctrl := range s.StorageControllers {
		ctrlCount++
		uids = append(uids, ctrl.ID)
		for _, dev := range ctrl.StorageDevices {
			devCount++
			uids = append(uids, dev.ID)
			for _, vol := range dev.StorageVolumes {
				volCount++
				uids = append(uids, vol.ID)
			}
		}
	}
	if ctrlCount > 0 {
		groups = append(groups, countGroup("Storage Controllers", ctrlCount))
	}
	if devCount > 0 {
		groups = append(groups, countGroup("Storage Devices", devCount))
	}
	if volCount > 0 {
		groups = append(groups, countGroup("Storage Volumes", volCount))
	}

	// IP address is preserved — not deleted.
	if s.OobIP != nil && s.OobIP.ID != "" {
		preserved = append(preserved, namedGroup("IP Address", []string{s.OobIP.Address}))
	}

	srvBefore := map[string]any{
		"name":               serverDisplayName(s.Hostname, s.Name),
		"orbId":              s.OrbID,
		"hostname":           s.Hostname,
		"storageControllers": ctrlCount,
		"storageDevices":     devCount,
		"storageVolumes":     volCount,
	}
	if s.OobIP != nil && s.OobIP.Address != "" {
		srvBefore["oobIP"] = s.OobIP.Address
	}

	return &serverDeletePlan{
		preview: DeletePreview{
			Name:       serverDisplayName(s.Hostname, s.Name),
			Type:       "Server",
			TotalCount: len(uids),
			Version:    s.Version,
			Groups:     groups,
			Preserved:  preserved,
		},
		uids:   uids,
		orbID:  s.OrbID,
		name:   serverDisplayName(s.Hostname, s.Name),
		before: srvBefore,
	}, nil
}

// ── Kubernetes Cluster ───────────────────────────────────────────────────────

// Cascade scope (settled): cluster + its nodes + control plane endpoint IP +
// (EKSA) tinkerbell IP. Servers are preserved — they're independent inventory,
// not owned by the cluster. The lookup goes through queryConfigItem because
// orbId lives on ConfigItem, not on the KubernetesCluster sub-interface.
const clusterDeleteGQL = `
  query GetClusterForDelete($orbId: String!) {
    queryConfigItem(filter: { orbId: { eq: $orbId } }, first: 1) {
      __typename
      ... on ConfigItem {
        id orbId name namespace version
      }
      ... on KubernetesCluster {
        controlPlaneEndpoint { id address }
        nodes {
          orbId role
          server { id orbId hostname serviceTag }
        }
        backup {
          id
          etcd { id }
          velero { id }
          s3Sync { id }
        }
      }
      ... on EksaKubernetesCluster {
        tinkerbellIP { id address }
      }
    }
  }`

type clusterDeleteRaw struct {
	Typename             string `json:"__typename"`
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	OrbID                string `json:"orbId"`
	Namespace            string `json:"namespace"`
	Version              int    `json:"version"`
	ControlPlaneEndpoint *struct {
		ID      string `json:"id"`
		Address string `json:"address"`
	} `json:"controlPlaneEndpoint"`
	Nodes []struct {
		OrbID  string `json:"orbId"`
		Role   string `json:"role"`
		Server struct {
			ID         string `json:"id"`
			OrbID      string `json:"orbId"`
			Hostname   string `json:"hostname"`
			ServiceTag string `json:"serviceTag"`
		} `json:"server"`
	} `json:"nodes"`
	TinkerbellIP *struct {
		ID      string `json:"id"`
		Address string `json:"address"`
	} `json:"tinkerbellIP,omitempty"`
	Backup *struct {
		ID   string `json:"id"`
		Etcd *struct {
			ID string `json:"id"`
		} `json:"etcd"`
		Velero *struct {
			ID string `json:"id"`
		} `json:"velero"`
		S3Sync *struct {
			ID string `json:"id"`
		} `json:"s3Sync"`
	} `json:"backup,omitempty"`
}

type clusterDeletePlan struct {
	preview DeletePreview
	uids    []string
	orbID   string
	name    string
	before  map[string]any
}

func nodeUIDFromOrbID(ctx context.Context, h *DeleteHandler, orbID string) (string, error) {
	// queryKubernetesNode → @id is orbId. Fetch the DGraph UID for one node.
	data, err := h.gqlQuery(ctx, `query($orbId: String!) {
		getKubernetesNode(orbId: $orbId) { id }
	}`, map[string]any{"orbId": orbID})
	if err != nil {
		return "", err
	}
	var resp struct {
		GetKubernetesNode *struct {
			ID string `json:"id"`
		} `json:"getKubernetesNode"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", err
	}
	if resp.GetKubernetesNode == nil {
		return "", nil
	}
	return resp.GetKubernetesNode.ID, nil
}

func (h *DeleteHandler) planClusterDelete(ctx context.Context, orbID string) (*clusterDeletePlan, error) {
	data, err := h.gqlQuery(ctx, clusterDeleteGQL, map[string]any{"orbId": orbID})
	if err != nil {
		return nil, err
	}
	var resp struct {
		QueryConfigItem []clusterDeleteRaw `json:"queryConfigItem"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode cluster: %w", err)
	}
	if len(resp.QueryConfigItem) == 0 {
		return nil, echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}
	c := resp.QueryConfigItem[0]
	if c.Typename != "EksaKubernetesCluster" {
		return nil, echo.NewHTTPError(http.StatusNotFound, "not a kubernetes cluster")
	}
	if c.ID == "" {
		return nil, echo.NewHTTPError(http.StatusNotFound, "cluster not found")
	}

	var uids []string
	var groups []DeleteGroup
	var preserved []DeleteGroup

	uids = append(uids, c.ID)

	// Nodes — owned by cluster, deleted. Each node has @id orbId; resolve to
	// DGraph UIDs in one round-trip per node (small N).
	nodeUIDs := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		uid, err := nodeUIDFromOrbID(ctx, h, n.OrbID)
		if err != nil {
			return nil, fmt.Errorf("resolve node uid: %w", err)
		}
		if uid != "" {
			nodeUIDs = append(nodeUIDs, uid)
		}
	}
	if len(nodeUIDs) > 0 {
		uids = append(uids, nodeUIDs...)
		groups = append(groups, countGroup("Kubernetes Nodes", len(nodeUIDs)))
	}

	if c.ControlPlaneEndpoint != nil && c.ControlPlaneEndpoint.ID != "" {
		uids = append(uids, c.ControlPlaneEndpoint.ID)
		groups = append(groups, countGroup("Control plane endpoint IP", 1))
	}
	if c.TinkerbellIP != nil && c.TinkerbellIP.ID != "" {
		uids = append(uids, c.TinkerbellIP.ID)
		groups = append(groups, countGroup("Tinkerbell IP", 1))
	}

	// Backup configuration + sub-kinds — all owned by the cluster, cascade-deleted.
	if c.Backup != nil && c.Backup.ID != "" {
		uids = append(uids, c.Backup.ID)
		backupKinds := 0
		if c.Backup.Etcd != nil && c.Backup.Etcd.ID != "" {
			uids = append(uids, c.Backup.Etcd.ID)
			backupKinds++
		}
		if c.Backup.Velero != nil && c.Backup.Velero.ID != "" {
			uids = append(uids, c.Backup.Velero.ID)
			backupKinds++
		}
		if c.Backup.S3Sync != nil && c.Backup.S3Sync.ID != "" {
			uids = append(uids, c.Backup.S3Sync.ID)
			backupKinds++
		}
		groups = append(groups, countGroup("Backup configuration", 1+backupKinds))
	}

	// Servers are NOT deleted — they're independent inventory. List the names
	// in the Preserved section so the operator sees what stays behind.
	if len(c.Nodes) > 0 {
		serverNames := make([]string, 0, len(c.Nodes))
		for _, n := range c.Nodes {
			name := n.Server.Hostname
			if name == "" {
				name = n.Server.ServiceTag
			}
			if name == "" {
				name = n.Server.OrbID
			}
			if name != "" {
				serverNames = append(serverNames, name)
			}
		}
		if len(serverNames) > 0 {
			preserved = append(preserved, namedGroup("Servers", serverNames))
		}
	}

	before := map[string]any{
		"name":      c.Name,
		"orbId":     c.OrbID,
		"namespace": c.Namespace,
		"typename":  c.Typename,
		"nodeCount": len(c.Nodes),
	}

	return &clusterDeletePlan{
		preview: DeletePreview{
			Name:       c.Name,
			Type:       "KubernetesCluster",
			TotalCount: len(uids),
			Version:    c.Version,
			Groups:     groups,
			Preserved:  preserved,
		},
		uids:   uids,
		orbID:  c.OrbID,
		name:   c.Name,
		before: before,
	}, nil
}

// ── DGraph helpers ────────────────────────────────────────────────────────────

func (h *DeleteHandler) gqlQuery(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dgraph: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result struct {
		Data   json.RawMessage            `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("dgraph: %s", result.Errors[0].Message)
	}
	return result.Data, nil
}

func (h *DeleteHandler) bulkDelete(ctx context.Context, uids []string) error {
	if len(uids) == 0 {
		return nil
	}
	type uidNode struct {
		UID string `json:"uid"`
	}
	nodes := make([]uidNode, len(uids))
	for i, uid := range uids {
		nodes[i] = uidNode{UID: uid}
	}
	body, _ := json.Marshal(map[string]any{"delete": nodes})
	url := h.dgraphDQLBase + "/mutate?commitNow=true"
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dql mutate: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dql mutate %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// refuse renders a guard's decision, or passes a genuine error through.
//
// The guards return a DECISION and never write. They used to call writeError
// directly, which was a fail-open bug worth remembering: writeError returns the
// result of c.JSON, which is nil on success, so `if err != nil { return err }`
// never fired — the 409 was written to the response AND the cascade went ahead
// and deleted. Status said refused, body said refused, entity gone. Caught only
// because the test asserted the entity still existed rather than stopping at
// the status code.
func (h *DeleteHandler) refuse(c echo.Context, err error) error {
	var pe *preflightError
	if errors.As(err, &pe) {
		return writeError(c, pe.Status, pe.Code, pe.Message, pe.Hint)
	}
	return err
}

// typesOfUIDs reads the ConfigItem types of a planned cascade, straight from the
// nodes it is about to delete.
//
// Derived rather than declared, deliberately. The alternative — tagging each
// plan builder's branches with the type they append — puts the gate's input in
// nine hand-maintained places across three functions, and the failure mode of
// forgetting one is SILENT UNDER-GATING: a protected child quietly stops being
// protected the day someone adds a branch. Asking DGraph what the uids actually
// are cannot drift, and costs one read on a rare, destructive, human-driven
// operation.
//
// Returns an error rather than an empty list when the read fails: an empty list
// would read as "no protected types here" and wave the delete through.
func (h *DeleteHandler) typesOfUIDs(ctx context.Context, uids []string) ([]string, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	dql := fmt.Sprintf(`{ nodes(func: uid(%s)) { dgraph.type } }`, strings.Join(uids, ", "))
	body, err := json.Marshal(map[string]string{"query": dql})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.dgraphDQLBase+"/query", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read cascade types: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("read cascade types (%d): %s", resp.StatusCode, raw)
	}
	var decoded struct {
		Data struct {
			Nodes []struct {
				Types []string `json:"dgraph.type"`
			} `json:"nodes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode cascade types: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range decoded.Data.Nodes {
		for _, t := range n.Types {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// guardDelete is the pair of checks a cascade delete has to pass before
// anything is removed: the caller's optimistic-concurrency precondition, and
// the approval policy.
//
// The gate half closes a MEASURED bypass (debt.md Track A2): this endpoint
// plans a cascade and POSTs a DQL delete, so it never passed through
// writeToDGraph and checkApprovalPolicy never ran. Under a policy with no
// bypass roles, `updateDataCenter` was refused 403 while DELETE of that same
// entity returned 200 seconds later — a rename gated, a cascade delete not.
//
// Ordering: the version check runs BEFORE planning (a stale caller should be
// told to reload, not have a cascade computed for them), the gate AFTER, so it
// can see every type the cascade would remove rather than only the declared one.
func (h *DeleteHandler) guardDelete(ctx context.Context, caller callerRole, orbID string, uids []string) error {
	if h.gql == nil {
		// Fail closed. A missing dependency must not silently disable a
		// security control — that is how the bypass above went unnoticed.
		return echo.NewHTTPError(http.StatusInternalServerError, "approval gate not configured")
	}
	types, err := h.typesOfUIDs(ctx, uids)
	if err != nil {
		return err
	}
	bypassed, err := h.gql.checkPolicyFor(ctx, []string{orbID}, types, caller)
	if err != nil {
		var gerr *gatedError
		if errors.As(err, &gerr) {
			// Same status, code and hint the /graphql refusal produces. A caller
			// must not have to learn two refusals for one control.
			return &preflightError{Status: gerr.Status, Code: gerr.Code, Message: gerr.Message, Hint: gerr.Hint}
		}
		return err
	}
	if bypassed != "" {
		h.logger.Warn("privileged delete — bypassed an approval policy",
			"policy", bypassed, "role", string(caller.Role), "orb_id", orbID,
			"types", strings.Join(types, ","), "entities", len(uids))
	}
	return nil
}

// checkDeleteVersion enforces `?ifVersion=` on a delete.
//
// A query parameter rather than an If-Match header: orbital already spells this
// precondition `ifVersion` on /graphql and in a changeset item, and a third
// spelling for the same question is exactly the API cost this whole change set
// exists to remove. DELETE carries no body by convention, so a parameter is
// where it goes.
//
// Absent means unconditional, matching every other path. Present and
// unparseable is a 400, not a 409 — retrying the same garbage would loop.
func (h *DeleteHandler) checkDeleteVersion(ctx context.Context, typeName, orbID, raw string) error {
	if raw == "" {
		return nil
	}
	want, err := strconv.Atoi(raw)
	if err != nil {
		return &preflightError{Status: http.StatusBadRequest, Code: CodeBadUserInput, Message: "ifVersion must be an integer"}
	}
	data, err := h.gqlQuery(ctx, fmt.Sprintf(
		`query GetVersion($orbId: String!) { get%s(orbId: $orbId) { version } }`, typeName),
		map[string]any{"orbId": orbID})
	if err != nil {
		return err
	}
	var resp map[string]struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("decode version: %w", err)
	}
	node, ok := resp["get"+typeName]
	if !ok || node.Version == nil {
		// No version to compare. Refused rather than waved through: a caller
		// that asked for a check and did not get one believes it is protected.
		return &preflightError{Status: http.StatusConflict, Code: CodeMVCCConflict,
			Message: "cannot verify this entity's version", Hint: "Reload the entity and try again."}
	}
	if *node.Version != want {
		return &preflightError{Status: http.StatusConflict, Code: CodeMVCCConflict,
			Message: fmt.Sprintf("This record was modified by someone else (you saw version %d, it is now %d). Please reload and try again.", want, *node.Version),
			Hint:    "Reload the entity and delete again if you still want to."}
	}
	return nil
}

// ── Group builders ────────────────────────────────────────────────────────────

func namedGroup(label string, names []string) DeleteGroup {
	if len(names) <= maxDeleteListItems {
		return DeleteGroup{Label: label, Items: names}
	}
	return DeleteGroup{Label: label, Items: names[:maxDeleteListItems], Extra: len(names) - maxDeleteListItems}
}

func countGroup(label string, count int) DeleteGroup {
	return DeleteGroup{Label: label, Count: count}
}

func serverDisplayName(hostname, name string) string {
	if hostname != "" {
		return hostname
	}
	return name
}
