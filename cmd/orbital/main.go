// @title           Orbital API
// @version         0.0.0-dev
// @description     API-first, graph-native source of truth for modular data centers.
//
// @tag.name         audit
// @tag.name         backup
// @tag.name         config-items
// @tag.name         divergence
// @tag.name         export
// @tag.name         graphql
// @tag.name         oci
// @tag.name         users

package main

import (
	"context"
	"crypto/fips140"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/armada/orbital/docs"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/migrate"
	"github.com/armada/orbital/internal/config"
	orbitaldb "github.com/armada/orbital/internal/db"
	"github.com/armada/orbital/internal/server"
	"github.com/armada/orbital/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	slog.Info("orbital starting", "version", version.Version)
	// FIPS-140 runtime enforcement (GODEBUG=fips140=on/only) restricts crypto to
	// FIPS-approved algorithms. Log it prominently: fips140=only is currently
	// INCOMPATIBLE with external-jwt/OIDC bearer auth — go-oidc's JWKS keyset
	// update (RemoteKeySet.updateKeys) uses SHA-1, which panics under
	// fips140=only and crashes the process on the first token verification.
	// Verified 2026-07-27 with go-oidc v3.18.0. Surfacing it here turns that
	// crash from a mystery into a one-line clue at startup.
	if fips140.Enabled() {
		slog.Warn("fips140 ENABLED — crypto restricted to FIPS-approved algorithms. INCOMPATIBLE with external-jwt/OIDC bearer auth: go-oidc JWKS verification uses SHA-1 and will PANIC the process on first token verification. Do not combine fips140=only with bearer auth until go-oidc is FIPS-clean.", "fips140", true)
	} else {
		slog.Info("fips140 disabled", "fips140", false)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.New()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	docs.SwaggerInfo.BasePath = cfg.BasePath
	docs.SwaggerInfo.Version = version.Version

	// One pool for the whole process. Under managed identity its password is minted
	// per connection, so every consumer must come from this pool - a second sql.Open
	// would bypass that hook and connect with no password.
	cred, err := orbitaldb.CredentialFor(cfg.DBUseAzMI, "")
	if err != nil {
		log.Fatalf("db credential: %v", err)
	}
	pool, err := orbitaldb.NewPool(ctx, cfg.DatabaseDSN(), cred, orbitaldb.DefaultMaxConns)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	sqlDB := orbitaldb.SQLDB(pool)
	db := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	defer db.Close()

	if err := db.Schema.Create(ctx, migrate.WithDropColumn(true)); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	srv, err := server.New(cfg, db, sqlDB)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}
}
