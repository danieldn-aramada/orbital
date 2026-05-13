// @title           Orbital API
// @version         0.1.0
// @description     API-first, graph-native configuration management system for modular data centers.
// @BasePath        /
//
// @tag.name         backup graph
// @tag.name         export subgraph
// @tag.name         events
// @tag.name         oci

package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/armada/orbital/docs"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/server"
	"github.com/armada/orbital/internal/version"
	_ "github.com/lib/pq"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	slog.Info("orbital starting", "version", version.Version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.New()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := ent.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	if err := db.Schema.Create(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	srv := server.New(cfg, db)

	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}
}
