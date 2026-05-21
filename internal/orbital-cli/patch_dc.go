package orbitalcli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/armada/orbital/internal/cli/out"
	"github.com/armada/orbital/internal/orbauth"
	"github.com/spf13/cobra"
)

var patchCmd = &cobra.Command{
	Use:   "patch",
	Short: "Update resources on the Orbital server",
}

var patchDatacenterCmd = &cobra.Command{
	Use:     "datacenter <name|orbId|id>",
	Aliases: []string{"dc"},
	Short:   "Update fields on a data center",
	Args:    cobra.ExactArgs(1),
	RunE:    runPatchDatacenter,
}

var patchJSON string

// dcPatchFieldTypes declares the GraphQL variable type for each patchable
// DataCenter field. Must match the DGraph schema.
var dcPatchFieldTypes = map[string]string{
	"name":        "String",
	"assetDataV2": "String",
}

func init() {
	patchCmd.AddCommand(patchDatacenterCmd)
	patchDatacenterCmd.Flags().StringVar(&serverURL, "server", "", "Orbital server URL (default: $ORBITAL_SERVER or http://localhost:8001)")
	patchDatacenterCmd.Flags().StringVarP(&patchJSON, "patch", "p", "", `JSON fields to update, e.g. '{"name":"new-name"}'`)
	_ = patchDatacenterCmd.MarkFlagRequired("patch")
}

func runPatchDatacenter(cmd *cobra.Command, args []string) error {
	var userFields map[string]any
	if err := json.Unmarshal([]byte(patchJSON), &userFields); err != nil {
		return fmt.Errorf("invalid patch JSON: %w", err)
	}

	// Validate fields before hitting the network.
	for k := range userFields {
		if _, ok := dcPatchFieldTypes[k]; !ok {
			supported := make([]string, 0, len(dcPatchFieldTypes))
			for f := range dcPatchFieldTypes {
				supported = append(supported, f)
			}
			return fmt.Errorf("unknown field %q — patchable fields: %s", k, strings.Join(supported, ", "))
		}
	}

	store, err := orbauth.OrbitalFileStore()
	if err != nil {
		return err
	}
	creds, _ := orbauth.LoadValid(store)
	if creds == nil {
		fmt.Fprintln(os.Stderr, "credentials expired — run: orbital login")
		os.Exit(1)
	}

	base := resolveServerURL()

	sp := out.Spinner(fmt.Sprintf("Resolving %q", args[0]))
	dc, err := fetchDataCenter(cmd, base, creds.AccessToken, args[0])
	if err != nil {
		sp.Fail("Failed to resolve data center")
		return err
	}
	if dc == nil {
		sp.Fail(fmt.Sprintf("Data center %q not found", args[0]))
		return fmt.Errorf("data center %q not found", args[0])
	}
	sp.Stop(fmt.Sprintf("Found %s (%s)", dc.Name, dc.OrbID))

	// Build the mutation to match the UI pattern:
	// - filter by DGraph id (accepts $id: ID! as variable, unlike @id fields)
	// - orbId as a top-level variable so the audit middleware can pre-fetch before-state
	// - all set fields as individual top-level variables (not nested in a $set object)
	// - updatedBy/updatedAt as top-level variables (skipVars in app.js hides them from diff view)
	// orbId is intentionally NOT declared in the mutation signature — DGraph ignores
	// undeclared variables, but the audit middleware reads variables["orbId"] before
	// forwarding to DGraph to pre-fetch the before-state for diff generation.
	varDecls := []string{"$id: ID!", "$updatedBy: String!", "$updatedAt: DateTime!"}
	setFields := []string{"updatedBy: $updatedBy", "updatedAt: $updatedAt"}
	variables := map[string]any{
		"id":        dc.ID,
		"orbId":     dc.OrbID,
		"updatedBy": creds.Email,
		"updatedAt": time.Now().UTC().Format(time.RFC3339),
	}

	for k, v := range userFields {
		varDecls = append(varDecls, fmt.Sprintf("$%s: %s", k, dcPatchFieldTypes[k]))
		setFields = append(setFields, fmt.Sprintf("%s: $%s", k, k))
		variables[k] = v
	}

	mutation := fmt.Sprintf(`mutation UpdateDataCenter(%s) {
  updateDataCenter(input: {
    filter: { id: [$id] }
    set: { %s }
  }) {
    dataCenter { id orbId name updatedBy updatedAt }
  }
}`, strings.Join(varDecls, ", "), strings.Join(setFields, ", "))

	var result struct {
		Data struct {
			UpdateDataCenter struct {
				DataCenter []struct {
					ID        string `json:"id"`
					OrbID     string `json:"orbId"`
					Name      string `json:"name"`
					UpdatedBy string `json:"updatedBy"`
					UpdatedAt string `json:"updatedAt"`
				} `json:"dataCenter"`
			} `json:"updateDataCenter"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}

	sp = out.Spinner("Applying patch")
	if err := gqlRequest(cmd, base, creds.AccessToken, mutation, variables, &result); err != nil {
		sp.Fail("Mutation failed")
		return err
	}
	if len(result.Errors) > 0 {
		sp.Fail("GraphQL error")
		return fmt.Errorf("graphql: %s", result.Errors[0].Message)
	}
	updated := result.Data.UpdateDataCenter.DataCenter
	if len(updated) == 0 {
		sp.Fail("No data center updated")
		return fmt.Errorf("no data center updated — check the id or field names")
	}
	u := updated[0]
	sp.Stop("Patch applied")

	fmt.Printf("Name:    %s\n", u.Name)
	fmt.Printf("OrbID:   %s\n", u.OrbID)
	fmt.Printf("By:      %s\n", u.UpdatedBy)
	fmt.Printf("At:      %s\n", u.UpdatedAt)
	return nil
}
