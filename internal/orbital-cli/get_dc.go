package orbitalcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/armada/orbital/internal/orbauth"
	"github.com/spf13/cobra"
)

// DefaultServerURL is the Orbital server endpoint used when --server is not
// passed and ORBITAL_SERVER is not set. Override at release time via ldflags:
//
//	-X 'github.com/armada/orbital/internal/orbital-cli.DefaultServerURL=http://ilb.devnew.armada.internal/orbital'
var DefaultServerURL = "http://localhost:8001"

// httpClient is shared across all commands with a 15-second timeout so the
// CLI fails fast when the server is unreachable rather than hanging.
var httpClient = &http.Client{Timeout: 15 * time.Second}

var serverURL string
var namespaceFilter string

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Fetch resources from the Orbital server",
}

// getDatacenterCmd lists all data centers when called with no args, or fetches
// a specific one by name when called with a single arg — kubectl
// `get pod[s] [name]` shape. The plural alias keeps muscle memory working.
var getDatacenterCmd = &cobra.Command{
	Use:     "datacenter [name]",
	Aliases: []string{"datacenters", "dc", "dcs"},
	Short:   "List data centers, or fetch one by name",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runGetDatacenter,
}

func init() {
	getCmd.PersistentFlags().StringVar(&serverURL, "server", "", "Orbital server URL (overrides $ORBITAL_SERVER and compiled-in default)")
	getCmd.PersistentFlags().StringVarP(&namespaceFilter, "namespace", "n", "", "Filter by namespace name")
	getCmd.AddCommand(getDatacenterCmd)
	getCmd.AddCommand(getServerCmd)
}

func runGetDatacenter(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return runGetDatacenters(cmd, args)
	}

	token, err := orbauth.GetToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "credentials expired — run: orbital login")
		os.Exit(1)
	}

	base := resolveServerURL()

	dc, err := queryByName(cmd, base, token, args[0])
	if err != nil {
		return err
	}
	if dc == nil {
		return fmt.Errorf("data center %q not found", args[0])
	}
	printDcSummary(dc)
	return nil
}


func queryByName(cmd *cobra.Command, base, token, name string) (*dcSummary, error) {
	var q string
	var vars map[string]any
	if namespaceFilter != "" {
		q = `query($ns: String!) {
  queryDataCenter(filter: {namespace: {eq: $ns}}) { ` + dcFields + ` }
}`
		vars = map[string]any{"ns": namespaceFilter}
	} else {
		q = `{ queryDataCenter { ` + dcFields + ` } }`
	}
	var result struct {
		Data   struct{ QueryDataCenter []*dcSummary } `json:"data"`
		Errors []struct{ Message string }             `json:"errors"`
	}
	if err := gqlRequest(cmd, base, token, q, vars, &result); err != nil {
		return nil, err
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", result.Errors[0].Message)
	}
	for _, dc := range result.Data.QueryDataCenter {
		if dc.Name == name {
			return dc, nil
		}
	}
	return nil, nil
}

func gqlRequest(cmd *cobra.Command, base, token, query string, vars map[string]any, dest any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	endpoint := base + "/api/v1/graphql"

	if verbose {
		logVerboseRequest(endpoint, token, query, vars)
	}

	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return requestError(err, base)
	}
	defer resp.Body.Close()

	if verbose {
		fmt.Fprintf(os.Stderr, "< %s\n\n", resp.Status)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, dest)
}

// logVerboseRequest prints a curl-style request trace to stderr.
func logVerboseRequest(endpoint, token, query string, vars map[string]any) {
	fmt.Fprintf(os.Stderr, "\n> POST %s\n", endpoint)
	fmt.Fprintf(os.Stderr, "> Authorization: Bearer %s\n", token)
	fmt.Fprintf(os.Stderr, "> Content-Type: application/json\n>\n")
	fmt.Fprintln(os.Stderr, strings.TrimSpace(query))
	if len(vars) > 0 {
		b, _ := json.MarshalIndent(vars, "", "  ")
		fmt.Fprintf(os.Stderr, "\nvariables: %s\n", b)
	}
	fmt.Fprintln(os.Stderr)
}

// requestError converts a network error into a user-friendly message.
// For timeouts against internal Armada endpoints it adds a VPN hint.
func requestError(err error, base string) error {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		if strings.Contains(base, "armada.internal") {
			return fmt.Errorf("connection timed out — ensure you are connected to VPN for %s", base)
		}
		return fmt.Errorf("connection timed out — check that the Orbital server is reachable at %s", base)
	}
	return fmt.Errorf("request failed: %w", err)
}

const dcFields = `
  id name orbId createdBy createdAt updatedBy updatedAt assetDataV2
  namespace
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
	Namespace   string `json:"namespace"`
	Racks       []struct {
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
	fmt.Printf("Namespace:  %s\n", dc.Namespace)
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

func runGetDatacenters(cmd *cobra.Command, _ []string) error {
	token, err := orbauth.GetToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "credentials expired — run: orbital login")
		os.Exit(1)
	}

	base := resolveServerURL()

	var q string
	var vars map[string]any
	if namespaceFilter != "" {
		q = `query($ns: String!) {
  queryDataCenter(filter: {namespace: {eq: $ns}}) {
    id orbId name createdBy createdAt serversAggregate { count }
  }
}`
		vars = map[string]any{"ns": namespaceFilter}
	} else {
		q = `{ queryDataCenter { id orbId name createdBy createdAt serversAggregate { count } } }`
	}

	var result struct {
		Data struct {
			QueryDataCenter []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				OrbID     string `json:"orbId"`
				CreatedBy string `json:"createdBy"`
				CreatedAt string `json:"createdAt"`
				ServersAggregate struct {
					Count int `json:"count"`
				} `json:"serversAggregate"`
			} `json:"queryDataCenter"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}

	if err := gqlRequest(cmd, base, token, q, vars, &result); err != nil {
		return err
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
	// Columns match UI DataTable: Name | Servers | Created By | Created At | ID | Orb ID
	wName, wSrv, wCreatedBy, wCreatedAt, wID, wOrbID :=
		len("NAME"), len("SERVERS"), len("CREATED BY"), len("CREATED AT"), len("ID"), len("ORB ID")
	for _, dc := range dcs {
		if n := len(dc.Name); n > wName {
			wName = n
		}
		if n := len(fmt.Sprintf("%d", dc.ServersAggregate.Count)); n > wSrv {
			wSrv = n
		}
		if n := len(dc.CreatedBy); n > wCreatedBy {
			wCreatedBy = n
		}
		if n := len(dc.CreatedAt); n > wCreatedAt {
			wCreatedAt = n
		}
		if n := len(dc.ID); n > wID {
			wID = n
		}
		if n := len(dc.OrbID); n > wOrbID {
			wOrbID = n
		}
	}
	colFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds  %%s\n",
		wName, wSrv, wCreatedBy, wCreatedAt, wID)
	fmt.Printf(colFmt, "NAME", "SERVERS", "CREATED BY", "CREATED AT", "ID", "ORB ID")
	for _, dc := range dcs {
		fmt.Printf(colFmt, dc.Name, fmt.Sprintf("%d", dc.ServersAggregate.Count),
			dc.CreatedBy, dc.CreatedAt, dc.ID, dc.OrbID)
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
	return DefaultServerURL
}
