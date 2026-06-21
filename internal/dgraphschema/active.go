// Package dgraphschema provides a thin client for querying the active
// GraphQL schema applied to a DGraph instance.
//
// Both orbital and orb need to display "what schema is actually loaded" on
// their respective Schema pages. The historical approach was to read a
// sidecar file on disk (orbital: schema/schema.graphql checked into the repo;
// orb: <DataDir>/schema.graphql written by the importer). Both drift from
// reality:
//   - orbital's file is the source-of-truth at AUTHORING time, not what's
//     applied to the running DGraph (which may lag if migrations haven't run).
//   - orb's file persists across DGraph wipes — the file claims a schema is
//     loaded when DGraph itself was reset out-of-band.
//
// The honest single-source-of-truth is DGraph itself. The admin API exposes
// `query { getGQLSchema { schema } }` which returns whatever GraphQL schema
// was last applied. This package is a 30-line wrapper around that.
package dgraphschema

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Active queries the DGraph admin endpoint and returns the currently-applied
// GraphQL SDL string. Returns empty string with no error when no schema has
// been applied yet — callers render that as "Awaiting import" / "No schema."
//
// `adminURL` is the full DGraph admin endpoint, e.g.
// `http://localhost:8080/admin` or `http://localhost:8082/admin`.
func Active(ctx context.Context, adminURL string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"query": `query { getGQLSchema { schema } }`,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("dgraph admin: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("dgraph admin returned %d: %s", resp.StatusCode, raw)
	}

	var result struct {
		Data struct {
			GetGQLSchema *struct {
				Schema string `json:"schema"`
			} `json:"getGQLSchema"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Errors) > 0 {
		return "", fmt.Errorf("dgraph admin: %s", result.Errors[0].Message)
	}
	if result.Data.GetGQLSchema == nil {
		return "", nil // no schema applied yet
	}
	return result.Data.GetGQLSchema.Schema, nil
}
