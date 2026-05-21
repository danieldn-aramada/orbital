package orbserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/armada/orbital/internal/orb"
	"github.com/armada/orbital/internal/web/data/layout"
	orbtemplates "github.com/armada/orbital/web/orb/templates"
	"github.com/labstack/echo/v4"
)

// --- DGraph query ---

const queryServerByIDFmt = `
  query GetServer($id: ID!) {
    getServer(id: $id) {
      id orbId hostname model manufacturer serviceTag rackPosition oobMAC
      createdAt updatedAt
      namespace { name }
      rack { name }
      dataCenter { id name }
      oobIP { address }
      idracSettings {
        firmwareVersion sshEnabled ipmiEnabled lockdownModeEnabled
        osToIdracPassThroughEnabled usbManagementPortEnabled dhcpEnabled racadmEnabled
      }
      storageControllers {
        name
        storageDevices { name capacityBytes manufacturer serialNumber wwn }
      }
    }
  }`

// --- DGraph response types ---

type orbServerQueryResponse struct {
	ID           string `json:"id"`
	OrbID        string `json:"orbId"`
	Hostname     string `json:"hostname"`
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
	ServiceTag   string `json:"serviceTag"`
	RackPosition int    `json:"rackPosition"`
	OobMAC       string `json:"oobMAC"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	Namespace    struct {
		Name string `json:"name"`
	} `json:"namespace"`
	Rack struct {
		Name string `json:"name"`
	} `json:"rack"`
	DataCenter struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"dataCenter"`
	OobIP struct {
		Address string `json:"address"`
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
	StorageControllers []struct {
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

// --- Template data types ---

type orbIdracData struct {
	FirmwareVersion             string
	OsToIdracPassThroughEnabled bool
	SshEnabled                  bool
	UsbManagementPortEnabled    bool
	IpmiEnabled                 bool
	LockdownModeEnabled         bool
	DhcpEnabled                 bool
	RacadmEnabled               bool
}

type orbStorageDeviceData struct {
	Name          string
	CapacityBytes int
	Manufacturer  string
	SerialNumber  string
	WWN           string
}

type orbStorageControllerData struct {
	Name           string
	StorageDevices []orbStorageDeviceData
}

type orbSrvDetailData struct {
	ID                 string
	OrbID              string
	Hostname           string
	Model              string
	Manufacturer       string
	ServiceTag         string
	RackPosition       int
	OobIP              string
	OobMAC             string
	CreatedAt          string
	UpdatedAt          string
	Namespace          string
	DataCenterName     string
	Rack               struct{ Name string }
	IdracSettings      *orbIdracData
	StorageControllers []orbStorageControllerData
}

type serverDetailPageData struct {
	layout.Base
	PageTitle        string
	Srv              *orbSrvDetailData
	OverridesByField map[string]string
}

// orbSrvTabData is the data model for the orb server-tab fragment.
type orbSrvTabData struct {
	ID                   string
	OrbID                string
	Hostname             string
	Model                string
	Manufacturer         string
	ServiceTag           string
	RackPosition         int
	OobIP                string
	OobMAC               string
	CreatedAt            string
	UpdatedAt            string
	Namespace            struct{ Name string }
	Rack                 struct{ Name string }
	DataCenterID         string
	DataCenterName       string
	ShowDCBack           bool
	IdracSettings        *orbIdracData
	StorageControllers   []orbStorageControllerData
	OverridesByField     map[string]string
	IdracOverridesByField map[string]string
}

// --- Handler ---

func (s *Server) serverDetailPage(c echo.Context) error {
	id := c.Param("id")

	raw, err := s.dgraphQuery(queryServerByIDFmt, map[string]any{"id": id})
	if err != nil {
		s.logger.Warn("dgraph server query failed", "err", err)
	}

	b := s.orbBase(c)
	b.UI.EditMode = "override"
	data := serverDetailPageData{Base: b, PageTitle: "Server"}

	if raw != nil {
		var result struct {
			Data struct {
				GetServer orbServerQueryResponse `json:"getServer"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &result); err == nil {
			r := result.Data.GetServer
			if r.ID != "" {
				srv := &orbSrvDetailData{
					ID:             r.ID,
					OrbID:          r.OrbID,
					Hostname:       r.Hostname,
					Model:          r.Model,
					Manufacturer:   r.Manufacturer,
					ServiceTag:     r.ServiceTag,
					RackPosition:   r.RackPosition,
					OobIP:          r.OobIP.Address,
					OobMAC:         r.OobMAC,
					CreatedAt:      r.CreatedAt,
					UpdatedAt:      r.UpdatedAt,
					Namespace:      r.Namespace.Name,
					DataCenterName: r.DataCenter.Name,
					Rack:           struct{ Name string }{Name: r.Rack.Name},
				}
				if r.IdracSettings != nil {
					srv.IdracSettings = &orbIdracData{
						FirmwareVersion:             r.IdracSettings.FirmwareVersion,
						OsToIdracPassThroughEnabled: r.IdracSettings.OsToIdracPassThroughEnabled,
						SshEnabled:                  r.IdracSettings.SshEnabled,
						UsbManagementPortEnabled:    r.IdracSettings.UsbManagementPortEnabled,
						IpmiEnabled:                 r.IdracSettings.IpmiEnabled,
						LockdownModeEnabled:         r.IdracSettings.LockdownModeEnabled,
						DhcpEnabled:                 r.IdracSettings.DhcpEnabled,
						RacadmEnabled:               r.IdracSettings.RacadmEnabled,
					}
				}
				for _, ctrl := range r.StorageControllers {
					sc := orbStorageControllerData{Name: ctrl.Name}
					for _, dev := range ctrl.StorageDevices {
						sc.StorageDevices = append(sc.StorageDevices, orbStorageDeviceData{
							Name:          dev.Name,
							CapacityBytes: dev.CapacityBytes,
							Manufacturer:  dev.Manufacturer,
							SerialNumber:  dev.SerialNumber,
							WWN:           dev.WWN,
						})
					}
					srv.StorageControllers = append(srv.StorageControllers, sc)
				}
				data.Srv = srv
				if r.Hostname != "" {
					data.PageTitle = r.Hostname
				} else if r.ServiceTag != "" {
					data.PageTitle = r.ServiceTag
				}
			}
		}
	}

	// Build per-field override map for this server so the template can show badges.
	if data.Srv != nil {
		if overrides, err := orb.LoadOverrides(s.cfg.DataDir); err == nil {
			m := make(map[string]string)
			for _, o := range overrides {
				if o.ResourceOrbID == data.Srv.OrbID {
					m[o.Field] = o.LocalValue
				}
			}
			data.OverridesByField = m
		}
	}

	return s.render(c, "server-detail", data)
}

// srvTab renders the server detail fragment for the given id.
// Called by the shared loadServerListTab() JS via HTMX GET /servers/:id.
func (s *Server) srvTab(c echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusFound, "/servers")
	}

	id := c.Param("id")
	dcCtx := c.QueryParam("dcCtx") == "1"

	raw, err := s.dgraphQuery(queryServerByIDFmt, map[string]any{"id": id})
	if err != nil {
		s.logger.Warn("dgraph server query failed", "err", err)
	}

	var srv orbSrvTabData
	if raw != nil {
		var result struct {
			Data struct {
				GetServer orbServerQueryResponse `json:"getServer"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &result); err == nil {
			r := result.Data.GetServer
			srv = orbSrvTabData{
				ID:             r.ID,
				OrbID:          r.OrbID,
				Hostname:       r.Hostname,
				Model:          r.Model,
				Manufacturer:   r.Manufacturer,
				ServiceTag:     r.ServiceTag,
				RackPosition:   r.RackPosition,
				OobIP:          r.OobIP.Address,
				OobMAC:         r.OobMAC,
				CreatedAt:      r.CreatedAt,
				UpdatedAt:      r.UpdatedAt,
				Namespace:      struct{ Name string }{Name: r.Namespace.Name},
				Rack:           struct{ Name string }{Name: r.Rack.Name},
				DataCenterID:   r.DataCenter.ID,
				DataCenterName: r.DataCenter.Name,
				ShowDCBack:     dcCtx,
			}
			if r.IdracSettings != nil {
				srv.IdracSettings = &orbIdracData{
					FirmwareVersion:             r.IdracSettings.FirmwareVersion,
					OsToIdracPassThroughEnabled: r.IdracSettings.OsToIdracPassThroughEnabled,
					SshEnabled:                  r.IdracSettings.SshEnabled,
					UsbManagementPortEnabled:    r.IdracSettings.UsbManagementPortEnabled,
					IpmiEnabled:                 r.IdracSettings.IpmiEnabled,
					LockdownModeEnabled:         r.IdracSettings.LockdownModeEnabled,
					DhcpEnabled:                 r.IdracSettings.DhcpEnabled,
					RacadmEnabled:               r.IdracSettings.RacadmEnabled,
				}
			}
			for _, ctrl := range r.StorageControllers {
				sc := orbStorageControllerData{Name: ctrl.Name}
				for _, dev := range ctrl.StorageDevices {
					sc.StorageDevices = append(sc.StorageDevices, orbStorageDeviceData{
						Name:          dev.Name,
						CapacityBytes: dev.CapacityBytes,
						Manufacturer:  dev.Manufacturer,
						SerialNumber:  dev.SerialNumber,
						WWN:           dev.WWN,
					})
				}
				srv.StorageControllers = append(srv.StorageControllers, sc)
			}
		}
	}

	if overrides, err := orb.LoadOverrides(s.cfg.DataDir); err == nil {
		m := make(map[string]string)
		im := make(map[string]string)
		idracOrbID := srv.OrbID + "-idrac"
		for _, o := range overrides {
			if o.ResourceOrbID == srv.OrbID {
				m[o.Field] = o.LocalValue
			} else if o.ResourceOrbID == idracOrbID {
				im[o.Field] = o.LocalValue
			}
		}
		srv.OverridesByField = m
		srv.IdracOverridesByField = im
	}

	tmpl := s.templates["server-tab"]
	if s.devMode {
		var err error
		tmpl, err = orbtemplates.ParseFragment("web/orb/templates/partials/server-tab.gohtml")
		if err != nil {
			return fmt.Errorf("parse fragment: %w", err)
		}
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.Execute(c.Response().Writer, srv)
}

// dgraphQuery sends a GraphQL query to orb's local DGraph.
func (s *Server) dgraphQuery(query string, variables map[string]any) ([]byte, error) {
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(s.cfg.DGraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dgraph query: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return raw, nil
}
