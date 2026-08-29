package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/graphdiff"
)

// baseScope expands a change request's DECLARED orbIds into the full set its
// base hash covers: each declared orbId plus, for the ones that exist, every
// entity they own.
//
// Two properties matter and both are deliberate:
//
//   - Scoping by DECLARED orbIds, not by "what exists". An orbId that does not
//     exist yet stays in the scope, contributing nothing to the query result
//     today. If someone creates that entity during review, the result set grows,
//     the hash changes, and the request goes stale — which is exactly right: the
//     proposal was written as a create and is now an overwrite. No extra
//     detection machinery; the mechanism falls out of the scoping.
//
//   - Including the owned subtree. A Server's approved change is not independent
//     of its IdracSettings: the owned subtree is the unit a reviewer actually
//     looked at (D1), so a third party editing the child must invalidate the
//     review of the parent.
func baseScope(ctx context.Context, dgraphURL string, declared []string, existing map[string]approval.EntityRef) []string {
	seen := make(map[string]bool, len(declared))
	scope := make([]string, 0, len(declared))
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		scope = append(scope, id)
	}

	for _, id := range declared {
		add(id)
		ref, ok := existing[id]
		if !ok {
			continue // a create — nothing owned yet
		}
		for _, owned := range collectRelatedOrbIDs(ctx, dgraphURL, ref.Type, id) {
			add(owned)
		}
	}

	sort.Strings(scope)
	return scope
}

// baseSnapshot reads the live state of an explicit orbId set and normalizes it
// into the same canonical form the export preview and guarded Apply compare
// against, so a change request's staleness token is the same kind of token as
// theirs — one content hash over a scoped subgraph.
func baseSnapshot(ctx context.Context, dgraphURL string, scope []string) (graphdiff.Snapshot, error) {
	nodes, err := fetchOrbIDSubgraph(ctx, dgraphURL, scope)
	if err != nil {
		return nil, err
	}
	return graphdiff.NormalizeCurrent(nodes), nil
}

// presentIn returns the subset of scope that the snapshot actually contains,
// sorted. This is what gets persisted as base_present.
func presentIn(snap graphdiff.Snapshot, scope []string) []string {
	out := make([]string, 0, len(scope))
	for _, id := range scope {
		if _, ok := snap[id]; ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// fetchOrbIDSubgraph retrieves the live nodes for an explicit orbId set, in the
// shape graphdiff.NormalizeCurrent consumes.
//
// Structurally identical to fetchNamespaceSubgraph — same two blocks merged by
// uid, for the same reason (DGraph drops uid predicates from expand(_all_) when
// they form cycles, which every edge in our schema does) — with two differences:
//
//   - the filter is an orbId set rather than a namespace, and
//   - there is no Namespace-node block. A changeset cannot touch the Namespace
//     node, and folding it into the hash would make an unrelated namespace edit
//     mark every open change request in that namespace stale.
//
// orbIds with no entity are silently absent from the result; that is the
// create-detection mechanism baseScope depends on, not an error.
func fetchOrbIDSubgraph(ctx context.Context, dgraphURL string, orbIDs []string) ([]map[string]any, error) {
	if len(orbIDs) == 0 {
		return nil, nil
	}

	uidPreds, err := fetchUIDPredicates(ctx, dgraphURL)
	if err != nil {
		return nil, fmt.Errorf("fetch uid predicates: %w", err)
	}
	var edgeLines strings.Builder
	for _, p := range uidPreds {
		fmt.Fprintf(&edgeLines, "\t\t\t%s { uid }\n", p)
	}

	quoted := make([]string, len(orbIDs))
	for i, id := range orbIDs {
		b, err := json.Marshal(id)
		if err != nil {
			return nil, fmt.Errorf("encode orbId %q: %w", id, err)
		}
		quoted[i] = string(b)
	}
	list := strings.Join(quoted, ", ")

	dql := fmt.Sprintf(`{
		items(func: eq(ConfigItem.orbId, [%s])) {
			uid
			dgraph.type
			expand(_all_)
		}
		edges(func: eq(ConfigItem.orbId, [%s])) {
			uid
			%s
		}
	}`, list, list, edgeLines.String())

	body, err := json.Marshal(map[string]string{"query": dql})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dqlBase(dgraphURL)+"/query", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dql query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dql query failed (%d): %s", resp.StatusCode, b)
	}

	var result struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Edges []map[string]any `json:"edges"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode dql response: %w", err)
	}

	edgesByUID := make(map[string]map[string]any, len(result.Data.Edges))
	for _, e := range result.Data.Edges {
		if uid, ok := e["uid"].(string); ok {
			edgesByUID[uid] = e
		}
	}
	for _, node := range result.Data.Items {
		uid, ok := node["uid"].(string)
		if !ok {
			continue
		}
		for k, v := range edgesByUID[uid] {
			if k == "uid" {
				continue
			}
			node[k] = v
		}
	}
	return result.Data.Items, nil
}
