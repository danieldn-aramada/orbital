package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// configItemTypes lists every concrete type implementing the ConfigItem interface.
// Adding a new schema type requires adding it here.
var configItemTypes = []string{
	"DataCenter", "Rack", "Server",
	"IdracSettings", "ServerConfigurationProfile",
	"StorageController", "StorageDevice", "StorageVolume",
	"KubernetesCluster", "EksaConfig", "IPAddress",
}

type Inventory struct {
	dgraphURL string
}

func NewInventory(dgraphURL string) *Inventory {
	return &Inventory{dgraphURL: dgraphURL}
}

type inventoryItem struct {
	UID       string `json:"uid"`
	Type      string `json:"type"`
	OrbID     string `json:"orbId"`
	Name      string `json:"name"`
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
}

// List returns all ConfigItem nodes across all namespaces via a DQL query.
//
// Fields declared on the ConfigItem interface are stored in DGraph under the
// ConfigItem. predicate prefix (e.g. ConfigItem.orbId), not the concrete type
// prefix. dgraph.type includes both the concrete type name and the interface
// name ("ConfigItem"); we skip the interface name to surface the concrete type.
//
// @Summary     List all config items
// @Description Returns every node implementing ConfigItem, across all namespaces.
// @Tags        graph
// @Produce     json
// @Success     200 {object} map[string]interface{}
// @Router      /api/v1/inventory [get]
func (h *Inventory) List(c echo.Context) error {
	var typeClauses []string
	for _, t := range configItemTypes {
		typeClauses = append(typeClauses, fmt.Sprintf("type(%s)", t))
	}

	dql := fmt.Sprintf(`{
  inventory(func: has(dgraph.type)) @filter(%s) {
    uid
    dgraph.type
    ConfigItem.orbId
    ConfigItem.name
    ConfigItem.createdBy
    ConfigItem.createdAt
  }
}`, strings.Join(typeClauses, " OR "))

	payload, _ := json.Marshal(map[string]string{"query": dql})
	dqlURL := strings.TrimSuffix(h.dgraphURL, "/graphql") + "/query"
	resp, err := http.Post(dqlURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("dql query: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read dql response: %w", err)
	}

	var result struct {
		Data struct {
			Inventory []map[string]any `json:"inventory"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode dql response: %w", err)
	}

	items := make([]inventoryItem, 0, len(result.Data.Inventory))
	for _, node := range result.Data.Inventory {
		uid, _ := node["uid"].(string)

		// dgraph.type includes both the concrete type and the interface name.
		// Skip "ConfigItem" to get the concrete type.
		typeName := ""
		if types, ok := node["dgraph.type"].([]any); ok {
			for _, t := range types {
				if tn, _ := t.(string); tn != "ConfigItem" {
					typeName = tn
					break
				}
			}
		}

		orbID, _ := node["ConfigItem.orbId"].(string)
		name, _ := node["ConfigItem.name"].(string)
		createdBy, _ := node["ConfigItem.createdBy"].(string)
		createdAt, _ := node["ConfigItem.createdAt"].(string)

		items = append(items, inventoryItem{
			UID:       uid,
			Type:      typeName,
			OrbID:     orbID,
			Name:      name,
			CreatedBy: createdBy,
			CreatedAt: createdAt,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}
