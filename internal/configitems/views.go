package configitems

import (
	"fmt"
	"sort"
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
		pinned := OrderFor(info.Doc)
		for _, in := range info.Implements {
			if len(pinned) > 0 {
				break
			}
			pinned = OrderFor(types[in].Doc)
		}
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
		views = append(views, View{
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
			Fields:          fields,
			Tabs:            tabs,
		})
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
	return views, nil
}

// isRootType reports whether nothing owns this type.
//
// Derived from containment rather than declared. The registry used to carry an
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
