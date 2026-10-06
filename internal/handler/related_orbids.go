package handler

import (
	"sort"

	"github.com/armada/orbital/internal/configitems"
)

// maxOwnershipDepth bounds the walk for the same reason OwnedOrbIDSelection is
// bounded: the ownership graph is operator-editable through `ownerReferences:`
// and nothing stops a cycle being declared.
const maxOwnershipDepth = 8

// ownedSubtreeOrbIDs returns rootOrbID plus the orbId of every entity the root
// OWNS, at every depth the ownership graph declares.
//
// The FULL subtree — everything a delete of the root would cascade to. The
// change-request scope pin takes this one: its staleness base has to cover what
// could change, and that is the cascade set, not a display subset.
//
// ONE walk for both consumers — this and auditRollupOrbIDs. They answer the
// same question and had drifted: the page's walk type-asserted map[string]any,
// so every LIST dependent (a server's NICs, adapters, storage controllers) was
// skipped in silence, while the scope pin included them. A server rolled up 3
// orbIds and cascade-deleted ~30.
func ownedSubtreeOrbIDs(views configitems.ViewSet, rootType, rootOrbID string, entity map[string]any) []string {
	return walkOwned(views, rootType, rootOrbID, entity, nil)
}

// auditRollupOrbIDs is the subtree a parent's AUDIT TAB covers: the owned
// subtree, stopping at any dependent that has its own PAGE.
//
// A type with a page has somewhere its events already live; a type without one
// does not, and carrying those is the whole job. It is the principle
// `canonicalParent:` already encodes — with no /racks/<id>, the data centre is
// the only answer to "that rack turned up in a diff, where do I look at it?".
//
// It is also what keeps the roll-up BOUNDED. A data centre owns every server,
// NIC and disk in it — 1,159 orbIds on the seeded colo namespace — and the
// audit-log API refuses a filter over 128 rather than truncating it, so an
// unbounded roll-up does not degrade, it 400s. Stopping at pages takes that
// data centre to 5 — itself and its four racks, which have no page of their
// own — while leaving a server's 34 untouched, because nothing a server owns
// has a page.
//
// The DIFFERENCE from ownedSubtreeOrbIDs is deliberate and lives here, at one
// call each, rather than in a second walk — two implementations of this is what
// the unification removed.
func auditRollupOrbIDs(views configitems.ViewSet, rootType, rootOrbID string, entity map[string]any) []string {
	return walkOwned(views, rootType, rootOrbID, entity, func(typeName string) bool {
		// Slug is non-empty exactly for a type with a `pages:` entry
		// (configitems.pageSlug), so this is the page test, not a URL detail.
		return views.Of(typeName).Slug != ""
	})
}

// walkOwned is the single traversal. `stopAt` names dependent types the walk
// neither collects nor descends into; nil walks everything.
//
// Ownership-GUIDED rather than a blind scrape for orbIds: it descends only the
// fields View.Dependents names, so a result that also carries non-owned
// references (a rack, a data centre, a cluster) cannot widen the set.
//
// Reads what the caller already fetched. It can therefore only report an orbId
// the query actually selected — which is why genericDetailQuery selects owned
// children the page does not tab.
//
// Root first, then descendants sorted: this is rendered into an HTML attribute
// and Go map iteration is random, so an unsorted tail churns on every render.
func walkOwned(views configitems.ViewSet, rootType, rootOrbID string, entity map[string]any, stopAt func(string) bool) []string {
	out := []string{rootOrbID}
	if rootType == "" || entity == nil {
		return out
	}
	seen := map[string]bool{rootOrbID: true}

	var walk func(typeName string, node map[string]any, path map[string]bool, depth int)
	walk = func(typeName string, node map[string]any, path map[string]bool, depth int) {
		if node == nil || depth >= maxOwnershipDepth || path[typeName] {
			return
		}
		path[typeName] = true
		defer delete(path, typeName)

		for _, oc := range views.Of(typeName).Dependents {
			if stopAt != nil && stopAt(oc.ChildType) {
				continue
			}
			for _, child := range childNodes(node[oc.ChildField]) {
				if id, _ := child["orbId"].(string); id != "" && !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
				walk(oc.ChildType, child, path, depth+1)
			}
		}
	}
	walk(rootType, entity, map[string]bool{}, 0)

	sort.Strings(out[1:])
	return out
}
