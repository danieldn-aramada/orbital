package cli

import (
	"context"
	"fmt"
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

	cfg, err := config.New()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// orb is the edge binary — no PostgreSQL dependency.
	srv := server.New(cfg, nil)

	return srv.Start(ctx)
}
