// Package graphdiff computes a per-orbId content diff between two Orbital
// subgraph snapshots. It is the shared normalize+compare core used by the
// export preview (Spike 30), the guarded-Apply hash (Spike 31), and the
// published-vs-published diff (Spike 25).
//
// The two inputs arrive in different wire shapes:
//
//   - Current side: the live DQL result from Export.fetchNamespaceSubgraph —
//     []map[string]any, one already-merged map per node (dgraph.type is an
//     array; edges are merged in as [{uid}]).
//   - Baseline side: a DGraph native JSON export (data.json.gz, unpacked) — a
//     flat array of per-predicate fragments {uid, namespace, <one-pred>}, where
//     the same uid recurs once per predicate and once per edge target, and
//     dgraph.type is a string per fragment.
//
// Both normalize into a canonical Node keyed by orbId so they compare equal
// regardless of wire shape or DGraph UID assignment (UIDs are reassigned on
// restore, so edges are compared by target orbId, never UID).
package graphdiff

import (
	"encoding/json"
	"fmt"
	"sort"
)

const orbIDKey = "ConfigItem.orbId"

// noiseFields are stripped from both sides before comparison: the
// auto-incremented optimistic-concurrency counter and the per-write audit
// stamps (which change on every touch and would mark every node modified), plus
// DGraph internals. createdAt/createdBy are deliberately kept (they change only
// on delete+recreate — real signal, not noise).
var noiseFields = map[string]bool{
	"ConfigItem.version":   true,
	"ConfigItem.updatedAt": true,
	"ConfigItem.updatedBy": true,
	"uid":                  true,
	"namespace":            true, // DGraph tenant id "0x0", NOT ConfigItem.namespace
}

// Node is the canonical form of a ConfigItem, identity-keyed by orbId.
type Node struct {
	OrbID  string
	Types  []string            // sorted; excludes the "ConfigItem" interface tag
	Fields map[string]any      // scalar predicates, noise excluded, orbId excluded (it's the key)
	Edges  map[string][]string // edge predicate -> sorted target orbIds
}

// Snapshot is a whole subgraph, orbId -> Node.
type Snapshot map[string]*Node

// NormalizeCurrent normalizes the live DQL result (already one map per node).
func NormalizeCurrent(nodes []map[string]any) Snapshot {
	raws := make(map[string]*rawNode, len(nodes))
	for _, m := range nodes {
		uid, _ := m["uid"].(string)
		if uid == "" {
			continue
		}
		r := raws[uid]
		if r == nil {
			r = newRaw(uid)
			raws[uid] = r
		}
		for k, v := range m {
			r.fold(k, v)
		}
	}
	return finalize(raws)
}

// NormalizeExport normalizes a DGraph native JSON export (unpacked, un-gzipped).
func NormalizeExport(exportJSON []byte) (Snapshot, error) {
	var frags []map[string]any
	if err := json.Unmarshal(exportJSON, &frags); err != nil {
		return nil, fmt.Errorf("unmarshal native export: %w", err)
	}
	raws := make(map[string]*rawNode)
	for _, f := range frags {
		uid, _ := f["uid"].(string)
		if uid == "" {
			continue
		}
		r := raws[uid]
		if r == nil {
			r = newRaw(uid)
			raws[uid] = r
		}
		for k, v := range f {
			r.fold(k, v)
		}
	}
	return finalize(raws), nil
}

type rawNode struct {
	uid    string
	types  map[string]bool
	fields map[string]any
	edges  map[string]map[string]bool // predicate -> set of target uids
}

func newRaw(uid string) *rawNode {
	return &rawNode{uid: uid, types: map[string]bool{}, fields: map[string]any{}, edges: map[string]map[string]bool{}}
}

// fold merges one predicate=value into the accumulating node. Scalars overwrite
// (there is one per node per predicate), dgraph.type unions into the type set,
// edge predicates union their target uids into a set.
func (r *rawNode) fold(key string, val any) {
	switch {
	case key == "uid" || key == "namespace":
		return
	case key == "dgraph.type":
		for _, t := range asStrings(val) {
			if t != "" && t != "ConfigItem" {
				r.types[t] = true
			}
		}
	case isEdge(val):
		set := r.edges[key]
		if set == nil {
			set = map[string]bool{}
			r.edges[key] = set
		}
		for _, u := range edgeUIDs(val) {
			set[u] = true
		}
	default:
		if noiseFields[key] {
			return
		}
		r.fields[key] = canonScalar(val)
	}
}

// canonScalar reconciles the one cross-format scalar mismatch: DGraph's native
// JSON export encodes booleans as quoted strings ("true"/"false"), while the
// live DQL result returns real JSON booleans. Coercing the strings to bool on
// both sides makes them compare equal. Ints/floats/strings/DateTimes already
// agree across the two formats, so they pass through untouched. Applied to both
// normalizers (fold is shared), so it is a consistent canonicalization, not a
// one-sided guess.
func canonScalar(v any) any {
	switch t := v.(type) {
	case string:
		switch t {
		case "true":
			return true
		case "false":
			return false
		}
		return t
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = canonScalar(e)
		}
		return out
	}
	return v
}

// finalize resolves edge targets from uid to orbId (within this snapshot) and
// drops nodes with no orbId (e.g. the Namespace node), which are not ConfigItems.
func finalize(raws map[string]*rawNode) Snapshot {
	uid2orb := make(map[string]string, len(raws))
	for uid, r := range raws {
		if orb, ok := r.fields[orbIDKey].(string); ok && orb != "" {
			uid2orb[uid] = orb
		}
	}
	snap := make(Snapshot, len(raws))
	for _, r := range raws {
		orb, ok := r.fields[orbIDKey].(string)
		if !ok || orb == "" {
			continue
		}
		n := &Node{OrbID: orb, Fields: make(map[string]any), Edges: make(map[string][]string)}
		for t := range r.types {
			n.Types = append(n.Types, t)
		}
		sort.Strings(n.Types)
		for k, v := range r.fields {
			if k == orbIDKey {
				continue // identity, not a diffable field
			}
			n.Fields[k] = v
		}
		for pred, set := range r.edges {
			targets := make([]string, 0, len(set))
			for u := range set {
				if o, ok := uid2orb[u]; ok {
					targets = append(targets, o)
				} else {
					targets = append(targets, "uid:"+u) // dangling ref: surface, don't vanish
				}
			}
			sort.Strings(targets)
			n.Edges[pred] = targets
		}
		snap[orb] = n
	}
	return snap
}

func asStrings(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// isEdge reports whether a predicate value is a uid reference (a map with a
// "uid" key, or an array of such maps) rather than a scalar.
func isEdge(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		_, ok := t["uid"]
		return ok
	case []any:
		if len(t) == 0 {
			return false
		}
		m, ok := t[0].(map[string]any)
		if !ok {
			return false
		}
		_, ok = m["uid"]
		return ok
	}
	return false
}

func edgeUIDs(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		if u, ok := t["uid"].(string); ok {
			out = append(out, u)
		}
	case []any:
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				if u, ok := m["uid"].(string); ok {
					out = append(out, u)
				}
			}
		}
	}
	return out
}
