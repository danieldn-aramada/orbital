package orbitalcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/armada/orbital/internal/orbauth"
	"github.com/spf13/cobra"
)

var serverURL string

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Fetch resources from the Orbital server",
}

var getDatacentersCmd = &cobra.Command{
	Use:   "datacenters",
	Short: "List all data centers",
	Args:  cobra.NoArgs,
	RunE:  runGetDatacenters,
}

var getDatacenterCmd = &cobra.Command{
	Use:     "datacenter <name|orbId|id>",
	Aliases: []string{"dc"},
	Short:   "Fetch and display a data center summary",
	Args:    cobra.ExactArgs(1),
	RunE:    runGetDatacenter,
}

func init() {
	getCmd.PersistentFlags().StringVar(&serverURL, "server", "", "Orbital server URL (default: $ORBITAL_SERVER or http://localhost:8001)")
	getCmd.AddCommand(getDatacentersCmd)
	getCmd.AddCommand(getDatacenterCmd)
}

func runGetDatacenter(cmd *cobra.Command, args []string) error {
	token, err := orbauth.GetToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "credentials expired — run: orbital login")
		os.Exit(1)
	}

	base := resolveServerURL()
	arg := args[0]

	dc, err := fetchDataCenter(cmd, base, token, arg)
	if err != nil {
		return err
	}
	if dc == nil {
		return fmt.Errorf("data center %q not found", arg)
	}
	printDcSummary(dc)
	return nil
}

// fetchDataCenter resolves the argument as a DGraph UID (starts with 0x),
// an orbId (contains :), or a name, trying each in order until one matches.
func fetchDataCenter(cmd *cobra.Command, base, token, arg string) (*dcSummary, error) {
	if strings.HasPrefix(arg, "0x") {
		return queryByUID(cmd, base, token, arg)
	}
	if strings.Contains(arg, ":") {
		return queryByOrbID(cmd, base, token, arg)
	}
	// Try orbId first (exact match), then name.
	dc, err := queryByOrbID(cmd, base, token, arg)
	if err != nil || dc != nil {
		return dc, err
	}
	return queryByName(cmd, base, token, arg)
}

func queryByUID(cmd *cobra.Command, base, token, id string) (*dcSummary, error) {
	const q = `query GetDataCenter($id: ID!) {
  getDataCenter(id: $id) { ` + dcFields + ` }
}`
	var result struct {
		Data   struct{ GetDataCenter *dcSummary } `json:"data"`
		Errors []struct{ Message string }         `json:"errors"`
	}
	if err := gqlRequest(cmd, base, token, q, map[string]any{"id": id}, &result); err != nil {
		return nil, err
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", result.Errors[0].Message)
	}
	return result.Data.GetDataCenter, nil
}

func queryByOrbID(cmd *cobra.Command, base, token, orbID string) (*dcSummary, error) {
	const q = `query GetDataCenterByOrbID($orbId: String!) {
  getDataCenter(orbId: $orbId) { ` + dcFields + ` }
}`
	var result struct {
		Data   struct{ GetDataCenter *dcSummary } `json:"data"`
		Errors []struct{ Message string }         `json:"errors"`
	}
	if err := gqlRequest(cmd, base, token, q, map[string]any{"orbId": orbID}, &result); err != nil {
		return nil, err
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", result.Errors[0].Message)
	}
	return result.Data.GetDataCenter, nil
}

func queryByName(cmd *cobra.Command, base, token, name string) (*dcSummary, error) {
	const q = `query QueryDataCenterByName($name: String!) {
  queryDataCenter(filter: { name: { eq: $name } }) { ` + dcFields + ` }
}`
	var result struct {
		Data   struct{ QueryDataCenter []*dcSummary } `json:"data"`
		Errors []struct{ Message string }             `json:"errors"`
	}
	if err := gqlRequest(cmd, base, token, q, map[string]any{"name": name}, &result); err != nil {
		return nil, err
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", result.Errors[0].Message)
	}
	if len(result.Data.QueryDataCenter) == 0 {
		return nil, nil
	}
	return result.Data.QueryDataCenter[0], nil
}

func gqlRequest(cmd *cobra.Command, base, token, query string, vars map[string]any, dest any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, base+"/api/v1/graphql", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, dest)
}

const dcFields = `
  id name orbId createdBy createdAt updatedBy updatedAt assetDataV2
  namespace { name }
  racks(order: { asc: name }) { id orbId name }
  serversAggregate { count }
  servers(order: { asc: rackPosition }) {
    id orbId name hostname serviceTag model
    oobIP { address }
    oobMAC rackPosition
    rack { name }
  }`

type dcSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	OrbID       string `json:"orbId"`
	CreatedBy   string `json:"createdBy"`
	CreatedAt   string `json:"createdAt"`
	UpdatedBy   string `json:"updatedBy"`
	UpdatedAt   string `json:"updatedAt"`
	AssetDataV2 string `json:"assetDataV2"`
	Namespace   struct {
		Name string `json:"name"`
	} `json:"namespace"`
	Racks []struct {
		ID    string `json:"id"`
		OrbID string `json:"orbId"`
		Name  string `json:"name"`
	} `json:"racks"`
	ServersAggregate struct {
		Count int `json:"count"`
	} `json:"serversAggregate"`
	Servers []struct {
		ID           string `json:"id"`
		OrbID        string `json:"orbId"`
		Name         string `json:"name"`
		Hostname     string `json:"hostname"`
		ServiceTag   string `json:"serviceTag"`
		Model        string `json:"model"`
		OobIP        struct{ Address string } `json:"oobIP"`
		OobMAC       string `json:"oobMAC"`
		RackPosition int    `json:"rackPosition"`
		Rack         struct{ Name string } `json:"rack"`
	} `json:"servers"`
}

func printDcSummary(dc *dcSummary) {
	fmt.Printf("Name:       %s\n", dc.Name)
	fmt.Printf("ID:         %s\n", dc.ID)
	fmt.Printf("OrbID:      %s\n", dc.OrbID)
	fmt.Printf("Namespace:  %s\n", dc.Namespace.Name)
	fmt.Printf("Created:    %s (by %s)\n", dc.CreatedAt, dc.CreatedBy)
	if dc.UpdatedAt != "" {
		fmt.Printf("Updated:    %s (by %s)\n", dc.UpdatedAt, dc.UpdatedBy)
	}
	fmt.Printf("Racks:      %d\n", len(dc.Racks))
	fmt.Printf("Servers:    %d\n", dc.ServersAggregate.Count)

	if len(dc.Racks) > 0 {
		fmt.Println("\nRacks:")
		for _, r := range dc.Racks {
			fmt.Printf("  %-30s  %s\n", r.Name, r.OrbID)
		}
	}

	if len(dc.Servers) > 0 {
		fmt.Println("\nServers:")
		fmt.Printf("  %-30s  %-25s  %-15s  %-12s  %s\n", "Hostname", "OrbID", "OOB IP", "Service Tag", "Model")
		for _, s := range dc.Servers {
			fmt.Printf("  %-30s  %-25s  %-15s  %-12s  %s\n",
				s.Hostname, s.OrbID, s.OobIP.Address, s.ServiceTag, s.Model)
		}
	}

	if dc.AssetDataV2 != "" {
		var buf bytes.Buffer
		if err := json.Indent(&buf, []byte(dc.AssetDataV2), "", "  "); err == nil {
			fmt.Printf("\nAsset Data:\n%s\n", buf.String())
		} else {
			fmt.Printf("\nAsset Data: %s\n", dc.AssetDataV2)
		}
	}
}

func runGetDatacenters(cmd *cobra.Command, args []string) error {
	token, err := orbauth.GetToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "credentials expired — run: orbital login")
		os.Exit(1)
	}

	base := resolveServerURL()

	const q = `{ queryDataCenter { name orbId serversAggregate { count } } }`
	var result struct {
		Data struct {
			QueryDataCenter []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				OrbID     string `json:"orbId"`
				Namespace struct {
					Name string `json:"name"`
				} `json:"namespace"`
				ServersAggregate struct {
					Count int `json:"count"`
				} `json:"serversAggregate"`
			} `json:"queryDataCenter"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}

	body, _ := json.Marshal(map[string]any{"query": q})
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, base+"/api/v1/graphql", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(raw))
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("graphql: %s", result.Errors[0].Message)
	}

	dcs := result.Data.QueryDataCenter
	if len(dcs) == 0 {
		fmt.Println("No data centers found.")
		return nil
	}

	// Compute column widths from data, kubectl-style.
	wName, wOrbID := len("NAME"), len("ORB ID")
	for _, dc := range dcs {
		if n := len(dc.Name); n > wName {
			wName = n
		}
		if n := len(dc.OrbID); n > wOrbID {
			wOrbID = n
		}
	}
	colFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%s\n", wName, wOrbID)
	fmt.Printf(colFmt, "NAME", "ORB ID", "SERVERS")
	for _, dc := range dcs {
		fmt.Printf(colFmt, dc.Name, dc.OrbID, fmt.Sprintf("%d", dc.ServersAggregate.Count))
	}
	return nil
}

func resolveServerURL() string {
	if serverURL != "" {
		return serverURL
	}
	if env := os.Getenv("ORBITAL_SERVER"); env != "" {
		return env
	}
	return "http://localhost:8001"
}
