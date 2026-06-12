package orbctl

import (
	"log/slog"
	"os"

	"github.com/armada/orbital/internal/version"
	"github.com/spf13/cobra"
)

// verbose is set by the persistent --verbose/-v flag on rootCmd. When true,
// slog is configured to emit Debug-level messages to stderr — surfacing
// network calls (AAD token endpoint, orbital GraphQL endpoint) and other
// internal events. login.go also reads it to decide whether to print the
// access token after a successful login.
var verbose bool

var rootCmd = &cobra.Command{
	Use:   "orbctl",
	Short: "orbctl — CLI for the Orbital configuration management system",
	Long: `orbctl — CLI for the Orbital configuration management system

By default, orbctl connects to http://localhost:8001.
Point it at a different instance via environment variable or flag:

  export ORBITAL_SERVER=http://ilb.devnew.armada.internal/orbital
  orbctl get datacenter`,
	SilenceUsage: true,
	CompletionOptions: cobra.CompletionOptions{
		HiddenDefaultCmd: true,
	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if verbose {
			slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: slog.LevelDebug,
			})))
		}
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the orbctl version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Println(version.Version)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false,
		"Verbose output — log network calls; print access token after login")
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(getCmd)
	rootCmd.AddCommand(versionCmd)
	// rootCmd.AddCommand(patchCmd) // disabled: CLI is read-only for now
}
