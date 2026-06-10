package orbctl

import (
	"fmt"
	"os"

	"github.com/armada/orbital/internal/cli/out"
	"github.com/armada/orbital/internal/orbauth"
	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Sign out and remove stored credentials",
	Args:  cobra.NoArgs,
	RunE:  runLogout,
}

func runLogout(_ *cobra.Command, _ []string) error {
	store, err := orbauth.OrbitalFileStore()
	if err != nil {
		return err
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "removing credentials: %s\n", store.Path)
	}

	if err := store.Delete(); err != nil {
		return err
	}
	out.Success("Signed out")
	return nil
}
