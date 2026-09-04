package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/configitems"
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

	// One query for every declared entity's owned subtree, not one per entity.
	// This runs for every change request a list renders, and the badge renders
	// the whole open queue on every page load — so a per-entity round-trip here
	// multiplies by two factors at once.
	owned := collectRelatedOrbIDsBatch(ctx, dgraphURL, declared, existing)
	for _, id := range declared {
		add(id)
		for _, o := range owned[id] {
			add(o)
		}
	}

	sort.Strings(scope)
	return scope
}

// collectRelatedOrbIDsBatch expands many roots' owned subtrees in ONE GraphQL
// round-trip, using aliases so each root keeps its own type-specific selection.
//
// Roots absent from `existing` are creates and own nothing yet, so they are
// skipped rather than queried for.
//
// Failure is non-fatal for the same reason collectRelatedOrbIDs's is: a root
// whose subtree cannot be read contributes only itself, which narrows the scope
// rather than corrupting it.
func collectRelatedOrbIDsBatch(ctx context.Context, dgraphURL string, declared []string, existing map[string]approval.EntityRef) map[string][]string {
	out := make(map[string][]string, len(declared))

	type rootAlias struct{ alias, id string }
	var roots []rootAlias
	var b strings.Builder
	b.WriteString("query {")
	for i, id := range declared {
		ref, ok := existing[id]
		if !ok || ref.Type == "" {
			continue
		}
		sel := configitems.OwnedOrbIDSelection(ref.Type)
		if sel == "" {
			continue // the type owns nothing — no query needed to learn that
		}
		q, err := json.Marshal(id)
		if err != nil {
			continue
		}
		alias := fmt.Sprintf("r%d", i)
		roots = append(roots, rootAlias{alias: alias, id: id})
		fmt.Fprintf(&b, "\n  %s: get%s(orbId: %s) { orbId %s }", alias, ref.Type, q, sel)
	}
	b.WriteString("\n}")
	if len(roots) == 0 {
		return out
	}

	body, err := json.Marshal(map[string]any{"query": b.String()})
	if err != nil {
		return out
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dgraphURL, bytes.NewReader(body))
	if err != nil {
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()

	var decoded struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return out
	}
	for _, r := range roots {
		node, ok := decoded.Data[r.alias]
		if !ok || node == nil {
			continue
		}
		seen := map[string]bool{}
		var ids []string
		walkOrbIDs(node, seen, &ids)
		out[r.id] = ids
	}
	return out
}

// walkOrbIDs collects every orbId reachable in a decoded GraphQL result.
//
// The selection is built from OwnedOrbIDSelection, so everything it can reach
// is owned by the root by construction — the walk does not need to re-decide
// ownership, only to find the ids.
func walkOrbIDs(v any, seen map[string]bool, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "orbId" {
				if s, ok := val.(string); ok && s != "" && !seen[s] {
					seen[s] = true
					*out = append(*out, s)
				}
				continue
			}
			walkOrbIDs(val, seen, out)
		}
	case []any:
		for _, item := range t {
			walkOrbIDs(item, seen, out)
		}
	}
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

// ── Scope versions: the cheap staleness anchor ──────────────────────────────

// scopeVersions reads just the OCC version of every entity in scope, in ONE
// DQL query over two predicates.
//
// This replaces fetching the scope's full content and hashing it. The question
// a change request asks is "has anything in my scope moved since I was
// opened?", which is a yes/no — and `version` is orbital's answer to exactly
// that. Every write orbital performs bumps it (graphql.go stamps `set.version`
// on update and `version: 1` on create; merge's applyItem does the same), so a
// version vector moves whenever intent moves.
//
// What it does NOT see is a write that reaches DGraph without passing through
// orbital and therefore never bumped the counter. That trade is deliberate: the
// counter IS orbital's MVCC, and a check that trusts it is consistent with
// every other place orbital relies on it — including the `version` conflict
// check on the mutation path.
//
// Entities absent from the graph are absent from the map, which is what makes
// creates and deletions both register as movement.
func scopeVersions(ctx context.Context, dgraphURL string, scope []string) (map[string]int, error) {
	out := map[string]int{}
	if len(scope) == 0 {
		return out, nil
	}

	quoted := make([]string, len(scope))
	for i, id := range scope {
		b, err := json.Marshal(id)
		if err != nil {
			return nil, fmt.Errorf("encode orbId %q: %w", id, err)
		}
		quoted[i] = string(b)
	}
	dql := fmt.Sprintf(`{
		items(func: eq(ConfigItem.orbId, [%s])) {
			ConfigItem.orbId
			ConfigItem.version
		}
	}`, strings.Join(quoted, ", "))

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
		return nil, fmt.Errorf("read scope versions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("read scope versions (%d): %s", resp.StatusCode, b)
	}

	var decoded struct {
		Data struct {
			Items []struct {
				OrbID   string `json:"ConfigItem.orbId"`
				Version int    `json:"ConfigItem.version"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode scope versions: %w", err)
	}
	for _, it := range decoded.Data.Items {
		if it.OrbID != "" {
			out[it.OrbID] = it.Version
		}
	}
	return out, nil
}

// versionHash reduces a scope's version vector to one comparable string.
//
// A hash rather than the map itself because it is STORED (base_hash) and
// compared against later (approved_at_hash), and those columns are single
// values. Sorted so the hash is a property of the vector rather than of DGraph's
// result order.
func versionHash(versions map[string]int) string {
	// An empty vector still hashes. A request that creates an entity has an
	// empty scope at open, and returning a sentinel "" there both fails
	// base_hash's length validator and makes "no entities yet" indistinguishable
	// from "not computed".
	ids := make([]string, 0, len(versions))
	for id := range versions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		fmt.Fprintf(h, "%s@%d\n", id, versions[id])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// presentInVersions is presentIn over a version vector.
func presentInVersions(versions map[string]int, scope []string) []string {
	out := make([]string, 0, len(scope))
	for _, id := range scope {
		if _, ok := versions[id]; ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// storedEffect computes the delta a changeset would apply, ready to persist as
// base_effect and to be served as the response's `effect`.
//
// Computed against the same scope base_hash is captured from, in the same
// breath, so the stored delta and the anchor that says when it went stale
// describe one moment. That pairing is what a saved Terraform plan is.
//
// NEVER FAILS THE CALLER. A returned nil means "no stored effect", and the read
// path falls back to counting the changeset. This is deliberate: the effect is
// a display convenience, and a subtree read that hiccups must not cost someone
// the proposal they just wrote — especially when the proposal itself is already
// validated and its staleness anchor already captured. The error is returned so
// the caller can log it, not so it can abort.
func storedEffect(snap graphdiff.Snapshot, cs approval.Changeset) (json.RawMessage, error) {
	res := graphdiff.Compare(snap, applyChangesetTo(snap, cs))
	out, err := json.Marshal(effectFromDiff(res))
	if err != nil {
		return nil, fmt.Errorf("marshal effect summary: %w", err)
	}
	return out, nil
}
