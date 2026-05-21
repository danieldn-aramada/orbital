package orbitalcli

import (
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
	if err := orbauth.ClearOrbitalCredentials(); err != nil {
		return err
	}
	out.Success("Signed out — run 'orbital login' to sign in again")
	return nil
}
