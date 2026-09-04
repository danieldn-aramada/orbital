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
      serverMaintenance { orbId version enabled windowStart windowEnd reason }
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
        networkInterfaces {
          orbId
          name
          macAddress
          portType
          linkSpeedMbps
          connectedNetworkDevicePort
          connectedNetworkDevice { orbId name role }
        }
      }
      networkInterfaces(filter: { mgmtOnly: true }) {
        orbId
        name
        macAddress
        portType
        linkSpeedMbps
        connectedNetworkDevicePort
        connectedNetworkDevice { orbId name role }
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
	ServerMaintenance *struct {
		OrbID       string  `json:"orbId"`
		Version     int     `json:"version"`
		Enabled     *bool   `json:"enabled"`
		WindowStart *string `json:"windowStart"`
		WindowEnd   *string `json:"windowEnd"`
		Reason      *string `json:"reason"`
	} `json:"serverMaintenance"`
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
		OrbID             string `json:"orbId"`
		Name              string `json:"name"`
		Model             string `json:"model"`
		Manufacturer      string `json:"manufacturer"`
		SerialNumber      string `json:"serialNumber"`
		NetworkInterfaces []struct {
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
		} `json:"networkInterfaces"`
	} `json:"networkAdapters"`
	// Management-plane interfaces (BMC/iDRAC) — owned by the server directly, no
	// NetworkAdapter FRU. Fetched via the mgmtOnly filter on server.networkInterfaces.
	MgmtInterfaces []struct {
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
	} `json:"networkInterfaces"`
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

type networkInterfaceTabData struct {
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
	OrbID             string
	Name              string
	Model             string
	Manufacturer      string
	SerialNumber      string
	NetworkInterfaces []networkInterfaceTabData
}

type serverTabDetailData struct {
	ID              string
	OrbID           string
	DomID           string // SafeDomID(OrbID)
	Name            string
	Hostname        string
	Model           string
	Manufacturer    string
	ServiceTag      string
	RackPosition    int
	OobIP           string
	OobMAC          string
	CreatedBy       string
	CreatedAt       string
	UpdatedBy       string
	UpdatedAt       string
	Namespace       string
	Rack            struct{ ID, Name string }
	Version         int
	DataCenterID    string
	DataCenterOrbID string
	DataCenterDomID string // SafeDomID(DataCenterOrbID)
	DataCenterName  string
	ShowDCBack      bool // true when drilled from a DC tab
	CurrentUser     string
	EditDataJSON    template.JS
	EditTargetsJSON template.JS // configitem-editor.js consumes — see configitems.BuildEditTargets
	IdracOrbID      string
	IdracVersion    int
	// IdracValuesJSON carries the child's RAW current values so the page can
	// overlay proposed changes on iDRAC fields, exactly as MaintenanceValuesJSON
	// does for the maintenance panel. An edit to an owned child records the
	// CHILD's orbId, so a mark on these rows can only fire from IdracOrbID.
	IdracValuesJSON   string
	IdracSettings     *idracSettingsTabData
	ConfigProfileJSON string
	// Maintenance display (read-only, field-accurate — mirrors the edit modal).
	// Configured == a serverMaintenance node exists; Enabled is its on/off
	// switch; the window (if any) is optional scheduling. Editing is via the
	// JSON editor.
	MaintenanceConfigured bool
	// MaintenanceOrbID and MaintenanceValuesJSON let the page overlay proposed
	// changes onto these fields. A maintenance edit is recorded against the
	// CHILD's orbId, never the server's, so the field marks need the child id;
	// and the raw values are carried as JSON because the mark is suppressed
	// when a proposal already matches the current value, and comparing a
	// proposed `true` against the string "true" the table renders would be a
	// guess.
	MaintenanceOrbID      string
	MaintenanceValuesJSON string
	// SummaryValuesJSON carries the server's OWN editable scalars raw, for the
	// same reason MaintenanceValuesJSON carries the child's: a field mark is
	// suppressed when the proposed value already equals the current one, and
	// comparing a proposed `5` against the string "5" the table renders would
	// be a guess. Only the fields the editor can change are included — a mark
	// on a field nobody can propose would never fire.
	SummaryValuesJSON      string
	MaintenanceEnabled     bool
	MaintenanceWindowStart string
	MaintenanceWindowEnd   string
	MaintenanceReason      string
	StorageControllers     []storageControllerTabData
	NetworkAdapters        []networkAdapterTabData
	MgmtInterfaces         []networkInterfaceTabData
	BasePath               string
	Actions                layout.PageActions
	// RelatedOrbIDsCSV is "<server-orbId>,<idrac-orbId>,<scp-orbId>,..." —
	// every ConfigItem in the rendered subgraph. The audit tab uses it to
	// fetch events for the whole server-and-its-children in one call. See
	// shared.js initDetailTabs / loadAuditPanelForTab.
	RelatedOrbIDsCSV string
	// AuditPanelID matches data-panel on the audit <li> and the id of the
	// placeholder <div>. Consumed by the shared audit-tab partial.
	AuditPanelID string
}

// fmtMaintTime renders an ISO-8601 timestamp as a readable UTC string for the
// maintenance display (e.g. "Aug 14, 2026 8:00 AM UTC"). Falls back to the raw
// value when it doesn't parse, so a hand-entered odd value still shows.
func fmtMaintTime(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.UTC().Format("Jan 2, 2006 3:04 PM MST")
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
	// serverMaintenance: null when the node is absent — that's what makes the
	// editor dispatch addServerMaintenance the first time an admin sets a
	// window (a non-null seed would be read as "exists" → updateServerMaintenance
	// against a missing node = silent no-op). When present, seed only the
	// non-null fields so a nullable windowStart round-trips as JSON null, never
	// "" (DGraph's DateTime rejects empty strings).
	if raw.ServerMaintenance != nil {
		// Render the full block with null placeholders for empty scheduling
		// fields so the editor shows a togglable form (flip `enabled`), not a
		// sparse object. The *string pointers marshal to their value or JSON
		// null — null (never "") because DGraph's DateTime rejects empty strings
		// and the editor would send that "" straight into the mutation set.
		editFields["serverMaintenance"] = map[string]any{
			"enabled":     raw.ServerMaintenance.Enabled != nil && *raw.ServerMaintenance.Enabled,
			"windowStart": raw.ServerMaintenance.WindowStart,
			"windowEnd":   raw.ServerMaintenance.WindowEnd,
			"reason":      raw.ServerMaintenance.Reason,
		}
	} else {
		editFields["serverMaintenance"] = nil
	}
	editJSON, _ := json.Marshal(editFields)

	// Edit-target metadata for the configitem-editor JS module — registry-derived.
	// Server has IdracSettings as a direct child (no wrapper), so targets contain
	// the server root + idracSettings target.
	editTargets := configitems.BuildEditTargets("Server", raw.OrbID, raw.Namespace, raw.Name)
	if raw.IdracSettings != nil && raw.IdracSettings.OrbID != "" {
		editTargets = configitems.OverrideEditTargetOrbID(editTargets, "IdracSettings", raw.IdracSettings.OrbID)
	}
	// ServerMaintenance uses the current prefix convention
	// (<ns>:server-maintenance-<serial>), not the legacy <name>-<suffix> shape
	// BuildEditTargets derives. Override it so first-time create and edits both
	// target the convention-correct id. serviceTag holds the serial — the same
	// value the server's own orbId (server-<serial>) is keyed on.
	maintenanceOrbID := raw.Namespace + ":server-maintenance-" + raw.ServiceTag
	editTargets = configitems.OverrideEditTargetOrbID(editTargets, "ServerMaintenance", maintenanceOrbID)

	// OCC versions, so the editor can send `ifVersion` and have a concurrent
	// edit refused rather than silently overwritten. Stamped AFTER the orbId
	// overrides above, because the stamp keys on orbId — a target still
	// carrying its registry-derived id would not match.
	//
	// A child that does not exist yet has no version and is left at zero, which
	// the editor reads as "omit ifVersion": a create has nothing to assert.
	editTargets = configitems.StampEditTargetVersion(editTargets, raw.OrbID, raw.Version)
	if raw.IdracSettings != nil {
		editTargets = configitems.StampEditTargetVersion(editTargets, raw.IdracSettings.OrbID, raw.IdracSettings.Version)
	}
	if raw.ServerMaintenance != nil {
		editTargets = configitems.StampEditTargetVersion(editTargets, raw.ServerMaintenance.OrbID, raw.ServerMaintenance.Version)
	}
	editTargetsJSON, _ := json.Marshal(editTargets)

	srv := serverTabDetailData{
		ID:           raw.ID,
		OrbID:        raw.OrbID,
		DomID:        SafeDomID(raw.OrbID),
		Name:         raw.Name,
		Hostname:     raw.Hostname,
		Model:        raw.Model,
		Manufacturer: raw.Manufacturer,
		ServiceTag:   raw.ServiceTag,
		RackPosition: raw.RackPosition,
		OobIP:        raw.OobIP.Address,
		OobMAC:       raw.OobMAC,
		SummaryValuesJSON: rawFieldValues(map[string]any{
			// Exactly the Server FormFields in configitems/registry.go. OOB IP
			// is absent on purpose: it is an IPAddress reference, not a scalar
			// the editor writes.
			"hostname":     raw.Hostname,
			"manufacturer": raw.Manufacturer,
			"model":        raw.Model,
			"oobMAC":       raw.OobMAC,
			"rackPosition": raw.RackPosition,
			"serviceTag":   raw.ServiceTag,
		}),
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
		// Keys MUST match the IdracSettings FormFields in configitems/registry.go
		// — that list is what a changeset can name, so a key that drifts from it
		// silently stops marking that field.
		srv.IdracValuesJSON = rawFieldValues(map[string]any{
			"firmwareVersion":             raw.IdracSettings.FirmwareVersion,
			"sshEnabled":                  raw.IdracSettings.SshEnabled,
			"ipmiEnabled":                 raw.IdracSettings.IpmiEnabled,
			"lockdownModeEnabled":         raw.IdracSettings.LockdownModeEnabled,
			"osToIdracPassThroughEnabled": raw.IdracSettings.OsToIdracPassThroughEnabled,
			"usbManagementPortEnabled":    raw.IdracSettings.UsbManagementPortEnabled,
			"dhcpEnabled":                 raw.IdracSettings.DhcpEnabled,
			"racadmEnabled":               raw.IdracSettings.RacadmEnabled,
		})
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

	// Show the full field table whenever a maintenance node exists (colo servers
	// are seeded with one, off by default). Timestamps render as readable UTC;
	// empty window/reason fields show as "—" (never "now").
	if raw.ServerMaintenance != nil {
		srv.MaintenanceConfigured = true
		srv.MaintenanceOrbID = raw.ServerMaintenance.OrbID
		srv.MaintenanceEnabled = raw.ServerMaintenance.Enabled != nil && *raw.ServerMaintenance.Enabled
		srv.MaintenanceValuesJSON = rawFieldValues(map[string]any{
			"enabled":     raw.ServerMaintenance.Enabled,
			"windowStart": raw.ServerMaintenance.WindowStart,
			"windowEnd":   raw.ServerMaintenance.WindowEnd,
			"reason":      raw.ServerMaintenance.Reason,
		})
		if raw.ServerMaintenance.WindowStart != nil {
			srv.MaintenanceWindowStart = fmtMaintTime(*raw.ServerMaintenance.WindowStart)
		}
		if raw.ServerMaintenance.WindowEnd != nil {
			srv.MaintenanceWindowEnd = fmtMaintTime(*raw.ServerMaintenance.WindowEnd)
		}
		if raw.ServerMaintenance.Reason != nil {
			srv.MaintenanceReason = *raw.ServerMaintenance.Reason
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
		for _, p := range na.NetworkInterfaces {
			port := networkInterfaceTabData{
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
			adp.NetworkInterfaces = append(adp.NetworkInterfaces, port)
		}
		srv.NetworkAdapters = append(srv.NetworkAdapters, adp)
	}

	for _, p := range raw.MgmtInterfaces {
		port := networkInterfaceTabData{
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
		srv.MgmtInterfaces = append(srv.MgmtInterfaces, port)
	}

	srv.RelatedOrbIDsCSV = strings.Join(collectRelatedOrbIDs(c.Request().Context(), h.dgraphURL, "Server", raw.OrbID), ",")
	srv.AuditPanelID = "srv-panel-audit-" + srv.DomID

	tmpl := h.fragment
	if h.dev {
		tmpl = parseServerFragment()
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return renderHTML(c, tmpl, "", srv)
}

// rawFieldValues encodes a field-name → current-value map for the page to
// compare proposals against.
//
// The RAW value, not the rendered one: the table shows a maintenance window as
// readable UTC and an unset field as "—", and comparing a proposed timestamp
// against "—" would suppress nothing and mark everything.
func rawFieldValues(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}" // no values means nothing is suppressed — marks still render
	}
	return string(b)
}
