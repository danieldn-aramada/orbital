package orbctl

import (
	"fmt"
	"os"

	"github.com/armada/orbital/internal/orbauth"
	"github.com/spf13/cobra"
)

var datacenterFilter string

var getServerCmd = &cobra.Command{
	Use:     "server [hostname]",
	Aliases: []string{"servers"},
	Short:   "List servers, or get one by hostname",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runGetServers,
}

func init() {
	getServerCmd.Flags().StringVar(&datacenterFilter, "datacenter", "", "Filter by data center name")
}

func runGetServers(cmd *cobra.Command, args []string) error {
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
  queryServer(filter: {namespace: {eq: $ns}}) {
    id orbId hostname serviceTag model
    oobIP { address }
    rack { name }
    dataCenter { name }
  }
}`
		vars = map[string]any{"ns": namespaceFilter}
	} else {
		q = `{ queryServer {
      id orbId hostname serviceTag model
      oobIP { address }
      rack { name }
      dataCenter { name }
    } }`
	}

	var result struct {
		Data struct {
			QueryServer []struct {
				ID         string `json:"id"`
				OrbID      string `json:"orbId"`
				Hostname   string `json:"hostname"`
				ServiceTag string `json:"serviceTag"`
				Model      string `json:"model"`
				OobIP      struct{ Address string } `json:"oobIP"`
				Rack       struct{ Name string }    `json:"rack"`
				DataCenter struct{ Name string }    `json:"dataCenter"`
			} `json:"queryServer"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}

	if err := gqlRequest(cmd, base, token, q, vars, &result); err != nil {
		return err
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("graphql: %s", result.Errors[0].Message)
	}

	servers := result.Data.QueryServer

	if len(args) > 0 {
		hostname := args[0]
		n := 0
		for _, s := range servers {
			if s.Hostname == hostname {
				servers[n] = s
				n++
			}
		}
		servers = servers[:n]
		if len(servers) == 0 {
			return fmt.Errorf("server %q not found", hostname)
		}
	}

	if datacenterFilter != "" {
		n := 0
		for _, s := range servers {
			if s.DataCenter.Name == datacenterFilter {
				servers[n] = s
				n++
			}
		}
		servers = servers[:n]
		if len(servers) == 0 {
			return fmt.Errorf("no servers found in data center %q", datacenterFilter)
		}
	}

	if len(servers) == 0 {
		fmt.Println("No servers found.")
		return nil
	}

	// Compute column widths from data, kubectl-style.
	// Columns match UI DataTable: Data Center | OOB IP | Hostname | Service Tag | Model | Rack | ID | Orb ID
	wDC, wOobIP, wHostname, wSvcTag, wModel, wRack, wID, wOrbID :=
		len("DATA CENTER"), len("OOB IP"), len("HOSTNAME"), len("SERVICE TAG"), len("MODEL"), len("RACK"), len("ID"), len("ORB ID")
	for _, s := range servers {
		dc := s.DataCenter.Name
		if dc == "" {
			dc = "—"
		}
		oob := s.OobIP.Address
		if oob == "" {
			oob = "—"
		}
		rack := s.Rack.Name
		if rack == "" {
			rack = "—"
		}
		if n := len(dc); n > wDC {
			wDC = n
		}
		if n := len(oob); n > wOobIP {
			wOobIP = n
		}
		if n := len(s.Hostname); n > wHostname {
			wHostname = n
		}
		if n := len(s.ServiceTag); n > wSvcTag {
			wSvcTag = n
		}
		if n := len(s.Model); n > wModel {
			wModel = n
		}
		if n := len(rack); n > wRack {
			wRack = n
		}
		if n := len(s.ID); n > wID {
			wID = n
		}
		if n := len(s.OrbID); n > wOrbID {
			wOrbID = n
		}
	}

	colFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds  %%s\n",
		wDC, wOobIP, wHostname, wSvcTag, wModel, wRack, wID)
	fmt.Printf(colFmt, "DATA CENTER", "OOB IP", "HOSTNAME", "SERVICE TAG", "MODEL", "RACK", "ID", "ORB ID")
	for _, s := range servers {
		dc := s.DataCenter.Name
		if dc == "" {
			dc = "—"
		}
		oob := s.OobIP.Address
		if oob == "" {
			oob = "—"
		}
		hostname := s.Hostname
		if hostname == "" {
			hostname = "—"
		}
		svcTag := s.ServiceTag
		if svcTag == "" {
			svcTag = "—"
		}
		model := s.Model
		if model == "" {
			model = "—"
		}
		rack := s.Rack.Name
		if rack == "" {
			rack = "—"
		}
		orbID := s.OrbID
		if orbID == "" {
			orbID = "—"
		}
		fmt.Printf(colFmt, dc, oob, hostname, svcTag, model, rack, s.ID, orbID)
	}
	return nil
}
