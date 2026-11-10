package configitems

import (
	"context"
	"sort"
	"strings"
)

// SchemaGraph is the deployed schema as nodes and edges: one node per
// ConfigItem type or interface, one edge per relationship. It carries
// everything a renderer needs to draw the schema top-down, so a client lays it
// out without knowing orbital's rules: each node's level, and which edges set
// those levels. Positions are deliberately absent — they belong to whichever
// library draws it.
type SchemaGraph struct {
	Nodes []SchemaGraphNode `json:"nodes"`
	Edges []SchemaGraphEdge `json:"edges"`
}

// SchemaGraphNode is one ConfigItem type or interface.
type SchemaGraphNode struct {
	// ID is the GraphQL type name, and what edges name in source/target.
	ID string `json:"id" example:"NetworkAdapter"`
	// Label is the type name split into words, for display.
	Label string `json:"label" example:"Network Adapter"`
	// Kind is "interface" for an abstract type (ConfigItem, KubernetesCluster),
	// otherwise "type".
	Kind string `json:"kind" example:"type" enums:"type,interface"`
	// Level is the row the type is drawn on, 0 at the top: one below the type it
	// lives inside. A drawing rule, not an ownership claim — see DGRAPH.md.
	Level int `json:"level" example:"2"`
	// Order is left to right among siblings, from the views config's
	// schemaGraph.order; unlisted types share the last value and go A–Z.
	Order int `json:"order" example:"1"`
}

// SchemaGraphEdge is one relationship (an `@hasInverse` pair is ONE edge) or
// one interface implementation.
type SchemaGraphEdge struct {
	// ID is "<source>.<field>" for a relationship.
	ID     string `json:"id" example:"Server.networkAdapters"`
	Source string `json:"source" example:"Server"`
	Target string `json:"target" example:"NetworkAdapter"`
	// Field is the field on the source pointing at the target; Inverse the field
	// on the target pointing back, empty when the schema declares none. Both are
	// empty on an implements edge.
	Field   string `json:"field,omitempty" example:"networkAdapters"`
	Inverse string `json:"inverse,omitempty" example:"server"`
	// Cardinality reads source-to-target: "1:N" is one source holding many
	// targets. Without an inverse it comes from the source field alone.
	Cardinality string `json:"cardinality,omitempty" example:"1:N" enums:"1:1,1:N,N:1,N:M"`
	Kind        string `json:"kind" example:"relationship" enums:"relationship,implements"`
	// SetsLevel marks the one edge that places its target below its source.
	// Every type below level 0 has exactly one; every other edge is a
	// cross-link and must not move a type between levels.
	SetsLevel bool `json:"setsLevel" example:"true"`
}

// SchemaGraph returns the deployed schema as a graph, with the views config's
// canonicalParent declarations deciding where a type with several parents is
// drawn.
func (r *Resolver) SchemaGraph(ctx context.Context) (SchemaGraph, error) {
	if err := r.ensure(ctx); err != nil {
		return SchemaGraph{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return BuildSchemaGraph(r.snapshot, r.inverse, r.viewConfig.CanonicalParent, r.viewConfig.SchemaGraph.Order), nil
}

const configItemInterface = "ConfigItem"

type graphEnd struct {
	typ, field string
	list       bool
}

// graphLink is a relationship (a.field on a.typ points at b.typ, b.field is its
// inverse) or, when implements is set, a.typ is an interface b.typ implements.
type graphLink struct {
	a, b       graphEnd
	implements bool
}

type graphParent struct {
	typ  string
	link int
}

// BuildSchemaGraph lays the schema out in levels.
//
// A type LIVES INSIDE another when it cannot exist without it — a non-null
// edge back — or when its orbId is built from that edge. It is drawn one level
// below the nearest such parent, and that edge is the one that sets its level.
// Three refinements:
//   - a parent typed by an interface counts as each implementor, so
//     ClusterBackup sits under EksaKubernetesCluster, not the abstract type;
//   - an implementor sits one level below its interface;
//   - a canonicalParent declaration overrides the choice, and places a type
//     that lives inside nothing (IPAddress) — DGRAPH.md calls picking one
//     parent "a view decision, not a derivable fact".
//
// ConfigItem is a node with no edges: every type implements it, so a line to
// each would say nothing.
func BuildSchemaGraph(types map[string]TypeInfo, inverse map[string]string, homes map[string][]CanonicalParentDecl, order []string) SchemaGraph {
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)

	field := func(typeName, name string) (DerivedField, bool) {
		for _, f := range types[typeName].Fields {
			if f.Name == name {
				return f, true
			}
		}
		return DerivedField{}, false
	}
	// Introspection lists an interface's fields on every implementor too. The
	// relationship is the interface's, so it is drawn once, from there.
	inherited := func(typeName, name string) bool {
		for _, in := range types[typeName].Implements {
			if types[in].IsInterface {
				if _, ok := field(in, name); ok {
					return true
				}
			}
		}
		return false
	}

	var links []graphLink
	seen := map[string]bool{}
	for _, t := range names {
		for _, f := range types[t].Fields {
			if f.Kind != "OBJECT" && f.Kind != "INTERFACE" {
				continue
			}
			if _, ok := types[f.TypeName]; !ok || inherited(t, f.Name) {
				continue
			}
			key := t + "." + f.Name
			inv := inverse[key]
			if inv != "" {
				pair := []string{key, f.TypeName + "." + inv}
				sort.Strings(pair)
				key = pair[0] + "|" + pair[1]
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			l := graphLink{a: graphEnd{typ: t, field: f.Name, list: f.IsList}, b: graphEnd{typ: f.TypeName, field: inv}}
			if far, ok := field(f.TypeName, inv); ok && inv != "" {
				l.b.list = far.IsList
			}
			links = append(links, l)
		}
	}
	for _, iface := range names {
		if !types[iface].IsInterface {
			continue
		}
		impls := append([]string(nil), types[iface].PossibleTypes...)
		sort.Strings(impls)
		for _, impl := range impls {
			if _, ok := types[impl]; ok {
				links = append(links, graphLink{a: graphEnd{typ: iface}, b: graphEnd{typ: impl}, implements: true})
			}
		}
	}

	// dependsOn: the child end's field is required, or its orbId is built from it.
	dependsOn := func(child graphEnd) bool {
		if f, ok := field(child.typ, child.field); ok && f.NonNull {
			return true
		}
		for _, p := range types[child.typ].OrbIDPattern {
			for _, path := range p.Paths() {
				if strings.SplitN(path, ".", 2)[0] == child.field {
					return true
				}
			}
		}
		return false
	}
	parents := map[string][]graphParent{}
	add := func(child, parent string, link int) {
		if child == parent {
			return
		}
		if types[parent].IsInterface && !links[link].implements {
			for _, impl := range types[parent].PossibleTypes {
				if _, ok := types[impl]; ok && impl != child {
					parents[child] = append(parents[child], graphParent{typ: impl, link: link})
				}
			}
			return
		}
		parents[child] = append(parents[child], graphParent{typ: parent, link: link})
	}
	for i, l := range links {
		switch {
		case l.implements:
			add(l.b.typ, l.a.typ, i)
		default:
			if dependsOn(l.a) {
				add(l.a.typ, l.b.typ, i)
			}
			if l.b.field != "" && dependsOn(l.b) {
				add(l.b.typ, l.a.typ, i)
			}
		}
	}

	// Nearest parent first: shortest distance from a type that lives inside
	// nothing. Longest path would push KubernetesNode a level down through its
	// cluster, which is the farther of its two parents.
	const unset = -1
	nearest := map[string]int{}
	for _, n := range names {
		nearest[n] = unset
		if len(parents[n]) == 0 {
			nearest[n] = 0
		}
	}
	for changed := true; changed; {
		changed = false
		for _, n := range names {
			for _, p := range parents[n] {
				if nearest[p.typ] == unset {
					continue
				}
				if l := nearest[p.typ] + 1; nearest[n] == unset || l < nearest[n] {
					nearest[n], changed = l, true
				}
			}
		}
	}

	home := func(n string) (graphParent, bool) {
		for _, d := range homes[n] {
			if _, ok := types[d.Type]; !ok {
				continue
			}
			for i, l := range links {
				if l.implements {
					continue
				}
				if (l.a.typ == n && l.a.field == d.Field && l.b.typ == d.Type) ||
					(l.b.typ == n && l.b.field == d.Field && l.a.typ == d.Type) {
					return graphParent{typ: d.Type, link: i}, true
				}
			}
		}
		return graphParent{}, false
	}
	chosen := map[string]graphParent{}
	for _, n := range names {
		if p, ok := home(n); ok {
			chosen[n] = p
			continue
		}
		for _, p := range parents[n] {
			if nearest[p.typ] == unset {
				continue
			}
			if c, ok := chosen[n]; !ok || nearest[p.typ] < nearest[c.typ] {
				chosen[n] = p
			}
		}
	}
	// A declared home can close a loop the derivation never would. Break it
	// where it closes rather than recurse forever.
	for _, n := range names {
		onPath := map[string]bool{}
		for cur := n; ; {
			p, ok := chosen[cur]
			if !ok {
				break
			}
			if onPath[cur] {
				delete(chosen, cur)
				break
			}
			onPath[cur] = true
			cur = p.typ
		}
	}
	level := map[string]int{}
	var depth func(string) int
	depth = func(n string) int {
		if v, ok := level[n]; ok {
			return v
		}
		v := 0
		if p, ok := chosen[n]; ok {
			v = depth(p.typ) + 1
		}
		level[n] = v
		return v
	}
	childOf := map[int]string{}
	for _, n := range names {
		depth(n)
		if p, ok := chosen[n]; ok {
			childOf[p.link] = n
		}
	}

	rank := func(id string) int {
		for i, t := range order {
			if t == id {
				return i
			}
		}
		return len(order)
	}
	var g SchemaGraph
	for _, n := range names {
		kind := "type"
		if types[n].IsInterface {
			kind = "interface"
		}
		g.Nodes = append(g.Nodes, SchemaGraphNode{ID: n, Label: HumanTypeName(n), Kind: kind, Level: level[n], Order: rank(n)})
	}
	if _, ok := types[configItemInterface]; !ok {
		g.Nodes = append(g.Nodes, SchemaGraphNode{
			ID: configItemInterface, Label: HumanTypeName(configItemInterface), Kind: "interface",
			Order: rank(configItemInterface)})
	}
	sort.SliceStable(g.Nodes, func(i, j int) bool {
		if g.Nodes[i].Level != g.Nodes[j].Level {
			return g.Nodes[i].Level < g.Nodes[j].Level
		}
		if g.Nodes[i].Order != g.Nodes[j].Order {
			return g.Nodes[i].Order < g.Nodes[j].Order
		}
		return g.Nodes[i].ID < g.Nodes[j].ID
	})

	for i, l := range links {
		child, setsLevel := childOf[i]
		if l.implements {
			g.Edges = append(g.Edges, SchemaGraphEdge{
				ID: "implements:" + l.a.typ + ":" + l.b.typ, Source: l.a.typ, Target: l.b.typ,
				Kind: "implements", SetsLevel: setsLevel})
			continue
		}
		src, dst := l.a, l.b
		switch {
		case setsLevel:
			if src.typ == child {
				src, dst = dst, src
			}
		case dst.field == "":
		case !src.list && dst.list:
			src, dst = dst, src
		case src.list == dst.list && dst.typ+"."+dst.field < src.typ+"."+src.field:
			src, dst = dst, src
		}
		g.Edges = append(g.Edges, SchemaGraphEdge{
			ID: src.typ + "." + src.field, Source: src.typ, Target: dst.typ,
			Field: src.field, Inverse: dst.field, Cardinality: cardinality(src, dst),
			Kind: "relationship", SetsLevel: setsLevel})
	}
	sort.Slice(g.Edges, func(i, j int) bool { return g.Edges[i].ID < g.Edges[j].ID })
	return g
}

func cardinality(src, dst graphEnd) string {
	switch {
	case dst.field == "" && src.list:
		return "1:N"
	case dst.field == "":
		return "1:1"
	case src.list && dst.list:
		return "N:M"
	case src.list:
		return "1:N"
	case dst.list:
		return "N:1"
	default:
		return "1:1"
	}
}
