// @title           Orbital API
// @version         0.1.0
// @description     API-first, graph-native configuration management system for modular data centers.
// @BasePath        /

package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	_ "github.com/armada/orbital/docs"
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
