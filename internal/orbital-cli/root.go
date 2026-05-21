package orbitalcli

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "orbital",
	Short: "Orbital CLI — manage and authenticate with the Orbital cloud service",
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
