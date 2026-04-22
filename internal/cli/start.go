package cli

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/server"
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

	cfg := config.New()
	srv := server.New(cfg)

	return srv.Start(ctx)
}
