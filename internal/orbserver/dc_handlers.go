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

// --- Page data types ---

type dcPageData struct {
	layout.Base
	PageTitle string
}

// dcTabData is the data model for the orb datacenter-tab fragment.
type dcTabData struct {
	ID          string
	OrbID       string
	Name        string
	CreatedBy   string
	CreatedAt   string
	UpdatedBy   string
	UpdatedAt   string
	Namespace   struct{ Name string }
	ServerCount int
	Racks       []orbRackTabData
	Servers     []orbServerTabData
	AssetDataV2 string
	Actions     layout.PageActions
}

type orbRackTabData struct {
	ID          string
	OrbID       string
	Name        string
	ServerCount int
}

type orbServerTabData struct {
	ID           string
	OrbID        string
	Name         string
	Hostname     string
	ServiceTag   string
	Model        string
	OobIP        string
	OobMAC       string
	RackPosition int
	Rack         struct{ Name string }
}

const orbGetDataCenterQuery = `
  query GetDataCenter($id: ID!) {
    getDataCenter(id: $id) {
      id
      name
      orbId
      createdBy
      createdAt
      updatedBy
      updatedAt
      assetDataV2
      namespace { name }
      racks(order: { asc: name }) {
        id
        orbId
        name
      }
      serversAggregate {
        count
      }
      servers(order: { asc: rackPosition }) {
        id
        orbId
        name
        hostname
        serviceTag
        model
        oobIP { address }
        oobMAC
        rackPosition
        rack { name }
      }
    }
  }`

// orbDCQueryResponse mirrors the DGraph JSON shape for GetDataCenter.
type orbDCQueryResponse struct {
	ID          string `json:"id"`
	OrbID       string `json:"orbId"`
	Name        string `json:"name"`
	CreatedBy   string `json:"createdBy"`
	CreatedAt   string `json:"createdAt"`
	UpdatedBy   string `json:"updatedBy"`
	UpdatedAt   string `json:"updatedAt"`
	AssetDataV2 string `json:"assetDataV2"`
	Namespace   struct {
		Name string `json:"name"`
	} `json:"namespace"`
	Racks []struct {
		ID    string `json:"id"`
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"racks"`
	ServersAggregate struct {
		Count int `json:"count"`
	} `json:"serversAggregate"`
	Servers []struct {
		ID           string `json:"id"`
		OrbID        string `json:"orbId"`
		Name         string `json:"name"`
		Hostname     string `json:"hostname"`
		ServiceTag   string `json:"serviceTag"`
		Model        string `json:"model"`
		OobIP        struct {
			Address string `json:"address"`
		} `json:"oobIP"`
		OobMAC       string `json:"oobMAC"`
		RackPosition int    `json:"rackPosition"`
		Rack         struct {
			Name string `json:"name"`
		} `json:"rack"`
	} `json:"servers"`
}

type serversPageData struct {
	layout.Base
	PageTitle string
}

// --- Page handlers ---

func (s *Server) dcPage(c echo.Context) error {
	b := s.orbBase(c)
	return s.render(c, "datacenter", dcPageData{Base: b, PageTitle: "Data Center"})
}

// dcTab renders the datacenter detail fragment for the given id.
// Called by the shared loadDataCenterTab() JS via HTMX GET /datacenters/:id.
func (s *Server) dcTab(c echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusFound, "/datacenter")
	}

	id := c.Param("id")

	body, _ := json.Marshal(map[string]any{
		"query":     orbGetDataCenterQuery,
		"variables": map[string]any{"id": id},
	})

	resp, err := http.Post(s.cfg.DGraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dgraph query: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var result struct {
		Data struct {
			GetDataCenter orbDCQueryResponse `json:"getDataCenter"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	raw := result.Data.GetDataCenter

	serversByRack := make(map[string]int)
	for _, sv := range raw.Servers {
		serversByRack[sv.Rack.Name]++
	}

	var prettyAssetData string
	if raw.AssetDataV2 != "" {
		var buf bytes.Buffer
		if err := json.Indent(&buf, []byte(raw.AssetDataV2), "", "  "); err == nil {
			prettyAssetData = buf.String()
		} else {
			prettyAssetData = raw.AssetDataV2
		}
	}

	dc := dcTabData{
		ID:          raw.ID,
		OrbID:       raw.OrbID,
		Name:        raw.Name,
		CreatedBy:   raw.CreatedBy,
		CreatedAt:   raw.CreatedAt,
		UpdatedBy:   raw.UpdatedBy,
		UpdatedAt:   raw.UpdatedAt,
		Namespace:   struct{ Name string }{Name: raw.Namespace.Name},
		ServerCount: raw.ServersAggregate.Count,
		AssetDataV2: prettyAssetData,
		Actions:     layout.OrbActions,
	}
	for _, r := range raw.Racks {
		dc.Racks = append(dc.Racks, orbRackTabData{
			ID:          r.ID,
			OrbID:       r.OrbID,
			Name:        r.Name,
			ServerCount: serversByRack[r.Name],
		})
	}
	for _, sv := range raw.Servers {
		dc.Servers = append(dc.Servers, orbServerTabData{
			ID:           sv.ID,
			OrbID:        sv.OrbID,
			Name:         sv.Name,
			Hostname:     sv.Hostname,
			ServiceTag:   sv.ServiceTag,
			Model:        sv.Model,
			OobIP:        sv.OobIP.Address,
			OobMAC:       sv.OobMAC,
			RackPosition: sv.RackPosition,
			Rack:         struct{ Name string }{Name: sv.Rack.Name},
		})
	}

	tmpl := s.templates["datacenter-tab"]
	if s.devMode {
		var err error
		tmpl, err = orbtemplates.ParseFragment("web/shared/templates/partials/datacenter-tab.gohtml")
		if err != nil {
			return fmt.Errorf("parse fragment: %w", err)
		}
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.Execute(c.Response().Writer, dc)
}

func (s *Server) serversPage(c echo.Context) error {
	b := s.orbBase(c)
	return s.render(c, "servers", serversPageData{Base: b, PageTitle: "Servers"})
}

// --- Divergence page ---

type divergencePageData struct {
	layout.Base
	PageTitle string
}

func (s *Server) divergencePage(c echo.Context) error {
	return s.render(c, "divergence", divergencePageData{
		Base:      s.orbBase(c),
		PageTitle: "Divergence Report",
	})
}

// --- Import history page ---

type importHistoryPageData struct {
	layout.Base
	PageTitle string
	History   []orb.ImportRecord
}

func (s *Server) importHistoryPage(c echo.Context) error {
	history, err := orb.LoadHistory(s.cfg.DataDir)
	if err != nil {
		s.logger.Warn("failed to load import history", "err", err)
		history = nil
	}
	b := s.orbBase(c)
	return s.render(c, "import-history", importHistoryPageData{
		Base:      b,
		PageTitle: "Import History",
		History:   history,
	})
}
