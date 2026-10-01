package handler

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/web/data/component"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/armada/orbital/internal/web/data/page"
	"github.com/labstack/echo/v4"
)

// GenericRenderer serves /{slug} and /{slug}/{id} for ANY ConfigItem type.
//
// It lives apart from orbital's UI handler because ORB renders the same pages.
// The only orbital-specific things these handlers touched were `base` and
// `render` — each app owns its own template map and its own page chrome — so
// those are injected and everything else is shared. Two apps, one renderer; a
// second copy is how the two would drift.
type GenericRenderer struct {
	fields         *SharedFields
	dgraphURL      string
	logger         *slog.Logger
	basePath       string
	base           func(echo.Context) layout.Base
	actions        func(echo.Context) layout.PageActions
	render         func(c echo.Context, name string, data any) error
	renderFragment func(c echo.Context, page, fragment string, data any) error
}

// NewGenericRenderer builds a renderer for one app.
func NewGenericRenderer(
	fields *SharedFields,
	dgraphURL string,
	basePath string,
	logger *slog.Logger,
	base func(echo.Context) layout.Base,
	actions func(echo.Context) layout.PageActions,
	render func(c echo.Context, name string, data any) error,
	renderFragment func(c echo.Context, page, fragment string, data any) error,
) *GenericRenderer {
	return &GenericRenderer{
		fields: fields, dgraphURL: dgraphURL, basePath: basePath, logger: logger,
		base: base, actions: actions, render: render, renderFragment: renderFragment,
	}
}

// List renders /{slug} for any ConfigItem type.
func (g *GenericRenderer) List(c echo.Context) error {
	slug := c.Param("slug")
	data := page.Generic{Base: g.base(c), PageTitle: slug}

	views, err := g.fields.Views(c.Request().Context())
	if err != nil {
		g.logger.Warn("generic list: cannot resolve views", "slug", slug, "err", err)
		data.Unavailable = "Orbital cannot read the schema from DGraph, so it does not know what this page should show. It recovers on its own once DGraph is reachable — no restart needed."
		return g.render(c, "generic-list", data)
	}
	v, ok := viewBySlug(views, slug)
	if !ok {
		// A slug nothing declares is a 404, not an empty page: an empty page
		// would say "there are none of these", which is a different claim from
		// "there is no such kind".
		return echo.NewHTTPError(http.StatusNotFound, "no such view: "+slug)
	}
	data.View = v
	data.PageTitle = v.Label

	// An interface view's columns come partly from its implementations, so the
	// query needs to look them up by type.
	byType := make(map[string]configitems.View, len(views))
	for _, vv := range views {
		byType[vv.Type] = vv
	}
	display := func(typeName string) []string { return byType[typeName].Display }
	viewOf := func(typeName string) configitems.View { return byType[typeName] }

	label := g.fieldLabeller(v.Type)
	data.Columns = columnHeaders(listColumns(v, display), label)
	data.RefColumns = refHeaders(v.RefColumns, label)

	raw, err := runGraphQL(c.Request().Context(), g.dgraphURL, genericListQuery(v, viewOf, display, 500), "query"+v.Type)
	if err != nil {
		g.logger.Warn("generic list query failed", "type", v.Type, "err", err)
		data.Unavailable = "Could not read " + v.Label + " from DGraph: " + err.Error()
		return g.render(c, "generic-list", data)
	}
	var rows []map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			g.logger.Warn("generic list decode failed", "type", v.Type, "err", err)
			data.Unavailable = "Could not decode " + v.Label + " from DGraph."
			return g.render(c, "generic-list", data)
		}
	}
	for _, r := range rows {
		data.Rows = append(data.Rows, stringifyRow(r))
	}
	data.Facet = buildFacet(v, data.Columns, data.RefColumns, data.Rows)
	return g.render(c, "generic-list", data)
}

// buildFacet resolves the view's `facet:` field into the control the template
// renders: which column index it filters, and the values present in it.
//
// Returns nil when the view declares no facet, and also when the column holds
// fewer than two distinct values — a dropdown whose only option selects
// everything is a control that cannot do anything, and one per page adds up.
func buildFacet(v configitems.View, cols []page.ColumnHeader, refs []page.RefHeader, rows []map[string]any) *page.Facet {
	if v.Facet == "" {
		return nil
	}
	// Column index must match the template's header order exactly: Name, then
	// scalar columns, then reference columns, then Orb ID.
	idx, label, isRef := -1, "", false
	for i, c := range cols {
		if c.Field == v.Facet {
			idx, label = i+1, c.Label
		}
	}
	if idx < 0 {
		for i, r := range refs {
			if r.Field == v.Facet {
				idx, label, isRef = len(cols)+1+i, r.Label, true
			}
		}
	}
	if idx < 0 {
		return nil
	}

	seen := map[string]bool{}
	for _, row := range rows {
		val := row[v.Facet]
		if isRef {
			// A reference cell renders the related entity's name.
			m, ok := val.(map[string]any)
			if !ok {
				continue
			}
			val = m["name"]
		}
		s, ok := val.(string)
		if !ok || s == "" {
			continue
		}
		seen[s] = true
	}
	if len(seen) < 2 {
		return nil
	}
	opts := make([]string, 0, len(seen))
	for s := range seen {
		opts = append(opts, s)
	}
	sort.Strings(opts)
	// "All Data Centers", not "All Data Center". The column heading is
	// singular because it heads one cell; the empty option names the whole
	// set. configitems.Pluralize already handles the awkward endings — the
	// "IdracSettings is already plural" case is exactly why it exists.
	return &page.Facet{Label: label, All: "All " + configitems.Pluralize(label), Column: idx, Options: opts}
}

// Detail renders /{slug}/{orbId} for any ConfigItem type.
func (g *GenericRenderer) Detail(c echo.Context) error {
	slug, orbID := c.Param("slug"), c.Param("orbId")
	data := page.GenericDetail{Base: g.base(c), PageTitle: orbID, OrbID: orbID}
	if g.actions != nil {
		data.Actions = g.actions(c)
	}

	views, err := g.fields.Views(c.Request().Context())
	if err != nil {
		g.logger.Warn("generic detail: cannot resolve views", "slug", slug, "err", err)
		data.Unavailable = "Orbital cannot read the schema from DGraph, so it does not know what this page should show. It recovers on its own once DGraph is reachable — no restart needed."
		return g.render(c, "generic-detail", data)
	}
	v, ok := viewBySlug(views, slug)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "no such view: "+slug)
	}

	// The list this page belongs to is the one the reader came from, which for
	// an interface view is the INTERFACE — /clusters, not the concrete type's
	// own page. Captured before v is swapped below.
	data.ListSlug, data.ListLabel = v.Slug, v.Label

	if v.IsInterface {
		// An interface has no get query, so the concrete type is resolved from
		// the data. This is what lets one URL family — /clusters/<id> — serve
		// every implementation.
		concrete, rerr := resolveConcreteType(c.Request().Context(), g.dgraphURL, orbID)
		if rerr != nil {
			g.logger.Warn("generic detail: cannot resolve concrete type", "orbId", orbID, "err", rerr)
			data.View = v
			data.Unavailable = "Could not determine what kind of item " + orbID + " is: " + rerr.Error()
			return g.render(c, "generic-detail", data)
		}
		if concrete == "" {
			return echo.NewHTTPError(http.StatusNotFound, "not found: "+orbID)
		}
		cv, known := viewByType(views, concrete)
		if !known {
			return echo.NewHTTPError(http.StatusNotFound, "no view for type "+concrete)
		}
		v = cv
	}
	data.View = v
	if data.ListSlug == "" {
		data.ListSlug, data.ListLabel = v.Slug, v.Label
	}

	// The display lookup lets the query fetch an owned child's own fields, so
	// the editor has a subtree to work with rather than just a link.
	byType := make(map[string]configitems.View, len(views))
	for _, vv := range views {
		byType[vv.Type] = vv
	}
	display := func(typeName string) []string { return byType[typeName].Display }
	refColumns := func(typeName string) []configitems.ViewRefColumn { return byType[typeName].RefColumns }
	viewOf := func(typeName string) configitems.View { return byType[typeName] }

	raw, err := runGraphQL(c.Request().Context(), g.dgraphURL, genericDetailQuery(v, viewOf, display, refColumns, orbID), "get"+v.Type)
	if err != nil {
		// Retry WITHOUT reference columns before giving up.
		//
		// A reference column is one extra hop, and a single dangling edge
		// anywhere in the result kills the whole query: DGraph propagates a
		// missing non-nullable field to the ROOT, so one deleted node takes out
		// the entire page. The generic renderer traverses far more edges than a
		// hand-written page, which makes it much likelier to meet one.
		//
		// The page is worth more than the extra column, so degrade to it and
		// log loudly — the underlying data IS corrupt and somebody should fix
		// it, but not by staring at a blank page.
		noRefs := func(string) []configitems.ViewRefColumn { return nil }
		if retry, rerr := runGraphQL(c.Request().Context(), g.dgraphURL, genericDetailQuery(v, viewOf, display, noRefs, orbID), "get"+v.Type); rerr == nil {
			g.logger.Warn("generic detail: dropped reference columns after a query error — "+
				"this usually means a DANGLING EDGE, where something points at a deleted node",
				"type", v.Type, "orbId", orbID, "err", err)
			raw, err = retry, nil
			data.RefColumnsDropped = true
		}
	}
	if err != nil {
		g.logger.Warn("generic detail query failed", "type", v.Type, "orbId", orbID, "err", err)
		data.Unavailable = "Could not read this " + v.Type + " from DGraph: " + err.Error()
		return g.render(c, "generic-detail", data)
	}
	var entity map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &entity); err != nil {
			data.Unavailable = "Could not decode this " + v.Type + " from DGraph."
			return g.render(c, "generic-detail", data)
		}
	}
	if entity == nil {
		return echo.NewHTTPError(http.StatusNotFound, v.Type+" not found: "+orbID)
	}

	data.Entity = stringifyRow(entity)
	if n, ok := entity["name"].(string); ok && n != "" {
		data.PageTitle = n
	}
	data.CanMutate, _ = c.Get("can_mutate").(bool)
	data.CanDelete = data.CanMutate
	g.attachGenericEditor(c, &data, v, byType, entity, orbID)

	// The audit panel needs a DOM id unique on the page and the subtree it
	// covers. Both are harvested from what the query already fetched rather
	// than re-queried: the detail query selects every owned child with its
	// orbId, so a second round trip to DGraph would only re-derive it.
	label := g.fieldLabeller(v.Type)

	// Proposed-change marks. The machinery is entirely attribute-driven
	// (`data-field-orbid`, `data-field-values`, `data-field`, `.js-field-mark`
	// — see loadFieldMarks in orbital.js), so a generic page joins it by
	// emitting the same attributes; nothing about it was ever Server-specific.
	editable := make(map[string]bool, len(v.Fields))
	for _, f := range v.Fields {
		editable[f] = true
	}
	// Pretty-printing is keyed on jsonString, NOT on detailOnly: one is about
	// what the value holds, the other about where it appears. A JSON field that
	// is small enough to be a column should still render formatted on detail.
	isJSON := make(map[string]bool, len(v.JSONString))
	for _, f := range v.JSONString {
		isJSON[f] = true
	}
	detailOnly := make(map[string]bool, len(v.DetailOnly))
	for _, f := range v.DetailOnly {
		detailOnly[f] = true
	}
	for _, f := range v.Display {
		val := data.Entity[f]
		if isJSON[f] {
			val = prettyJSON(val)
		}
		data.FieldRows = append(data.FieldRows, page.FieldRow{
			Field:    f,
			Label:    label(f),
			Value:    val,
			Editable: editable[f],
			Blob:     isJSON[f] || detailOnly[f],
		})
	}
	data.FieldValuesJSON = rawValuesJSON(editableSubtree(v, entity))

	data.Meta = g.metaRows(v, entity)
	data.AuditPanelID = "generic-panel-audit-" + data.EditModal.DomID
	data.RelatedOrbIDsCSV = strings.Join(ownedSubtreeOrbIDs(orbID, v.Type, entity), ",")
	// Single relationships are split out of the tab list: owned ones get a box
	// that shows their data, everything else becomes a link row in the field
	// list. What is left — the LIST relationships — is what a tab actually is.
	owned := map[string]configitems.OwnedChild{}
	for _, oc := range configitems.OwnedChildren(v.Type) {
		owned[oc.ChildField] = oc
	}
	for _, tab := range v.Tabs {
		if tab.IsList || strings.Contains(tab.Field, ".") {
			continue
		}
		node, _ := entity[tab.Field].(map[string]any)
		// Owned AND exclusively so. A shared type — IPAddress, reachable from a
		// server, a node and two cluster fields — is a record in its own right,
		// and inlining it would claim an ownership no single parent has.
		if oc, isOwned := owned[tab.Field]; isOwned && configitems.ExclusivelyOwned(oc.ChildType) {
			// Rendered even when absent. "This cluster has no backup
			// configured" is a fact an operator needs; an omitted box says
			// nothing at all, and the two are easy to confuse.
			data.Owned = append(data.Owned, g.ownedBox(tab, oc, byType, node))
			continue
		}
		if node == nil {
			continue
		}
		id, _ := node["orbId"].(string)
		name, _ := node["name"].(string)
		if name == "" {
			name = refLabel(id)
		}
		data.Refs = append(data.Refs, page.GenericRef{
			Label: humanFieldLabel(tab.Field), Slug: tab.Slug, OrbID: id, Name: name,
		})
	}

	for _, tab := range v.Tabs {
		if !tab.IsList {
			continue
		}
		rows := rowsForTab(entity, tab.Field)
		if len(rows) == 0 {
			// A relationship with nothing on the other end is noise on a detail
			// page; the field list already says the relationship exists.
			continue
		}
		stamped := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			stamped = append(stamped, stringifyRow(r))
		}
		data.Tabs = append(data.Tabs, page.GenericTab{
			Label:      tabLabel(tab.Field, label),
			Slug:       tab.Slug,
			Field:      tab.Field,
			IsList:     tab.IsList,
			Columns:    columnHeaders(byType[tab.Type].ColumnFields(), g.fieldLabeller(tab.Type)),
			RefColumns: refHeaders(withoutBackReferences(tabRefColumns(byType[tab.Type], data.RefColumnsDropped), stamped, v.Type, orbID), g.fieldLabeller(tab.Type)),
			Rows:       stamped,
		})
	}

	// HTML fragment negotiation on the EXISTING route, never a sibling
	// /fragment path: the list page opens this same URL in a tab and wants just
	// the body. One handler, one template block, so the page and the tab cannot
	// drift.
	if c.Request().Header.Get("HX-Request") == "true" {
		return g.renderFragment(c, "generic-detail", "generic-detail-content", data)
	}
	return g.render(c, "generic-detail", data)
}

// attachGenericEditor wires the shared edit modal onto a generic detail page.
//
// The editor is the SAME one the bespoke pages use: one template
// (edit-modal.gohtml), one JS module (configitem-editor.js), and edit targets
// from configitems.BuildEditTargets, which stopped being per-type when its
// field list and metadata started coming from the schema. Nothing here is
// specific to a kind, which is the point — four near-identical openers in
// orbital.js are what this replaces.
//
// ROOT FIELDS ONLY for now. The detail query selects just `orbId name` for each
// relationship, so an owned child's own fields are not on the page and its edit
// target has no data behind it. BuildEditTargets still emits those targets and
// they are inert: with no subtree in the tree, `changed` is false and the editor
// never builds a mutation for them. Deepening the query is what makes owned
// children editable here, and it is deliberately a later step.
func (g *GenericRenderer) attachGenericEditor(c echo.Context, data *page.GenericDetail, v configitems.View, byType map[string]configitems.View, entity map[string]any, orbID string) {
	if !data.CanMutate {
		return
	}
	name, _ := entity["name"].(string)

	// The tree the JSON editor renders: EDITABLE scalars only, at the same paths
	// BuildEditTargets addresses.
	//
	// Editable, not display: a field the editor shows but cannot write is an
	// unknown key on submit, and configitem-editor.js refuses the whole save
	// naming it. Display-only fields belong on the page, not in the editor.
	//
	// `version` and the provenance fields are orbital's to write and are absent
	// for the same reason — showing them invites someone to type into them.
	editData := editableSubtree(v, entity)
	for _, oc := range configitems.OwnedChildren(v.Type) {
		childView, known := byType[oc.ChildType]
		if !known {
			continue
		}
		childRaw, ok := entity[oc.ChildField].(map[string]any)
		if !ok {
			continue // absent, or a list — lists are not edited inline
		}
		sub := editableSubtree(childView, childRaw)

		// A wrapper (ClusterBackup) has no scalars of its own; its GRANDchildren
		// are the edit targets, so the tree has to nest one level further or
		// those targets have nothing behind them.
		for _, gc := range configitems.OwnedChildren(oc.ChildType) {
			gcView, gcKnown := byType[gc.ChildType]
			if !gcKnown {
				continue
			}
			if gcRaw, ok := childRaw[gc.ChildField].(map[string]any); ok {
				sub[gc.ChildField] = editableSubtree(gcView, gcRaw)
			}
		}
		if len(sub) > 0 {
			editData[oc.ChildField] = sub
		}
	}
	data.HasEditable = len(editData) > 0
	if !data.HasEditable {
		return
	}
	editJSON, err := json.Marshal(editData)
	if err != nil {
		g.logger.Warn("generic editor: marshal edit data", "type", v.Type, "err", err)
		return
	}

	targets := configitems.BuildEditTargets(g.fields.Fields, g.fields.Meta, v.Type, orbID, namespaceOf(orbID), name)

	// Stamp the OCC version on EVERY reachable target, not just the root.
	//
	// A target the editor can write without a version is written UNGUARDED: a
	// concurrent edit is overwritten silently instead of refused. Once owned
	// children became reachable here, their targets were exactly that — the
	// same failure `edit_targets_invariant_test` was written for after MVCC was
	// off for every UI edit for two and a half months and nothing noticed.
	//
	// Each child's orbId and version come from the same fetch that populated the
	// tree, so a stamped version always describes the data on the page.
	targets = stampFetchedVersions(targets, entity, orbID)
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		g.logger.Warn("generic editor: marshal edit targets", "type", v.Type, "err", err)
		return
	}

	version := 0
	if vv, ok := entity["version"].(float64); ok {
		version = int(vv)
	}
	data.EditModal = component.EditModal{
		EditorUnavailable: editorUnavailableReason(g.fields.State()),
		Prefix:            "generic",
		Title:             "Edit " + v.Type + ": " + name,
		DomID:             SafeDomID(orbID),
		OrbID:             orbID,
		Version:           version,
		CurrentUser:       actorFromContext(c),
		Typename:          v.Type,
		EditDataJSON:      template.JS(editJSON),
		EditTargetsJSON:   template.JS(targetsJSON),
	}
}

// stampFetchedVersions applies the OCC version of every entity the detail query
// fetched — the root, its owned children, and a wrapper's grandchildren.
//
// It also OVERRIDES each target's derived orbId with the real one where the
// fetch found it. BuildEditTargets derives owned-child orbIds from a naming
// convention (`<ns>:<parent>-<suffix>`) because a child may not exist yet; when
// it does exist, the stored id is authoritative and a derived one that differs
// would upsert a phantom entity instead of editing the real one.
func stampFetchedVersions(targets []configitems.EditTarget, entity map[string]any, rootOrbID string) []configitems.EditTarget {
	stamp := func(ts []configitems.EditTarget, kind string, raw map[string]any) []configitems.EditTarget {
		orbID, _ := raw["orbId"].(string)
		if orbID == "" {
			return ts
		}
		if kind != "" {
			ts = configitems.OverrideEditTargetOrbID(ts, kind, orbID)
		}
		if vv, ok := raw["version"].(float64); ok {
			ts = configitems.StampEditTargetVersion(ts, orbID, int(vv))
		}
		return ts
	}

	targets = stamp(targets, "", entity)
	if vv, ok := entity["version"].(float64); ok {
		targets = configitems.StampEditTargetVersion(targets, rootOrbID, int(vv))
	}
	for _, t := range targets {
		if len(t.Path) == 0 {
			continue
		}
		raw := entity
		ok := true
		for _, seg := range t.Path {
			next, isMap := raw[seg].(map[string]any)
			if !isMap {
				ok = false
				break
			}
			raw = next
		}
		if ok {
			targets = stamp(targets, t.Kind, raw)
		}
	}
	return targets
}

// editableSubtree picks an entity's EDITABLE scalars out of what was fetched.
func editableSubtree(v configitems.View, raw map[string]any) map[string]any {
	out := make(map[string]any, len(v.Fields))
	for _, f := range v.Fields {
		if val, ok := raw[f]; ok && val != nil {
			out[f] = val
		}
	}
	return out
}

// namespaceOf returns the namespace half of an orbId (`<namespace>:<key>`).
// Owned-child orbIds are derived as `<namespace>:<parentName>-<suffix>`, so the
// editor needs it to address a child that does not exist yet.
func namespaceOf(orbID string) string {
	if i := strings.Index(orbID, ":"); i > 0 {
		return orbID[:i]
	}
	return ""
}

// tabRefColumns returns a tab's reference columns, or none when the retry
// dropped them — headers over cells that can never fill read as missing data
// rather than as an omission.
func tabRefColumns(v configitems.View, dropped bool) []configitems.ViewRefColumn {
	if dropped {
		return nil
	}
	return v.RefColumns
}

// Fallback serves /{slug} and /{slug}/{id} for paths no other route claimed.
//
// Registered with Echo's RouteNotFound rather than as `/:slug` routes, and that
// is not a detail. A param route at the ROOT makes Echo answer 405 for requests
// that should be 404: the param node matches the path, the method does not, and
// unrelated assertions about routes that do not exist start failing. As a
// fallback the generic renderer cannot affect any real route's resolution at
// all — "a static page always wins" becomes structural instead of a consequence
// of registration order.
func (g *GenericRenderer) Fallback(c echo.Context) error {
	// Only GET renders a page; anything else on an unmatched path is a 404, as
	// it was before.
	if c.Request().Method != http.MethodGet {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	p := strings.Trim(c.Request().URL.Path, "/")
	if base := strings.Trim(g.basePath, "/"); base != "" {
		p = strings.TrimPrefix(strings.TrimPrefix(p, base), "/")
	}
	if p == "" {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	parts := strings.SplitN(p, "/", 3)
	switch len(parts) {
	case 1:
		c.SetParamNames("slug")
		c.SetParamValues(parts[0])
		return g.List(c)
	case 2:
		c.SetParamNames("slug", "orbId")
		c.SetParamValues(parts[0], parts[1])
		return g.Detail(c)
	default:
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
}

// ownedSubtreeOrbIDs returns the entity's orbId plus the orbId of every owned
// child and grandchild present in the fetched entity, in a stable order.
//
// Derived from the response rather than the schema so it can never claim an
// orbId the page did not actually load — an audit query for a child that was
// not fetched would silently widen what the panel reports.
func ownedSubtreeOrbIDs(rootOrbID, rootType string, entity map[string]any) []string {
	out := []string{rootOrbID}
	seen := map[string]bool{rootOrbID: true}
	var walk func(typeName string, node map[string]any)
	walk = func(typeName string, node map[string]any) {
		for _, oc := range configitems.OwnedChildren(typeName) {
			child, ok := node[oc.ChildField].(map[string]any)
			if !ok {
				continue
			}
			if id, _ := child["orbId"].(string); id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
			walk(oc.ChildType, child)
		}
	}
	walk(rootType, entity)
	return out
}

// metaRows builds the provenance box: one row per ConfigItem interface field,
// in the view's declared order, with the label and the local-time decision
// already made.
// fieldLabeller returns a label function for a type, honouring `label:`
// annotations and falling back to title-casing.
func (g *GenericRenderer) fieldLabeller(typeName string) func(string) string {
	declared := map[string]string{}
	for _, f := range g.fields.Meta(typeName).Fields {
		if l := configitems.LabelFor(f.Doc); l != "" {
			declared[f.Name] = l
		}
	}
	return func(field string) string {
		if l, ok := declared[field]; ok {
			return l
		}
		return humanFieldLabel(field)
	}
}

func (g *GenericRenderer) metaRows(v configitems.View, entity map[string]any) []page.MetaRow {
	kinds := map[string]string{}
	for _, f := range g.fields.Meta(v.Type).Fields {
		kinds[f.Name] = f.TypeName
	}
	// The SAME labeller the field table and every column header uses, so there
	// is one label mechanism rather than two. This function had its own Go map
	// of overrides ({"orbId": "Orb ID"}) that duplicated the `label:`
	// annotation — the annotation now carries it, on ConfigItem.orbId.
	label := g.fieldLabeller(v.Type)
	rows := make([]page.MetaRow, 0, len(v.Meta))
	for _, f := range v.Meta {
		// %v, not a string assertion: `version` is an Int and comes back as a
		// number, and asserting would have rendered it blank.
		val := ""
		if sv := stringify(entity[f]); sv != nil {
			val = fmt.Sprintf("%v", sv)
		}
		rows = append(rows, page.MetaRow{
			Label:       label(f),
			Value:       val,
			IsTimestamp: kinds[f] == "DateTime",
		})
	}
	return rows
}

// ownedBox renders one owned child inline: its own display fields, plus a
// nested box per owned grandchild.
//
// One level of recursion, matching the query: ownedChildFields selects
// grandchildren because a wrapper's children ARE its content, and stops there
// because a deeper walk on a cyclic schema does not terminate.
func (g *GenericRenderer) ownedBox(tab configitems.ViewTab, oc configitems.OwnedChild, byType map[string]configitems.View, node map[string]any) page.GenericOwned {
	cv := byType[oc.ChildType]
	id, _ := node["orbId"].(string)
	name, _ := node["name"].(string)
	box := page.GenericOwned{
		Label: humanFieldLabel(tab.Field),
		Slug:  tab.Slug,
		OrbID: id,
		Name:  name,
	}
	if node == nil {
		// Absent: still list the sub-kinds this child would have, each marked
		// unconfigured, so the page shows the SHAPE of what is missing.
		for _, gc := range configitems.OwnedChildren(oc.ChildType) {
			box.Children = append(box.Children, page.GenericOwned{Label: humanFieldLabel(gc.ChildField)})
		}
		return box
	}
	box.FieldValuesJSON = rawValuesJSON(editableSubtree(cv, node))
	childLabel := g.fieldLabeller(oc.ChildType)
	editable := make(map[string]bool, len(cv.Fields))
	for _, f := range cv.Fields {
		editable[f] = true
	}
	for _, f := range cv.Display {
		val := ""
		if sv := stringify(node[f]); sv != nil {
			val = fmt.Sprintf("%v", sv)
		}
		row := page.MetaRow{Label: childLabel(f), Value: val}
		if editable[f] {
			row.Field = f
		}
		box.Fields = append(box.Fields, row)
	}
	for _, gc := range configitems.OwnedChildren(oc.ChildType) {
		child, ok := node[gc.ChildField].(map[string]any)
		if !ok {
			// A sub-kind that was never configured. Shown as such rather than
			// omitted: "no velero backup" and "velero backup disabled" are
			// different answers, and an absent row gives neither.
			box.Children = append(box.Children, page.GenericOwned{Label: humanFieldLabel(gc.ChildField)})
			continue
		}
		gv := byType[gc.ChildType]
		gid, _ := child["orbId"].(string)
		gname, _ := child["name"].(string)
		sub := page.GenericOwned{
			Label:           humanFieldLabel(gc.ChildField),
			Slug:            gv.Slug,
			OrbID:           gid,
			Name:            gname,
			FieldValuesJSON: rawValuesJSON(editableSubtree(gv, child)),
		}
		gLabel := g.fieldLabeller(gc.ChildType)
		gEditable := make(map[string]bool, len(gv.Fields))
		for _, f := range gv.Fields {
			gEditable[f] = true
		}
		for _, f := range gv.Display {
			val := ""
			if sv := stringify(child[f]); sv != nil {
				val = fmt.Sprintf("%v", sv)
			}
			row := page.MetaRow{Label: gLabel(f), Value: val}
			if gEditable[f] {
				row.Field = f
			}
			sub.Fields = append(sub.Fields, row)
		}
		box.Children = append(box.Children, sub)
	}
	return box
}

// listColumns is the scalar column list for a list page: the view's own
// scalars, then any an implementation adds. Implementation order is the
// schema's, so the columns do not reshuffle between requests.
func listColumns(v configitems.View, display func(string) []string) []string {
	cols := v.ColumnFields()
	seen := map[string]bool{}
	for _, f := range cols {
		seen[f] = true
	}
	for _, impl := range v.Implementations {
		for _, f := range display(impl) {
			if !seen[f] {
				seen[f] = true
				cols = append(cols, f)
			}
		}
	}
	// Re-apply the pin to the UNION. An implementation-only field can be
	// pinned — `clusterType` is EKSA's and the cluster pin names it third —
	// but the interface's own Display cannot contain it, so ordering before
	// the union left it appended at the end, in a position the schema
	// explicitly said it should not be in.
	return configitems.ApplyOrder(cols, v.Order)
}

// withoutBackReferences drops a relationship table's column that points back at
// the entity whose page this is.
//
// A cluster's Nodes table carried a `cluster` column reading the same name on
// every row, and its Workload Clusters table a `managementCluster` column doing
// the same — you arrived by clicking that entity, so the column is guaranteed
// to name it. Pure width, and on the workload table it was part of what pushed
// 13 columns past the container.
//
// Two conditions, both required. The type must match — the column's target is
// this page's type, or an interface it implements — AND every row must actually
// point back here. The second is what makes this safe: a type CAN hold two
// references to the same kind, and only one of them is the edge you traversed.
// Confirming against the data means a column carrying anything real is kept,
// without needing @hasInverse (which introspection cannot see anyway).
func withoutBackReferences(refs []configitems.ViewRefColumn, rows []map[string]any, parentType, parentOrbID string) []configitems.ViewRefColumn {
	if len(refs) == 0 || len(rows) == 0 {
		return refs
	}
	parentIs := map[string]bool{parentType: true}
	for _, in := range configitems.ImplementsFor(parentType) {
		parentIs[in] = true
	}
	out := make([]configitems.ViewRefColumn, 0, len(refs))
	for _, rc := range refs {
		if !parentIs[rc.Type] || !allPointBackTo(rows, rc.Field, parentOrbID) {
			out = append(out, rc)
		}
	}
	return out
}

// allPointBackTo reports whether every row's `field` reference is the parent.
//
// A row missing the field entirely does NOT count as pointing back: an absent
// value is information (this node has no such link), and hiding the column
// would hide it.
func allPointBackTo(rows []map[string]any, field, parentOrbID string) bool {
	for _, r := range rows {
		ref, ok := r[field].(map[string]any)
		if !ok {
			return false
		}
		if id, _ := ref["orbId"].(string); id != parentOrbID {
			return false
		}
	}
	return true
}

// rawValuesJSON encodes current values for the proposed-change marks.
//
// RAW, not rendered: a mark is suppressed when the proposed value already
// equals the current one, and comparing a proposed `5` against the string "5"
// the table renders would be a guess.
func rawValuesJSON(values map[string]any) string {
	b, err := json.Marshal(values)
	if err != nil {
		// A mark is never worth failing a page for. An empty object means
		// "nothing to compare against", so every proposal shows — which is the
		// safe direction: over-reporting a pending change is recoverable,
		// hiding one is not.
		return "{}"
	}
	return string(b)
}

// columnHeaders pairs each field with its label.
func columnHeaders(fields []string, label func(string) string) []page.ColumnHeader {
	out := make([]page.ColumnHeader, 0, len(fields))
	for _, f := range fields {
		out = append(out, page.ColumnHeader{Field: f, Label: label(f)})
	}
	return out
}

// refHeaders does the same for link columns.
func refHeaders(refs []configitems.ViewRefColumn, label func(string) string) []page.RefHeader {
	out := make([]page.RefHeader, 0, len(refs))
	for _, rc := range refs {
		out = append(out, page.RefHeader{Field: rc.Field, Label: label(rc.Field), Slug: rc.Slug})
	}
	return out
}

// prettyJSON re-indents a jsonString value for display, leaving anything that
// does not parse exactly as it is.
//
// Unparseable is NOT an error to swallow: the field is annotated as holding
// JSON, and if it does not, showing the raw value is how someone finds that
// out. Reformatting is a display concern only — the editor and the API see the
// stored string untouched.
func prettyJSON(v any) any {
	raw, ok := v.(string)
	if !ok || raw == "" {
		return v
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return v
	}
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return v
	}
	return string(out)
}
