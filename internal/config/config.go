package config

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/armada/orbital/internal/auth"
	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Port                   string        `envconfig:"ORBITAL_PORT"                    default:"8001"`
	ShutdownTimeout        time.Duration `envconfig:"ORBITAL_SHUTDOWN_TIMEOUT"        default:"10s"`
	DGraphURL              string        `envconfig:"DGRAPH_URL"                      default:"http://localhost:8080/graphql"`
	DGraphAdminURL         string        `envconfig:"DGRAPH_ADMIN_URL"                default:"http://localhost:8080/admin"`
	RatelURL               string        `envconfig:"RATEL_URL"                       default:"http://localhost:8000"`
	IssueTrackerURL        string        `envconfig:"ORBITAL_ISSUE_TRACKER_URL"       default:"https://dev.azure.com/armadasystems/Commander/_workitems/create/Bug?[System.AreaPath]=Commander\\Edge\\Edge Platform"`
	Dev                    bool          `envconfig:"ORBITAL_DEV"                     default:"true"`
	LogLevel               string        `envconfig:"ORBITAL_LOG_LEVEL"               default:"info"`
	DGraphScratchURL       string        `envconfig:"DGRAPH_SCRATCH_URL"              default:"http://localhost:8081/graphql"`
	DGraphScratchAdminURL  string        `envconfig:"DGRAPH_SCRATCH_ADMIN_URL"        default:"http://localhost:8081/admin"`
	DatabaseURL            string        `envconfig:"DATABASE_URL"                    default:"postgres://orbital:orbital@localhost:5432/orbital?sslmode=disable"`
	ExportDir              string        `envconfig:"ORBITAL_EXPORT_DIR"              default:"./subgraph-exports"`
	DGraphScratchExportDir string        `envconfig:"DGRAPH_SCRATCH_EXPORT_DIR"       default:"./subgraph-exports/scratch"`
	SchemaPath             string        `envconfig:"ORBITAL_SCHEMA_PATH"             default:"schema/schema-demo.graphql"`
	SessionHMACKey         string        `envconfig:"ORBITAL_SESSION_HMAC_KEY"        default:"local-dev-hmac-key-change-in-prod"` // must be changed in prod
	SessionEncryptionKey   string        `envconfig:"ORBITAL_SESSION_ENCRYPTION_KEY"  default:"local-dev-enc-key-32-bytes-pad!!"`  // must be exactly 32 bytes for AES-256; empty disables cookie encryption
	DGraphExportDir        string        `envconfig:"DGRAPH_EXPORT_DIR"               default:"./dgraph-exports"`                  // host-side mount of /dgraph/export on blue alpha
	S3Endpoint             string        `envconfig:"ORBITAL_S3_ENDPOINT"             default:"https://armadagalleonbackups.blob.core.windows.net"`
	S3Region               string        `envconfig:"ORBITAL_S3_REGION"               default:"us-east-1"`
	S3Bucket               string        `envconfig:"ORBITAL_S3_BUCKET"               default:"cmdb"`
	S3AccessKey            string        `envconfig:"ORBITAL_S3_ACCESS_KEY"           default:"armadagalleonbackups"`
	S3SecretKey            string        `envconfig:"ORBITAL_S3_SECRET_KEY"           default:""`
	S3Prefix               string        `envconfig:"ORBITAL_S3_PREFIX"               default:"backups/"` // optional path prefix within the bucket
	S3RetentionCount       int           `envconfig:"ORBITAL_S3_RETENTION_COUNT"      default:"30"`       // max backups to retain; 0 = unlimited
	OIDCIssuerURL          string        `envconfig:"ORBITAL_OIDC_ISSUER_URL"         default:"https://login.microsoftonline.com/8f231c2a-9551-4b40-be17-5b24afe5e890/v2.0"`
	OIDCClientID           string        `envconfig:"ORBITAL_OIDC_CLIENT_ID"          default:"5fc832f6-843e-4207-93dd-b3c3a77c06f2"`
	OIDCClientSecret       string        `envconfig:"ORBITAL_OIDC_CLIENT_SECRET"      default:""`
	OIDCRedirectURL        string        `envconfig:"ORBITAL_OIDC_REDIRECT_URL"       default:"http://localhost:8001/auth/callback"`
	OCIRegistry            string        `envconfig:"ORBITAL_OCI_REGISTRY"            default:"armadaeksatest.azurecr.io"`
	OCIRepo                string        `envconfig:"ORBITAL_OCI_REPO"                default:"orbital"`
	OCIUsername            string        `envconfig:"ORBITAL_OCI_USERNAME"            default:"armadaeksatest"` // ACR admin username = registry name
	OCIPassword            string        `envconfig:"ORBITAL_OCI_PASSWORD"            default:""`               // ACR admin password — set via env
	OCISigningKeyPath      string        `envconfig:"ORBITAL_OCI_SIGNING_KEY_PATH"    default:"cosign.key"`     // run: cosign generate-key-pair
	BasePath               string        `envconfig:"ORBITAL_BASE_PATH"               default:""`
	RestoreTimeout         time.Duration `envconfig:"ORBITAL_RESTORE_TIMEOUT"         default:"10m"`
	DGraphNamespace        string        `envconfig:"ORBITAL_DGRAPH_NAMESPACE"        default:"dgraph"`
	DGraphAlphaGRPC        string        `envconfig:"ORBITAL_DGRAPH_ALPHA_GRPC"       default:"localhost:9080"`
	DGraphZeroGRPC         string        `envconfig:"ORBITAL_DGRAPH_ZERO_GRPC"        default:"localhost:5080"`
	RestoreDir             string        `envconfig:"ORBITAL_RESTORE_DIR"             default:"/restore"`
}

func New() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	if cfg.SessionEncryptionKey != "" && len(cfg.SessionEncryptionKey) != 32 {
		return nil, fmt.Errorf("ORBITAL_SESSION_ENCRYPTION_KEY must be exactly 32 bytes for AES-256, got %d", len(cfg.SessionEncryptionKey))
	}
	return &cfg, nil
}

func (c *Config) SessionKeys() auth.SessionKeys {
	return auth.SessionKeys{
		HMACKey:       c.SessionHMACKey,
		EncryptionKey: c.SessionEncryptionKey,
	}
}

// OCIConfigured returns true when the minimum OCI fields are set to enable publishing.
func (c *Config) OCIConfigured() bool {
	return c.OCIRegistry != "" && c.OCISigningKeyPath != ""
}

func (c *Config) SlogLevel() slog.Level {
	if c.LogLevel == "debug" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
