package orbconfig

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// ConsumerConfig registers an external layer consumer for orb dispatch.
// Consumer-centric, not media-type-centric: one entry per consumer process.
// orb broadcasts each non-graph layer to every registered consumer with
// `Content-Type: <layer media type>`. Consumers route internally and
// return 415 for layers they don't handle — that's the contract.
//
// `Name` is the friendly identifier (e.g. "cb-controller"); surfaces in
// import history and logs. `URL` is the consumer's dispatch endpoint.
type ConsumerConfig struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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

	// OCI registry — edge-local registry, never ACR directly.
	// OCIRepo is the OCI repository path orb polls for artifacts. A single
	// registry typically holds many repositories; this field selects which one
	// this orb pulls from (e.g. "orbital/colo-galleon"). The DC slug appearing
	// in the path is operator convention, not a requirement — orb does not
	// parse the path for identity. Runtime DC identity comes from imported
	// DGraph data, never from this field.
	OCIRegistry      string `envconfig:"ORB_OCI_REGISTRY"       default:"localhost:5001"`
	OCIRepo          string `envconfig:"ORB_OCI_REPO"           default:"orbital/colo-galleon"`
	OCIUsername      string `envconfig:"ORB_OCI_USERNAME"       default:""`
	OCIPassword      string `envconfig:"ORB_OCI_PASSWORD"       default:""`
	OCIAllowHTTP     bool   `envconfig:"ORB_OCI_ALLOW_HTTP"     default:"true"`
	OCIPublicKeyPath string `envconfig:"ORB_OCI_PUBLIC_KEY_PATH" default:"deploy/local/cosign.pub"`

	// S3 — divergence report publishing. Defaults target the local MinIO from
	// docker-compose; production must override every value via env.
	S3Endpoint  string `envconfig:"ORB_S3_ENDPOINT"   default:"http://localhost:9000"`
	S3Region    string `envconfig:"ORB_S3_REGION"     default:"us-east-1"`
	S3Bucket    string `envconfig:"ORB_S3_BUCKET"     default:"orbital"`
	S3AccessKey string `envconfig:"ORB_S3_ACCESS_KEY" default:"minioadmin"`
	S3SecretKey string `envconfig:"ORB_S3_SECRET_KEY" default:"minioadmin"`

	// EnableOCIRegistry activates the OCI import source: registry polling, /import/tags route,
	// and the "Pull from Registry" UI section. When false, only /import/subgraph (courier/API)
	// is available. Set ORB_ENABLE_OCI_REGISTRY=false when using ConfigBundle Controller.
	EnableOCIRegistry bool `envconfig:"ORB_ENABLE_OCI_REGISTRY" default:"true"`

	// Consumers holds registered layer consumers for dispatch via /import/artifact.
	// Set via: ORB_CONSUMERS='[{"mediaType":"...","url":"..."}]'.
	// Default points at cb-controller's /consume + /mapping running on the host
	// at :8095 (configbundle repo's `make run-controller` exposes these). Local
	// dev requires no env-var setup — `make run-orb` connects out of the box.
	// Production deploys override with the cluster service URL.
	// One entry per consumer process; orb broadcasts each non-graph layer to
	// every consumer with `Content-Type: <layer media type>`. Consumers route
	// internally and 415 on unknown types. Default points at the local cb-
	// controller from configbundle repo's `make run-controller`.
	Consumers ConsumersConfig `envconfig:"ORB_CONSUMERS" default:"[{\"name\":\"cb-controller\",\"url\":\"http://localhost:8095/dispatch\"}]"`

	// OCIPollInterval — how often orb checks the OCI registry for a newer
	// artifact version. 30s default trades 2x more registry calls for half the
	// banner-update lag on the Status page. Configurable via
	// ORB_OCI_POLL_INTERVAL for environments that need different cadence
	// (rate-limited registries, dev iteration, etc.). Named under the
	// ORB_OCI_* namespace alongside ORB_OCI_REGISTRY/USERNAME/PASSWORD so a
	// future second poll loop (e.g. consumer health checks) can claim its own
	// mechanism-named env var without ambiguity.
	PollInterval time.Duration `envconfig:"ORB_OCI_POLL_INTERVAL" default:"30s"`

	// Data directory — holds import history and divergence reports
	DataDir string `envconfig:"ORB_DATA_DIR" default:"./orb-data"`

	// DGraph zero gRPC endpoint — used by the dgraph live subprocess during import.
	DGraphZeroGRPC string `envconfig:"ORB_DGRAPH_ZERO_GRPC" default:"localhost:5082"`

	// DivergencePublishSchedule is a cron expression for the divergence publish
	// scheduler. Empty (default) disables the scheduler — divergence is published
	// only by the manual UI button. Example: "0 9 * * *" → daily at 09:00 UTC.
	// Mirrors orbital's ORBITAL_BACKUP_SCHEDULE pattern.
	DivergencePublishSchedule string `envconfig:"ORB_DIVERGENCE_PUBLISH_SCHEDULE" default:""`

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
