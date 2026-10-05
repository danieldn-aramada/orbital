package configitems

import (
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
	// Slug is the URL segment, and EMPTY means the type has no page: it renders
	// as rows on somebody else's page and nothing links to it. The top level of
	// `pages:` is the menu, so having a slug and being in the menu are one fact,
	// not two — an `isRoot` alongside this could only ever restate it or lie.
	//
	// A CONTRACT where it is set: integrators and bookmarks depend on it, so
	// changing one is a deliberate breaking act.
	Slug string `json:"slug"`

	// Type is the GraphQL type name.
	Type string `json:"type"`

	// Label is DISPLAY, not contract — free to change without breaking anyone.
	Label string `json:"label"`

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

	// Labels override the title-cased field name, for the fields where
	// title-casing is wrong — acronyms, essentially (`cni` → "Cni",
	// `oobIP` → "Oob IP").
	//
	// Carried on the view, resolved server-side, for the same reason every other
	// computed value here is: an integrator building a table from
	// orbital's responses should not have to re-derive its title-casing rule to
	// agree with orbital's own headers.
	Labels map[string]string `json:"labels,omitempty"`

	// Contains is what this type cannot be deleted without taking with it —
	// derived from the schema's non-null back-edges, plus the one declared
	// exception. It drives the delete cascade, the audit roll-up and the reach
	// of the editor's tree.
	//
	// A property of the TYPE, not of a page: a StorageController contains its
	// devices whether or not any page shows them.
	Contains []EditableMember `json:"contains,omitempty"`

	// SummaryRefs are the link rows under a detail page's field list, in the
	// order declared. A PAGE fact: it is what that one screen shows.
	//
	// Separate from RefColumns, which is a TYPE fact — the two were briefly one
	// field, and collapsing them emptied the reference columns of every table
	// showing a pageless type. A NetworkInterface has no page and still needs to
	// say which Server it belongs to when it renders as a row.
	SummaryRefs []ViewRefColumn `json:"summaryRefs,omitempty"`

	// RefColumns are the SINGLE relationships rendered as an extra column when
	// this type appears as a row ANYWHERE: a reference to another entity shows
	// WHICH entity.
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

	// Editable marks the member as part of THIS page's edit unit: fetched with
	// its own fields, written through the parent's JSON tree, and rolled up onto
	// the parent's audit tab.
	//
	// Published because it answers a question an integrator has to answer too —
	// "if I write this entity, do I write it here or on its own page?" — and the
	// alternative is every client re-deriving containment from the graph.
	Editable bool `json:"editable,omitempty"`
}

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

func resolveColumnPaths(types map[string]TypeInfo, info TypeInfo, typeName string, ifaceFields, paths []string) ([]ViewColumn, []string) {
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
// Deliberately NOT filtered by editability. That answers "may a human type into
// this", and the answer has no bearing on whether they may read it — a
// StorageDevice whose every field is a scanned hardware fact would otherwise
// render nothing at all.
//
// `ignore` is the page's subtractive scalar list: scalars are shown unless
// NAMED, so a field added to the schema appears without anyone editing the file.
func displayScalars(info TypeInfo, ifaceFields []string, ignore map[string]bool) []string {
	iface := make(map[string]bool, len(ifaceFields))
	for _, f := range ifaceFields {
		iface[f] = true
	}
	out := []string{}
	for _, f := range info.Fields {
		if !f.Editable || iface[f.Name] || ignore[f.Name] {
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
