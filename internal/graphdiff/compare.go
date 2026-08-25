package graphdiff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
)

// FieldChange is a single predicate's before/after. For an added node Before is
// nil; for a removed node After is nil.
type FieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// Change is one changed entity. The response is deliberately FLAT — one entry
// per changed node, never a nested tree — so len(Changes) equals the summary
// counts and a client renders rows without re-implementing orbital's model.
//
// API-first: orbital's own UI is a thin renderer over this shape. If the UI
// needs bespoke logic to display something, that's a signal the API should
// carry it instead.
type Change struct {
	OrbID  string        `json:"orbId"`
	Type   string        `json:"type"`
	Change string        `json:"change"` // "added" | "removed" | "modified"
	Fields []FieldChange `json:"fields,omitempty"`
}

// Summary is the "N to add, N to change, N to destroy" line.
type Summary struct {
	Added     int `json:"added"`
	Removed   int `json:"removed"`
	Modified  int `json:"modified"`
	Unchanged int `json:"unchanged"`
}

// Result is the net delta plus a stable hash of the current snapshot.
type Result struct {
	Summary     Summary            `json:"summary"`
	ByType      map[string]Summary `json:"byType"`
	Changes     []*Change          `json:"changes"`
	ContentHash string             `json:"contentHash"`
}

// Compare returns the net delta from baseline to current as a flat list, one
// entry per changed orbId, sorted by orbId.
func Compare(baseline, current Snapshot) *Result {
	seen := make(map[string]bool, len(baseline)+len(current))
	for k := range baseline {
		seen[k] = true
	}
	for k := range current {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	res := &Result{ByType: map[string]Summary{}, ContentHash: current.ContentHash()}
	bump := func(typ, kind string) {
		s := res.ByType[typ]
		switch kind {
		case "added":
			s.Added++
		case "removed":
			s.Removed++
		case "modified":
			s.Modified++
		default:
			s.Unchanged++
		}
		res.ByType[typ] = s
	}
	for _, orb := range keys {
		b, c := baseline[orb], current[orb]
		switch {
		case b == nil && c != nil:
			res.Changes = append(res.Changes, sideChange(c, "added"))
			res.Summary.Added++
			bump(primaryType(c), "added")
		case b != nil && c == nil:
			res.Changes = append(res.Changes, sideChange(b, "removed"))
			res.Summary.Removed++
			bump(primaryType(b), "removed")
		default:
			if fc := diffFields(b, c); len(fc) > 0 {
				res.Changes = append(res.Changes, &Change{OrbID: orb, Type: primaryType(c), Change: "modified", Fields: fc})
				res.Summary.Modified++
				bump(primaryType(c), "modified")
			} else {
				res.Summary.Unchanged++
				bump(primaryType(c), "unchanged")
			}
		}
	}
	return res
}

// sideChange renders a wholly-added or wholly-removed node.
func sideChange(n *Node, kind string) *Change {
	ch := &Change{OrbID: n.OrbID, Type: primaryType(n), Change: kind}
	for _, k := range sortedKeys(n.Fields) {
		if kind == "added" {
			ch.Fields = append(ch.Fields, FieldChange{Field: k, Before: nil, After: n.Fields[k]})
		} else {
			ch.Fields = append(ch.Fields, FieldChange{Field: k, Before: n.Fields[k], After: nil})
		}
	}
	for _, k := range sortedKeys(edgesAsAny(n.Edges)) {
		val := n.Edges[k]
		if kind == "added" {
			ch.Fields = append(ch.Fields, FieldChange{Field: k, Before: nil, After: val})
		} else {
			ch.Fields = append(ch.Fields, FieldChange{Field: k, Before: val, After: nil})
		}
	}
	return ch
}

func diffFields(b, c *Node) []FieldChange {
	var out []FieldChange
	if !reflect.DeepEqual(b.Types, c.Types) {
		out = append(out, FieldChange{Field: "dgraph.type", Before: b.Types, After: c.Types})
	}
	for _, k := range unionKeys(b.Fields, c.Fields) {
		bv, bok := b.Fields[k]
		cv, cok := c.Fields[k]
		switch {
		case !bok:
			out = append(out, FieldChange{Field: k, Before: nil, After: cv})
		case !cok:
			out = append(out, FieldChange{Field: k, Before: bv, After: nil})
		case !reflect.DeepEqual(bv, cv):
			out = append(out, FieldChange{Field: k, Before: bv, After: cv})
		}
	}
	for _, k := range unionKeys(edgesAsAny(b.Edges), edgesAsAny(c.Edges)) {
		be, bok := b.Edges[k]
		ce, cok := c.Edges[k]
		switch {
		case !bok:
			out = append(out, FieldChange{Field: k, Before: nil, After: ce})
		case !cok:
			out = append(out, FieldChange{Field: k, Before: be, After: nil})
		case !reflect.DeepEqual(be, ce):
			out = append(out, FieldChange{Field: k, Before: be, After: ce})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// ContentHash is a stable sha256 over the normalized snapshot — the TOCTOU
// marker. Same intent (noise excluded) => identical hash regardless of UID
// churn or map iteration order.
func (s Snapshot) ContentHash() string {
	orbs := make([]string, 0, len(s))
	for k := range s {
		orbs = append(orbs, k)
	}
	sort.Strings(orbs)

	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, orb := range orbs {
		n := s[orb]
		flat := map[string]any{"orbId": n.OrbID, "types": n.Types}
		for _, k := range sortedKeys(n.Fields) {
			flat["f:"+k] = n.Fields[k]
		}
		for _, k := range sortedKeys(edgesAsAny(n.Edges)) {
			flat["e:"+k] = n.Edges[k]
		}
		_ = enc.Encode(flat) // json.Marshal sorts map keys => deterministic
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func primaryType(n *Node) string {
	if len(n.Types) > 0 {
		return n.Types[0]
	}
	return ""
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unionKeys(a, b map[string]any) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func edgesAsAny(e map[string][]string) map[string]any {
	out := make(map[string]any, len(e))
	for k, v := range e {
		out[k] = v
	}
	return out
}
