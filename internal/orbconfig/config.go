package orbconfig

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config holds all configuration for the orb edge service, loaded from environment variables.
type Config struct {
	// Web server
	Port string `envconfig:"ORB_PORT" default:"8010"`

	// Local DGraph (orb's own instance, separate from orbital)
	DGraphURL       string `envconfig:"ORB_DGRAPH_URL"        default:"http://localhost:8082/graphql"`
	DGraphAdminURL  string `envconfig:"ORB_DGRAPH_ADMIN_URL"   default:"http://localhost:8082/admin"`
	DGraphAlphaGRPC string `envconfig:"ORB_DGRAPH_ALPHA_GRPC"  default:"localhost:9082"`

	// OCI registry (Zot — never ACR directly)
	// OCIRepo is the full repository path for this orb's artifact stream,
	// e.g. "orbital/colo-galleon". The DC identity is encoded here — not
	// as a separate config field. Orb derives who it is from imported data.
	OCIRegistry      string `envconfig:"ORB_OCI_REGISTRY"       default:"localhost:5001"`
	OCIRepo          string `envconfig:"ORB_OCI_REPO"           default:"orbital"`
	OCIUsername      string `envconfig:"ORB_OCI_USERNAME"       default:""`
	OCIPassword      string `envconfig:"ORB_OCI_PASSWORD"       default:""`
	OCIAllowHTTP     bool   `envconfig:"ORB_OCI_ALLOW_HTTP"     default:"true"`
	OCIPublicKeyPath string `envconfig:"ORB_OCI_PUBLIC_KEY_PATH" default:"cosign.pub"`

	// Orbital upstream — for publishing divergence reports
	OrbitalURL string `envconfig:"ORB_ORBITAL_URL" default:"http://localhost:8001"`

	// Polling — how often orb checks Zot for a newer artifact version
	PollInterval time.Duration `envconfig:"ORB_POLL_INTERVAL" default:"60s"`

	// Data directory — holds import history and local overrides
	DataDir string `envconfig:"ORB_DATA_DIR" default:"./orb-data"`

	// Docker container name for dgraph live import exec
	DGraphContainerName string `envconfig:"ORB_DGRAPH_CONTAINER" default:"local-dgraph-orb-alpha-1"`

	LogLevel string `envconfig:"ORB_LOG_LEVEL" default:"info"`
}

// SlogLevel converts the LogLevel string to a slog.Level.
func (c *Config) SlogLevel() slog.Level {
	switch c.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New loads Config from environment variables.
func New() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, fmt.Errorf("orb config: %w", err)
	}
	return &cfg, nil
}
