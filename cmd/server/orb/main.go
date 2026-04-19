package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg := config.New()
	srv := server.New(cfg)

	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}
}
