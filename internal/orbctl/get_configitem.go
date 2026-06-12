package orbctl

import (
	"fmt"
	"os"
	"sort"

	"github.com/armada/orbital/internal/orbauth"
	"github.com/spf13/cobra"
)

var getConfigItemCmd = &cobra.Command{
	Use:     "configitem",
	Aliases: []string{"ci"},
	Short:   "List all config items across all types",
	Args:    cobra.NoArgs,
	RunE:    runGetConfigItems,
}

func init() {
	getCmd.AddCommand(getConfigItemCmd)
}

func runGetConfigItems(cmd *cobra.Command, args []string) error {
	token, err := orbauth.GetToken()
	if err != nil {
		fmt.Fprintln(os.Stderr, "credentials expired — run: orbital login")
		os.Exit(1)
	}

	base := resolveServerURL()

	var query string
	var vars map[string]any

	if namespaceFilter != "" {
		query = `query($ns: String!) {
  queryConfigItem(filter: {namespace: {eq: $ns}}) {
    orbId name namespace createdBy createdAt __typename
  }
}`
		vars = map[string]any{"ns": namespaceFilter}
	} else {
		query = `{ queryConfigItem {
  id orbId name namespace createdBy createdAt __typename
} }`
	}

	var result struct {
		Data struct {
			QueryConfigItem []struct {
			OrbID     string `json:"orbId"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				CreatedBy string `json:"createdBy"`
				CreatedAt string `json:"createdAt"`
				Typename  string `json:"__typename"`
			} `json:"queryConfigItem"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}

	if err := gqlRequest(cmd, base, token, query, vars, &result); err != nil {
		return err
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("graphql: %s", result.Errors[0].Message)
	}

	items := result.Data.QueryConfigItem
	if len(items) == 0 {
		fmt.Println("No config items found.")
		return nil
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Typename != items[j].Typename {
			return items[i].Typename < items[j].Typename
		}
		return items[i].Name < items[j].Name
	})

	wType, wName, wNS, wOrbID, wCreatedBy :=
		len("TYPE"), len("NAME"), len("NAMESPACE"), len("ORB ID"), len("CREATED BY")
	for _, ci := range items {
		if n := len(ci.Typename); n > wType {
			wType = n
		}
		if n := len(ci.Name); n > wName {
			wName = n
		}
		if n := len(ci.Namespace); n > wNS {
			wNS = n
		}
		if n := len(ci.OrbID); n > wOrbID {
			wOrbID = n
		}
		if n := len(ci.CreatedBy); n > wCreatedBy {
			wCreatedBy = n
		}
	}

	colFmt := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds  %%s\n",
		wType, wName, wNS, wOrbID, wCreatedBy)
	fmt.Printf(colFmt, "TYPE", "NAME", "NAMESPACE", "ORB ID", "CREATED BY", "CREATED AT")
	for _, ci := range items {
		name := ci.Name
		if name == "" {
			name = "—"
		}
		ns := ci.Namespace
		if ns == "" {
			ns = "—"
		}
		orbID := ci.OrbID
		if orbID == "" {
			orbID = "—"
		}
		createdBy := ci.CreatedBy
		if createdBy == "" {
			createdBy = "—"
		}
		createdAt := ci.CreatedAt
		if createdAt == "" {
			createdAt = "—"
		}
		fmt.Printf(colFmt, ci.Typename, name, ns, orbID, createdBy, createdAt)
	}
	return nil
}
