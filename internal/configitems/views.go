package configitems

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A View is one renderable page, derived entirely from the running schema.
//
// Defining a type in DGraph is meant to yield `/{slug}` and `/{slug}/{id}` with
// no code and no configuration — configuration is the EXCEPTION, not the
// mechanism. So everything here is derived; the P1 override layer will adjust
// these, not supply them.
type View struct {
	// Slug is the URL segment. A CONTRACT: integrators and bookmarks depend on
	// it, so it derives from the schema and changing it is a deliberate
	// breaking act.
	Slug string `json:"slug"`

	// Type is the GraphQL type name.
	Type string `json:"type"`

	// Label is DISPLAY, not contract — free to change without breaking anyone.
	Label string `json:"label"`

	// IsRoot decides NAV membership, not routability. Every type is routable;
	// only roots appear in the menu, because an owned child like IdracSettings
	// is meaningless outside its parent and a nineteen-item nav helps nobody.
	// Derived from containment: a root is a type nothing else owns.
	IsRoot bool `json:"isRoot"`

	// Nav is the menu position from the type's `menuWeight:` annotation, or
	// MenuWeightUnpinned when it declares none. Published so a client building its own
	// navigation gets the same order without re-deriving it — the UI sorts
	// nothing.
	MenuWeight int `json:"menuWeight"`

	// IsInterface marks a view backed by a GraphQL interface. Such a view
	// LISTS rows of several concrete types; its detail route resolves each
	// row's own type, because DGraph generates no get<Interface>.
	IsInterface bool `json:"isInterface"`

	// Implementations are the concrete types an interface view lists. Empty
	// for a concrete view.
	Implementations []string `json:"implementations,omitempty"`

	// Fields are the EDITABLE scalars — what the editor may write.
	Fields []string `json:"fields"`

	// Display are the scalars a page SHOWS, which is a superset of Fields.
	//
	// `editorIgnored` is editor-scoped, not display-scoped: a scanned hardware
	// fact like capacityBytes is not something a human should type, and is
	// exactly what someone opens the page to read. Using Fields for display
	// made a StorageDevice detail page render nothing at all, because every one
	// of its fields is annotated.
	Display []string `json:"display"`

	// DetailOnly are Display fields that render on a detail page but never as a
	// table column — the `detailOnly` annotation. Placement is DECLARED, not
	// inferred from what a field holds.
	//
	// Reported rather than silently dropped, because a client building its own
	// table needs the same distinction and should not have to re-derive it.
	DetailOnly []string `json:"detailOnly,omitempty"`

	// JSONString are fields whose String value holds a JSON document. Says
	// nothing about placement: it drives editor parsing and the pretty-printed
	// rendering. A field is commonly both, but they are separate facts.
	JSONString []string `json:"jsonString,omitempty"`

	// Columns are extra columns whose value lives at the end of a PATH —
	// `servers.count`, `kubernetesNode.cluster.name`. Resolved and validated
	// against the schema here, so a client rendering its own table gets the
	// same columns without re-walking the graph.
	Columns []ViewColumn `json:"columns,omitempty"`

	// FilterBy is the single column the list page offers as a filter dropdown,
	// from the type's `filterBy:` annotation. Empty when unannotated, and also
	// when the annotation named something this page does not render as a
	// column — a dropdown over an absent column is a dead control, so it is
	// dropped here and reported by FilterByWarnings rather than shipped broken.
	//
	// Carried on the view because orbital's UI is a consumer of this API like
	// any other: a client building its own table should be told which column is
	// worth a filter, not have to guess from cardinality.
	FilterBy string `json:"filterBy,omitempty"`

	// Order is the pinned field prefix from the type's `order:` annotation.
	// Exposed because a list page re-applies it after unioning an interface's
	// implementations, and because a client building its own table should
	// order it the way orbital does rather than guess.
	Order []string `json:"order,omitempty"`

	// Meta are the ConfigItem interface fields — identity and provenance —
	// which displayScalars deliberately strips out of Display. They are shown
	// separately, in their own box, exactly as every hand-written detail page
	// showed them.
	Meta []string `json:"meta"`

	// Tabs are the relationships this type has to other ConfigItem types — what
	// a detail page renders as tabs.
	Tabs []ViewTab `json:"tabs"`

	// RefColumns are the SINGLE relationships rendered as an extra column when
	// this type appears as a row: a reference to another entity shows WHICH
	// entity.
	//
	// That is the difference between a useful table and one you have to click
	// through — a DataCenter's servers table shows each server's rack and OOB
	// IP, and those live one hop away. Derived, not declared: "single" means one
	// value fits in a cell, which is the only question a column can answer.
	RefColumns []ViewRefColumn `json:"refColumns"`
}

// ViewRefColumn is a single relationship rendered as a column of links.
type ViewRefColumn struct {
	Field string `json:"field"`
	Type  string `json:"type"`
	Slug  string `json:"slug"`
}

// ViewColumn is one computed column: a path from the row's type to a value.
type ViewColumn struct {
	// Path is the declared dotted path, e.g. "kubernetesNode.cluster.name".
	Path string `json:"path"`
	// Field is the leaf — the label is derived from it, so a column reads
	// "Cluster" rather than "Kubernetes Node Cluster Name".
	Field string `json:"field"`
	// IsCount marks an aggregate over a list relationship.
	IsCount bool `json:"isCount,omitempty"`

	// OwnerType is the type the label field belongs to — the type at the END
	// of the path, not the row's own type. A `gpu` field annotated
	// `label: GPU` lives on KubernetesNode; labelling it with Server's
	// labeller silently ignores that and renders "Gpu".
	OwnerType string `json:"ownerType,omitempty"`

	// Selection is the GraphQL fragment that fetches this column, built here
	// because this is the only place that knows whether a hop lands on an
	// INTERFACE — `cluster { name }` is rejected, because `name` lives on
	// ConfigItem rather than on KubernetesCluster, and needs a type condition.
	//
	// Published so a client fetching its own rows can ask for the same thing
	// rather than reverse-engineering the path.
	Selection string `json:"selection"`
}

// ViewTab is one relationship, as a detail page would render it.
type ViewTab struct {
	// Field is the GraphQL field holding the relationship.
	Field string `json:"field"`
	// Type is the ConfigItem type at the other end.
	Type string `json:"type"`
	// Slug is that type's own page, so a client can link through without
	// re-deriving anything.
	Slug string `json:"slug"`
	// IsList distinguishes a table of children from a single related entity.
	IsList bool `json:"isList"`
}

// ResolveViews builds the view list from a schema snapshot.
//
// It returns an error rather than a partial list when two types claim the same
// slug: a duplicate silently shadows one type's pages, and which one wins would
// depend on map iteration order.
func ResolveViews(types map[string]TypeInfo, ifaceFields []string) ([]View, error) {
	derived := Derive(types, ifaceFields)

	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)

	bySlug := make(map[string]string, len(names))
	views := make([]View, 0, len(names))
	for _, name := range names {
		info := types[name]
		slug := SlugFor(name, info.Doc)
		if other, clash := bySlug[slug]; clash {
			return nil, fmt.Errorf("slug %q is claimed by both %s and %s; "+
				"give one of them a distinct `slug:` annotation", slug, other, name)
		}
		bySlug[slug] = name

		var tabs []ViewTab
		for _, f := range info.Fields {
			if f.Kind == "SCALAR" || f.Kind == "ENUM" {
				continue
			}
			if IsViewIgnored(f.Doc) {
				// Filtered HERE, at the single source: RefColumns, the detail
				// page's owned boxes and its reference rows are all derived
				// from Tabs, so one filter drops the relationship from every
				// surface rather than four places remembering to agree.
				continue
			}
			target, known := types[f.TypeName]
			if !known {
				// A relationship to something that is not a ConfigItem (or to an
				// interface). Not renderable as a page, so not a tab.
				continue
			}
			tabs = append(tabs, ViewTab{
				Field:  f.Name,
				Type:   f.TypeName,
				Slug:   SlugFor(f.TypeName, target.Doc),
				IsList: f.IsList,
			})
		}

		// Included paths become ordinary list tabs whose Field is the path.
		// Resolved here, where the whole type map is in hand, so the renderer
		// only ever sees a tab.
		for _, path := range IncludePaths(info.Doc) {
			seg := strings.SplitN(path, ".", 2)
			mid, midOK := fieldType(info, seg[0])
			if !midOK {
				continue
			}
			midInfo, known := types[mid]
			if !known {
				continue
			}
			leaf, leafOK := fieldType(midInfo, seg[1])
			if !leafOK {
				continue
			}
			target, known := types[leaf]
			if !known {
				continue
			}
			tabs = append(tabs, ViewTab{
				Field:  path,
				Type:   leaf,
				Slug:   SlugFor(leaf, target.Doc),
				IsList: true,
			})
		}

		// Empty slices, never nil: these serialise as [] rather than null, so a
		// client can iterate without a null check. A type with no editable
		// fields is a normal case (ClusterBackup is a pure wrapper), not an
		// absence of information.
		fields := derived[name]
		if fields == nil {
			fields = []string{}
		}
		if tabs == nil {
			tabs = []ViewTab{}
		}
		// Pinned order, preferring the type's own annotation and falling back
		// to an interface it implements. Inheritance is explicit because a
		// TYPE docstring does not reach implementations the way one might
		// assume — KubernetesCluster carries the pin, and /clusters uses the
		// interface view while every detail page uses a concrete one, so
		// without this the two would order their columns differently.
		pinned := typeAnnotation(types, info, OrderFor, func(s []string) bool { return len(s) == 0 })
		display := ApplyOrder(displayScalars(info, ifaceFields), pinned)
		detailOnly := DetailOnlyFieldsFor(info)
		jsonFields := JSONStringFieldsFor(info)
		meta := metaFields(ifaceFields)

		// EVERY single relationship, owned or not.
		//
		// Excluding owned ones lost a genuinely useful column — a server's OOB
		// IP is an owned IPAddress, and it is the FIRST column on the
		// hand-written data-centre page. Ownership answers "is this edited
		// inline", which has no bearing on whether its identity is worth a
		// column. Columns are hideable; a missing one is not discoverable.
		//
		// It also removes the only reason this function needed the containment
		// walk, which is what re-entered the resolver and hung every page.
		refCols := []ViewRefColumn{}
		for _, t := range tabs {
			if t.IsList {
				continue
			}
			refCols = append(refCols, ViewRefColumn{Field: t.Field, Type: t.Type, Slug: t.Slug})
		}
		weight, _ := MenuWeightFor(info.Doc)
		v := View{
			IsInterface:     info.IsInterface,
			Implementations: info.PossibleTypes,
			Display:         display,
			DetailOnly:      detailOnly,
			JSONString:      jsonFields,
			Order:           pinned,
			Meta:            meta,
			RefColumns:      refCols,
			Slug:            slug,
			Type:            name,
			Label:           Label(slug),
			IsRoot:          isRootType(name),
			MenuWeight:      weight,
			Fields:          fields,
			Tabs:            tabs,
		}
		// Validated against the view that was just built, not against the raw
		// field list: a filterBy over a `detailOnly` field or a list relationship
		// would render a dropdown that filters a column the table does not
		// have. Dropped here, reported by FilterByWarnings.
		if f, _ := filterByAnnotation(types, info); f != "" && v.HasColumn(f) {
			v.FilterBy = f
		}
		v.Columns, _ = resolveColumns(types, info, name, ifaceFields)
		views = append(views, v)
	}

	// An implementation of a sub-interface is not a nav root: the interface
	// view already lists it, and two entries would mean "Clusters" and "Eksa
	// Kubernetes Clusters" side by side showing the same rows today and
	// diverging tomorrow. Its pages stay reachable by URL — only the nav entry
	// goes.
	covered := map[string]bool{}
	for _, v := range views {
		if v.IsInterface {
			for _, impl := range v.Implementations {
				covered[impl] = true
			}
		}
	}
	for i := range views {
		if covered[views[i].Type] {
			views[i].IsRoot = false
		}
	}
	// Nav order, then name. Callers render in slice order, so the ordering
	// decision lives here once rather than in every consumer.
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].MenuWeight != views[j].MenuWeight {
			return views[i].MenuWeight < views[j].MenuWeight
		}
		return views[i].Type < views[j].Type
	})
	return views, nil
}

// isRootType reports whether nothing owns this type.
//
// Derived from containment rather than declared. The registry used to carry an
// ColumnFields returns the scalar fields a LIST page renders as columns:
// Display minus DetailOnly.
//
// Placement comes from the `detailOnly` annotation, not from what the field
// holds. One data centre's assetDataV2 is ~600 characters of JSON, and a column
// of them makes the table unreadable. Applies equally to a list page and to a
// relationship table, because they are the same problem.
//
// One definition, because there were nearly two: the handler had its own copy
// of this subtraction, and a filterBy validated against a different notion of
// "column" than the table renders would accept an annotation that produces a
// dropdown filtering a column nobody can see.
func (v View) ColumnFields() []string {
	if len(v.DetailOnly) == 0 {
		return v.Display
	}
	skip := make(map[string]bool, len(v.DetailOnly))
	for _, f := range v.DetailOnly {
		skip[f] = true
	}
	out := make([]string, 0, len(v.Display))
	for _, f := range v.Display {
		if !skip[f] {
			out = append(out, f)
		}
	}
	return out
}

// HasColumn reports whether a field renders as a list-page column, scalar or
// reference.
func (v View) HasColumn(field string) bool {
	for _, f := range v.ColumnFields() {
		if f == field {
			return true
		}
	}
	for _, r := range v.RefColumns {
		if r.Field == field {
			return true
		}
	}
	return false
}

// typeAnnotation reads a TYPE-level annotation, falling back to an interface
// the type implements when the type itself does not carry one.
//
// Inheritance is per-annotation and deliberate, not a general rule: `slug:`
// must NEVER inherit or every implementation would claim the interface's URL.
// `order:` and `filterBy:` both should, because the interface view and the
// concrete detail pages are the same page to a reader, and a control that is
// present on /clusters and absent on /eksa-kubernetes-clusters is the kind of
// gap nobody notices for months.
//
// Extracted from the `order:` lookup that used to spell this out inline — the
// second annotation to want it was the moment to stop copying it.
func typeAnnotation[T any](types map[string]TypeInfo, info TypeInfo, read func(string) T, empty func(T) bool) T {
	got := read(info.Doc)
	if !empty(got) {
		return got
	}
	for _, in := range info.Implements {
		if got = read(types[in].Doc); !empty(got) {
			return got
		}
	}
	return got
}

// resolveColumns turns a type's `column:` paths into renderable columns,
// dropping any the schema cannot support and saying why.
//
// The walk enforces what makes a path a COLUMN rather than a sub-query: every
// intermediate hop must be a SINGLE relationship, so exactly one value arrives
// at the leaf. The one exception is a `count` leaf, whose preceding hop must be
// a LIST — an aggregate is the only thing that collapses many rows to one cell.
//
// A path naming a field that does not exist is dropped, not fatal: a stale
// annotation must never stop a page rendering, and the warning is how someone
// finds out.
func resolveColumns(types map[string]TypeInfo, info TypeInfo, typeName string, ifaceFields []string) ([]ViewColumn, []string) {
	paths := typeAnnotation(types, info, ColumnPaths, func(p []string) bool { return len(p) == 0 })
	if len(paths) == 0 {
		return nil, nil
	}
	var cols []ViewColumn
	var warn []string
	for _, path := range paths {
		segs := strings.Split(path, ".")
		leaf := segs[len(segs)-1]
		hops := segs[:len(segs)-1]

		cur, curName := info, typeName
		ok := true
		for i, seg := range hops {
			f, found := fieldNamed(cur, seg)
			if !found {
				warn = append(warn, typeName+": column "+path+" — "+cur2(cur, typeName, i)+" has no field "+seg)
				ok = false
				break
			}
			if f.Kind == "SCALAR" || f.Kind == "ENUM" {
				warn = append(warn, typeName+": column "+path+" — "+seg+" is a scalar, so nothing follows it")
				ok = false
				break
			}
			isLast := i == len(hops)-1
			if f.IsList && !(isLast && leaf == "count") {
				warn = append(warn, typeName+": column "+path+" — "+seg+" is a list, and only a `count` leaf may follow one")
				ok = false
				break
			}
			if !f.IsList && isLast && leaf == "count" {
				warn = append(warn, typeName+": column "+path+" — count needs a list, and "+seg+" is a single relationship")
				ok = false
				break
			}
			next, known := types[f.TypeName]
			if !known {
				warn = append(warn, typeName+": column "+path+" — "+f.TypeName+" is not a ConfigItem type")
				ok = false
				break
			}
			cur, curName = next, f.TypeName
		}
		if !ok {
			continue
		}
		if leaf == "count" {
			cols = append(cols, ViewColumn{
				Path:      path,
				Field:     hops[len(hops)-1],
				OwnerType: curName,
				IsCount:   true,
				Selection: wrapHops(hops[:len(hops)-1], hops[len(hops)-1]+"Aggregate { count }"),
			})
			continue
		}
		lf, found := fieldNamed(cur, leaf)
		if !found {
			// `name` and `orbId` are declared on the ConfigItem INTERFACE, not
			// on each type, so they are absent from a type's own field list
			// while being perfectly selectable. `cluster.name` is the whole
			// point of this annotation, so without this the headline case
			// fails validation.
			if !isIfaceField(ifaceFields, leaf) {
				warn = append(warn, typeName+": column "+path+" — no field "+leaf+" at the end of the path")
				continue
			}
			lf = DerivedField{Name: leaf, Kind: "SCALAR"}
		}
		if lf.Kind != "SCALAR" && lf.Kind != "ENUM" {
			warn = append(warn, typeName+": column "+path+" — "+leaf+" is a relationship, not a value")
			continue
		}
		// A leaf that lives on the ConfigItem interface — `name`, `orbId` —
		// cannot be selected bare off a sub-interface; DGraph rejects it at
		// validation. Wrapped in a type condition when the hop landed on one.
		leafSel := leaf
		if cur.IsInterface && isIfaceField(ifaceFields, leaf) {
			leafSel = "... on ConfigItem { " + leaf + " }"
		}
		// Labelled by the LEAF, except for an identity leaf: `cluster.name`
		// reads "Cluster", not "Name" — which would also collide with the
		// row's own Name column.
		labelField := leaf
		if isIfaceField(ifaceFields, leaf) && len(hops) > 0 {
			labelField = hops[len(hops)-1]
		}
		cols = append(cols, ViewColumn{
			Path:      path,
			Field:     labelField,
			OwnerType: curName,
			Selection: wrapHops(hops, leafSel),
		})
	}
	return cols, warn
}

// wrapHops nests a leaf selection inside its path: ["a","b"], "x" -> "a { b { x } }".
func wrapHops(hops []string, leaf string) string {
	sel := leaf
	for i := len(hops) - 1; i >= 0; i-- {
		sel = hops[i] + " { " + sel + " }"
	}
	return sel
}

func isIfaceField(ifaceFields []string, name string) bool {
	for _, f := range ifaceFields {
		if f == name {
			return true
		}
	}
	return false
}

func fieldNamed(info TypeInfo, name string) (DerivedField, bool) {
	for _, f := range info.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return DerivedField{}, false
}

// cur2 names the type a failed hop was looking at, for the warning.
func cur2(_ TypeInfo, root string, hop int) string {
	if hop == 0 {
		return root
	}
	return "the type at hop " + strconv.Itoa(hop)
}

// ColumnWarnings reports `column:` paths the schema could not support.
func ColumnWarnings(types map[string]TypeInfo, views []View, ifaceFields []string) []string {
	var out []string
	for _, v := range views {
		info, ok := types[v.Type]
		if !ok {
			continue
		}
		if _, w := resolveColumns(types, info, v.Type, ifaceFields); len(w) > 0 {
			out = append(out, w...)
		}
	}
	return out
}

// filterByAnnotation reads a type's `filterBy:` declaration, inheriting from an
// interface it implements, and returns the field in use plus any extras.
func filterByAnnotation(types map[string]TypeInfo, info TypeInfo) (string, []string) {
	type decl struct {
		field string
		extra []string
	}
	d := typeAnnotation(types, info,
		func(doc string) decl {
			f, rest := FilterByFor(doc)
			return decl{field: f, extra: rest}
		},
		func(d decl) bool { return d.field == "" })
	return d.field, d.extra
}

// MenuWeightWarnings reports `menuWeight:` annotations whose value is not a number.
//
// UnknownAnnotations cannot catch these — "menuWeight: left" is a KNOWN prefix with an
// unusable value, so it passes the typo check and then silently does nothing.
// The type still appears in the menu, at the unpinned position.
func MenuWeightWarnings(types map[string]TypeInfo) []string {
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		if _, ok := MenuWeightFor(types[n].Doc); !ok {
			out = append(out, n+": nav is not a number; menu position ignored")
		}
	}
	return out
}

// FilterByWarnings reports `filterBy:` annotations that did not produce a filterBy.
//
// A dropped annotation is otherwise completely silent: the schema says the page
// has a filter, the page does not, and nothing connects the two. Same reasoning
// as UnknownAnnotations — an annotation that reads as correct and does nothing
// is worse than one that was never written.
func FilterByWarnings(types map[string]TypeInfo, views []View) []string {
	var out []string
	for _, v := range views {
		info, ok := types[v.Type]
		if !ok {
			continue
		}
		want, extra := filterByAnnotation(types, info)
		if want == "" {
			continue
		}
		if v.FilterBy == "" {
			out = append(out, v.Type+": filterBy names "+want+", which /"+v.Slug+" does not render as a column")
		}
		if len(extra) > 0 {
			out = append(out, v.Type+": filterBy names "+strings.Join(extra, ", ")+" as well; only one filterBy is supported and "+want+" is the one in use")
		}
	}
	return out
}

// `IsRoot` bool on four types; it was deleted 2026-09-25 once this was shown to
// reproduce it exactly — DataCenter, Server, NetworkDevice and
// EksaKubernetesCluster — which is what made it safe to delete.
func isRootType(name string) bool {
	t, ok := FindByName(name)
	if !ok {
		// A type the Go registry has never heard of — which is the whole point:
		// defining a type in DGraph must yield a working page AND a nav entry
		// with no code. Nothing declares ownership of it, so nothing owns it.
		//
		// Returning false here meant a runtime-defined type got pages that were
		// reachable only by typing the URL, which is half a feature. Containment
		// direction is not derivable from the schema (`@hasInverse` sits on both
		// ends), so "no declared owner" is the only honest answer available, and
		// erring toward VISIBLE beats erring toward hidden.
		return true
	}
	// BOTH must be empty. A multi-parent type declares only OwnerEdges and
	// leaves OwnerType blank — IPAddress is owned by Server, KubernetesNode and
	// two cluster types, and checking OwnerType alone called it a root and would
	// have put "IP Addresses" in the nav as a top-level page.
	return t.OwnerType == "" && len(t.OwnerEdges) == 0
}

// Label is the human-readable name of a view, derived from its SLUG:
// "network-devices" → "Network Devices". Display only — never put it in a URL.
//
// From the slug rather than the type name so `"""slug: clusters"""` renames the
// page AND its nav entry together. Deriving them separately gave /clusters two
// names — the hand-written menu said "Clusters" and the derived one said
// "Kubernetes Clusters", both linking to the same page. For a type with no slug
// annotation the slug IS the kebab-cased plural type name, so this changes
// nothing for them.
func Label(slug string) string {
	words := strings.Split(slug, "-")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	// No pluralisation: the slug is already plural, and applying it twice
	// turned "addresses" into "addresseses".
	return strings.Join(words, " ")
}

// displayScalars returns every scalar a page may SHOW: the type's own
// non-list scalars, minus the ConfigItem interface's fields (which are identity
// and provenance, rendered separately where they matter).
//
// Deliberately NOT filtered by `editorIgnored`. That annotation answers "may a
// human type into this", and the answer has no bearing on whether they may read
// it.
func displayScalars(info TypeInfo, ifaceFields []string) []string {
	iface := make(map[string]bool, len(ifaceFields))
	for _, f := range ifaceFields {
		iface[f] = true
	}
	out := []string{}
	for _, f := range info.Fields {
		if !f.Editable || iface[f.Name] {
			continue
		}
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// metaOrder is the order the provenance fields read best in, and is the order
// every hand-written detail page showed them. Interface fields outside this
// list are appended, so adding one to the ConfigItem interface surfaces it
// without a code change here.
var metaOrder = []string{"namespace", "orbId", "version", "createdBy", "createdAt", "updatedAt", "updatedBy"}

// metaFields returns the ConfigItem interface fields a detail page shows, in
// display order.
//
// `name` is excluded: it is the page's heading, and repeating it as a row is
// the kind of duplication that makes a provenance box look like filler.
func metaFields(ifaceFields []string) []string {
	have := make(map[string]bool, len(ifaceFields))
	for _, f := range ifaceFields {
		if f != "name" && f != "id" {
			have[f] = true
		}
	}
	out := make([]string, 0, len(have))
	for _, f := range metaOrder {
		if have[f] {
			out = append(out, f)
			delete(have, f)
		}
	}
	rest := make([]string, 0, len(have))
	for f := range have {
		rest = append(rest, f)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// fieldType returns the named type of a field on a type, and whether it exists.
func fieldType(info TypeInfo, field string) (string, bool) {
	for _, f := range info.Fields {
		if f.Name == field {
			return f.TypeName, f.TypeName != ""
		}
	}
	return "", false
}
