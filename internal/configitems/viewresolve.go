package configitems

import (
	"fmt"
	"sort"
	"strings"
)

// Resolving a views document against the deployed schema into the view list the
// UI renders from.
//
// The split of responsibility is the point of the whole redesign:
//
//	the SCHEMA says what exists   — types, fields, kinds, relationships
//	the VIEWS CONFIG says what a page does with it
//
// Nothing here reads a docstring annotation except the two that stayed in the
// schema because they are facts about the data rather than about a page.

// ResolveViewsFromConfig builds the view list from a schema snapshot and a
// validated views document.
//
// Pass the config through Validate first: this function assumes every member
// path resolves, and silently skipping one here would be the silent failure the
// validator exists to prevent.
//
// It returns an error rather than a partial list when two views claim the same
// slug, for the same reason the annotation-driven resolver did: a duplicate
// shadows one type's pages, and which one wins would depend on map iteration
// order.
func ResolveViewsFromConfig(types map[string]TypeInfo, ifaceFields []string, cfg ViewConfig) ([]View, error) {
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)

	bySlug := make(map[string]string, len(names))
	views := make([]View, 0, len(names))
	for _, name := range names {
		info := types[name]
		page, isPage := cfg.Pages[name]
		td := cfg.TypeOf(info, name)

		// Only a PAGE claims a slug, so only a page can collide with one.
		slug := ""
		if isPage {
			slug = page.Slug
			if slug == "" {
				slug = DerivedSlug(name)
			}
			if other, clash := bySlug[slug]; clash {
				return nil, fmt.Errorf("slug %q is claimed by both %s and %s; "+
					"give one of them a distinct `slug:`", slug, other, name)
			}
			bySlug[slug] = name
		}

		// THE TAB STRIP, in declared order. A type with no page has none.
		tabs := make([]ViewTab, 0, len(page.Tabs))
		for _, m := range page.Tabs {
			target, isList, ok := walkMemberPath(types, info, m.Path)
			if !ok {
				continue
			}
			tabs = append(tabs, ViewTab{
				Field:    m.Path,
				Type:     target,
				Slug:     pageSlug(cfg, target),
				IsList:   isList,
				Editable: m.Editable,
			})
		}

		// THE SUMMARY LINK ROWS, in declared order. Listed rather than derived,
		// which is what retired `viewIgnored`: a relationship you did not want
		// shown previously had no way out but a flag.
		summaryRefs := []ViewRefColumn{}
		for _, r := range page.Summary.Refs {
			f, found := fieldNamed(info, r)
			if !found || f.IsList {
				continue
			}
			summaryRefs = append(summaryRefs, ViewRefColumn{Field: r, Type: f.TypeName, Slug: pageSlug(cfg, f.TypeName)})
		}

		// REFERENCE COLUMNS, for when this type renders as a ROW somewhere else.
		// DERIVED from every single relationship, and that is deliberate: a type
		// with no page still renders as rows and still has to say what it points
		// at. A NetworkInterface in a network device's Connections tab names its
		// Server; declaring that per page would mean declaring it three times.
		// DECLARED, in `types.<T>.refColumns`. Deriving these — every
		// single-cardinality edge to a ConfigItem — put `Idrac Settings` and
		// `Server Configuration Profile` on the servers list, which no
		// hand-written page ever showed, and meant a new edge in the schema
		// widened every table of that type with nobody deciding to. The file's
		// own rule is that relationships are listed; this is a relationship.
		refCols := []ViewRefColumn{}
		for _, r := range td.RefColumns {
			f, ok := fieldNamed(info, r)
			if !ok {
				continue // Validate already reported it
			}
			refCols = append(refCols, ViewRefColumn{Field: r, Type: f.TypeName, Slug: pageSlug(cfg, f.TypeName)})
		}

		display := ApplyOrder(displayScalars(info, ifaceFields, td.IgnoreFields(page)), td.Order)

		v := View{
			IsInterface:     info.IsInterface,
			Implementations: info.PossibleTypes,
			Display:         display,
			DetailOnly:      detailOnlyFields(info, td),
			JSONString:      JSONStringFieldsFor(info),
			Order:           td.Order,
			Labels:          labelsFor(td),
			Meta:            metaFields(ifaceFields),
			SummaryRefs:     summaryRefs,
			RefColumns:      refCols,
			Slug:            slug,
			Type:            name,
			Label:           Label(slug),
			MenuWeight:      page.MenuWeight,
			Fields:          editableFields(info, td, ifaceFields),
			Tabs:            tabs,
			Contains:        containedChildren(types, cfg, name),
		}
		if !isPage {
			// A pageless type still renders — as rows on somebody else's page —
			// so it keeps its columns, labels and field sets. What it does not
			// have is a URL, which is what stops its rows linking.
			v.Label = Label(DerivedSlug(name))
			v.MenuWeight = MenuWeightUnpinned
		}
		if page.FilterBy != "" && v.HasColumn(page.FilterBy) {
			v.FilterBy = page.FilterBy
		}
		v.Columns, _ = resolveColumnPaths(types, info, name, ifaceFields, td.Columns)
		views = append(views, v)
	}

	sort.SliceStable(views, func(i, j int) bool {
		if views[i].MenuWeight != views[j].MenuWeight {
			return views[i].MenuWeight < views[j].MenuWeight
		}
		return views[i].Type < views[j].Type
	})
	return views, nil
}

// pageSlug is a type's URL, or "" when it has no page.
//
// Empty is load-bearing: it is what makes a row of that type a DEAD ROW rather
// than a link to a page that does not exist. Derived at resolve time, so
// promoting a type to a page turns its rows into links with no other change.
func pageSlug(cfg ViewConfig, typeName string) string {
	p, ok := cfg.Pages[typeName]
	if !ok {
		return ""
	}
	if p.Slug != "" {
		return p.Slug
	}
	return DerivedSlug(typeName)
}

// IgnoreFields is the page's subtractive scalar list. It lives on the PAGE
// rather than the type because hiding a field is a decision about one screen —
// and a type without a page has nothing to hide it from.
func (d TypeDecl) IgnoreFields(page PageDecl) map[string]bool {
	if len(page.Summary.IgnoreFields) == 0 {
		return nil
	}
	out := make(map[string]bool, len(page.Summary.IgnoreFields))
	for _, f := range page.Summary.IgnoreFields {
		out[f] = true
	}
	return out
}

// ColumnWarningsFromConfig reports `columns:` paths the deployed schema cannot
// support, so a column that simply does not appear is not indistinguishable
// from one nobody declared.
func ColumnWarningsFromConfig(types map[string]TypeInfo, ifaceFields []string, cfg ViewConfig) []string {
	var out []string
	for _, name := range sortedKeys(cfg.Types) {
		info, ok := types[name]
		if !ok {
			continue
		}
		td := cfg.TypeOf(info, name)
		if _, w := resolveColumnPaths(types, info, name, ifaceFields, td.Columns); len(w) > 0 {
			out = append(out, w...)
		}
	}
	return out
}

// FilterByWarningsFromConfig reports a `filterBy:` that named something the page
// does not render as a column. Dropped silently it is completely invisible: the
// config says the list page has a filter, the page does not, and nothing
// connects the two.
func FilterByWarningsFromConfig(views []View, cfg ViewConfig) []string {
	var out []string
	for _, v := range views {
		want := cfg.Pages[v.Type].FilterBy
		if want == "" || v.FilterBy != "" {
			continue
		}
		out = append(out, v.Type+": filterBy names "+want+", which /"+v.Slug+" does not render as a column")
	}
	return out
}

// walkMemberPath resolves a member path to the type at its end and whether it
// yields rows.
//
// A path is a list the moment ANY hop is one: `storageControllers.storageDevices`
// reaches many devices even though the second hop alone would not say so. Getting
// this from the last hop only would render a table of grandchildren as a single
// panel.
func walkMemberPath(types map[string]TypeInfo, info TypeInfo, path string) (target string, isList, ok bool) {
	cur := info
	for _, seg := range strings.Split(path, ".") {
		f, found := fieldNamed(cur, seg)
		if !found || f.Kind == "SCALAR" || f.Kind == "ENUM" {
			return "", false, false
		}
		next, known := types[f.TypeName]
		if !known {
			return "", false, false
		}
		isList = isList || f.IsList
		cur, target = next, f.TypeName
	}
	return target, isList, target != ""
}

// editableFields is the set the EDITOR may write.
//
// One rule with one override, where there used to be two annotations pointing in
// opposite directions. A type's own plain scalars are editable; the ConfigItem
// interface's are not, because they are identity and provenance that orbital
// writes. `editable:` on a field says otherwise in either direction — false for
// a scanned hardware fact a human should not type, true to re-admit `name` on
// the pages that have always offered it.
func editableFields(info TypeInfo, decl TypeDecl, ifaceFields []string) []string {
	iface := make(map[string]bool, len(ifaceFields))
	for _, f := range ifaceFields {
		iface[f] = true
	}
	out := []string{}
	for _, f := range info.Fields {
		if !f.Editable {
			continue
		}
		if e := decl.Fields[f.Name].Editable; e != nil {
			if *e {
				out = append(out, f.Name)
			}
			continue
		}
		if iface[f.Name] {
			continue
		}
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// detailOnlyFields are the fields that render on a detail page and never as a
// table column — on a list page or a relationship table, which are the same
// problem.
func detailOnlyFields(info TypeInfo, decl TypeDecl) []string {
	var out []string
	for _, f := range info.Fields {
		if decl.Fields[f.Name].DetailOnly {
			out = append(out, f.Name)
		}
	}
	return out
}

// labelsFor collects the declared labels for a type's own fields plus the
// ConfigItem interface's.
//
// The interface's live once, at the top of the document, because DGraph forbids
// redeclaring an interface field on an implementor — so `orbId` could never have
// carried a per-type label even when this was a schema annotation. Merged here
// rather than at every read site, so a caller asking for a label never has to
// know which of the two declared it.
func labelsFor(decl TypeDecl) map[string]string {
	out := map[string]string{}
	for name, fd := range decl.Fields {
		if fd.Label != "" {
			out[name] = fd.Label
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ViewSet is a resolved view list, together with the questions consumers ask of
// it that no single view can answer alone.
//
// These used to be package functions over the Go registry, which is why they
// needed a package-level interface-lookup hook and why calling one from inside
// the resolver re-entered it and hung every page. A value that is PASSED to a
// consumer cannot re-enter anything.
type ViewSet []View

// Of returns one type's view, or the zero view when the schema has no such
// type. The zero value is usable — no members, no columns — because a page must
// not panic over a type that has gone.
func (s ViewSet) Of(typeName string) View {
	for _, v := range s {
		if v.Type == typeName {
			return v
		}
	}
	return View{}
}

// BySlug finds the view a URL segment names.
func (s ViewSet) BySlug(slug string) (View, bool) {
	if slug == "" {
		// A type with no PAGE has an empty slug, and several of them do. Without
		// this guard an empty request path would resolve to whichever one sorted
		// first.
		return View{}, false
	}
	for _, v := range s {
		if v.Slug == slug {
			return v, true
		}
	}
	return View{}, false
}

// EditableMember is one member a page writes through its own editor — the
// containment relation, read off the view instead of a Go registry.
type EditableMember struct {
	ChildType  string
	ChildField string

	// ParentEdge is the field on the CHILD pointing back at this parent — the
	// edge a first-time CREATE links through.
	//
	// Resolved here because for a multi-parent type the answer depends on which
	// parent you are creating from, which is exactly why `derivesIdFrom:`
	// (single-valued) could not supply it.
	ParentEdge string

	// IsList distinguishes a table of children from a single related entity.
	// It decides whether the member can be ADDRESSED: a path like
	// ["storageControllers", "storageDevices"] names one entity, and for a list
	// it cannot say WHICH storage controller.
	IsList bool
}

// EditableMembers returns what this type CONTAINS — what dies with it, what
// rolls up onto its audit tab, and what its editor may reach.
//
// Derived from the schema, not from the page. A StorageController contains its
// devices whether or not any page shows them, and a type with no page at all
// still contains things. Reading this off a page's tab list was the overload
// that put `editable: true` on lists the editor has never been able to edit.
func (v View) EditableMembers() []EditableMember { return v.Contains }

// EditorMembers returns what THIS PAGE's editor may write: a contained child
// the page declares `editable: true` on, that is single-cardinality.
//
// Three conditions, and each removes a different mistake. CONTAINED, or the
// editor would write an entity this page does not own. DECLARED, because a rack
// page may show its servers and choose not to edit them from there. SINGLE,
// because an edit target is addressed by PATH and a path cannot say which row.
//
// It can only ever SHRINK the contained set — never grow it. That is the §11
// hazard: the editor must not load what the page does not show, or dropping
// something from the page reads as "the user cleared it".
func (v View) EditorMembers() []EditableMember {
	declared := map[string]bool{}
	for _, t := range v.Tabs {
		if t.Editable && !strings.Contains(t.Field, ".") {
			declared[t.Field] = true
		}
	}
	var out []EditableMember
	for _, c := range v.Contains {
		if declared[c.ChildField] && !c.IsList {
			out = append(out, c)
		}
	}
	return out
}

// ContainedSingles returns what this type contains at single cardinality —
// the rule that governs the editor BELOW the page's top level.
//
// The page declares which of its own children its editor may write; it says
// nothing about a wrapper two hops down, because a wrapper has no page. Once a
// child is in the unit, the whole contained subtree below it is in the unit —
// which is the same statement the delete cascade makes, and the reason a
// cluster's etcd schedule is editable from the cluster page while ClusterBackup
// has no page of its own.
//
// Single-cardinality for the same reason EditorMembers is: a target is
// addressed by PATH, and a path cannot say which row of a list it means.
func (v View) ContainedSingles() []EditableMember {
	var out []EditableMember
	for _, c := range v.Contains {
		if !c.IsList {
			out = append(out, c)
		}
	}
	return out
}

// containedChildren returns the children a type cannot be deleted without
// taking with it.
//
// A child whose back-edge to this type is NON-NULL has no existence without it:
// that is the schema's own statement, and 13 of orbital's 14 containment
// relations derive from it. The exception is declared, because its ownership is
// an XOR that nullability cannot express.
func containedChildren(types map[string]TypeInfo, cfg ViewConfig, typeName string) []EditableMember {
	info, known := types[typeName]
	if !known {
		return nil
	}
	// The names this type answers to, so a child pointing at an INTERFACE this
	// type implements still counts — KubernetesNode.cluster is typed by the
	// KubernetesCluster interface, and an EksaKubernetesCluster contains it.
	is := map[string]bool{typeName: true}
	for _, in := range info.Implements {
		is[in] = true
	}

	var out []EditableMember
	for _, f := range info.Fields {
		if f.Kind == "SCALAR" || f.Kind == "ENUM" || f.TypeName == "" {
			continue
		}
		child, childKnown := types[f.TypeName]
		if !childKnown {
			continue
		}
		if edge := containmentEdge(cfg, child, f.TypeName, is); edge != "" {
			out = append(out, EditableMember{
				ChildType: f.TypeName, ChildField: f.Name, IsList: f.IsList, ParentEdge: edge,
			})
		}
	}
	return out
}

// containmentEdge returns the child's edge back to one of `is`, or "" when the
// child is not contained by it.
//
// The edge and the containment are one answer: if a child points back at this
// parent with a non-null edge it is contained by it, and that same edge is what
// a create links through.
func containmentEdge(cfg ViewConfig, child TypeInfo, childType string, is map[string]bool) string {
	// DECLARED first: an XOR owner, ordered most-specific-first, which no single
	// non-null edge can express. Order matters — a NIC nests under its adapter
	// before its server.
	for _, edge := range cfg.Containment[childType] {
		if f, ok := fieldNamed(child, edge); ok && is[f.TypeName] {
			return edge
		}
	}
	// DERIVED: a non-null back-edge IS the containment statement.
	for _, cf := range child.Fields {
		if cf.NonNull && is[cf.TypeName] {
			return cf.Name
		}
	}
	return ""
}

// ContainmentEdge returns the field on `childType` pointing back at `parentType`
// — the edge a first-time CREATE links through.
//
// For a single-parent type this is the non-null back-edge. For a multi-parent
// type the answer depends on which page you are creating from, which is why
// `derivesIdFrom:` (single-valued) could not supply it and the ordered
// containment declaration does.
func (s ViewSet) ContainmentEdge(types map[string]TypeInfo, cfg ViewConfig, childType, parentType string) string {
	child, ok := types[childType]
	if !ok {
		return ""
	}
	is := map[string]bool{parentType: true}
	if p, ok := types[parentType]; ok {
		for _, in := range p.Implements {
			is[in] = true
		}
	}
	for _, edge := range cfg.Containment[childType] {
		if f, ok := fieldNamed(child, edge); ok && is[f.TypeName] {
			return edge
		}
	}
	for _, cf := range child.Fields {
		if cf.NonNull && is[cf.TypeName] {
			return cf.Name
		}
	}
	return ""
}

// ExclusivelyOwned reports whether exactly one page claims typeName as an
// editable member — the test that decides whether it renders INLINE on that
// page or as a link to its own.
//
// Derived, never declared, and that is deliberate: making it a config key would
// be a second axis for a question the first axis already answers. A
// ClusterBackup is only ever a cluster's, so a cluster page shows it inline; an
// IPAddress is claimed by a server, a node and two cluster fields, so inlining
// it anywhere would assert an ownership no single page has.
//
// An interface and its implementations count ONCE. Both the KubernetesCluster
// view and the EksaKubernetesCluster view declare `backup`, because the
// interface backs the list page and the concrete type backs the detail page —
// counting them as two parents would make every backup sub-kind render as a
// link on the only page that can edit it.
func (s ViewSet) ExclusivelyOwned(typeName string) bool {
	claims := map[string]bool{}
	for _, v := range s {
		for _, c := range v.Contains {
			if c.ChildType == typeName {
				claims[s.collapseToInterface(v.Type)+"."+c.ChildField] = true
			}
		}
	}
	return len(claims) == 1
}

// collapseToInterface maps a concrete type to the interface a view covers it
// with, so an implementation and its interface are not counted as two parents.
func (s ViewSet) collapseToInterface(typeName string) string {
	for _, v := range s {
		if !v.IsInterface {
			continue
		}
		for _, impl := range v.Implementations {
			if impl == typeName {
				return v.Type
			}
		}
	}
	return typeName
}

// EditableOrbIDSelection returns a GraphQL sub-selection fetching every orbId in
// a page's edit unit — the single source the audit roll-up derives from, so a
// parent's audit tab shows exactly the entities its editor can write.
//
// Depth- and path-guarded: a members graph is operator-editable and nothing
// stops someone declaring a cycle, which a naive walk would follow forever.
func (s ViewSet) EditableOrbIDSelection(rootType string) string {
	var b strings.Builder
	var rec func(typeName string, path map[string]bool, depth int)
	rec = func(typeName string, path map[string]bool, depth int) {
		if depth > 8 || path[typeName] {
			return
		}
		path[typeName] = true
		for _, m := range s.Of(typeName).EditableMembers() {
			b.WriteString(m.ChildField)
			b.WriteString(" { orbId ")
			rec(m.ChildType, path, depth+1)
			b.WriteString("} ")
		}
		delete(path, typeName)
	}
	rec(rootType, map[string]bool{}, 0)
	return strings.TrimSpace(b.String())
}

// Labeller returns the display-label function for a type: the declared label
// where there is one, title-casing otherwise.
//
// One mechanism, resolved server-side and carried on the view, so a column
// header, a field row and the metadata box cannot disagree — and so an
// integrator rendering their own table gets orbital's answer rather than
// re-deriving the title-casing rule.
func (v View) Labeller() func(string) string {
	return func(field string) string {
		if l, ok := v.Labels[field]; ok {
			return l
		}
		return HumanFieldLabel(field)
	}
}

// HumanFieldLabel title-cases a field name: "serviceTag" → "Service Tag".
//
// A run of capitals is an acronym and stays together — splitting on every
// capital rendered "Tinkerbell I P", which reads as a typo rather than a label.
// A capital that STARTS a run still breaks the previous word, and the last
// capital of a run starts the next word when a lowercase follows it
// ("oobIPAddress" → "Oob IP Address").
//
// It cannot know a genuine acronym, which is what the `label:` declaration is
// for — `cni` becomes "Cni" here and "CNI" with one. Only declare what this
// rule gets wrong.
func HumanFieldLabel(field string) string {
	isUpper := func(i int) bool { return i >= 0 && i < len(field) && field[i] >= 'A' && field[i] <= 'Z' }
	isLower := func(i int) bool { return i >= 0 && i < len(field) && field[i] >= 'a' && field[i] <= 'z' }

	var b strings.Builder
	for i := 0; i < len(field); i++ {
		c := field[i]
		if i > 0 && isUpper(i) && (!isUpper(i-1) || isLower(i+1)) {
			b.WriteByte(' ')
		}
		if i == 0 && isLower(i) {
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Implements returns the interfaces a type implements, from the resolved views.
//
// It replaces a package-level lookup hook that production set at startup. That
// hook existed because the registry's containment walk was a package function
// with no resolver to thread, and it is exactly what made calling one of those
// functions from inside the resolver re-enter it — every generic page hung, with
// no error and nothing in the access log, because the request never finished.
// A value passed to a consumer cannot re-enter anything.
func (s ViewSet) Implements(typeName string) []string {
	var out []string
	for _, v := range s {
		if !v.IsInterface {
			continue
		}
		for _, impl := range v.Implementations {
			if impl == typeName {
				out = append(out, v.Type)
			}
		}
	}
	return out
}
