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
	"entgo.io/ent/dialect/sql/sqljson"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/auditevent"
	"github.com/armada/orbital/ent/auditeventresource"
	"github.com/armada/orbital/ent/auditeventresourcetype"
	"github.com/armada/orbital/ent/predicate"
	"github.com/armada/orbital/internal/metrics"
	"github.com/armada/orbital/web"
	"github.com/labstack/echo/v4"
)

// auditTimeFields: the event's createdAt is stored in the `timestamp` column.
var auditTimeFields = TimeFields{"createdAt": auditevent.FieldTimestamp}

type AuditHandler struct {
	db       *ent.Client
	logger   *slog.Logger
	fragment *template.Template
	basePath string
}

func NewEventHandler(db *ent.Client, logger *slog.Logger, basePath string) *AuditHandler {
	return &AuditHandler{
		db:       db,
		logger:   logger,
		fragment: template.Must(template.ParseFS(web.Dir(), "templates/orbital/partials/events-table.gohtml")),
		basePath: basePath,
	}
}

// auditLogResponse is the JSON body of GET /api/v1/audit-log.
type auditLogResponse struct {
	Events []eventItem `json:"events"`
	// Total is the count of events matching the filters, ignoring limit/offset.
	Total int `json:"total" example:"42"`
}

// eventItem is one audit event in the audit-log response.
type eventItem struct {
	ID            string          `json:"id"            example:"3d6bb15f-8c4c-45f0-8a6c-939b6f9cc512"`
	Operations    []string        `json:"operations"`    // DGraph mutation fields, e.g. ["updateVeleroBackup"]
	ResourceTypes []string        `json:"resourceTypes"` // ConfigItem types touched, e.g. ["VeleroBackup"]
	ResourceIDs   []string        `json:"resourceIds"`   // orbIds touched
	Actor         string          `json:"actor"         example:"asharma@armada.ai"`
	CreatedAt     string          `json:"createdAt"     example:"2026-07-29T17:26:55.418293Z"`
	Timestamp     string          `json:"timestamp"     example:"2026-07-29T17:26:55.418293Z"` // Deprecated: same value as createdAt.
	Details       json.RawMessage `json:"details,omitempty" swaggertype:"object"`              // raw {operationName, query, variables, before}
	EventCategory string          `json:"eventCategory" example:"data"`                        // data | management | auth
	// CloudTrail parity. All four are omitempty: absent means "no HTTP request
	// behind this event" (a background writer), which is different from empty.
	EventSource     string         `json:"eventSource,omitempty"     example:"graphql"`          // graphql | rest | internal
	SourceIPAddress string         `json:"sourceIpAddress,omitempty" example:"10.1.2.3"`         // caller address
	RequestID       string         `json:"requestId,omitempty"       example:"a1b2c3d4"`         // correlates events from one request
	ActingClient    string         `json:"actingClient,omitempty" example:"aep-fleet-commander"` // service acting for the human in `actor`; absent when they are the same
	Authorization   *authorization `json:"authorization,omitempty"`                              // what authorized this write; absent on a direct write
	// Changes is the pre-computed field-level diff. **Present ONLY for a clean
	// single-entity update** (omitted otherwise via omitempty) — so its presence
	// is the client's signal that a field diff is available; no need to inspect
	// operations/before. Server-managed metadata (version/updatedAt/updatedBy/…)
	// and DGraph UIDs are already excluded. Same data the UI's colored diff renders.
	Changes    []fieldChange `json:"changes,omitempty"`
	VarSummary template.HTML `json:"-"`
	DiffHTML   template.HTML `json:"-"`
}

// authorization names the act that authorized a write — the change request that
// was merged, or the divergence entry that was resolved. Present only when the
// write passed a control; absent on a direct write.
// (Naming rationale — `type` over `kind`, `authorization` over `authorizedBy` —
// is in docs/reference/AUDIT.md, deliberately NOT here: a doc comment on an
// exported API type becomes the published swagger description, and an
// integrator does not need our reasoning.)
type authorization struct {
	// Which kind of act authorized the write.
	Type string `json:"type" example:"changeRequest" enums:"changeRequest,divergenceResolution"`
	// The act's identifier, resolvable through its own API — a change request
	// via GET /api/v1/change-requests/{id}, a divergence via its entry id.
	ID string `json:"id" example:"colo-58"`
}

// authorizationOf builds the response field from the row's columns. Returns nil
// when no authority was recorded, so the key is omitted rather than present-and-empty.
func authorizationOf(e *ent.AuditEvent) *authorization {
	if e.AuthorizationType == "" {
		return nil
	}
	return &authorization{Type: e.AuthorizationType, ID: e.AuthorizationID}
}

// fieldChange is one changed field in an event's diff: the field name plus its
// old and new values (raw JSON types — number, bool, string, object). Emitted in
// the `changes` array; also what the HTML panel renders.
type fieldChange struct {
	Field  string `json:"field"  example:"retentionDays"`
	Before any    `json:"before" swaggertype:"object"` // old value (e.g. 7)
	After  any    `json:"after"  swaggertype:"object"` // new value (e.g. 15)
}

type eventDetails struct {
	OperationName string         `json:"operationName"`
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	Before        map[string]any `json:"before"`
}

// skipDiffFields are metadata fields excluded from before/after diffs across
// every resource type. Everything else present in BOTH before and after is
// diffed generically — no per-type allowlist. Adding a new ConfigItem type
// requires no changes here; divergence-accept / DispatchMutation diffs work
// out of the box.
var skipDiffFields = map[string]bool{
	"id":        true,
	"version":   true,
	"orbId":     true,
	"namespace": true,
	"createdAt": true,
	"createdBy": true,
	"updatedAt": true,
	"updatedBy": true,
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
// @Description Audit trail of intent mutations, newest first. The **data** is append-only — orbital adds events and never updates or deletes them. This **endpoint** is read-only: there is no API to create or modify an event. Append-only is a property of orbital's write paths, not a tamper-evidence mechanism; see docs/audit-model.md § Integrity for what is and is not protected.
// @Description
// @Description **Scope a query** by combining filters: `orbId` (repeatable, **max 128** — over that the request is refused with `400 BAD_USER_INPUT`, never silently truncated) for a specific resource; `namespace` for a whole data center; `type`/`operation` to narrow; `createdAt_gte`/`createdAt_lte` (RFC3339; `_gt`/`_lt` for exclusive bounds) for a time window. To see everything under a server/cluster, fetch its subgraph orbIds from the GraphQL Topology API and pass them as repeatable `orbId` params (there is no single "cluster" scope — a child mutation records the child's orbId, not the parent's).
// @Description
// @Description **Render a diff:** when an event is a clean single-entity update it carries a `changes` array (`[{field, before, after}]`) with metadata and DGraph UIDs already excluded — render it directly. When `changes` is absent (bulk add, create, or a multi-operation event), there is no field diff; fall back to showing `operations` + `resourceIds`. The raw `details` (with `before`/`variables`) is always included for callers that want it.
// @Description
// @Description Returns JSON by default; returns an HTML table fragment when the `HX-Request` header is present (used by orbital's own UI). See docs/api-cheatsheet.md § "Audit log".
// @Tags        audit
// @Produce     json
// @Param       limit          query int    false "Max results (default 100, max 500)"
// @Param       offset         query int    false "Pagination offset"
// @Param       orbId          query []string false "Filter by resource orbId (e.g. alaska-dot:GRTLY24). Repeatable, max 128 — matches events touching ANY of them. Over 128 the request is refused (400), not truncated."
// @Param       namespace      query string false "Filter by namespace prefix (e.g. \"colo\" matches every orbId starting with \"colo:\"). Cheap DC-scope filter that avoids enumerating child orbIds."
// @Param       createdAt_gte  query string false "Events at or after this time (RFC3339)"
// @Param       createdAt_lte  query string false "Events at or before this time (RFC3339)"
// @Param       type           query []string false "Filter by resource type (e.g. Server, DataCenter). Repeatable, OR-ed." collectionFormat(multi)
// @Param       operation      query string false "Filter to events containing this operation (exact, case-sensitive; stored form is verb-lowercased, e.g. updateVeleroBackup)"
// @Param       category       query string false "Filter by event category: data, management or auth"
// @Param       since          query string false "Deprecated: use createdAt_gt."
// @Param       until          query string false "Deprecated: use createdAt_lte."
// @Param       resource_id    query string false "Deprecated: use orbId."
// @Param       resource_type  query string false "Deprecated: use type."
// @Param       operation_name query string false "Deprecated: use operation."
// @Success     200 {object} auditLogResponse
// @Failure     400 {object} errorResponse
// @Router      /api/v1/audit-log [get]
func (h *AuditHandler) List(c echo.Context) error {
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

	timePreds, err := TimeFilters[predicate.AuditEvent](c.QueryParams(), auditTimeFields)
	if err != nil {
		return WriteQueryError(c, err)
	}

	q := h.db.AuditEvent.Query()
	// orbId is repeatable: ?orbId=server&orbId=idrac&orbId=scp&... Single value
	// covers a node-specific filter; the list covers a UI panel that aggregates
	// events across a parent and its nested ConfigItems (e.g. a Server tab
	// pulling its IdracSettings / ServerConfigurationProfile / StorageControllers
	// in one fetch). Capped to defend against URL/query bloat.
	rawOrbIDs := c.QueryParams()["orbId"]
	// Drop empties so an attribute like data-subgraph-orb-ids="" doesn't insert "".
	orbIDFilter := make([]string, 0, len(rawOrbIDs))
	for _, id := range rawOrbIDs {
		if id != "" {
			orbIDFilter = append(orbIDFilter, id)
		}
	}
	if len(orbIDFilter) > maxOrbIDFilter {
		// Refused, not truncated. Truncating silently answered a narrower
		// question than the caller asked and looked exactly like a correct
		// answer: a Server audit tab whose subgraph exceeded the cap was
		// dropping its overflow children, and "no events for that disk" is
		// indistinguishable from "that disk was never queried".
		return writeError(c, http.StatusBadRequest, CodeBadUserInput,
			fmt.Sprintf("too many orbId filters: %d (max %d)", len(orbIDFilter), maxOrbIDFilter),
			fmt.Sprintf("Query at most %d orbIds at a time, or drop orbId and filter by namespace instead.", maxOrbIDFilter))
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
			auditevent.HasResourcesWith(auditeventresource.OrbIDIn(orbIDFilter...)),
			auditevent.EventCategoryNEQ("auth"),
		)
	}
	if rid := c.QueryParam("resource_id"); rid != "" {
		q = q.Where(auditevent.HasResourcesWith(auditeventresource.OrbIDEQ(rid)))
	}
	if types := queryValues(c, "type", "resource_type"); len(types) > 0 {
		q = q.Where(auditevent.HasResourceTypesWith(auditeventresourcetype.ResourceTypeIn(types...)))
	}
	if cat := queryParam(c, "category", "event_category"); cat != "" {
		// event_category=data restricts to intent mutations (used by the
		// publish-changes panel to exclude the surrounding system events
		// like `export` from the diff itself).
		q = q.Where(auditevent.EventCategoryEQ(cat))
	}
	if op := queryParam(c, "operation", "operation_name"); op != "" {
		// `operations` is a JSON array column; match events whose array contains
		// op. Postgres: operations::jsonb @> '"<op>"'. Exact and case-sensitive —
		// callers pass the stored form (verb lowercased + Type, e.g.
		// updateVeleroBackup), which is exactly what the `operations` field returns.
		q = q.Where(func(s *sql.Selector) {
			s.Where(sqljson.ValueContains(auditevent.FieldOperations, op))
		})
	}

	// Namespace prefix filter. Orbital's schema convention is "orbId =
	// namespace:name" with each DC owning one namespace, so matching orb_id
	// LIKE 'colo:%' captures every event on every entity under DC "colo:*"
	// without enumerating servers/idracs/clusters/etc. Auth events stay
	// excluded (they belong on the global audit log, not a resource panel).
	if ns := c.QueryParam("namespace"); ns != "" {
		q = q.Where(
			auditevent.HasResourcesWith(auditeventresource.OrbIDHasPrefix(ns+":")),
			auditevent.EventCategoryNEQ("auth"),
		)
	}

	q = q.Where(timePreds...)
	// Deprecated spellings: since = createdAt_gt, until = createdAt_lte.
	if v := c.QueryParam("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return writeError(c, http.StatusBadRequest, CodeBadUserInput,
				fmt.Sprintf("since must be an RFC3339 time, got %q", v), "Use createdAt_gt; since is deprecated.")
		}
		q = q.Where(auditevent.TimestampGT(t))
	}
	if v := c.QueryParam("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return writeError(c, http.StatusBadRequest, CodeBadUserInput,
				fmt.Sprintf("until must be an RFC3339 time, got %q", v), "Use createdAt_lte; until is deprecated.")
		}
		q = q.Where(auditevent.TimestampLTE(t))
	}

	total, err := q.Clone().Count(c.Request().Context())
	if err != nil {
		return fmt.Errorf("count events: %w", err)
	}

	events, err := q.
		Order(auditevent.ByTimestamp(sql.OrderDesc())).
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
			// Both sub-second, and both from the same value: createdAt
			// supersedes timestamp, and a deprecated field that disagreed with
			// its replacement would be worse than not having deprecated it.
			CreatedAt:     e.Timestamp.UTC().Format(time.RFC3339Nano),
			Timestamp:     e.Timestamp.UTC().Format(time.RFC3339Nano),
			Details:       e.Details,
			EventCategory: e.EventCategory,

			EventSource:     e.EventSource,
			SourceIPAddress: e.SourceIPAddress,
			RequestID:       e.RequestID,
			ActingClient:    e.ActingClient,
			Authorization:   authorizationOf(e),
		}
		var d eventDetails
		if len(e.Details) > 0 {
			json.Unmarshal(e.Details, &d) //nolint:errcheck
		}
		// Structured diff for the JSON API — present only for a clean
		// single-entity update (same guard the HTML panel uses below).
		if d.Before != nil && len(resTypes) > 0 {
			item.Changes = computeChanges(d.Before, d.Variables, d.Query)
		}
		if c.Request().Header.Get("HX-Request") == "true" {
			if d.Before != nil && len(resTypes) > 0 {
				item.DiffHTML = buildDiffHTML(d.Before, d.Variables, d.Query)
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

	return c.JSON(http.StatusOK, auditLogResponse{Events: items, Total: total})
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

// computeChanges is the single source of truth for "what changed" in a
// single-entity mutation: the fields present in BOTH before and after (minus
// skipDiffFields metadata) whose value actually changed, sorted by field name.
// Returns raw typed values so the JSON `changes` array carries real before/after
// (numbers, bools, strings). Both the API (`changes`) and the HTML audit panel
// (buildDiffHTML) derive from this, so they can never disagree about a diff.
//
// Patch-style mutations (`update{Type}(input: {filter, set: $set})`) keep
// after-values nested under a patch variable; user-driven flat-shape edits keep
// them at the top level. Both shapes work. Generic across resource types — new
// ConfigItem types diff automatically with no edits here.
//
// `query` is needed because the patch variable need not be called `set`. Reading
// only variables["set"] and falling back to the WHOLE variables map is the
// dangerous shape: for `set: $patch` the fallback intersects {"orbId","patch"}
// with the before-state, matches nothing, and renders an audit row with no
// changes at all — no error, while the write itself lands normally. Resolved
// through resolveSetMap, the same helper the stamper writes through. Audit rows
// persist `query` alongside `variables`, so historical rows resolve too.
func computeChanges(before, variables map[string]any, query string) []fieldChange {
	after := variables
	if set, _, ok := resolveSetMap(query, variables); ok {
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

	var changes []fieldChange
	for _, field := range fields {
		bv, av := before[field], after[field]
		if valStr(bv, av) == valStr(av, av) {
			continue // present in the set but not actually changed
		}
		changes = append(changes, fieldChange{Field: field, Before: bv, After: av})
	}
	return changes
}

// buildDiffHTML renders the field-level diff (computeChanges) as the colored HTML
// the audit panel shows. Returns "" when nothing changed. It is a pure renderer
// over computeChanges — the field selection lives there, so the HTML and the JSON
// `changes` array always agree.
func buildDiffHTML(before, variables map[string]any, query string) template.HTML {
	var sections strings.Builder
	for _, c := range computeChanges(before, variables, query) {
		beforeStr := valStr(c.Before, c.After)
		afterStr := valStr(c.After, c.After)
		beforeLines := prettyLines(beforeStr)
		afterLines := prettyLines(afterStr)
		diffLines := lineDiff(beforeLines, afterLines)

		sections.WriteString(`<div style="margin-bottom:0.5rem">`)
		sections.WriteString(`<strong style="font-size:0.7rem">` + template.HTMLEscapeString(c.Field) + `</strong>`)
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

// auditOrigin is WHERE a write came from: which surface, which caller, which
// HTTP request. CloudTrail parity — see docs/reference/AUDIT.md.
//
// A plain value, and that is the point. The audit write runs in a goroutine
// (`go h.auditMutation(...)`), and Echo POOLS AND REUSES its Context objects —
// holding one past the handler's return reads whatever request recycled it
// next, so the IP on an audit row could belong to a different caller. A wrong
// IP is worse than a missing one: it reads as fact. Capture at the boundary
// with originFromContext, pass the value.
//
// Zero value = an internal write with no request behind it, which is the honest
// record for the backup scheduler, the restore job and DispatchMutation.
type auditOrigin struct {
	Source    string // "graphql" | "rest" | "internal"
	IP        string // caller address; empty for internal writers
	RequestID string // X-Request-Id, correlating every event from one request
	// ActingClient is the OAuth client that presented the token, when it is not
	// the human named in `actor` — a trusted upstream service acting on a user's
	// behalf. Empty when the caller IS the subject, so the field means something
	// when present rather than being noise on every row.
	ActingClient string
	// AuthorizationType / AuthorizationID name the ACT that authorized this write
	// — a merged change request, an accepted divergence. Set only when one
	// exists, so presence means "this write passed a control" and absence means
	// it did not, rather than the field being noise on every row.
	//
	// This completes a trichotomy with details.bypassedPolicy, which records a
	// control that was SKIPPED. Reviewed, break-glass, ungated.
	AuthorizationType string
	AuthorizationID   string
}

// auditAuthorized is the origin for a write an internal dispatcher makes on
// behalf of a reviewed act.
//
// Source stays "internal" — the write did not come through the GraphQL proxy,
// and that distinction is what the field is for. But requestID IS carried, which
// looks like it contradicts "an async job records no request id" in AUDIT.md and
// does not: merge and divergence-resolve run INSIDE the HTTP request that
// triggered them, so a live request id exists. Export, restore and the scheduled
// backup are the async cases, and they still pass auditInternal().
func auditAuthorized(typ, id, requestID string) auditOrigin {
	return auditOrigin{
		Source:            "internal",
		RequestID:         requestID,
		AuthorizationType: typ,
		AuthorizationID:   id,
	}
}

// auditInternal is the origin for writes with no HTTP request behind them.
func auditInternal() auditOrigin { return auditOrigin{Source: "internal"} }

// originFromContext captures the request-scoped values an audit row needs,
// at the boundary, while the Context is still valid. source is the surface:
// "graphql" for the proxy, "rest" for /api/v1 handlers.
func originFromContext(c echo.Context, source string) auditOrigin {
	o := auditOrigin{
		Source:    source,
		IP:        c.RealIP(),
		RequestID: c.Response().Header().Get(echo.HeaderXRequestID),
	}
	// Only when the acting client differs from the subject. A user signing in
	// directly is their own actor, and stamping that on every row would train
	// readers to ignore the column.
	if ac, _ := c.Get("acting_client").(string); ac != "" {
		o.ActingClient = ac
	}
	return o
}

// writeAuditEvent persists a single audit event row. Failures are logged and
// swallowed — audit writes must never block or fail a request.
// eventCategory must be "data" (entity mutations), "management" (system operations), or "auth" (login/logout events).
func writeAuditEvent(db *ent.Client, logger *slog.Logger, eventCategory, actor, opName string, operations, resourceTypes, resourceIDs []string, details map[string]any, origin auditOrigin) {
	raw, _ := json.Marshal(details)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := db.Tx(ctx)
	if err != nil {
		metrics.RecordAuditWriteFailure("begin_tx")
		logger.Error("audit event LOST: could not begin transaction", "op", opName, "err", err)
		return
	}

	ec := tx.AuditEvent.Create().
		SetActor(actor).
		SetEventCategory(eventCategory).
		SetDetails(json.RawMessage(raw))
	if len(operations) > 0 {
		ec = ec.SetOperations(operations)
	}
	// Set only what we actually have. Writing "" would turn "no HTTP request
	// behind this event" into an empty-string value an operator filtering the
	// column has to learn to ignore.
	if origin.Source != "" {
		ec = ec.SetEventSource(origin.Source)
	}
	if origin.IP != "" {
		ec = ec.SetSourceIPAddress(origin.IP)
	}
	if origin.RequestID != "" {
		ec = ec.SetRequestID(origin.RequestID)
	}
	if origin.ActingClient != "" {
		ec = ec.SetActingClient(origin.ActingClient)
	}
	// Columns, matching every other record-metadata fact on this table. Set only
	// when an authority exists — absence is what distinguishes a direct write.
	if origin.AuthorizationType != "" {
		ec = ec.SetAuthorizationType(origin.AuthorizationType).SetAuthorizationID(origin.AuthorizationID)
	}

	ev, err := ec.Save(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		metrics.RecordAuditWriteFailure("event")
		logger.Error("audit event LOST: could not write event row", "op", opName, "err", err)
		return
	}

	if len(resourceIDs) > 0 {
		builders := make([]*ent.AuditEventResourceCreate, len(resourceIDs))
		for i, rid := range resourceIDs {
			builders[i] = tx.AuditEventResource.Create().
				SetOrbID(rid).
				SetAuditEventID(ev.ID)
		}
		if err := tx.AuditEventResource.CreateBulk(builders...).Exec(ctx); err != nil {
			tx.Rollback() //nolint:errcheck
			metrics.RecordAuditWriteFailure("resources")
			logger.Error("audit event LOST: could not write event resources", "op", opName, "err", err)
			return
		}
	}

	if len(resourceTypes) > 0 {
		builders := make([]*ent.AuditEventResourceTypeCreate, len(resourceTypes))
		for i, rt := range resourceTypes {
			builders[i] = tx.AuditEventResourceType.Create().
				SetResourceType(rt).
				SetAuditEventID(ev.ID)
		}
		if err := tx.AuditEventResourceType.CreateBulk(builders...).Exec(ctx); err != nil {
			tx.Rollback() //nolint:errcheck
			metrics.RecordAuditWriteFailure("resource_types")
			logger.Error("audit event LOST: could not write event resource types", "op", opName, "err", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		metrics.RecordAuditWriteFailure("commit")
		logger.Error("audit event LOST: could not commit", "op", opName, "err", err)
	}
}

func orbIDs(resources []*ent.AuditEventResource) []string {
	ids := make([]string, len(resources))
	for i, r := range resources {
		ids[i] = r.OrbID
	}
	return ids
}

func resourceTypeNames(rts []*ent.AuditEventResourceType) []string {
	names := make([]string, len(rts))
	for i, rt := range rts {
		names[i] = rt.ResourceType
	}
	return names
}
