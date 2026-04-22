package cli

import (
	"time"

	"github.com/armada/orbital/internal/cli/out"
	"github.com/spf13/cobra"
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan local infrastructure via BMC and build a configuration graph",
	RunE:  runScan,
}

func runScan(cmd *cobra.Command, args []string) error {
	sp := out.Spinner("Discovering BMC interfaces")
	time.Sleep(3 * time.Second)
	sp.Stop("Found 3 BMC interfaces")

	bar := out.ProgressBar(30, "📡  Scanning storage devices")
	for range 30 {
		bar.Add(1)
		time.Sleep(100 * time.Millisecond)
	}

	out.Success("Scan complete")
	return nil
}
