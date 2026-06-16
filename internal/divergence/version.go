package divergence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// FetchCurrentVersion queries DGraph for the current `version` field on a
// ConfigItem identified by (typeName, orbId). Returns nil if the version is
// unavailable — caller must treat nil as "MVCC degraded, proceed with a
// logged warning" rather than blocking.
//
// Used at two sites:
//   - Divergence intake: capture `intended_at_version` at the moment a new
//     report is ingested.
//   - Divergence Accept: re-fetch and compare to the captured version. 409
//     conflict surfaces "intent has moved on since this report" so the cloud
//     admin can re-review instead of silently overwriting their own intent edit.
//
// Shared across packages to keep the query shape and the nil-handling rules
// in one place.
func FetchCurrentVersion(ctx context.Context, dgraphURL, typeName, orbID string) (*int, error) {
	if typeName == "" || orbID == "" {
		// Legacy entry without type info, or pathological empty orbId.
		// Surface as "MVCC unavailable" rather than an error.
		return nil, nil
	}

	query := fmt.Sprintf(
		`query CurrentVersion($orbId: String!) { get%s(orbId: $orbId) { version } }`,
		typeName,
	)
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"orbId": orbID},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal version query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dgraphURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build version query request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dgraph version query: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dgraph version query returned %d", resp.StatusCode)
	}

	var result struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode version response: %w", err)
	}
	if len(result.Errors) > 0 {
		// e.g. "Cannot query field 'getFoo' on type 'Query'" — unknown type.
		return nil, fmt.Errorf("dgraph version query: %s", result.Errors[0].Message)
	}

	raw, ok := result.Data["get"+typeName]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		// ConfigItem doesn't exist yet (intake racing with a not-yet-imported
		// node, etc.). MVCC unavailable, proceed with warning.
		return nil, nil
	}

	var entity struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(raw, &entity); err != nil {
		return nil, fmt.Errorf("decode entity: %w", err)
	}
	return entity.Version, nil
}
