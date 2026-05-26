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
		fragment: template.Must(template.ParseFiles("web/orbital/templates/partials/events-table.gohtml")),
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
	DiffHTML      template.HTML   `json:"-"`
}

type eventDetails struct {
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
	Before        map[string]any `json:"before"`
}

// diffableFields lists the fields to include in the before/after diff per resource type.
var diffableFields = map[string][]string{
	"DataCenter":        {"name", "assetDataV2"},
	"Server":            {"name", "hostname", "model", "manufacturer", "serviceTag", "rackPosition", "oobMAC"},
	"KubernetesCluster": {"name", "provider"},
	"EksaConfig":        {"name", "clusterType"},
}

type eventsFragmentData struct {
	Items []eventItem
	Total int
}

var skipVarsSet = map[string]bool{
	"updatedBy": true,
	"updatedAt": true,
	"id":        true,
}

// List returns a paginated list of audit events ordered by timestamp desc.
//
// @Summary     List audit events
// @Description Returns recorded mutation events. Supports limit/offset pagination and optional filtering by orbId, resource_type, resource_id, or operation_name. Returns JSON by default; returns an HTML table fragment when the HX-Request header is present.
// @Tags        audit
// @Produce     json
// @Param       limit          query int    false "Max results (default 100, max 500)"
// @Param       offset         query int    false "Pagination offset"
// @Param       orbId          query string false "Filter by resource orbId (e.g. alaska-dot:GRTLY24)"
// @Param       resource_type  query string false "Filter by resource type (e.g. DataCenter, Server)"
// @Param       resource_id    query string false "Filter by resource ID"
// @Param       operation_name query string false "Filter by operation name (e.g. UpdateServer)"
// @Success     200 {object} map[string]interface{}
// @Router      /api/v1/audit-log [get]
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
			var d eventDetails
			if len(e.Details) > 0 {
				json.Unmarshal(e.Details, &d) //nolint:errcheck
			}
			if d.Before != nil && len(e.ResourceTypes) > 0 {
				item.DiffHTML = buildDiffHTML(d.Before, d.Variables, e.ResourceTypes[0])
			}
			if item.DiffHTML == "" {
				item.VarSummary = buildVarSummary(e.Details)
			}
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
		var valStr string
		switch v.(type) {
		case string, float64, bool, int, int64:
			valStr = fmt.Sprintf("%v", v)
		default:
			if b, err := json.Marshal(v); err == nil {
				valStr = string(b)
			} else {
				valStr = fmt.Sprintf("%v", v)
			}
		}
		parts = append(parts, fmt.Sprintf("<span style=\"white-space:nowrap\"><strong>%s:</strong> %s</span>", template.HTMLEscapeString(k), template.HTMLEscapeString(valStr)))
	}
	if len(parts) == 0 {
		return "—"
	}
	return template.HTML(strings.Join(parts, "<br>"))
}

// buildDiffHTML computes a before/after line diff for diffable fields of the given
// resource type and returns colored HTML. Returns "" when nothing changed or no
// diffable fields are present.
func buildDiffHTML(before, variables map[string]any, resourceType string) template.HTML {
	fields := diffableFields[resourceType]
	if len(fields) == 0 {
		return ""
	}

	var sections strings.Builder
	for _, field := range fields {
		bv, inBefore := before[field]
		av, inVars := variables[field]
		if !inBefore || !inVars {
			continue
		}
		beforeStr := valStr(bv, av)
		afterStr := valStr(av, av)
		if beforeStr == afterStr {
			continue
		}
		beforeLines := prettyLines(beforeStr)
		afterLines := prettyLines(afterStr)
		diffLines := lineDiff(beforeLines, afterLines)

		sections.WriteString(`<div style="margin-bottom:0.5rem">`)
		sections.WriteString(`<strong style="font-size:0.7rem">` + template.HTMLEscapeString(field) + `</strong>`)
		sections.WriteString(`<pre style="font-size:0.7rem;margin:0.2rem 0 0;background:#fafafa;padding:0.4rem;overflow-x:auto;white-space:pre-wrap;word-break:break-all">`)
		for _, line := range diffLines {
			if len(line) == 0 {
				sections.WriteString("\n")
				continue
			}
			switch line[0] {
			case '+':
				sections.WriteString(`<span style="color:#1a7f37">` + template.HTMLEscapeString(line) + `</span>` + "\n")
			case '-':
				sections.WriteString(`<span style="color:#cf222e;font-style:italic">` + template.HTMLEscapeString(line) + `</span>` + "\n")
			default:
				sections.WriteString(template.HTMLEscapeString(line) + "\n")
			}
		}
		sections.WriteString(`</pre></div>`)
	}

	// iDRAC diff — when the before-snapshot includes idracSettings and the mutation included idracInput
	if resourceType == "Server" {
		beforeIdrac, hasBefore := before["idracSettings"].(map[string]any)
		afterIdracArr, _ := variables["idracInput"].([]any)
		if hasBefore && len(afterIdracArr) > 0 {
			afterIdrac, _ := afterIdracArr[0].(map[string]any)
			for _, field := range []string{
				"firmwareVersion", "sshEnabled", "ipmiEnabled", "lockdownModeEnabled",
				"osToIdracPassThroughEnabled", "usbManagementPortEnabled", "dhcpEnabled", "racadmEnabled",
			} {
				beforeStr := valStr(beforeIdrac[field], afterIdrac[field])
				afterStr := valStr(afterIdrac[field], afterIdrac[field])
				if beforeStr == afterStr {
					continue
				}
				diffLines := lineDiff(prettyLines(beforeStr), prettyLines(afterStr))
				sections.WriteString(`<div style="margin-bottom:0.5rem">`)
				sections.WriteString(`<strong style="font-size:0.7rem">idrac: ` + template.HTMLEscapeString(field) + `</strong>`)
				sections.WriteString(`<pre style="font-size:0.7rem;margin:0.2rem 0 0;background:#fafafa;padding:0.4rem;overflow-x:auto;white-space:pre-wrap;word-break:break-all">`)
				for _, line := range diffLines {
					if len(line) == 0 {
						sections.WriteString("\n")
						continue
					}
					switch line[0] {
					case '+':
						sections.WriteString(`<span style="color:#1a7f37">` + template.HTMLEscapeString(line) + `</span>` + "\n")
					case '-':
						sections.WriteString(`<span style="color:#cf222e;font-style:italic">` + template.HTMLEscapeString(line) + `</span>` + "\n")
					default:
						sections.WriteString(template.HTMLEscapeString(line) + "\n")
					}
				}
				sections.WriteString(`</pre></div>`)
			}
		}
	}

	result := sections.String()
	if result == "" {
		return ""
	}
	return template.HTML(result)
}

// prettyLines attempts JSON pretty-printing then splits on newlines.
func prettyLines(s string) []string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
			return strings.Split(string(pretty), "\n")
		}
	}
	return strings.Split(s, "\n")
}

// lineDiff computes a simple LCS-based line diff.
// Lines are prefixed with ' ' (context), '+' (added), or '-' (removed).
func lineDiff(before, after []string) []string {
	m, n := len(before), len(after)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if before[i-1] == after[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	var out []string
	i, j := m, n
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && before[i-1] == after[j-1]:
			out = append(out, " "+before[i-1])
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			out = append(out, "+"+after[j-1])
			j--
		default:
			out = append(out, "-"+before[i-1])
			i--
		}
	}
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}

// valStr converts v to a string for diff comparison. When v is nil it returns
// the zero-value string for the type of ref (the after-value), so that an
// unset DGraph field (nil) compares equal to the form's default zero value.
func valStr(v, ref any) string {
	if v == nil {
		switch ref.(type) {
		case float64, int, int64, json.Number:
			return "0"
		case bool:
			return "false"
		default:
			return ""
		}
	}
	return fmt.Sprintf("%v", v)
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
