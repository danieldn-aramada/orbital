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
// registry-driven source for the audit tab's data-related-orb-ids across every
// page (Server, KubernetesCluster, NetworkDevice, DataCenter) — replacing the
// per-type hand-walked collectors that had drifted from the diff rollup's
// ownership (Spike 33). Ownership comes entirely from internal/configitems, so
// it can never drift from graphdiff again.
//
// On any query error it degrades to just the root orbId (the audit panel still
// shows the root's own events).
func collectRelatedOrbIDs(ctx context.Context, dgraphURL, rootType, rootOrbID string) []string {
	root := []string{rootOrbID}
	if rootType == "" || rootOrbID == "" {
		return root
	}
	sel := configitems.OwnedOrbIDSelection(rootType)
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
