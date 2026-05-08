package config

import (
	"log/slog"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Port            string        `envconfig:"ORBITAL_PORT"             default:"8001"`
	ShutdownTimeout time.Duration `envconfig:"ORBITAL_SHUTDOWN_TIMEOUT" default:"10s"`
	DGraphURL       string        `envconfig:"DGRAPH_URL"               default:"http://localhost:8080/graphql"`
	DGraphAdminURL  string        `envconfig:"DGRAPH_ADMIN_URL"         default:"http://localhost:8080/admin"`
	RatelURL        string        `envconfig:"RATEL_URL"                default:"http://localhost:8000"`
	IssueTrackerURL    string        `envconfig:"ORBITAL_ISSUE_TRACKER_URL" default:""`
	Dev                bool          `envconfig:"ORBITAL_DEV"               default:"false"`
	LogLevel           string        `envconfig:"ORBITAL_LOG_LEVEL"         default:"info"`
	DGraphScratchURL      string        `envconfig:"DGRAPH_SCRATCH_URL"       default:"http://localhost:8081/graphql"`
	DGraphScratchAdminURL string        `envconfig:"DGRAPH_SCRATCH_ADMIN_URL" default:"http://localhost:8081/admin"`
	DatabaseURL           string        `envconfig:"DATABASE_URL"             default:"postgres://orbital:orbital@localhost:5432/orbital?sslmode=disable"`
	ExportDir             string        `envconfig:"ORBITAL_EXPORT_DIR"            default:"./subgraph-exports"`
	DGraphScratchExportDir string       `envconfig:"DGRAPH_SCRATCH_EXPORT_DIR"     default:"./subgraph-exports/scratch"`
	SchemaPath            string        `envconfig:"ORBITAL_SCHEMA_PATH"           default:"schema/schema-demo.graphql"`
	SessionSecret         string        `envconfig:"ORBITAL_SESSION_SECRET"        default:"change-me-in-production"`
	OIDCIssuerURL         string        `envconfig:"ORBITAL_OIDC_ISSUER_URL"       default:""`
	OIDCClientID          string        `envconfig:"ORBITAL_OIDC_CLIENT_ID"        default:""`
	OIDCClientSecret      string        `envconfig:"ORBITAL_OIDC_CLIENT_SECRET"    default:""`
	OIDCRedirectURL       string        `envconfig:"ORBITAL_OIDC_REDIRECT_URL"     default:""`
}

func New() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) SlogLevel() slog.Level {
	if c.LogLevel == "debug" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
