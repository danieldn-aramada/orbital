package orbserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/armada/orbital/internal/orb"
	"github.com/labstack/echo/v4"
)

type overrideRequest struct {
	ResourceType  string `json:"resourceType"`
	ResourceID    string `json:"resourceId"`
	ResourceOrbID string `json:"resourceOrbId"`
	Field         string `json:"field"`
	LocalValue    string `json:"localValue"`
}

// @Summary     Record local override
// @Description Records a local field override for a Server or DataCenter resource. Writes the local value to DGraph and persists to overrides.json.
// @Tags        overrides
// @Accept      json
// @Produce     json
// @Param       body body overrideRequest true "Override request"
// @Success     200 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Router      /overrides [post]
func (s *Server) postOverride(c echo.Context) error {
	var req overrideRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	if req.ResourceID == "" || req.ResourceOrbID == "" || req.Field == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "resourceId, resourceOrbId and field are required"})
	}
	if req.ResourceType != "Server" && req.ResourceType != "DataCenter" && req.ResourceType != "IdracSettings" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "unsupported resourceType"})
	}

	// Read intent value from DGraph before recording — must capture orbital's value, not a prior local override.
	intentValue, err := s.fetchFieldFromDGraph(c.Request().Context(), req.ResourceType, req.ResourceID, req.Field)
	if err != nil {
		s.logger.Warn("failed to fetch intent value from DGraph", "err", err)
		intentValue = ""
	}

	// Persist to overrides.json. DGraph is not mutated — it remains a mirror of orbital's intent.
	o := orb.Override{
		ResourceType:  req.ResourceType,
		ResourceOrbID: req.ResourceOrbID,
		ResourceID:    req.ResourceID,
		Field:         req.Field,
		IntentValue:   intentValue,
		LocalValue:    req.LocalValue,
		OverriddenBy:  "local-admin",
		OverriddenAt:  time.Now().UTC(),
	}
	if err := orb.SaveOverride(s.cfg.DataDir, o); err != nil {
		s.logger.Error("failed to save override", "err", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to save override"})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// @Summary     List overrides
// @Description Returns all current local field overrides from overrides.json.
// @Tags        overrides
// @Produce     json
// @Success     200 {array}  orb.Override
// @Router      /overrides [get]
func (s *Server) getOverrides(c echo.Context) error {
	overrides, err := orb.LoadOverrides(s.cfg.DataDir)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, overrides)
}

func (s *Server) fetchFieldFromDGraph(ctx context.Context, resourceType, id, field string) (string, error) {
	var query string
	switch resourceType {
	case "Server":
		query = fmt.Sprintf(`{ getServer(id: %q) { %s } }`, id, field)
	case "DataCenter":
		query = fmt.Sprintf(`{ getDataCenter(id: %q) { %s } }`, id, field)
	case "IdracSettings":
		query = fmt.Sprintf(`{ getServer(id: %q) { idracSettings { %s } } }`, id, field)
	default:
		return "", fmt.Errorf("unknown resource type: %s", resourceType)
	}

	body, _ := json.Marshal(map[string]any{"query": query})
	resp, err := http.Post(s.cfg.DGraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}

	dataBlock, _ := result["data"].(map[string]any)
	var obj map[string]any
	switch resourceType {
	case "Server":
		obj, _ = dataBlock["getServer"].(map[string]any)
	case "DataCenter":
		obj, _ = dataBlock["getDataCenter"].(map[string]any)
	case "IdracSettings":
		serverObj, _ := dataBlock["getServer"].(map[string]any)
		if serverObj != nil {
			obj, _ = serverObj["idracSettings"].(map[string]any)
		}
	}
	if obj == nil {
		return "", nil
	}
	// DGraph returns booleans as bool, not string.
	if strVal, ok := obj[field].(string); ok {
		return strVal, nil
	}
	if boolVal, ok := obj[field].(bool); ok {
		return fmt.Sprintf("%t", boolVal), nil
	}
	return "", nil
}

