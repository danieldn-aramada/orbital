// @title           Orbital API
// @version         0.0.0-dev
// @description     API-first, graph-native source of truth for modular data centers.
//
// @tag.name         audit
// @tag.name         graph
// @tag.name         graphql
// @tag.name         oci
// @tag.name         subgraph

package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/armada/orbital/docs"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/migrate"
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

	docs.SwaggerInfo.BasePath = cfg.BasePath
	docs.SwaggerInfo.Version = version.Version

	db, err := ent.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	if err := db.Schema.Create(ctx, migrate.WithDropColumn(true)); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	srv := server.New(cfg, db)

	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}
}
