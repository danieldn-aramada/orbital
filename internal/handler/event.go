package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/event"
	"github.com/labstack/echo/v4"
)

type EventHandler struct {
	db     *ent.Client
	logger *slog.Logger
}

func NewEventHandler(db *ent.Client, logger *slog.Logger) *EventHandler {
	return &EventHandler{db: db, logger: logger}
}

type eventItem struct {
	ID           string          `json:"id"`
	ResourceType string          `json:"resourceType"`
	ResourceID   string          `json:"resourceId"`
	ResourceName string          `json:"resourceName"`
	Type         string          `json:"type"`
	Actor        string          `json:"actor"`
	Timestamp    string          `json:"timestamp"`
	Details      json.RawMessage `json:"details,omitempty"`
}

// ListEvents returns a paginated list of audit events ordered by timestamp desc.
//
// @Summary     List audit events
// @Description Returns recorded mutation events. Supports limit/offset pagination and optional filtering by resource_type or resource_id.
// @Tags        graph
// @Produce     json
// @Param       limit         query int    false "Max results (default 100, max 500)"
// @Param       offset        query int    false "Pagination offset"
// @Param       resource_type query string false "Filter by resource type (e.g. Server)"
// @Param       resource_id   query string false "Filter by resource orbId"
// @Success     200 {object} map[string]interface{}
// @Router      /api/v1/events [get]
func (h *EventHandler) List(c echo.Context) error {
	limit := 100
	offset := 0
	if v := c.QueryParam("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	if v := c.QueryParam("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	q := h.db.Event.Query()
	if rt := c.QueryParam("resource_type"); rt != "" {
		q = q.Where(event.ResourceTypeEQ(rt))
	}
	if rid := c.QueryParam("resource_id"); rid != "" {
		q = q.Where(event.ResourceIDEQ(rid))
	}

	total, err := q.Clone().Count(c.Request().Context())
	if err != nil {
		return fmt.Errorf("count events: %w", err)
	}

	events, err := q.
		Order(event.ByTimestamp(sql.OrderDesc())).
		Limit(limit).
		Offset(offset).
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("query events: %w", err)
	}

	items := make([]eventItem, 0, len(events))
	for _, e := range events {
		items = append(items, eventItem{
			ID:           e.ID.String(),
			ResourceType: e.ResourceType,
			ResourceID:   e.ResourceID,
			ResourceName: e.ResourceName,
			Type:         string(e.Type),
			Actor:        e.Actor,
			Timestamp:    e.Timestamp.UTC().Format(time.RFC3339),
			Details:      e.Details,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"events": items,
		"total":  total,
	})
}
