package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/armada/orbital/internal/configitems"
)

// collectRelatedOrbIDs returns a root ConfigItem's orbId plus every owned
// descendant's orbId, deduped with the root first. It is the single, generic,
// view-driven source for the audit tab's data-related-orb-ids across every page
// (Server, KubernetesCluster, NetworkDevice, DataCenter) — replacing the
// per-type hand-walked collectors that had drifted from each other (Spike 33).
//
// The subtree is the page's EDIT UNIT, which is the right unit by construction:
// an audit tab answers "what changed here", and what can change here is exactly
// what this page's editor writes.
//
// On any query error it degrades to just the root orbId (the audit panel still
// shows the root's own events).
func collectRelatedOrbIDs(ctx context.Context, dgraphURL string, views configitems.ViewSet, rootType, rootOrbID string) []string {
	root := []string{rootOrbID}
	if rootType == "" || rootOrbID == "" {
		return root
	}
	sel := views.EditableOrbIDSelection(rootType)
	query := "query($id: String!) { get" + rootType + "(orbId: $id) { orbId " + sel + " } }"
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]string{"id": rootOrbID},
	})
	if err != nil {
		return root
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dgraphURL, bytes.NewReader(body))
	if err != nil {
		return root
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return root
	}
	defer resp.Body.Close()

	var raw any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return root
	}

	seen := map[string]bool{rootOrbID: true}
	out := []string{rootOrbID}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if id, ok := t["orbId"].(string); ok && id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
			for _, vv := range t {
				walk(vv)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(raw)
	// Deterministic output: root first, then descendants sorted. JSON object
	// key order is random in Go, so without this the CSV would churn per render.
	sort.Strings(out[1:])
	return out
}
