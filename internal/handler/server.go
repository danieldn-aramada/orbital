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

const getServerQuery = `
  query GetServer($id: ID!) {
    getServer(id: $id) {
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
      namespace { name }
      rack { id name }
      dataCenter { id name }
      oobIP { address role }
      idracSettings {
        firmwareVersion
        osToIdracPassThroughEnabled
        sshEnabled
        usbManagementPortEnabled
        ipmiEnabled
        lockdownModeEnabled
        dhcpEnabled
        racadmEnabled
      }
      serverConfigurationProfile { json }
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
    }
  }`

type ServerHandler struct {
	dev       bool
	dgraphURL string
	fragment  *template.Template
	logger    *slog.Logger
	basePath  string
}

func NewServerHandler(dgraphURL string, dev bool, logger *slog.Logger, basePath string) *ServerHandler {
	return &ServerHandler{
		dgraphURL: dgraphURL,
		dev:       dev,
		fragment:  parseServerFragment(),
		logger:    logger,
		basePath:  basePath,
	}
}

func parseServerFragment() *template.Template {
	return template.Must(template.ParseFiles(
		"web/shared/templates/partials/server-tab.gohtml",
		"web/shared/templates/components/edit-modal-server.gohtml",
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
	Namespace    struct {
		Name string `json:"name"`
	} `json:"namespace"`
	Rack struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"rack"`
	DataCenter struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"dataCenter"`
	OobIP struct {
		Address string `json:"address"`
		Role    string `json:"role"`
	} `json:"oobIP"`
	IdracSettings *struct {
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
		JSON string `json:"json"`
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

type serverTabDetailData struct {
	ID                 string
	OrbID              string
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
	Namespace          struct{ Name string }
	Rack               struct{ ID, Name string }
	Version            int
	DataCenterID       string
	DataCenterName     string
	ShowDCBack         bool // true when drilled from a DC tab
	CurrentUser        string
	EditDataJSON       template.JS
	IdracSettings      *idracSettingsTabData
	ConfigProfileJSON  string
	StorageControllers []storageControllerTabData
	BasePath           string
	Actions            layout.PageActions
}

func (h *ServerHandler) Tab(c echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusFound, h.basePath+"/")
	}

	if h.dev {
		time.Sleep(150 * time.Millisecond)
	}

	id := c.Param("id")

	body, _ := json.Marshal(map[string]any{
		"query":     getServerQuery,
		"variables": map[string]any{"id": id},
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

	currentUser := ""
	if v := c.Get("user_name"); v != nil {
		currentUser, _ = v.(string)
	}
	if currentUser == "" {
		if v := c.Get("user_email"); v != nil {
			currentUser, _ = v.(string)
		}
	}

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
		"hostname":        raw.Hostname,
		"manufacturer":    raw.Manufacturer,
		"model":           raw.Model,
		"oobMAC":          raw.OobMAC,
		"rackPosition":    raw.RackPosition,
		"serviceTag":      raw.ServiceTag,
		"idracSettings":   idracFields,
	}
	editJSON, _ := json.Marshal(editFields)

	srv := serverTabDetailData{
		ID:             raw.ID,
		OrbID:          raw.OrbID,
		Name:           raw.Name,
		Hostname:       raw.Hostname,
		Model:          raw.Model,
		Manufacturer:   raw.Manufacturer,
		ServiceTag:     raw.ServiceTag,
		RackPosition:   raw.RackPosition,
		OobIP:          raw.OobIP.Address,
		OobMAC:         raw.OobMAC,
		CreatedBy:      raw.CreatedBy,
		CreatedAt:      raw.CreatedAt,
		UpdatedBy:      raw.UpdatedBy,
		UpdatedAt:      raw.UpdatedAt,
		Namespace:      struct{ Name string }{Name: raw.Namespace.Name},
		Rack:           struct{ ID, Name string }{ID: raw.Rack.ID, Name: raw.Rack.Name},
		Version:        raw.Version,
		DataCenterID:   raw.DataCenter.ID,
		DataCenterName: raw.DataCenter.Name,
		ShowDCBack:     c.QueryParam("dcCtx") == "1",
		CurrentUser:    currentUser,
		EditDataJSON:   template.JS(editJSON),
		BasePath:       h.basePath,
		Actions:        layout.OrbitalActions,
	}

	if raw.IdracSettings != nil {
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

	tmpl := h.fragment
	if h.dev {
		tmpl = parseServerFragment()
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.Execute(c.Response().Writer, srv)
}
