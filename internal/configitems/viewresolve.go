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
// `inverse` is the schema's `@hasInverse` lookup (InverseEdges): it names the
// edge a subgraph member links back through on create. Nil means none known.
//
// It returns an error rather than a partial list when two views claim the same
// slug, for the same reason the annotation-driven resolver did: a duplicate
// shadows one type's pages, and which one wins would depend on map iteration
// order.
func ResolveViewsFromConfig(types map[string]TypeInfo, ifaceFields []string, cfg ViewConfig, inverse func(typeName, field string) string) ([]View, error) {
	if inverse == nil {
		inverse = func(string, string) string { return "" }
	}
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

		// THE SUBGRAPH, in declared order. A type with no page has none.
		subgraph := make([]ViewTab, 0, len(page.Subgraph))
		inSubgraph := map[string]bool{}
		for _, m := range page.Subgraph {
			target, isList, ok := walkMemberPath(types, info, m.Path)
			if !ok {
				continue
			}
			inSubgraph[m.Path] = true
			subgraph = append(subgraph, ViewTab{
				Field:      m.Path,
				Type:       target,
				Slug:       pageSlug(cfg, target),
				IsList:     isList,
				Editable:   m.Editable,
				ParentEdge: parentEdgeOf(types, info, name, m.Path, inverse),
			})
		}

		// THE SUMMARY LINK ROWS. Derived: every single relationship to a
		// ConfigItem that the subgraph does not already render and the page
		// does not ignore. Navigation only — a link row is never edited,
		// audited or deleted from here, which is what keeps a server's data
		// centre out of its delete.
		//
		// A concrete type with no page of its own renders on its INTERFACE's
		// page (an EksaKubernetesCluster on /clusters), and its own single
		// relationships — tinkerbellIP, managementCluster — are rows there too,
		// so it derives them against that page's declaration.
		layout, hasLayout := page, isPage
		if !hasLayout {
			for _, in := range info.Implements {
				if p, ok := cfg.Pages[in]; ok {
					layout, hasLayout = p, true
					for _, m := range p.Subgraph {
						inSubgraph[m.Path] = true
					}
					break
				}
			}
		}
		summaryRefs := []ViewRefColumn{}
		ignored := map[string]bool{}
		for _, f := range layout.Summary.IgnoreFields {
			ignored[f] = true
		}
		if hasLayout {
			for _, f := range info.Fields {
				if f.Kind == "SCALAR" || f.Kind == "ENUM" || f.IsList || inSubgraph[f.Name] || ignored[f.Name] {
					continue
				}
				if _, isConfigItem := types[f.TypeName]; !isConfigItem {
					continue
				}
				summaryRefs = append(summaryRefs, ViewRefColumn{Field: f.Name, Type: f.TypeName, Slug: pageSlug(cfg, f.TypeName)})
			}
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
			TableHidden:     tableHiddenFields(info, td),
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
			OrbIDPattern:    info.OrbIDPattern,
			OrbIDKind:       OrbIDKindFor(name, info.Doc),
			Defaults:        defaultsFor(info, td, orbIDPaths(info.OrbIDPattern)),
			NoCreate:        noCreateFor(info, td),
			Fields:          editableFields(info, td, ifaceFields),
			Subgraph:        subgraph,
			Relations:       relationsOf(info),
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

// tableHiddenFields are the fields that render on a detail page and never as a
// table column — on a list page or a relationship table, which are the same
// problem.
func tableHiddenFields(info TypeInfo, decl TypeDecl) []string {
	var out []string
	for _, f := range info.Fields {
		if decl.Fields[f.Name].TableHidden {
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

// EditorMembers returns what THIS PAGE's editor may write: the subgraph
// members declared `editable: true`. Validate has already refused the flag on a
// list and beyond two hops, so every one is addressable by path.
func (v View) EditorMembers() []ViewTab {
	var out []ViewTab
	for _, m := range v.Subgraph {
		if m.Editable && !m.IsList {
			out = append(out, m)
		}
	}
	return out
}

// relationsOf maps each relationship field to the type at its far end.
func relationsOf(info TypeInfo) map[string]string {
	out := map[string]string{}
	for _, f := range info.Fields {
		if f.Kind != "SCALAR" && f.Kind != "ENUM" && f.TypeName != "" {
			out[f.Name] = f.TypeName
		}
	}
	return out
}

// parentEdgeOf returns the field on the entity at the end of `path` pointing
// back one hop — the `@hasInverse` partner of the last hop.
//
// Tries the declaring type, then every interface it implements: DGraph forbids
// redeclaring an interface field on an implementor, so `backup` is declared on
// the KubernetesCluster INTERFACE and the inverse is keyed there, not on
// EksaKubernetesCluster.
func parentEdgeOf(types map[string]TypeInfo, info TypeInfo, typeName, path string, inverse func(string, string) string) string {
	segs := strings.Split(path, ".")
	cur, curName := info, typeName
	for i, seg := range segs {
		f, found := fieldNamed(cur, seg)
		if !found {
			return ""
		}
		if i == len(segs)-1 {
			if back := inverse(curName, seg); back != "" {
				return back
			}
			for _, in := range cur.Implements {
				if back := inverse(in, seg); back != "" {
					return back
				}
			}
			return ""
		}
		cur, curName = types[f.TypeName], f.TypeName
	}
	return ""
}

// SubgraphFor returns the declared subgraph a type's page carries — its own,
// or for a concrete type with no page, its INTERFACE's. An
// EksaKubernetesCluster renders on /clusters, so the cluster page's subgraph is
// what it shows, audits and deletes.
func (s ViewSet) SubgraphFor(typeName string) []ViewTab {
	v := s.Of(typeName)
	if v.Slug != "" || len(v.Subgraph) > 0 {
		return v.Subgraph
	}
	for _, in := range s.Implements(typeName) {
		if iv := s.Of(in); iv.Slug != "" {
			return iv.Subgraph
		}
	}
	return nil
}

// SubgraphSelection returns a GraphQL sub-selection fetching the orbId of
// every entity a page's declared subgraph reaches — the set the audit tab and
// the change-request scope pin both cover.
//
// Built from the declared paths and nothing else, so it terminates by
// construction: a finite list of finite paths. Paths sharing a prefix share one
// selection, because GraphQL selects a field once per level.
//
// An INTERFACE-typed hop takes an inline fragment. `orbId` is declared on
// ConfigItem rather than on a sub-interface, so selecting it bare off
// DataCenter.kubernetesClusters is rejected at VALIDATION — and that failure is
// not local: the batch expander puts every root in one aliased query, so one
// interface hop anywhere would empty every root's subgraph.
func (s ViewSet) SubgraphSelection(rootType string) string {
	root := PathTree(s.SubgraphFor(rootType))
	var b strings.Builder
	var rec func(n *PathNode, typeName string)
	rec = func(n *PathNode, typeName string) {
		for _, c := range n.Children {
			childType := s.hopType(typeName, c.Field)
			b.WriteString(c.Field)
			if s.Of(childType).IsInterface {
				b.WriteString(" { __typename ... on ConfigItem { orbId } ")
			} else {
				b.WriteString(" { orbId ")
			}
			rec(c, childType)
			b.WriteString("} ")
		}
	}
	rec(root, rootType)
	return strings.TrimSpace(b.String())
}

// hopType is the type at the far end of one relationship field.
func (s ViewSet) hopType(typeName, field string) string {
	return s.Of(typeName).Relations[field]
}

// PathNode is one hop in a subgraph's path tree: paths sharing a prefix share
// the node, so a field is visited and selected once per level.
type PathNode struct {
	Field    string
	Member   *ViewTab // the declared member ending here, or nil for an intermediate hop
	Children []*PathNode
}

// PathTree folds a declared subgraph into a tree of hops.
func PathTree(members []ViewTab) *PathNode {
	root := &PathNode{}
	for i := range members {
		n := root
		for _, seg := range strings.Split(members[i].Field, ".") {
			var next *PathNode
			for _, c := range n.Children {
				if c.Field == seg {
					next = c
					break
				}
			}
			if next == nil {
				next = &PathNode{Field: seg}
				n.Children = append(n.Children, next)
			}
			n = next
		}
		n.Member = &members[i]
	}
	return root
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
// hook existed because the registry's ownership walk was a package function
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

// orbIDPaths is every placeholder a type's patterns reference, as a set.
func orbIDPaths(patterns []OrbIDPattern) map[string]bool {
	out := map[string]bool{}
	for _, p := range patterns {
		for _, path := range p.Paths() {
			out[path] = true
		}
	}
	return out
}

// defaultsFor collects the declared create-form defaults for a type.
//
// A default on a field the orbId is BUILT FROM is dropped: pre-filling identity
// creates a node under a key nobody chose, and unlike a wrong manufacturer a
// wrong orbId cannot be corrected — re-keying orphans every child id and splits
// the audit trail across two ids.
func defaultsFor(info TypeInfo, decl TypeDecl, identity map[string]bool) map[string]string {
	var out map[string]string
	for _, f := range info.Fields {
		d := decl.Fields[f.Name].CreateDefault
		if d == "" || identity[f.Name] {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[f.Name] = d
	}
	return out
}

// noCreateFor collects the fields a type keeps off the create form.
func noCreateFor(info TypeInfo, decl TypeDecl) map[string]bool {
	var out map[string]bool
	for _, f := range info.Fields {
		if !decl.Fields[f.Name].CreateHidden {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[f.Name] = true
	}
	return out
}
