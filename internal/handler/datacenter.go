package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

const getDataCenterQuery = `
  query GetDataCenter($orbId: String!) {
    getDataCenter(orbId: $orbId) {
      id
      name
      orbId
      createdBy
      createdAt
      updatedBy
      updatedAt
      version
      assetDataV2
      namespace
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

type DataCenter struct {
	dev       bool
	dgraphURL string
	fragment  *template.Template
	logger    *slog.Logger
	basePath  string
	// actions resolves per-request PageActions. orbital passes a closure that
	// reads can_mutate from the context; orb passes a const returning OrbActions.
	actions func(echo.Context) layout.PageActions
}

func NewDataCenter(dgraphURL string, dev bool, logger *slog.Logger, basePath string, actions func(echo.Context) layout.PageActions) *DataCenter {
	return &DataCenter{
		dgraphURL: dgraphURL,
		dev:       dev,
		fragment:  parseDataCenterFragment(),
		logger:    logger,
		basePath:  basePath,
		actions:   actions,
	}
}

func parseDataCenterFragment() *template.Template {
	return template.Must(template.ParseFiles(
		"web/templates/shared/partials/datacenter-tab.gohtml",
		"web/templates/shared/partials/audit-tab.gohtml",
		"web/templates/shared/components/edit-modal-datacenter.gohtml",
	))
}

// dcQueryResponse is the raw JSON shape returned by DGraph.
type dcQueryResponse struct {
	ID          string `json:"id"`
	OrbID       string `json:"orbId"`
	Name        string `json:"name"`
	CreatedBy   string `json:"createdBy"`
	CreatedAt   string `json:"createdAt"`
	UpdatedBy   string `json:"updatedBy"`
	UpdatedAt   string `json:"updatedAt"`
	Version     int    `json:"version"`
	AssetDataV2 string `json:"assetDataV2"`
	Namespace string `json:"namespace"`
	Racks     []struct {
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

type serverTabData struct {
	ID           string
	OrbID        string
	DomID        string // SafeDomID(OrbID) — used for HTML id attrs / CSS selectors
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
	ID           string
	OrbID        string
	DomID        string // SafeDomID(OrbID)
	Name         string
	CreatedBy    string
	CreatedAt    string
	UpdatedBy    string
	UpdatedAt    string
	Namespace   string
	ServerCount int
	Racks        []rackTabData
	Servers      []serverTabData
	Version      int
	AssetDataV2  string
	CurrentUser  string
	EditDataJSON    template.JS // pre-serialized JSON for the edit modal
	EditTargetsJSON template.JS // configitem-editor.js targets list
	BasePath     string
	Actions      layout.PageActions
	// AuditPanelID + RelatedOrbIDsCSV are consumed by the shared audit-tab
	// partial (web/templates/shared/partials/audit-tab.gohtml). DC's audit
	// panel does NOT aggregate child events, so RelatedOrbIDsCSV stays empty
	// and initDetailTabs falls back to data-orb-id.
	AuditPanelID     string
	RelatedOrbIDsCSV string
}

func (h *DataCenter) Tab(c echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusFound, h.basePath+"/")
	}

	// Path-param decoding is handled by middleware.DecodePathParams.
	orbID := c.Param("orbId")

	body, _ := json.Marshal(map[string]any{
		"query":     getDataCenterQuery,
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
			GetDataCenter dcQueryResponse `json:"getDataCenter"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	raw := result.Data.GetDataCenter
	// Missing DC unmarshals as zero-value (DGraph returns the field as absent);
	// detect and 404 instead of rendering a broken tab with empty IDs. Mirrors
	// the server + cluster handlers.
	if raw.OrbID == "" {
		return echo.NewHTTPError(http.StatusNotFound, "data center not found")
	}
	h.logger.Debug("dgraph decoded", "servers", len(raw.Servers), "racks", len(raw.Racks))

	serversByRack := make(map[string]int)
	for _, s := range raw.Servers {
		serversByRack[s.Rack.Name]++
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

	currentUser := actorFromContext(c)

	editFields := map[string]any{"name": raw.Name}
	if raw.AssetDataV2 != "" {
		var parsed any
		if err := json.Unmarshal([]byte(raw.AssetDataV2), &parsed); err == nil {
			editFields["assetDataV2"] = parsed
		} else {
			editFields["assetDataV2"] = raw.AssetDataV2
		}
	}
	editJSON, _ := json.Marshal(editFields)
	editTargets := configitems.BuildEditTargets("DataCenter", raw.OrbID, raw.Namespace, raw.Name)
	editTargetsJSON, _ := json.Marshal(editTargets)

	domID := SafeDomID(raw.OrbID)
	dc := dataCenterTabData{
		ID:           raw.ID,
		OrbID:        raw.OrbID,
		DomID:        domID,
		AuditPanelID: "dc-panel-audit-" + domID,
		Name:         raw.Name,
		CreatedBy:    raw.CreatedBy,
		CreatedAt:    raw.CreatedAt,
		UpdatedBy:    raw.UpdatedBy,
		UpdatedAt:    raw.UpdatedAt,
		Namespace:   raw.Namespace,
		ServerCount:  raw.ServersAggregate.Count,
		Version:      raw.Version,
		AssetDataV2:  prettyAssetData,
		CurrentUser:     currentUser,
		EditDataJSON:    template.JS(editJSON),
		EditTargetsJSON: template.JS(editTargetsJSON),
		BasePath:        h.basePath,
		Actions:         h.actions(c),
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
			DomID:        SafeDomID(s.OrbID),
			Name:         s.Name,
			Hostname:     s.Hostname,
			ServiceTag:   s.ServiceTag,
			Model:        s.Model,
			OobIP:        s.OobIP.Address,
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
	return tmpl.Execute(c.Response(), dc)
}

