package orbitalcli

import (
	"os"

	"github.com/armada/orbital/internal/version"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:     "orbital",
	Short:   "Orbital CLI — manage and authenticate with the Orbital cloud service",
	Version: version.Version,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(getCmd)
	rootCmd.AddCommand(patchCmd)
}
