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

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

const getServerQuery = `
  query GetServer($orbId: String!) {
    getServer(orbId: $orbId) {
      id
      orbId
      name
      hostname
      model
      manufacturer
      serviceTag
      rackPosition
      oobMAC
      createdBy
      createdAt
      updatedBy
      updatedAt
      version
      namespace
      rack { id name }
      dataCenter { id orbId name }
      oobIP { orbId address role }
      idracSettings {
        orbId
        version
        firmwareVersion
        osToIdracPassThroughEnabled
        sshEnabled
        usbManagementPortEnabled
        ipmiEnabled
        lockdownModeEnabled
        dhcpEnabled
        racadmEnabled
      }
      serverConfigurationProfile { orbId json }
      storageControllers {
        orbId
        name
        storageDevices {
          name
          capacityBytes
          manufacturer
          serialNumber
          wwn
        }
      }
      networkAdapters {
        orbId
        name
        model
        manufacturer
        serialNumber
        networkPorts {
          orbId
          name
          macAddress
          portType
          linkSpeedMbps
          connectedNetworkDevicePort
          connectedNetworkDevice { orbId name role }
        }
      }
    }
  }`

type ServerHandler struct {
	dev       bool
	dgraphURL string
	fragment  *template.Template
	logger    *slog.Logger
	basePath  string
	// actions resolves per-request PageActions. orbital passes a closure that
	// reads can_mutate from the context; orb passes a const returning OrbActions.
	actions func(echo.Context) layout.PageActions
}

func NewServerHandler(dgraphURL string, dev bool, logger *slog.Logger, basePath string, actions func(echo.Context) layout.PageActions) *ServerHandler {
	return &ServerHandler{
		dgraphURL: dgraphURL,
		dev:       dev,
		fragment:  parseServerFragment(),
		logger:    logger,
		basePath:  basePath,
		actions:   actions,
	}
}

func parseServerFragment() *template.Template {
	return template.Must(template.ParseFiles(
		"web/templates/shared/partials/server-tab.gohtml",
		"web/templates/shared/partials/audit-tab.gohtml",
		"web/templates/shared/components/edit-modal-server.gohtml",
	))
}

type serverQueryResponse struct {
	ID           string `json:"id"`
	OrbID        string `json:"orbId"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
	ServiceTag   string `json:"serviceTag"`
	RackPosition int    `json:"rackPosition"`
	OobMAC       string `json:"oobMAC"`
	CreatedBy    string `json:"createdBy"`
	CreatedAt    string `json:"createdAt"`
	UpdatedBy    string `json:"updatedBy"`
	UpdatedAt    string `json:"updatedAt"`
	Version      int    `json:"version"`
	Namespace    string `json:"namespace"`
	Rack         struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"rack"`
	DataCenter struct {
		ID    string `json:"id"`
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"dataCenter"`
	OobIP struct {
		OrbID   string `json:"orbId"`
		Address string `json:"address"`
		Role    string `json:"role"`
	} `json:"oobIP"`
	IdracSettings *struct {
		OrbID                       string `json:"orbId"`
		Version                     int    `json:"version"`
		FirmwareVersion             string `json:"firmwareVersion"`
		OsToIdracPassThroughEnabled bool   `json:"osToIdracPassThroughEnabled"`
		SshEnabled                  bool   `json:"sshEnabled"`
		UsbManagementPortEnabled    bool   `json:"usbManagementPortEnabled"`
		IpmiEnabled                 bool   `json:"ipmiEnabled"`
		LockdownModeEnabled         bool   `json:"lockdownModeEnabled"`
		DhcpEnabled                 bool   `json:"dhcpEnabled"`
		RacadmEnabled               bool   `json:"racadmEnabled"`
	} `json:"idracSettings"`
	ServerConfigurationProfile *struct {
		OrbID string `json:"orbId"`
		JSON  string `json:"json"`
	} `json:"serverConfigurationProfile"`
	StorageControllers []struct {
		OrbID          string `json:"orbId"`
		Name           string `json:"name"`
		StorageDevices []struct {
			Name          string `json:"name"`
			CapacityBytes int    `json:"capacityBytes"`
			Manufacturer  string `json:"manufacturer"`
			SerialNumber  string `json:"serialNumber"`
			WWN           string `json:"wwn"`
		} `json:"storageDevices"`
	} `json:"storageControllers"`
	NetworkAdapters []struct {
		OrbID        string `json:"orbId"`
		Name         string `json:"name"`
		Model        string `json:"model"`
		Manufacturer string `json:"manufacturer"`
		SerialNumber string `json:"serialNumber"`
		NetworkPorts []struct {
			OrbID                  string `json:"orbId"`
			Name                   string `json:"name"`
			MacAddress             string `json:"macAddress"`
			PortType               string `json:"portType"`
			LinkSpeedMbps          int    `json:"linkSpeedMbps"`
			RemotePort             string `json:"connectedNetworkDevicePort"`
			ConnectedNetworkDevice *struct {
				OrbID string `json:"orbId"`
				Name  string `json:"name"`
				Role  string `json:"role"`
			} `json:"connectedNetworkDevice"`
		} `json:"networkPorts"`
	} `json:"networkAdapters"`
}

type idracSettingsTabData struct {
	FirmwareVersion             string
	OsToIdracPassThroughEnabled bool
	SshEnabled                  bool
	UsbManagementPortEnabled    bool
	IpmiEnabled                 bool
	LockdownModeEnabled         bool
	DhcpEnabled                 bool
	RacadmEnabled               bool
}

type storageDeviceTabData struct {
	Name          string
	CapacityBytes int
	Manufacturer  string
	SerialNumber  string
	WWN           string
}

type storageControllerTabData struct {
	OrbID          string
	Name           string
	StorageDevices []storageDeviceTabData
}

type networkPortTabData struct {
	Name          string
	MacAddress    string
	PortType      string
	LinkSpeedMbps int
	RemotePort    string
	DeviceName    string
	DeviceOrbID   string
	DeviceRole    string
}

type networkAdapterTabData struct {
	OrbID        string
	Name         string
	Model        string
	Manufacturer string
	SerialNumber string
	NetworkPorts []networkPortTabData
}

type serverTabDetailData struct {
	ID                 string
	OrbID              string
	DomID              string // SafeDomID(OrbID)
	Name               string
	Hostname           string
	Model              string
	Manufacturer       string
	ServiceTag         string
	RackPosition       int
	OobIP              string
	OobMAC             string
	CreatedBy          string
	CreatedAt          string
	UpdatedBy          string
	UpdatedAt          string
	Namespace          string
	Rack               struct{ ID, Name string }
	Version            int
	DataCenterID       string
	DataCenterOrbID    string
	DataCenterDomID    string // SafeDomID(DataCenterOrbID)
	DataCenterName     string
	ShowDCBack         bool // true when drilled from a DC tab
	CurrentUser        string
	EditDataJSON       template.JS
	EditTargetsJSON    template.JS // configitem-editor.js consumes — see configitems.BuildEditTargets
	IdracOrbID         string
	IdracVersion       int
	IdracSettings      *idracSettingsTabData
	ConfigProfileJSON  string
	StorageControllers []storageControllerTabData
	NetworkAdapters    []networkAdapterTabData
	BasePath           string
	Actions            layout.PageActions
	// RelatedOrbIDsCSV is "<server-orbId>,<idrac-orbId>,<scp-orbId>,..." —
	// every ConfigItem in the rendered subgraph. The audit tab uses it to
	// fetch events for the whole server-and-its-children in one call. See
	// shared.js initDetailTabs / loadAuditPanelForTab.
	RelatedOrbIDsCSV string
	// AuditPanelID matches data-panel on the audit <li> and the id of the
	// placeholder <div>. Consumed by the shared audit-tab partial.
	AuditPanelID string
}

// collectRelatedOrbIDs returns the server's orbId followed by every nested
// ConfigItem orbId present on the GraphQL response. Empty / zero values are
// skipped so the result is ready for the data-related-orb-ids attribute.
// Order is stable: the server's own orbId comes first.
func collectRelatedOrbIDs(raw *serverQueryResponse) []string {
	out := make([]string, 0, 4+len(raw.StorageControllers))
	add := func(id string) {
		if id != "" {
			out = append(out, id)
		}
	}
	add(raw.OrbID)
	if raw.IdracSettings != nil {
		add(raw.IdracSettings.OrbID)
	}
	if raw.ServerConfigurationProfile != nil {
		add(raw.ServerConfigurationProfile.OrbID)
	}
	add(raw.OobIP.OrbID)
	for _, sc := range raw.StorageControllers {
		add(sc.OrbID)
	}
	for _, na := range raw.NetworkAdapters {
		add(na.OrbID)
		for _, p := range na.NetworkPorts {
			add(p.OrbID)
		}
	}
	return out
}

func (h *ServerHandler) Tab(c echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusFound, h.basePath+"/")
	}

	// Path-param decoding is handled by middleware.DecodePathParams.
	orbID := c.Param("orbId")

	body, _ := json.Marshal(map[string]any{
		"query":     getServerQuery,
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
	h.logger.Debug("dgraph response", "body", string(rawBody))

	var result struct {
		Data struct {
			GetServer serverQueryResponse `json:"getServer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	raw := result.Data.GetServer
	// DGraph's `getServer(orbId: $x)` returns a missing record as an absent
	// field in the JSON response, which unmarshals into a zero-value
	// serverQueryResponse. Detect that and 404 instead of rendering a server
	// tab with empty DomID/OrbID — those produce broken `id="edit-modal-srv-"`
	// markup and silently-broken interactions. Mirrors the cluster handler's
	// not-found check.
	if raw.OrbID == "" {
		return echo.NewHTTPError(http.StatusNotFound, "server not found")
	}

	currentUser := actorFromContext(c)

	idracFields := map[string]any{
		"firmwareVersion":             "",
		"sshEnabled":                  false,
		"ipmiEnabled":                 false,
		"lockdownModeEnabled":         false,
		"osToIdracPassThroughEnabled": false,
		"usbManagementPortEnabled":    false,
		"dhcpEnabled":                 false,
		"racadmEnabled":               false,
	}
	if raw.IdracSettings != nil {
		idracFields["firmwareVersion"] = raw.IdracSettings.FirmwareVersion
		idracFields["sshEnabled"] = raw.IdracSettings.SshEnabled
		idracFields["ipmiEnabled"] = raw.IdracSettings.IpmiEnabled
		idracFields["lockdownModeEnabled"] = raw.IdracSettings.LockdownModeEnabled
		idracFields["osToIdracPassThroughEnabled"] = raw.IdracSettings.OsToIdracPassThroughEnabled
		idracFields["usbManagementPortEnabled"] = raw.IdracSettings.UsbManagementPortEnabled
		idracFields["dhcpEnabled"] = raw.IdracSettings.DhcpEnabled
		idracFields["racadmEnabled"] = raw.IdracSettings.RacadmEnabled
	}
	editFields := map[string]any{
		"hostname":      raw.Hostname,
		"manufacturer":  raw.Manufacturer,
		"model":         raw.Model,
		"oobMAC":        raw.OobMAC,
		"rackPosition":  raw.RackPosition,
		"serviceTag":    raw.ServiceTag,
		"idracSettings": idracFields,
	}
	editJSON, _ := json.Marshal(editFields)

	// Edit-target metadata for the configitem-editor JS module — registry-derived.
	// Server has IdracSettings as a direct child (no wrapper), so targets contain
	// the server root + idracSettings target.
	editTargets := configitems.BuildEditTargets("Server", raw.OrbID, raw.Namespace, raw.Name)
	if raw.IdracSettings != nil && raw.IdracSettings.OrbID != "" {
		editTargets = configitems.OverrideEditTargetOrbID(editTargets, "IdracSettings", raw.IdracSettings.OrbID)
	}
	editTargetsJSON, _ := json.Marshal(editTargets)

	srv := serverTabDetailData{
		ID:              raw.ID,
		OrbID:           raw.OrbID,
		DomID:           SafeDomID(raw.OrbID),
		Name:            raw.Name,
		Hostname:        raw.Hostname,
		Model:           raw.Model,
		Manufacturer:    raw.Manufacturer,
		ServiceTag:      raw.ServiceTag,
		RackPosition:    raw.RackPosition,
		OobIP:           raw.OobIP.Address,
		OobMAC:          raw.OobMAC,
		CreatedBy:       raw.CreatedBy,
		CreatedAt:       raw.CreatedAt,
		UpdatedBy:       raw.UpdatedBy,
		UpdatedAt:       raw.UpdatedAt,
		Namespace:       raw.Namespace,
		Rack:            struct{ ID, Name string }{ID: raw.Rack.ID, Name: raw.Rack.Name},
		Version:         raw.Version,
		DataCenterID:    raw.DataCenter.ID,
		DataCenterOrbID: raw.DataCenter.OrbID,
		DataCenterDomID: SafeDomID(raw.DataCenter.OrbID),
		DataCenterName:  raw.DataCenter.Name,
		ShowDCBack:      c.QueryParam("dcCtx") == "1",
		CurrentUser:     currentUser,
		EditDataJSON:    template.JS(editJSON),
		EditTargetsJSON: template.JS(editTargetsJSON),
		BasePath:        h.basePath,
		Actions:         h.actions(c),
	}

	if raw.IdracSettings != nil {
		srv.IdracOrbID = raw.IdracSettings.OrbID
		srv.IdracVersion = raw.IdracSettings.Version
		srv.IdracSettings = &idracSettingsTabData{
			FirmwareVersion:             raw.IdracSettings.FirmwareVersion,
			OsToIdracPassThroughEnabled: raw.IdracSettings.OsToIdracPassThroughEnabled,
			SshEnabled:                  raw.IdracSettings.SshEnabled,
			UsbManagementPortEnabled:    raw.IdracSettings.UsbManagementPortEnabled,
			IpmiEnabled:                 raw.IdracSettings.IpmiEnabled,
			LockdownModeEnabled:         raw.IdracSettings.LockdownModeEnabled,
			DhcpEnabled:                 raw.IdracSettings.DhcpEnabled,
			RacadmEnabled:               raw.IdracSettings.RacadmEnabled,
		}
	}

	if raw.ServerConfigurationProfile != nil {
		var buf bytes.Buffer
		if err := json.Indent(&buf, []byte(raw.ServerConfigurationProfile.JSON), "", "  "); err == nil {
			srv.ConfigProfileJSON = buf.String()
		} else {
			srv.ConfigProfileJSON = raw.ServerConfigurationProfile.JSON
		}
	}

	for _, sc := range raw.StorageControllers {
		ctrl := storageControllerTabData{OrbID: sc.OrbID, Name: sc.Name}
		for _, d := range sc.StorageDevices {
			ctrl.StorageDevices = append(ctrl.StorageDevices, storageDeviceTabData{
				Name:          d.Name,
				CapacityBytes: d.CapacityBytes,
				Manufacturer:  d.Manufacturer,
				SerialNumber:  d.SerialNumber,
				WWN:           d.WWN,
			})
		}
		srv.StorageControllers = append(srv.StorageControllers, ctrl)
	}

	for _, na := range raw.NetworkAdapters {
		adp := networkAdapterTabData{
			OrbID:        na.OrbID,
			Name:         na.Name,
			Model:        na.Model,
			Manufacturer: na.Manufacturer,
			SerialNumber: na.SerialNumber,
		}
		for _, p := range na.NetworkPorts {
			port := networkPortTabData{
				Name:          p.Name,
				MacAddress:    p.MacAddress,
				PortType:      p.PortType,
				LinkSpeedMbps: p.LinkSpeedMbps,
				RemotePort:    p.RemotePort,
			}
			if p.ConnectedNetworkDevice != nil {
				port.DeviceName = p.ConnectedNetworkDevice.Name
				port.DeviceOrbID = p.ConnectedNetworkDevice.OrbID
				port.DeviceRole = p.ConnectedNetworkDevice.Role
			}
			adp.NetworkPorts = append(adp.NetworkPorts, port)
		}
		srv.NetworkAdapters = append(srv.NetworkAdapters, adp)
	}

	srv.RelatedOrbIDsCSV = strings.Join(collectRelatedOrbIDs(&raw), ",")
	srv.AuditPanelID = "srv-panel-audit-" + srv.DomID

	tmpl := h.fragment
	if h.dev {
		tmpl = parseServerFragment()
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return renderHTML(c, tmpl, "", srv)
}
