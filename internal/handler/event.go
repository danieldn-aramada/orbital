package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/event"
	"github.com/armada/orbital/ent/eventresource"
	"github.com/armada/orbital/ent/eventresourcetype"
	"github.com/labstack/echo/v4"
)

type EventHandler struct {
	db       *ent.Client
	logger   *slog.Logger
	fragment *template.Template
	basePath string
}

func NewEventHandler(db *ent.Client, logger *slog.Logger, basePath string) *EventHandler {
	return &EventHandler{
		db:       db,
		logger:   logger,
		fragment: template.Must(template.ParseFiles("web/templates/orbital/partials/events-table.gohtml")),
		basePath: basePath,
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
	EventCategory string          `json:"eventCategory"`
	VarSummary    template.HTML   `json:"-"`
	DiffHTML      template.HTML   `json:"-"`
}

type eventDetails struct {
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
	Before        map[string]any `json:"before"`
}

// skipDiffFields are metadata fields excluded from before/after diffs across
// every resource type. Everything else present in BOTH before and after is
// diffed generically — no per-type allowlist. Adding a new ConfigItem type
// requires no changes here; divergence-accept / DispatchMutation diffs work
// out of the box.
var skipDiffFields = map[string]bool{
	"id":         true,
	"version":    true,
	"orbId":      true,
	"namespace":  true,
	"createdAt":  true,
	"createdBy":  true,
	"updatedAt":  true,
	"updatedBy":  true,
	"ifVersion":  true,
}

type eventsFragmentData struct {
	Items            []eventItem
	Total            int
	ShownCount       int
	OrbIDQueryString string
	BasePath         string
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
// @Param       orbId          query string false "Filter by resource orbId (e.g. alaska-dot:GRTLY24). Repeatable, max 32."
// @Param       namespace      query string false "Filter by namespace prefix (e.g. \"colo\" matches every orbId starting with \"colo:\"). Cheap DC-scope filter that avoids enumerating child orbIds."
// @Param       since          query string false "RFC3339 lower bound (exclusive) on event timestamp"
// @Param       until          query string false "RFC3339 upper bound (inclusive) on event timestamp"
// @Param       resource_id    query string false "Filter by resource ID"
// @Param       resource_type  query string false "Filter by resource type (e.g. Server, DataCenter)"
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
	// orbId is repeatable: ?orbId=server&orbId=idrac&orbId=scp&... Single value
	// covers a node-specific filter; the list covers a UI panel that aggregates
	// events across a parent and its nested ConfigItems (e.g. a Server tab
	// pulling its IdracSettings / ServerConfigurationProfile / StorageControllers
	// in one fetch). Capped to defend against URL/query bloat.
	const maxOrbIDs = 32
	rawOrbIDs := c.QueryParams()["orbId"]
	// Drop empties so an attribute like data-related-orb-ids="" doesn't insert "".
	orbIDFilter := make([]string, 0, len(rawOrbIDs))
	for _, id := range rawOrbIDs {
		if id != "" {
			orbIDFilter = append(orbIDFilter, id)
		}
	}
	if len(orbIDFilter) > maxOrbIDs {
		orbIDFilter = orbIDFilter[:maxOrbIDs]
	}
	if len(orbIDFilter) > 0 {
		// Resource-scoped audit panels (DC, Server, etc.) must show only events
		// whose event_resources include the queried orbId. Management events
		// that affect a specific resource attach the resource as a resourceID
		// (e.g. exportSubgraph attaches the DataCenter; restoreBackup attaches
		// every affected DC; resolveDivergence attaches the resolved entity).
		// Management events with no resource link (createBackup, authorizationDenied,
		// updateUserRole) are system-wide and belong only on the global audit log.
		// Auth events stay excluded from resource panels.
		q = q.Where(
			event.HasResourcesWith(eventresource.OrbIDIn(orbIDFilter...)),
			event.EventCategoryNEQ("auth"),
		)
	}
	if rid := c.QueryParam("resource_id"); rid != "" {
		q = q.Where(event.HasResourcesWith(eventresource.OrbIDEQ(rid)))
	}
	if rt := c.QueryParam("resource_type"); rt != "" {
		q = q.Where(event.HasResourceTypesWith(eventresourcetype.ResourceTypeEQ(rt)))
	}
	if cat := c.QueryParam("event_category"); cat != "" {
		// event_category=data restricts to intent mutations (used by the
		// publish-changes panel to exclude the surrounding system events
		// like `export` from the diff itself).
		q = q.Where(event.EventCategoryEQ(cat))
	}

	// Namespace prefix filter. Orbital's schema convention is "orbId =
	// namespace:name" with each DC owning one namespace, so matching orb_id
	// LIKE 'colo:%' captures every event on every entity under DC "colo:*"
	// without enumerating servers/idracs/clusters/etc. Auth events stay
	// excluded (they belong on the global audit log, not a resource panel).
	if ns := c.QueryParam("namespace"); ns != "" {
		q = q.Where(
			event.HasResourcesWith(eventresource.OrbIDHasPrefix(ns+":")),
			event.EventCategoryNEQ("auth"),
		)
	}

	// Timestamp window. `since` is exclusive, `until` is inclusive — pick
	// consecutive windows and no event is counted twice on the boundary.
	if v := c.QueryParam("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "since must be RFC3339: "+err.Error())
		}
		q = q.Where(event.TimestampGT(t))
	}
	if v := c.QueryParam("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "until must be RFC3339: "+err.Error())
		}
		q = q.Where(event.TimestampLTE(t))
	}

	total, err := q.Clone().Count(c.Request().Context())
	if err != nil {
		return fmt.Errorf("count events: %w", err)
	}

	events, err := q.
		Order(event.ByTimestamp(sql.OrderDesc())).
		Limit(limit).
		Offset(offset).
		WithResources().
		WithResourceTypes().
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("query events: %w", err)
	}

	items := make([]eventItem, 0, len(events))
	for _, e := range events {
		resTypes := resourceTypeNames(e.Edges.ResourceTypes)
		item := eventItem{
			ID:            e.ID.String(),
			Operations:    e.Operations,
			ResourceTypes: resTypes,
			ResourceIDs:   orbIDs(e.Edges.Resources),
			Actor:         e.Actor,
			Timestamp:     e.Timestamp.UTC().Format(time.RFC3339),
			Details:       e.Details,
			EventCategory: e.EventCategory,
		}
		if c.Request().Header.Get("HX-Request") == "true" {
			var d eventDetails
			if len(e.Details) > 0 {
				json.Unmarshal(e.Details, &d) //nolint:errcheck
			}
			if d.Before != nil && len(resTypes) > 0 {
				item.DiffHTML = buildDiffHTML(d.Before, d.Variables)
			}
			if item.DiffHTML == "" {
				item.VarSummary = buildVarSummary(e.Details)
			}
		}
		items = append(items, item)
	}

	if c.Request().Header.Get("HX-Request") == "true" {
		orbIDQS := strings.Join(func() []string {
			parts := make([]string, 0, len(orbIDFilter))
			for _, id := range orbIDFilter {
				parts = append(parts, "orbId="+id)
			}
			return parts
		}(), "&")
		tmpl := h.fragment
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return renderHTML(c, tmpl, "", eventsFragmentData{
			Items:            items,
			Total:            total,
			ShownCount:       len(items),
			OrbIDQueryString: orbIDQS,
			BasePath:         h.basePath,
		})
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

// buildDiffHTML computes a before/after line diff and returns colored HTML.
// Returns "" when nothing changed.
//
// Generic across resource types — no allowlist gating. The top-level field
// loop diffs every field present in BOTH before and after (minus skipDiffFields
// metadata). The nested-iDRAC block at the bottom fires whenever the before
// snapshot includes idracSettings AND the mutation included idracInput. New
// ConfigItem types produce diffs automatically without any edits here.
//
// Patch-style mutations (`update{Type}(input: {filter, set: $set})`) keep
// after-values nested under variables["set"]; user-driven flat-shape edits
// keep them at the top level. Both shapes work.
func buildDiffHTML(before, variables map[string]any) template.HTML {
	after := variables
	if set, ok := variables["set"].(map[string]any); ok {
		after = set
	}

	// Intersection of before and after keys, stable-sorted, metadata excluded.
	fields := make([]string, 0, len(before))
	for k := range before {
		if skipDiffFields[k] {
			continue
		}
		if _, ok := after[k]; !ok {
			continue
		}
		fields = append(fields, k)
	}
	sort.Strings(fields)

	var sections strings.Builder
	for _, field := range fields {
		bv := before[field]
		av := after[field]
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

	// (Historical: a hardcoded nested-iDRAC diff block lived here, required by
	// the UpdateServerAndIdrac compound mutation that wrapped Server + iDRAC
	// edits in a single body. The Server edit modal now dispatches parallel
	// updateServer + updateIdracSettings via configitem-editor.js — each
	// produces its own audit row with the generic diff above. The compound
	// path is gone; the special-case block is gone. Same generic diff handles
	// every type.)

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
//
// Complex values (maps, slices) are marshalled to JSON so that a raw JSON
// string from a DGraph before-snapshot and a parsed map[string]any from
// mutation variables compare equal when the underlying data is the same.
// Go's json.Marshal sorts map keys, so the output is stable.
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
	switch v.(type) {
	case map[string]any, []any:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	case string:
		// Normalize JSON strings so a raw string from DGraph and a parsed
		// map[string]any from mutation variables compare equal.
		s := v.(string)
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			if b, err := json.Marshal(parsed); err == nil {
				return string(b)
			}
		}
		return s
	}
	return fmt.Sprintf("%v", v)
}

// writeAuditEvent persists a single audit event row. Failures are logged and
// swallowed — audit writes must never block or fail a request.
// eventCategory must be "data" (entity mutations), "management" (system operations), or "auth" (login/logout events).
func writeAuditEvent(db *ent.Client, logger *slog.Logger, eventCategory, actor, opName string, operations, resourceTypes, resourceIDs []string, details map[string]any) {
	raw, _ := json.Marshal(details)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := db.Tx(ctx)
	if err != nil {
		logger.Warn("failed to begin audit transaction", "op", opName, "err", err)
		return
	}

	ec := tx.Event.Create().
		SetActor(actor).
		SetEventCategory(eventCategory).
		SetDetails(json.RawMessage(raw))
	if len(operations) > 0 {
		ec = ec.SetOperations(operations)
	}

	ev, err := ec.Save(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		logger.Warn("failed to write audit event", "op", opName, "err", err)
		return
	}

	if len(resourceIDs) > 0 {
		builders := make([]*ent.EventResourceCreate, len(resourceIDs))
		for i, rid := range resourceIDs {
			builders[i] = tx.EventResource.Create().
				SetOrbID(rid).
				SetEventID(ev.ID)
		}
		if err := tx.EventResource.CreateBulk(builders...).Exec(ctx); err != nil {
			tx.Rollback() //nolint:errcheck
			logger.Warn("failed to write audit event resources", "op", opName, "err", err)
			return
		}
	}

	if len(resourceTypes) > 0 {
		builders := make([]*ent.EventResourceTypeCreate, len(resourceTypes))
		for i, rt := range resourceTypes {
			builders[i] = tx.EventResourceType.Create().
				SetResourceType(rt).
				SetEventID(ev.ID)
		}
		if err := tx.EventResourceType.CreateBulk(builders...).Exec(ctx); err != nil {
			tx.Rollback() //nolint:errcheck
			logger.Warn("failed to write audit event resource types", "op", opName, "err", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		logger.Warn("failed to commit audit event", "op", opName, "err", err)
	}
}

func orbIDs(resources []*ent.EventResource) []string {
	ids := make([]string, len(resources))
	for i, r := range resources {
		ids[i] = r.OrbID
	}
	return ids
}

func resourceTypeNames(rts []*ent.EventResourceType) []string {
	names := make([]string, len(rts))
	for i, rt := range rts {
		names[i] = rt.ResourceType
	}
	return names
}
