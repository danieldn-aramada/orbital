package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/armada/orbital/internal/configitems"
	"github.com/labstack/echo/v4"
)

// The cascade delete: the root plus its page's DECLARED subgraph.
//
// The page decides what dies — the paths under `subgraph:` in
// config/views.yaml, and nothing derived from the schema. GraphQL has no
// concept of ownership; `!` means non-null and nothing more. This route serves
// orbital's own UI and follows that UI's page config; an API client deletes
// through /graphql and composes its own set.
//
// What the schema still decides is what must be REPAIRED. Every node outside
// the deleted set that holds an edge into it has that edge cleared in the same
// transaction, and a node whose edge is NON-NULL is named in the preview as
// ORPHANED: the dev's subgraph left it holding a required link to nothing.
// DGraph propagates a missing non-null field to the ROOT of any query selecting
// it, so an orphan can break export for its whole data centre — DGRAPH.md
// records it happening. The preview informs; it does not prevent.

// cascadePlan is everything a delete needs, read in one query at one instant.
type cascadePlan struct {
	preview  DeletePreview
	uids     []string
	versions map[string]int
	orbID    string
	before   map[string]any
	dangling []danglingEdge
}

// planCascade reads the subgraph a delete would remove, and the edges held by
// everything that survives it.
//
// Against the CONCRETE type, never an interface: DGraph generates no
// `get<Interface>`, and a view backed by an interface has the same members as
// each implementation anyway (introspection inherits them), so the walk is
// identical and the special case that used to map Eksa→KubernetesCluster is
// gone.
func (h *DeleteHandler) planCascade(ctx context.Context, views configitems.ViewSet, typeName, orbID string) (*cascadePlan, error) {
	v := views.Of(typeName)
	if v.Type == "" {
		return nil, fmt.Errorf("%q is not a type this deployment renders, so there is no cascade to compute", typeName)
	}

	// An INTERFACE root resolves to its concrete type first. DGraph generates
	// `query<Interface>` but no `get<Interface>` — an interface filter carries
	// only its own @search fields and `orbId` is declared on ConfigItem — so the
	// row's own type has to be read from the data. The same move the detail route
	// makes, for the same reason.
	//
	// This is the INVERSE of the mapping that used to be here. `deletableType`
	// turned a concrete type into the interface whose hand-written plan covered
	// it; nothing needs that now, because the plan is derived per type.
	if v.IsInterface {
		concrete, err := h.concreteTypeOf(ctx, orbID)
		if err != nil {
			return nil, err
		}
		typeName, v = concrete, views.Of(concrete)
		if v.Type == "" {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "unknown type for "+orbID)
		}
	}

	inverse := inverseWithInterfaces(views, h.inverseOf(ctx))
	fieldsOf := h.fieldsOf()
	tree := configitems.PathTree(views.SubgraphFor(typeName))
	sel := cascadeSelection(views, fieldsOf, inverse, typeName, tree)
	query := fmt.Sprintf(`query PlanDelete($orbId: String!) { get%s(orbId: $orbId) { %s } }`, typeName, sel)
	raw, err := h.gqlQuery(ctx, query, map[string]any{"orbId": orbID})
	if err != nil {
		return nil, err
	}
	// gqlQuery returns `data`, not the root field inside it.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode delete plan: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(envelope["get"+typeName], &root); err != nil {
		return nil, fmt.Errorf("decode delete plan: %w", err)
	}
	if root == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, typeName+" "+orbID+" not found")
	}

	w := &cascadeWalk{views: views, fieldsOf: fieldsOf, inverse: inverse, seenUID: map[string]bool{}}
	w.walk(typeName, root, tree, true, true)
	w.resolveSurvivors()

	rootName, _ := root["name"].(string)
	if hostname, ok := root["hostname"].(string); ok && hostname != "" {
		rootName = hostname
	}
	version := 0
	if vv, ok := root["version"].(float64); ok {
		version = int(vv)
	}

	versions, err := h.planVersions(ctx, w.uids)
	if err != nil {
		return nil, err
	}

	return &cascadePlan{
		preview: DeletePreview{
			Name:       rootName,
			Type:       typeName,
			TypeLabel:  singularLabel(views, typeName),
			TotalCount: len(w.uids),
			Version:    version,
			Groups:     w.groups(),
			Preserved:  w.preservedGroups(),
			Orphaned:   w.orphanedGroups(),
		},
		uids:     w.uids,
		versions: versions,
		orbID:    orbID,
		before:   w.before(typeName, root, rootName, orbID),
		dangling: w.dangling,
	}, nil
}

// cascadeSelection builds the GraphQL selection for one node of the walk.
//
// It iterates the SCHEMA's relationship fields, not only the declared paths,
// and the difference is a correctness requirement rather than a preference.
//
// The subgraph decides what is DELETED. It does not get to decide what is
// CLEARED: a node that points at something being deleted is left holding an
// edge to a tombstone whether or not any page chose to show that edge, and
// DGraph then fails every query that walks it. `docs/reference/DGRAPH.md` states
// the obligation as a rule: any DQL write that removes a node must also remove
// the edges held by nodes that SURVIVE it.
//
// So: a declared hop is followed; every other relationship with a declared
// inverse is selected deeply enough to identify the far node. An INTERMEDIATE
// hop — a node the path passes through without declaring — survives, so only
// the declared hops below it are followed.
func cascadeSelection(views configitems.ViewSet, fieldsOf func(string) []configitems.DerivedField,
	inverse func(string, string) string, typeName string, n *configitems.PathNode) string {

	sel := idSel(views, typeName)
	if typeName == "Server" {
		// The one display quirk the preview depends on: a server is named by its
		// hostname where it has one.
		sel += " hostname"
	}
	dying := n.Member != nil || n.Field == ""
	for _, f := range fieldsOf(typeName) {
		if f.Kind == "SCALAR" || f.Kind == "ENUM" || f.TypeName == "" {
			continue
		}
		if c := hop(n, f.Name); c != nil {
			sel += " " + f.Name + " { " + cascadeSelection(views, fieldsOf, inverse, f.TypeName, c) + " }"
			continue
		}
		if !dying || inverse(typeName, f.Name) == "" {
			// A surviving intermediate's edges are not ours to repair, and an
			// edge nothing points back along cannot be left dangling. Not
			// selected — a delete should not read more of the graph than it has
			// an obligation to repair.
			continue
		}
		sel += " " + f.Name + " { " + idSel(views, f.TypeName) + " }"
	}
	return sel
}

// hop returns the declared path node one field below n, or nil.
func hop(n *configitems.PathNode, field string) *configitems.PathNode {
	for _, c := range n.Children {
		if c.Field == field {
			return c
		}
	}
	return nil
}

// idSel is enough to identify and name a node. `id` and `orbId` live on the
// ConfigItem interface, so selecting them bare off a SUB-interface is rejected
// at validation — the inline fragment is the only spelling that works for both.
func idSel(views configitems.ViewSet, typeName string) string {
	if views.Of(typeName).IsInterface {
		return "__typename ... on ConfigItem { id orbId name version }"
	}
	return "id orbId name version"
}

// cascadeWalk collects what dies, what survives, and the edges a survivor holds
// into the dying set — in one pass over the response, so the preview and the
// delete can never describe different sets.
type cascadeWalk struct {
	views    configitems.ViewSet
	fieldsOf func(typeName string) []configitems.DerivedField
	inverse  func(typeName, field string) string

	uids      []string
	seenUID   map[string]bool
	byType    map[string][]string // type -> display names, for the preview
	preserved map[string][]string
	orphaned  map[string][]string

	// candidates are edges into the deleted set from nodes that MIGHT survive.
	// Resolved after the walk, because survival is a property of the whole set:
	// a cluster's node points back at the cluster deleting it, and clearing an
	// edge on a node in the same delete makes the upsert fight itself.
	candidates []survivorEdge
	dangling   []danglingEdge
}

// survivorEdge is one candidate back-edge plus what the preview needs to name
// the node holding it.
type survivorEdge struct {
	uid      string
	typeName string
	name     string
	edge     danglingEdge
	// required marks a NON-NULL edge: the survivor is left holding a required
	// link to nothing.
	required bool
}

// resolveSurvivors keeps only the edges held by nodes that OUTLIVE the delete.
//
// DGRAPH.md's rule is precise about this: only survivors matter. When both ends
// are deleted the stale edge sits on a tombstone and is unreachable — and
// clearing it anyway puts the same node in the delete and the edge-clear halves
// of one upsert, which the version guard then reads as a concurrent edit and
// refuses the whole delete.
func (w *cascadeWalk) resolveSurvivors() {
	// A survivor can hold several edges into the deleted set. Every edge is
	// cleared, but the node is NAMED once — under orphaned if any of its edges
	// is required, since that is the consequence the operator has to see.
	type named struct {
		typeName, name string
		required       bool
	}
	byUID := map[string]*named{}
	var order []string
	for _, c := range w.candidates {
		if w.seenUID[c.uid] {
			continue
		}
		w.dangling = append(w.dangling, c.edge)
		n, ok := byUID[c.uid]
		if !ok {
			n = &named{typeName: c.typeName, name: c.name}
			byUID[c.uid] = n
			order = append(order, c.uid)
		}
		n.required = n.required || c.required
	}
	for _, uid := range order {
		n := byUID[uid]
		into := &w.preserved
		if n.required {
			into = &w.orphaned
		}
		if *into == nil {
			*into = map[string][]string{}
		}
		(*into)[n.typeName] = append((*into)[n.typeName], n.name)
	}
}

// displayName is what the preview calls a node.
func displayName(node map[string]any) string {
	if n, _ := node["name"].(string); n != "" {
		return n
	}
	id, _ := node["orbId"].(string)
	return id
}

// walk visits one node. `dying` is false for an intermediate hop: a node a
// declared path passes through without declaring, which survives the delete.
func (w *cascadeWalk) walk(typeName string, node map[string]any, n *configitems.PathNode, dying, isRoot bool) {
	uid, _ := node["id"].(string)
	if uid == "" {
		return
	}
	if dying {
		if w.seenUID[uid] {
			return
		}
		w.seenUID[uid] = true
		w.uids = append(w.uids, uid)
		if !isRoot {
			w.record(&w.byType, typeName, node)
		}
	}

	for _, f := range w.fieldsOf(typeName) {
		if f.Kind == "SCALAR" || f.Kind == "ENUM" || f.TypeName == "" {
			continue
		}
		c := hop(n, f.Name)
		for _, child := range childNodes(node[f.Name]) {
			childType := childTypeOf(f.TypeName, child)
			if c != nil {
				w.walk(childType, child, c, c.Member != nil, false)
				if c.Member != nil {
					continue
				}
				// An intermediate below a dying node survives it, so its edge
				// back is a survivor's edge like any other.
			}
			if !dying {
				continue
			}
			// A survivor. It is named in the preview so an operator can see what
			// stays behind, and its edge BACK into the dying node is cleared in
			// the same transaction — a DQL delete does not maintain
			// `@hasInverse`, so without this it is left pointing at a tombstone.
			far, _ := child["id"].(string)
			back := w.inverse(typeName, f.Name)
			if far == "" || back == "" {
				continue
			}
			// Recorded as a CANDIDATE. Whether this node survives is not known
			// yet: an edge can be walked before the node at its far end is
			// reached by another branch. Resolved once the whole set is known.
			w.candidates = append(w.candidates, survivorEdge{
				uid:      far,
				typeName: childType,
				name:     displayName(child),
				required: w.nonNull(childType, back),
				edge: danglingEdge{
					ParentUID: far,
					Predicate: w.predicateFor(childType, back),
					ChildUID:  uid,
				},
			})
		}
	}
}

// nonNull reports whether a type's field is declared non-null, on the type or
// on an interface it implements.
func (w *cascadeWalk) nonNull(typeName, field string) bool {
	for _, t := range append([]string{typeName}, w.views.Implements(typeName)...) {
		for _, f := range w.fieldsOf(t) {
			if f.Name == field {
				return f.NonNull
			}
		}
	}
	return false
}

// inverseWithInterfaces finds the edge pointing back, trying the concrete type
// and then every interface it implements.
//
// DGraph forbids redeclaring an interface field on an implementor, so
// `dataCenter` is declared once on the KubernetesCluster INTERFACE and carries
// `@hasInverse` there. A lookup keyed only on `EksaKubernetesCluster` finds
// nothing — and finding nothing here means a survivor is silently left pointing
// at a tombstone, which is the precise failure this clearing exists to prevent.
//
// Wrapped once and shared by the query builder and the walk, deliberately: when
// only the walk had the fallback, the field was never SELECTED, so the walk had
// nothing to apply it to and the edge went uncleared.
func inverseWithInterfaces(views configitems.ViewSet, inverse func(string, string) string) func(string, string) string {
	return func(typeName, field string) string {
		if back := inverse(typeName, field); back != "" {
			return back
		}
		for _, iface := range views.Implements(typeName) {
			if back := inverse(iface, field); back != "" {
				return back
			}
		}
		return ""
	}
}

// predicateFor names the DQL predicate an edge is stored under.
//
// DGraph namespaces a predicate by the type that DECLARES the field, not by the
// row's concrete type: `nodes` is declared on the KubernetesCluster INTERFACE,
// so the predicate is `KubernetesCluster.nodes` and `EksaKubernetesCluster.nodes`
// does not exist. Clearing the wrong name silently removes nothing, and the
// survivor is left pointing at a tombstone — the exact failure this is for.
//
// Verified against the live DQL schema: of the four `*.servers`/`*.nodes`
// predicates, only `KubernetesCluster.nodes` is interface-namespaced, which is
// why every hand-written list got away with using the concrete type.
func (w *cascadeWalk) predicateFor(typeName, field string) string {
	for _, iface := range w.views.Implements(typeName) {
		for _, f := range w.fieldsOf(iface) {
			if f.Name == field {
				return iface + "." + field
			}
		}
	}
	return typeName + "." + field
}

// childTypeOf resolves a row's concrete type, which matters when a member is
// typed as an interface — `KubernetesNode.cluster` is a `KubernetesCluster`, and
// what it actually holds is an `EksaKubernetesCluster` with its own members.
func childTypeOf(declared string, node map[string]any) string {
	if t, ok := node["__typename"].(string); ok && t != "" {
		return t
	}
	return declared
}

func (w *cascadeWalk) record(into *map[string][]string, typeName string, node map[string]any) {
	if *into == nil {
		*into = map[string][]string{}
	}
	name, _ := node["name"].(string)
	if name == "" {
		name, _ = node["orbId"].(string)
	}
	(*into)[typeName] = append((*into)[typeName], name)
}

// groups is what WILL be deleted — counted, because the template renders a
// count and because a list of 240 hostnames tells an operator nothing they need.
func (w *cascadeWalk) groups() []DeleteGroup { return countedGroups(w.byType) }

// preservedGroups is what survives — NAMED, because "3 IP Addresses" does not
// let an operator check that the right ones stayed.
func (w *cascadeWalk) preservedGroups() []DeleteGroup { return namedGroups(w.preserved) }

// orphanedGroups is what survives holding a NON-NULL edge into the deleted set
// — named, for the same reason.
func (w *cascadeWalk) orphanedGroups() []DeleteGroup { return namedGroups(w.orphaned) }

func countedGroups(byType map[string][]string) []DeleteGroup {
	return buildGroups(byType, func(label string, items []string) DeleteGroup {
		return countGroup(label, len(items))
	})
}

func namedGroups(byType map[string][]string) []DeleteGroup {
	return buildGroups(byType, func(label string, items []string) DeleteGroup {
		if named := namedItems(items); named != nil {
			return namedGroup(label, named)
		}
		return countGroup(label, len(items))
	})
}

// buildGroups turns a type→names map into preview rows, in a stable order.
func buildGroups(byType map[string][]string, mk func(string, []string) DeleteGroup) []DeleteGroup {
	if len(byType) == 0 {
		return nil
	}
	names := make([]string, 0, len(byType))
	for t := range byType {
		names = append(names, t)
	}
	sort.Strings(names)
	out := make([]DeleteGroup, 0, len(names))
	for _, t := range names {
		items := byType[t]
		sort.Strings(items)
		out = append(out, mk(configitems.Label(configitems.DerivedSlug(t)), items))
	}
	return out
}

// namedItems returns the names worth printing, or nil when they are all blank —
// a column of empty strings is worse than a count.
func namedItems(items []string) []string {
	for _, s := range items {
		if s != "" {
			return items
		}
	}
	return nil
}

// before is the audit event's snapshot of what was removed.
func (w *cascadeWalk) before(typeName string, root map[string]any, name, orbID string) map[string]any {
	out := map[string]any{"name": name, "orbId": orbID, "type": typeName}
	if hostname, ok := root["hostname"].(string); ok && hostname != "" {
		out["hostname"] = hostname
	}
	for t, items := range w.byType {
		out[configitems.Pluralize(configitems.KebabTypeName(t))] = len(items)
	}
	return out
}

// childNodes normalises a relationship's value: DGraph returns an object for a
// single edge and an array for a list, and a nil for neither.
//
// Shared by the delete cascade and the subgraph walk (subgraphOrbIDs): an
// earlier walk handled only the object form, so the cascade deleted a
// server's NICs while its audit tab never showed their events.
func childNodes(v any) []map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return []map[string]any{t}
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// inverseOf returns the `@hasInverse` lookup, or one that answers nothing.
//
// Answering nothing means survivor edges are NOT cleared, which is a real
// degradation — so it is logged rather than absorbed. It cannot be made fatal:
// refusing every delete because the schema is briefly unreadable trades a
// silent problem for a loud outage.
func (h *DeleteHandler) inverseOf(ctx context.Context) func(string, string) string {
	if h.fields == nil {
		h.logger.Warn("delete: no schema source, so edges held by surviving nodes will NOT be cleared",
			"consequence", "a survivor may be left pointing at a deleted node")
		return func(string, string) string { return "" }
	}
	return func(typeName, field string) string { return h.fields.InverseOf(ctx, typeName, field) }
}

// concreteTypeOf reads a node's own type. An interface-backed page sends the
// interface name, and only the data knows which implementation it holds.
func (h *DeleteHandler) concreteTypeOf(ctx context.Context, orbID string) (string, error) {
	raw, err := h.gqlQuery(ctx,
		`query ConcreteType($orbId: String!) { queryConfigItem(filter: {orbId: {eq: $orbId}}) { __typename } }`,
		map[string]any{"orbId": orbID})
	if err != nil {
		return "", err
	}
	var envelope struct {
		Items []struct {
			Typename string `json:"__typename"`
		} `json:"queryConfigItem"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("resolve concrete type: %w", err)
	}
	if len(envelope.Items) == 0 || envelope.Items[0].Typename == "" {
		return "", echo.NewHTTPError(http.StatusNotFound, orbID+" not found")
	}
	return envelope.Items[0].Typename, nil
}

// fieldsOf reads a type's fields from the deployed schema, or none.
//
// None means the cascade degrades to the root node alone, which is safe: it
// removes less than it should rather than more, and the version guard still
// refuses anything that moved.
func (h *DeleteHandler) fieldsOf() func(string) []configitems.DerivedField {
	if h.fields == nil {
		return func(string) []configitems.DerivedField { return nil }
	}
	return func(typeName string) []configitems.DerivedField { return h.fields.Meta(typeName).Fields }
}

// singularLabel is the view's label with its plural dropped — "Data Centers"
// names a page, and a delete dialog is talking about one thing.
func singularLabel(views configitems.ViewSet, typeName string) string {
	label := views.Of(typeName).Label
	if label == "" {
		label = configitems.Label(configitems.DerivedSlug(typeName))
	}
	label = strings.ToLower(label)
	switch {
	case strings.HasSuffix(label, "ies"):
		return strings.TrimSuffix(label, "ies") + "y"
	case strings.HasSuffix(label, "sses"), strings.HasSuffix(label, "ches"), strings.HasSuffix(label, "shes"):
		return strings.TrimSuffix(label, "es")
	case strings.HasSuffix(label, "s"):
		return strings.TrimSuffix(label, "s")
	}
	return label
}
