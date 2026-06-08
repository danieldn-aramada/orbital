package orbconfig

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// ConsumerConfig registers an external layer consumer for orb dispatch.
type ConsumerConfig struct {
	MediaType string `json:"mediaType"`
	URL       string `json:"url"`
}

// ConsumersConfig is a []ConsumerConfig that decodes from a JSON-array env var.
type ConsumersConfig []ConsumerConfig

// Decode implements envconfig.Decoder for ORB_CONSUMERS JSON array values.
func (c *ConsumersConfig) Decode(value string) error {
	if value == "" {
		*c = nil
		return nil
	}
	return json.Unmarshal([]byte(value), c)
}

// Config holds all configuration for the orb edge service, loaded from environment variables.
type Config struct {
	// Web server
	Port string `envconfig:"ORB_PORT" default:"8010"`
	Dev  bool   `envconfig:"ORB_DEV"  default:"true"`

	// Local DGraph (orb's own instance, separate from orbital)
	DGraphURL       string `envconfig:"ORB_DGRAPH_URL"        default:"http://localhost:8082/graphql"`
	DGraphAdminURL  string `envconfig:"ORB_DGRAPH_ADMIN_URL"   default:"http://localhost:8082/admin"`
	DGraphAlphaGRPC string `envconfig:"ORB_DGRAPH_ALPHA_GRPC"  default:"localhost:9082"`

	// OCI registry (Zot — never ACR directly)
	// OCIRepo is the full repository path for this orb's artifact stream,
	// e.g. "orbital/colo-galleon". The DC identity is encoded here — not
	// as a separate config field. Orb derives who it is from imported data.
	OCIRegistry      string `envconfig:"ORB_OCI_REGISTRY"       default:"localhost:5001"`
	OCIRepo          string `envconfig:"ORB_OCI_REPO"           default:"orbital/colo-galleon"`
	OCIUsername      string `envconfig:"ORB_OCI_USERNAME"       default:""`
	OCIPassword      string `envconfig:"ORB_OCI_PASSWORD"       default:""`
	OCIAllowHTTP     bool   `envconfig:"ORB_OCI_ALLOW_HTTP"     default:"true"`
	OCIPublicKeyPath string `envconfig:"ORB_OCI_PUBLIC_KEY_PATH" default:"deploy/local/cosign.pub"`

	// S3 — divergence report publishing
	S3Endpoint  string `envconfig:"ORB_S3_ENDPOINT"   default:""`
	S3Region    string `envconfig:"ORB_S3_REGION"     default:"us-east-1"`
	S3Bucket    string `envconfig:"ORB_S3_BUCKET"     default:""`
	S3AccessKey string `envconfig:"ORB_S3_ACCESS_KEY" default:""`
	S3SecretKey string `envconfig:"ORB_S3_SECRET_KEY" default:""`

	// EnableOCIRegistry activates the OCI import source: registry polling, /import/tags route,
	// and the "Pull from Registry" UI section. When false, only /import/subgraph (courier/API)
	// is available. Set ORB_ENABLE_OCI_REGISTRY=false when using ConfigBundle Controller.
	EnableOCIRegistry bool `envconfig:"ORB_ENABLE_OCI_REGISTRY" default:"true"`

	// Consumers holds registered layer consumers for dispatch via /import/artifact.
	// Set via: ORB_CONSUMERS='[{"mediaType":"...","url":"..."}]'
	Consumers ConsumersConfig `envconfig:"ORB_CONSUMERS" default:""`

	// Polling — how often orb checks Zot for a newer artifact version (OCI source only)
	PollInterval time.Duration `envconfig:"ORB_POLL_INTERVAL" default:"60s"`

	// Data directory — holds import history and divergence reports
	DataDir string `envconfig:"ORB_DATA_DIR" default:"./orb-data"`

	// Backend selects the dgraph live execution strategy: "docker" (local dev) or "k8s" (production).
	// docker: uses docker cp + docker exec into the DGraph alpha container.
	// k8s: execs into an idle dgraph-live pod; ORB_DATA_DIR must be the shared PVC mount path.
	Backend string `envconfig:"ORB_BACKEND" default:"docker"`

	// Docker container name — used only when ORB_BACKEND=docker.
	DGraphContainerName string `envconfig:"ORB_DGRAPH_CONTAINER" default:"local-dgraph-orb-alpha-1"`

	// K8s backend fields — used only when ORB_BACKEND=k8s.
	DGraphZeroGRPC string `envconfig:"ORB_DGRAPH_ZERO_GRPC"  default:"localhost:5082"`
	K8sNamespace   string `envconfig:"ORB_K8S_NAMESPACE"     default:""`

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
