//go:build integration

package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// DGraphAdminURL returns the DGraph admin URL for the test stack.
// Defaults to http://localhost:18080/admin.
func DGraphAdminURL() string {
	if v := os.Getenv("TEST_DGRAPH_ADMIN_URL"); v != "" {
		return v
	}
	return "http://localhost:8080/admin"
}

// DGraphURL returns the DGraph GraphQL URL for the test stack.
// Defaults to http://localhost:18080/graphql.
func DGraphURL() string {
	if v := os.Getenv("TEST_DGRAPH_URL"); v != "" {
		return v
	}
	return "http://localhost:8080/graphql"
}

// DGraphScratchAdminURL returns the scratch DGraph admin URL for the test stack.
func DGraphScratchAdminURL() string {
	if v := os.Getenv("TEST_DGRAPH_SCRATCH_ADMIN_URL"); v != "" {
		return v
	}
	return "http://localhost:8081/admin"
}

// DGraphScratchURL returns the scratch DGraph GraphQL URL for the test stack.
func DGraphScratchURL() string {
	if v := os.Getenv("TEST_DGRAPH_SCRATCH_URL"); v != "" {
		return v
	}
	return "http://localhost:8081/graphql"
}

// ResetDGraph drops all data and re-applies the schema against the given admin URL.
// Call once in TestMain before the suite runs — not between individual tests.
func ResetDGraph(t *testing.T, adminURL, schemaPath string) {
	t.Helper()
	if err := ResetDGraphE(adminURL, schemaPath); err != nil {
		t.Fatalf("ResetDGraph: %v", err)
	}
}

// ResetDGraphE is the error-returning variant of ResetDGraph for use in TestMain.
func ResetDGraphE(adminURL, schemaPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// DGraph drop_all uses the /alter HTTP endpoint, not the admin GraphQL API.
	alterURL := strings.TrimSuffix(adminURL, "/admin") + "/alter"
	if err := dgraphPost(ctx, alterURL, []byte(`{"drop_all": true}`)); err != nil {
		return fmt.Errorf("drop_all: %w", err)
	}

	// Brief pause to let DGraph finish internal index cleanup before applying schema.
	time.Sleep(500 * time.Millisecond)

	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema %s: %w", schemaPath, err)
	}

	updatePayload, err := json.Marshal(map[string]any{
		"query": `mutation UpdateGQLSchema($schema: String!) {
			updateGQLSchema(input: { set: { schema: $schema } }) {
				gqlSchema { schema }
			}
		}`,
		"variables": map[string]any{"schema": string(schemaBytes)},
	})
	if err != nil {
		return fmt.Errorf("marshal schema mutation: %w", err)
	}

	if err := dgraphPost(ctx, adminURL, updatePayload); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

// SeedMinimal creates one Namespace and DataCenter in DGraph and returns their IDs.
// Use as the known starting state for integration tests that need graph data.
func SeedMinimal(t *testing.T, graphqlURL string) (namespaceID, dcID string) {
	t.Helper()
	namespaceID, dcID, err := SeedMinimalE(graphqlURL)
	if err != nil {
		t.Fatalf("SeedMinimal: %v", err)
	}
	return namespaceID, dcID
}

// SeedMinimalE is the error-returning variant of SeedMinimal for use in TestMain.
func SeedMinimalE(graphqlURL string) (namespaceID, dcID string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	nsMutation := `
	mutation {
		addNamespace(input: [{
			name: "Test Namespace"
		}]) {
			namespace { id }
		}
	}`

	var nsResult struct {
		Data struct {
			AddNamespace struct {
				Namespace []struct {
					ID string `json:"id"`
				} `json:"namespace"`
			} `json:"addNamespace"`
		} `json:"data"`
	}
	if err := dgraphGQL(ctx, graphqlURL, nsMutation, nil, &nsResult); err != nil {
		return "", "", fmt.Errorf("create namespace: %w", err)
	}
	if len(nsResult.Data.AddNamespace.Namespace) == 0 {
		return "", "", fmt.Errorf("addNamespace returned no results")
	}
	namespaceID = nsResult.Data.AddNamespace.Namespace[0].ID

	dcMutation := `
	mutation($nsId: ID!) {
		addDataCenter(input: [{
			orbId:     "test-dc"
			name:      "Test DC"
			namespace: { id: $nsId }
		}]) {
			dataCenter { id }
		}
	}`

	var dcResult struct {
		Data struct {
			AddDataCenter struct {
				DataCenter []struct {
					ID string `json:"id"`
				} `json:"dataCenter"`
			} `json:"addDataCenter"`
		} `json:"data"`
	}
	if err := dgraphGQL(ctx, graphqlURL, dcMutation, map[string]any{"nsId": namespaceID}, &dcResult); err != nil {
		return "", "", fmt.Errorf("create datacenter: %w", err)
	}
	if len(dcResult.Data.AddDataCenter.DataCenter) == 0 {
		return "", "", fmt.Errorf("addDataCenter returned no results")
	}
	dcID = dcResult.Data.AddDataCenter.DataCenter[0].ID

	return namespaceID, dcID, nil
}

func dgraphGQL(ctx context.Context, url, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var wrapper struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	rawBody, err := readAndDecode(resp, &wrapper)
	if err != nil {
		return err
	}
	if len(wrapper.Errors) > 0 {
		return fmt.Errorf("graphql error: %s", wrapper.Errors[0].Message)
	}

	return json.Unmarshal(rawBody, out)
}

func dgraphPost(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func readAndDecode(resp *http.Response, v any) ([]byte, error) {
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	raw := buf.Bytes()
	if err := json.Unmarshal(raw, v); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return raw, nil
}
