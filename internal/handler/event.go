package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/event"
	"github.com/labstack/echo/v4"
)

type EventHandler struct {
	db       *ent.Client
	logger   *slog.Logger
	fragment *template.Template
}

func NewEventHandler(db *ent.Client, logger *slog.Logger) *EventHandler {
	return &EventHandler{
		db:       db,
		logger:   logger,
		fragment: template.Must(template.ParseFiles("web/templates/fragments/events-table.gohtml")),
	}
}

type eventItem struct {
	ID            string          `json:"id"`
	Operations    []string        `json:"operations"`
	ResourceTypes []string        `json:"resourceTypes"`
	ResourceIDs   []string        `json:"resourceIds"`
	Actor         string          `json:"actor"`
	Timestamp     string          `json:"timestamp"`
	Details       json.RawMessage `json:"details,omitempty"`
	VarSummary    template.HTML   `json:"-"`
}

type eventsFragmentData struct {
	Items []eventItem
	Total int
}

var skipVarsSet = map[string]bool{
	"updatedBy": true,
	"updatedAt": true,
}

// List returns a paginated list of audit events ordered by timestamp desc.
//
// @Summary     List audit events
// @Description Returns recorded mutation events. Supports limit/offset pagination and optional filtering by orbId, resource_type, resource_id, or operation_name. Returns JSON by default; returns an HTML table fragment when the HX-Request header is present.
// @Tags        events
// @Produce     json
// @Param       limit          query int    false "Max results (default 100, max 500)"
// @Param       offset         query int    false "Pagination offset"
// @Param       orbId          query string false "Filter by resource orbId (e.g. alaska-dot:GRTLY24)"
// @Param       resource_type  query string false "Filter by resource type (e.g. DataCenter, Server)"
// @Param       resource_id    query string false "Filter by resource ID"
// @Param       operation_name query string false "Filter by operation name (e.g. UpdateServer)"
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
	if oid := c.QueryParam("orbId"); oid != "" {
		pattern := `%"` + oid + `"%`
		q = q.Where(func(s *sql.Selector) {
			s.Where(sql.P(func(b *sql.Builder) {
				b.WriteString("resource_ids::text LIKE ")
				b.Arg(pattern)
			}))
		})
	}
	if rid := c.QueryParam("resource_id"); rid != "" {
		pattern := `%"` + rid + `"%`
		q = q.Where(func(s *sql.Selector) {
			s.Where(sql.P(func(b *sql.Builder) {
				b.WriteString("resource_ids::text LIKE ")
				b.Arg(pattern)
			}))
		})
	}
	if rt := c.QueryParam("resource_type"); rt != "" {
		pattern := `%"` + rt + `"%`
		q = q.Where(func(s *sql.Selector) {
			s.Where(sql.P(func(b *sql.Builder) {
				b.WriteString("resource_types::text LIKE ")
				b.Arg(pattern)
			}))
		})
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
		item := eventItem{
			ID:            e.ID.String(),
			Operations:    e.Operations,
			ResourceTypes: e.ResourceTypes,
			ResourceIDs:   e.ResourceIds,
			Actor:         e.Actor,
			Timestamp:     e.Timestamp.UTC().Format(time.RFC3339),
			Details:       e.Details,
		}
		if c.Request().Header.Get("HX-Request") == "true" {
			item.VarSummary = buildVarSummary(e.Details)
		}
		items = append(items, item)
	}

	if c.Request().Header.Get("HX-Request") == "true" {
		tmpl := h.fragment
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return tmpl.Execute(c.Response().Writer, eventsFragmentData{Items: items, Total: total})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"events": items,
		"total":  total,
	})
}

func buildVarSummary(raw json.RawMessage) template.HTML {
	if raw == nil {
		return "—"
	}
	var d struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Variables) == 0 {
		return "—"
	}
	var parts []string
	for k, v := range d.Variables {
		if skipVarsSet[k] {
			continue
		}
		parts = append(parts, fmt.Sprintf("<span style=\"white-space:nowrap\"><strong>%s:</strong> %v</span>", template.HTMLEscapeString(k), template.HTMLEscapeString(fmt.Sprintf("%v", v))))
	}
	if len(parts) == 0 {
		return "—"
	}
	return template.HTML(strings.Join(parts, "<br>"))
}

// writeAuditEvent persists a single audit event row. Failures are logged and
// swallowed — audit writes must never block or fail a request.
func writeAuditEvent(db *ent.Client, logger *slog.Logger, actor, opName string, operations, resourceTypes, resourceIDs []string, details map[string]any) {
	raw, _ := json.Marshal(details)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := db.Event.Create().
		SetActor(actor).
		SetDetails(json.RawMessage(raw))

	if len(operations) > 0 {
		c = c.SetOperations(operations)
	}
	if len(resourceTypes) > 0 {
		c = c.SetResourceTypes(resourceTypes)
	}
	if len(resourceIDs) > 0 {
		c = c.SetResourceIds(resourceIDs)
	}

	if err := c.Exec(ctx); err != nil {
		logger.Warn("failed to write audit event", "op", opName, "err", err)
	}
}
