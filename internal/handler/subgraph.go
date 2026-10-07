package handler

import (
	"sort"

	"github.com/armada/orbital/internal/configitems"
)

// subgraphOrbIDs returns rootOrbID plus the orbId of every entity the root's
// page declares in its `subgraph:` — the set its audit tab covers and the
// change-request scope pin takes as its staleness base.
//
// The DECLARED subgraph and nothing else. It walks the declared paths over what
// the caller already fetched, so it can only report an orbId the query
// selected; the detail query and SubgraphSelection both select exactly these
// paths. An entity reachable only through an intermediate hop the page did not
// list is not in the set, because the dev did not put it there.
//
// Root first, then the rest sorted: this is rendered into an HTML attribute and
// Go map iteration is random, so an unsorted tail churns on every render.
func subgraphOrbIDs(views configitems.ViewSet, rootType, rootOrbID string, entity map[string]any) []string {
	out := []string{rootOrbID}
	if rootType == "" || entity == nil {
		return out
	}
	seen := map[string]bool{rootOrbID: true}

	var walk func(n *configitems.PathNode, node map[string]any)
	walk = func(n *configitems.PathNode, node map[string]any) {
		for _, c := range n.Children {
			for _, child := range childNodes(node[c.Field]) {
				if c.Member != nil {
					if id, _ := child["orbId"].(string); id != "" && !seen[id] {
						seen[id] = true
						out = append(out, id)
					}
				}
				walk(c, child)
			}
		}
	}
	walk(configitems.PathTree(views.SubgraphFor(rootType)), entity)

	sort.Strings(out[1:])
	return out
}
