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

// The integration suite's LIVE graph is dgraph-alpha-test (:8083), a dedicated
// cluster in deploy/local/docker-compose.yml — NOT blue (:8080), which is the
// developer's own working graph.
//
// This is not a preference. TestMain calls ResetDGraphE, which drop_all's
// whatever these return, and it runs as package-level setup BEFORE test
// selection — so narrowing with `-run` never protected anything. Three separate
// sessions lost a restored graph to a routine `go test -tags=integration`, and
// the fix each time was a rule about the order to run things in, which is a
// human protocol guarding a shared mutable resource. It failed every time.
//
// PostgreSQL never had this problem: EnsureTestDatabase gives the suite its own
// `orbital_test` database. This is the same answer for DGraph.
//
// Do NOT point these back at :8080. blueAdminURL below refuses it outright.

// DGraphAdminURL returns the DGraph admin URL for the test stack.
func DGraphAdminURL() string {
	if v := os.Getenv("TEST_DGRAPH_ADMIN_URL"); v != "" {
		return v
	}
	return "http://localhost:8083/admin"
}

// DGraphURL returns the DGraph GraphQL URL for the test stack.
func DGraphURL() string {
	if v := os.Getenv("TEST_DGRAPH_URL"); v != "" {
		return v
	}
	return "http://localhost:8083/graphql"
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

// devGraphPorts are the host ports of clusters a test must never drop_all:
// blue (the developer's working graph) and orb's own graph. Matched on the
// authority so a URL reaching them by any path or scheme is still caught.
var devGraphPorts = []string{":8080", ":9080", ":8082", ":9082"}

// refuseDevGraph is the backstop for the failure this file's header describes.
// The default URLs are safe; this catches the ways they get overridden — a
// stale TEST_DGRAPH_URL exported in a shell, a copied CI snippet, a Makefile
// edit. The override exists because "never" would be a lie, but it has to be
// typed on purpose.
func refuseDevGraph(url string) error {
	if os.Getenv("ORBITAL_ALLOW_DEV_DGRAPH_WIPE") == "true" {
		return nil
	}
	for _, p := range devGraphPorts {
		if strings.Contains(url, p) {
			return fmt.Errorf(
				"refusing to drop_all %s: that is a DEV DGraph (blue :8080 / orb :8082), not the test cluster. "+
					"The integration suite uses dgraph-alpha-test on :8083 — run `make up` to start it. "+
					"If you really mean to wipe your own graph, set ORBITAL_ALLOW_DEV_DGRAPH_WIPE=true", url)
		}
	}
	return nil
}

// ResetDGraphE is the error-returning variant of ResetDGraph for use in TestMain.
func ResetDGraphE(adminURL, schemaPath string) error {
	if err := refuseDevGraph(adminURL); err != nil {
		return err
	}
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

	// Poll until the schema is active. DGraph propagates schema changes
	// asynchronously — the update mutation returns 200 before the schema
	// is queryable. Without this, the next test that calls a GraphQL mutation
	// may hit "There's no GraphQL schema in Dgraph" even though the update
	// appeared to succeed.
	graphqlURL := strings.TrimSuffix(adminURL, "/admin") + "/graphql"
	probePayload, _ := json.Marshal(map[string]string{"query": "{ queryNamespace { id } }"})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlURL, bytes.NewReader(probePayload))
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var probe struct {
			Errors []struct{ Message string } `json:"errors"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&probe)
		resp.Body.Close()
		if len(probe.Errors) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("schema did not activate within 10s after reset")
}

// SeedMinimal creates one Namespace and DataCenter in DGraph and returns the
// namespace DGraph ID and the data center orbId.
// Use as the known starting state for integration tests that need graph data.
func SeedMinimal(t *testing.T, graphqlURL string) (namespaceID, dcOrbID string) {
	t.Helper()
	namespaceID, dcOrbID, err := SeedMinimalE(graphqlURL)
	if err != nil {
		t.Fatalf("SeedMinimal: %v", err)
	}
	return namespaceID, dcOrbID
}

// SeedMinimalE is the error-returning variant of SeedMinimal for use in TestMain.
// Returns the namespace DGraph ID and the data center orbId.
func SeedMinimalE(graphqlURL string) (namespaceID, dcOrbID string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	nsMutation := `
	mutation {
		addNamespace(input: [{
			name: "test-namespace"
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

	const seedDCOrbID = "test-dc"
	dcMutation := `
	mutation {
		addDataCenter(input: [{
			orbId:     "test-dc"
			name:      "Test DC"
			namespace: "test-namespace"
			version:   1
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
	if err := dgraphGQL(ctx, graphqlURL, dcMutation, nil, &dcResult); err != nil {
		return "", "", fmt.Errorf("create datacenter: %w", err)
	}
	if len(dcResult.Data.AddDataCenter.DataCenter) == 0 {
		return "", "", fmt.Errorf("addDataCenter returned no results")
	}

	return namespaceID, seedDCOrbID, nil
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
