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

	"github.com/labstack/echo/v4"
)

const getDataCenterQuery = `
  query GetDataCenter($id: ID!) {
    getDataCenter(id: $id) {
      id
      name
      orbId
      createdBy
      createdAt
      updatedBy
      updatedAt
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
        oobIP
        oobMAC
        rackPosition
        rack { name }
      }
    }
  }`

type DataCenter struct {
	dev       bool
	dgraphURL string
	fragment  *template.Template
	logger    *slog.Logger
}

func NewDataCenter(dgraphURL string, dev bool, logger *slog.Logger) *DataCenter {
	return &DataCenter{
		dgraphURL: dgraphURL,
		dev:       dev,
		fragment:  parseDataCenterFragment(),
		logger:    logger,
	}
}

func parseDataCenterFragment() *template.Template {
	return template.Must(template.ParseFiles(
		"web/templates/fragments/datacenter-tab.gohtml",
	))
}

// dcQueryResponse is the raw JSON shape returned by DGraph.
type dcQueryResponse struct {
	ID        string `json:"id"`
	OrbID     string `json:"orbId"`
	Name      string `json:"name"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
	UpdatedBy string `json:"updatedBy"`
	UpdatedAt string `json:"updatedAt"`
	Namespace struct {
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
		OobIP        string `json:"oobIP"`
		OobMAC       string `json:"oobMAC"`
		RackPosition int    `json:"rackPosition"`
		Rack         struct {
			Name string `json:"name"`
		} `json:"rack"`
	} `json:"servers"`
}

type serverTabData struct {
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

type rackTabData struct {
	ID          string
	OrbID       string
	Name        string
	ServerCount int
}

type dataCenterTabData struct {
	ID          string
	OrbID       string
	Name        string
	CreatedBy   string
	CreatedAt   string
	UpdatedBy   string
	UpdatedAt   string
	Namespace   struct{ Name string }
	ServerCount int
	Racks       []rackTabData
	Servers     []serverTabData
}

func (h *DataCenter) Tab(c echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusFound, "/")
	}

	if h.dev {
		time.Sleep(150 * time.Millisecond)
	}

	id := c.Param("id")

	body, _ := json.Marshal(map[string]any{
		"query":     getDataCenterQuery,
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
			GetDataCenter dcQueryResponse `json:"getDataCenter"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	raw := result.Data.GetDataCenter
	h.logger.Debug("dgraph decoded", "servers", len(raw.Servers), "racks", len(raw.Racks))

	serversByRack := make(map[string]int)
	for _, s := range raw.Servers {
		serversByRack[s.Rack.Name]++
	}

	dc := dataCenterTabData{
		ID:          raw.ID,
		OrbID:       raw.OrbID,
		Name:        raw.Name,
		CreatedBy:   raw.CreatedBy,
		CreatedAt:   raw.CreatedAt,
		UpdatedBy:   raw.UpdatedBy,
		UpdatedAt:   raw.UpdatedAt,
		Namespace:   struct{ Name string }{Name: raw.Namespace.Name},
		ServerCount: raw.ServersAggregate.Count,
	}
	for _, r := range raw.Racks {
		dc.Racks = append(dc.Racks, rackTabData{
			ID:          r.ID,
			OrbID:       r.OrbID,
			Name:        r.Name,
			ServerCount: serversByRack[r.Name],
		})
	}
	for _, s := range raw.Servers {
		dc.Servers = append(dc.Servers, serverTabData{
			ID:           s.ID,
			OrbID:        s.OrbID,
			Name:         s.Name,
			Hostname:     s.Hostname,
			ServiceTag:   s.ServiceTag,
			Model:        s.Model,
			OobIP:        s.OobIP,
			OobMAC:       s.OobMAC,
			RackPosition: s.RackPosition,
			Rack:         struct{ Name string }{Name: s.Rack.Name},
		})
	}

	tmpl := h.fragment
	if h.dev {
		tmpl = parseDataCenterFragment()
	}

	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.Execute(c.Response().Writer, dc)
}
