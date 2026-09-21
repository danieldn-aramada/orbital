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

// getNetworkDeviceQuery fetches a NetworkDevice plus the server ports cabled to
// it (the reverse `connectedNetworkDevice` edge) so the detail page can render
// "who connects to me" — the blast-radius view.
const getNetworkDeviceQuery = `
  query GetNetworkDevice($orbId: String!) {
    getNetworkDevice(orbId: $orbId) {
      id orbId name namespace version
      createdBy createdAt updatedBy updatedAt
      manufacturer model serial role macAddress rackPosition platform face vcPosition vcPriority
      dataCenter { id orbId name }
      rack { id orbId name }
      virtualChassis { id orbId name }
      networkInterfaceConnectedNetworkDevice {
        orbId name macAddress connectedNetworkDevicePort
        server {
          id orbId name hostname
          kubernetesNode {
            role gpu
            cluster { ... on EksaKubernetesCluster { orbId name provider } }
          }
        }
        networkDevice { orbId name role }
      }
    }
  }`

type NetworkDeviceHandler struct {
	dev       bool
	dgraphURL string
	fragment  *template.Template
	logger    *slog.Logger
	basePath  string
	// actions resolves per-request PageActions — same seam as the cluster/server
	// handlers so orbital (role-based) and orb (read-only) share one code path.
	actions func(echo.Context) layout.PageActions
}

func NewNetworkDeviceHandler(dgraphURL string, dev bool, logger *slog.Logger, basePath string, actions func(echo.Context) layout.PageActions) *NetworkDeviceHandler {
	return &NetworkDeviceHandler{
		dgraphURL: dgraphURL,
		dev:       dev,
		fragment:  parseNetworkDeviceFragment(),
		logger:    logger,
		basePath:  basePath,
		actions:   actions,
	}
}

func parseNetworkDeviceFragment() *template.Template {
	return template.Must(template.ParseFiles(
		"web/templates/shared/partials/networkdevice-tab.gohtml",
		"web/templates/shared/partials/audit-tab.gohtml",
		"web/templates/shared/components/edit-modal-networkdevice.gohtml",
	))
}

type networkDeviceQueryResponse struct {
	ID           string `json:"id"`
	OrbID        string `json:"orbId"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Version      int    `json:"version"`
	CreatedBy    string `json:"createdBy"`
	CreatedAt    string `json:"createdAt"`
	UpdatedBy    string `json:"updatedBy"`
	UpdatedAt    string `json:"updatedAt"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Serial       string `json:"serial"`
	Role         string `json:"role"`
	MacAddress   string `json:"macAddress"`
	RackPosition int    `json:"rackPosition"`
	Platform     string `json:"platform"`
	Face         string `json:"face"`
	VcPosition   int    `json:"vcPosition"`
	VcPriority   int    `json:"vcPriority"`
	DataCenter   *struct {
		ID    string `json:"id"`
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"dataCenter"`
	Rack *struct {
		ID    string `json:"id"`
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"rack"`
	VirtualChassis *struct {
		ID    string `json:"id"`
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"virtualChassis"`
	Ports []struct {
		OrbID      string `json:"orbId"`
		Name       string `json:"name"`
		MacAddress string `json:"macAddress"`
		RemotePort string `json:"connectedNetworkDevicePort"`
		Server     *struct {
			ID             string `json:"id"`
			OrbID          string `json:"orbId"`
			Name           string `json:"name"`
			Hostname       string `json:"hostname"`
			KubernetesNode *struct {
				Role    string `json:"role"`
				Gpu     bool   `json:"gpu"`
				Cluster *struct {
					OrbID    string `json:"orbId"`
					Name     string `json:"name"`
					Provider string `json:"provider"`
				} `json:"cluster"`
			} `json:"kubernetesNode"`
		} `json:"server"`
		NetworkDevice *struct {
			OrbID string `json:"orbId"`
			Name  string `json:"name"`
			Role  string `json:"role"`
		} `json:"networkDevice"`
	} `json:"networkInterfaceConnectedNetworkDevice"`
}

type networkDeviceConnectedServer struct {
	ServerOrbID  string
	ServerName   string
	PortName     string
	PortMac      string
	RemotePort   string
	ClusterOrbID string
	ClusterName  string
	NodeRole     string
	Gpu          bool
}

// networkDeviceConnection is one cabled connection on this device — the interface-level
// wiring view (server NICs AND device↔device fabric). RemoteType is "server" or "device";
// the remote end links into its own record. LocalPort is this device's port, RemotePort the
// remote end's. Cluster/role context lives on the Servers tab, not here.
type networkDeviceConnection struct {
	RemoteType  string
	RemoteOrbID string
	RemoteName  string
	LocalPort   string
	RemotePort  string
}

type networkDeviceTabData struct {
	ID                  string
	OrbID               string
	DomID               string
	Name                string
	Typename            string
	Namespace           string
	Version             int
	CreatedBy           string
	CreatedAt           string
	UpdatedBy           string
	UpdatedAt           string
	Manufacturer        string
	Model               string
	Serial              string
	Role                string
	MacAddress          string
	RackPosition        int
	Platform            string
	Face                string
	VcPosition          int
	VcPriority          int
	DataCenterOrbID     string
	DataCenterName      string
	RackOrbID           string
	RackName            string
	VirtualChassisOrbID string
	VirtualChassisName  string
	ConnectedServers    []networkDeviceConnectedServer
	Connections         []networkDeviceConnection

	CurrentUser     string
	EditDataJSON    template.JS
	EditTargetsJSON template.JS
	BasePath        string
	Actions         layout.PageActions

	RelatedOrbIDsCSV string
	AuditPanelID     string
}

func (h *NetworkDeviceHandler) Tab(c echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusFound, h.basePath+"/")
	}

	if h.dev {
		time.Sleep(150 * time.Millisecond)
	}

	orbID := c.Param("orbId")

	body, _ := json.Marshal(map[string]any{
		"query":     getNetworkDeviceQuery,
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
	h.logger.Debug("network device query response", "body", string(rawBody))

	var result struct {
		Data struct {
			GetNetworkDevice *networkDeviceQueryResponse `json:"getNetworkDevice"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if result.Data.GetNetworkDevice == nil {
		return echo.NewHTTPError(http.StatusNotFound, "network device not found")
	}
	raw := result.Data.GetNetworkDevice

	// Editable scalars — the JSON editor tree. Values are NetBox-seeded, but
	// orbital is the system of record so operators can correct them here.
	editFields := map[string]any{
		"manufacturer": raw.Manufacturer,
		"model":        raw.Model,
		"serial":       raw.Serial,
		"role":         raw.Role,
		"macAddress":   raw.MacAddress,
		"rackPosition": raw.RackPosition,
		"platform":     raw.Platform,
		"face":         raw.Face,
		"vcPosition":   raw.VcPosition,
		"vcPriority":   raw.VcPriority,
	}
	editJSON, _ := json.Marshal(editFields)

	// Edit targets are derived from the configitems registry — one entry for the
	// NetworkDevice root (it has no owned children).
	targets := configitems.BuildEditTargets("NetworkDevice", raw.OrbID, raw.Namespace, raw.Name)
	// OCC version, so the editor sends `version` and a concurrent edit is
	// refused rather than silently overwritten.
	targets = configitems.StampEditTargetVersion(targets, raw.OrbID, raw.Version)
	targetsJSON, _ := json.Marshal(targets)

	tab := networkDeviceTabData{
		ID:               raw.ID,
		OrbID:            raw.OrbID,
		DomID:            SafeDomID(raw.OrbID),
		Name:             raw.Name,
		Typename:         "NetworkDevice",
		Namespace:        raw.Namespace,
		Version:          raw.Version,
		CreatedBy:        raw.CreatedBy,
		CreatedAt:        raw.CreatedAt,
		UpdatedBy:        raw.UpdatedBy,
		UpdatedAt:        raw.UpdatedAt,
		Manufacturer:     raw.Manufacturer,
		Model:            raw.Model,
		Serial:           raw.Serial,
		Role:             raw.Role,
		MacAddress:       raw.MacAddress,
		RackPosition:     raw.RackPosition,
		Platform:         raw.Platform,
		Face:             raw.Face,
		VcPosition:       raw.VcPosition,
		VcPriority:       raw.VcPriority,
		CurrentUser:      actorFromContext(c),
		EditDataJSON:     template.JS(editJSON),
		EditTargetsJSON:  template.JS(targetsJSON),
		BasePath:         h.basePath,
		Actions:          h.actions(c),
		RelatedOrbIDsCSV: strings.Join(collectRelatedOrbIDs(c.Request().Context(), h.dgraphURL, "NetworkDevice", raw.OrbID), ","),
		AuditPanelID:     "network-device-panel-audit-" + SafeDomID(raw.OrbID),
	}
	if raw.DataCenter != nil {
		tab.DataCenterOrbID = raw.DataCenter.OrbID
		tab.DataCenterName = raw.DataCenter.Name
	}
	if raw.Rack != nil {
		tab.RackOrbID = raw.Rack.OrbID
		tab.RackName = raw.Rack.Name
	}
	if raw.VirtualChassis != nil {
		tab.VirtualChassisOrbID = raw.VirtualChassis.OrbID
		tab.VirtualChassisName = raw.VirtualChassis.Name
	}
	// Assemble the interface-level connection list (server NICs + device↔device
	// fabric) from the reverse edge, plus the server-only rollup for the Servers
	// tab. LocalPort = this device's port (the remote interface's remote-port label);
	// RemotePort = the remote interface's own name.
	for _, p := range raw.Ports {
		switch {
		case p.Server != nil:
			s := p.Server
			name := s.Hostname
			if name == "" {
				name = s.Name
			}
			conn := networkDeviceConnection{
				RemoteType:  "server",
				RemoteOrbID: s.OrbID,
				RemoteName:  name,
				LocalPort:   p.RemotePort,
				RemotePort:  p.Name,
			}
			cs := networkDeviceConnectedServer{
				ServerOrbID: s.OrbID,
				ServerName:  name,
				PortName:    p.Name,
				PortMac:     p.MacAddress,
				RemotePort:  p.RemotePort,
			}
			// Cross-domain context — the graph traversal NetBox can't do: which of
			// these connected servers are cluster nodes, and their role.
			if n := s.KubernetesNode; n != nil {
				cs.NodeRole, cs.Gpu = n.Role, n.Gpu
				if n.Cluster != nil {
					cs.ClusterOrbID = n.Cluster.OrbID
					cs.ClusterName = n.Cluster.Name
				}
			}
			tab.Connections = append(tab.Connections, conn)
			tab.ConnectedServers = append(tab.ConnectedServers, cs)
		case p.NetworkDevice != nil:
			d := p.NetworkDevice
			tab.Connections = append(tab.Connections, networkDeviceConnection{
				RemoteType:  "device",
				RemoteOrbID: d.OrbID,
				RemoteName:  d.Name,
				LocalPort:   p.RemotePort,
				RemotePort:  p.Name,
			})
		}
	}

	return renderHTML(c, h.fragment, "", tab)
}
