package cli

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/armada/orbital/internal/orbconfig"
	"github.com/armada/orbital/internal/orbserver"
	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Launch the self-contained edge service",
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := orbconfig.New()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	srv, err := orbserver.New(cfg)
	if err != nil {
		return fmt.Errorf("server init: %w", err)
	}
	return srv.Start(ctx)
}
