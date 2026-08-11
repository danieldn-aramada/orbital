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
      manufacturer model serial role macAddress
      dataCenter { id orbId name }
      networkPortConnectedNetworkDevice {
        orbId name macAddress connectedNetworkDevicePort
        networkAdapter { server { id orbId name hostname } }
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
	DataCenter   *struct {
		ID    string `json:"id"`
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"dataCenter"`
	Ports []struct {
		OrbID          string `json:"orbId"`
		Name           string `json:"name"`
		MacAddress     string `json:"macAddress"`
		RemotePort     string `json:"connectedNetworkDevicePort"`
		NetworkAdapter *struct {
			Server *struct {
				ID       string `json:"id"`
				OrbID    string `json:"orbId"`
				Name     string `json:"name"`
				Hostname string `json:"hostname"`
			} `json:"server"`
		} `json:"networkAdapter"`
	} `json:"networkPortConnectedNetworkDevice"`
}

type netdevConnectedServer struct {
	ServerOrbID string
	ServerName  string
	PortName    string
	PortMac     string
	RemotePort  string
}

type netdevTabData struct {
	ID               string
	OrbID            string
	DomID            string
	Name             string
	Typename         string
	Namespace        string
	Version          int
	CreatedBy        string
	CreatedAt        string
	UpdatedBy        string
	UpdatedAt        string
	Manufacturer     string
	Model            string
	Serial           string
	Role             string
	MacAddress       string
	DataCenterOrbID  string
	DataCenterName   string
	ConnectedServers []netdevConnectedServer

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
	}
	editJSON, _ := json.Marshal(editFields)

	// Edit targets are derived from the configitems registry — one entry for the
	// NetworkDevice root (it has no owned children).
	targets := configitems.BuildEditTargets("NetworkDevice", raw.OrbID, raw.Namespace, raw.Name)
	targetsJSON, _ := json.Marshal(targets)

	tab := netdevTabData{
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
		CurrentUser:      actorFromContext(c),
		EditDataJSON:     template.JS(editJSON),
		EditTargetsJSON:  template.JS(targetsJSON),
		BasePath:         h.basePath,
		Actions:          h.actions(c),
		RelatedOrbIDsCSV: raw.OrbID,
		AuditPanelID:     "netdev-panel-audit-" + SafeDomID(raw.OrbID),
	}
	if raw.DataCenter != nil {
		tab.DataCenterOrbID = raw.DataCenter.OrbID
		tab.DataCenterName = raw.DataCenter.Name
	}
	for _, p := range raw.Ports {
		if p.NetworkAdapter == nil || p.NetworkAdapter.Server == nil {
			continue
		}
		s := p.NetworkAdapter.Server
		name := s.Hostname
		if name == "" {
			name = s.Name
		}
		tab.ConnectedServers = append(tab.ConnectedServers, netdevConnectedServer{
			ServerOrbID: s.OrbID,
			ServerName:  name,
			PortName:    p.Name,
			PortMac:     p.MacAddress,
			RemotePort:  p.RemotePort,
		})
	}

	return renderHTML(c, h.fragment, "", tab)
}
