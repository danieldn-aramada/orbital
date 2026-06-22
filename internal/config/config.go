package config

import (
	"fmt"
	"log/slog"
	"strings"
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
	DGraphScratchZeroURL   string        `envconfig:"DGRAPH_SCRATCH_ZERO_URL"         default:"http://localhost:6081"`
	DatabaseURL            string        `envconfig:"DATABASE_URL"                    default:"postgres://orbital:orbital@localhost:5432/orbital?sslmode=disable"`
	ExportDir              string        `envconfig:"ORBITAL_EXPORT_DIR"              default:"./subgraph-exports"`
	DGraphScratchExportDir string        `envconfig:"DGRAPH_SCRATCH_EXPORT_DIR"       default:"/tmp/orbital-test-scratch"`
	SchemaPath             string        `envconfig:"ORBITAL_SCHEMA_PATH"             default:"schema/schema.graphql"`
	SessionHMACKey         string        `envconfig:"ORBITAL_SESSION_HMAC_KEY"        default:"local-dev-hmac-key-change-in-prod"` // must be changed in prod
	SessionEncryptionKey   string        `envconfig:"ORBITAL_SESSION_ENCRYPTION_KEY"  default:"local-dev-enc-key-32-bytes-pad!!"`  // must be exactly 32 bytes for AES-256; empty disables cookie encryption
	DGraphExportDir        string        `envconfig:"DGRAPH_EXPORT_DIR"               default:"/tmp/orbital-test-blue"`            // host-side mount of /dgraph/export on blue alpha
	S3Endpoint             string        `envconfig:"ORBITAL_S3_ENDPOINT"             default:"http://localhost:9000"`
	S3Region               string        `envconfig:"ORBITAL_S3_REGION"               default:"us-east-1"`
	S3Bucket               string        `envconfig:"ORBITAL_S3_BUCKET"               default:"orbital"`
	S3AccessKey            string        `envconfig:"ORBITAL_S3_ACCESS_KEY"           default:"minioadmin"`
	S3SecretKey            string        `envconfig:"ORBITAL_S3_SECRET_KEY"           default:"minioadmin"`
	S3Prefix                string        `envconfig:"ORBITAL_S3_PREFIX"                default:"backups/"` // optional path prefix within the bucket
	S3RetentionCount        int           `envconfig:"ORBITAL_S3_RETENTION_COUNT"       default:"0"`        // deprecated: use ORBITAL_BACKUP_RETENTION_MIN_COUNT
	BackupRetentionDays     int           `envconfig:"ORBITAL_BACKUP_RETENTION_DAYS"    default:"14"`       // delete backups older than N days; 0 = no time-based pruning
	BackupRetentionMinCount int           `envconfig:"ORBITAL_BACKUP_RETENTION_MIN_COUNT" default:"3"`     // always keep at least N backups regardless of age
	BackupSchedule   string `envconfig:"ORBITAL_BACKUP_SCHEDULE"    default:""`    // cron expression for in-process scheduler (e.g. "0 8 * * *" = midnight PT); empty = disabled
	// DivergenceIngestEnabled toggles the S3 poller that ingests divergence
	// snapshots published by orbs. Defaults on so `make run-orbital` picks up
	// snapshots seeded by scripts/seed-divergence-s3.sh without extra env vars.
	// Production AKS overrides interval via deploy/base/deploy.yaml.
	DivergenceIngestEnabled bool          `envconfig:"ORBITAL_DIVERGENCE_INGEST_ENABLED" default:"true"`
	DivergencePollInterval  time.Duration `envconfig:"ORBITAL_DIVERGENCE_POLL_INTERVAL"  default:"10s"`
	// OIDCIssuerURL defaults to the Azure AD tenant URL so the SSO login button
	// is available in `make run-orbital` for daily UI work (provided the user
	// also sets ORBITAL_OIDC_CLIENT_SECRET). In dev mode (ORBITAL_DEV=true),
	// bearer auth on /api/v1 + /graphql is bypassed at the middleware layer
	// (see internal/server/server.go), so machine-to-machine callers like
	// cb-bundler can query without an OAuth2 token. Production (Dev=false)
	// enforces bearer auth strictly.
	OIDCIssuerURL          string        `envconfig:"ORBITAL_OIDC_ISSUER_URL"         default:"https://login.microsoftonline.com/8f231c2a-9551-4b40-be17-5b24afe5e890/v2.0"`
	OIDCClientID           string        `envconfig:"ORBITAL_OIDC_CLIENT_ID"          default:"5fc832f6-843e-4207-93dd-b3c3a77c06f2"`
	OIDCClientSecret       string        `envconfig:"ORBITAL_OIDC_CLIENT_SECRET"      default:""`
	OIDCRedirectURL        string        `envconfig:"ORBITAL_OIDC_REDIRECT_URL"       default:"http://localhost:8001/auth/callback"`
	OAuth2DeviceCode       bool          `envconfig:"ORBITAL_OAUTH2_DEVICE_CODE"      default:"true"`   // enables device code flow for browser SSO; set false to use Authorization Code + PKCE (requires publicly resolvable redirect URI). RFC 8628 — OAuth 2.0, not OIDC despite living next to ORBITAL_OIDC_* settings.
	// AppTokenAllowedAppIDs gates which app-only (client-credentials) bearer
	// tokens orbital accepts on /api/v1 and /graphql. Defaults to allowing
	// only the orbital app itself (in-pod cb-bundler authenticates as the
	// orbital app via client credentials). Set explicitly to widen for other
	// internal callers, or empty to allow any AAD app token bound to the
	// orbital audience. See docs/decisions/010-bundler-service-auth.md.
	AppTokenAllowedAppIDs  []string      `envconfig:"ORBITAL_APP_TOKEN_ALLOWED_APPIDS" default:"5fc832f6-843e-4207-93dd-b3c3a77c06f2"`
	AdminEmails            string        `envconfig:"ORBITAL_ADMIN_EMAILS"            default:"admin@armada.ai"` // comma-separated emails promoted to admin on first OIDC login
	OCIRegistry            string        `envconfig:"ORBITAL_OCI_REGISTRY"            default:"localhost:5001"`
	OCIRepo                string        `envconfig:"ORBITAL_OCI_REPO"                default:"orbital"`
	OCIUsername            string        `envconfig:"ORBITAL_OCI_USERNAME"            default:""`
	OCIPassword            string        `envconfig:"ORBITAL_OCI_PASSWORD"            default:""`
	OCIAllowHTTP           bool          `envconfig:"ORBITAL_OCI_ALLOW_HTTP"          default:"true"`              // set false in prod (TLS registry)
	OCISigningKeyPath      string        `envconfig:"ORBITAL_OCI_SIGNING_KEY_PATH"    default:"deploy/local/cosign.key"`        // run: cosign generate-key-pair
	BasePath                  string        `envconfig:"ORBITAL_BASE_PATH"                    default:""`
	BundlerTimeout           time.Duration `envconfig:"ORBITAL_BUNDLER_TIMEOUT"             default:"30s"`  // per-attempt HTTP timeout; per-request URLs supplied in publish body
	BundlerMaxAttempts       int           `envconfig:"ORBITAL_BUNDLER_MAX_ATTEMPTS"        default:"3"`    // total attempts (1 initial + N-1 retries)
	BundlerMaxResponseBytes  int64         `envconfig:"ORBITAL_BUNDLER_MAX_RESPONSE_BYTES"  default:"10485760"` // 10 MB
	// BundlerURLs is the comma-separated list of bundler `name=url` entries to
	// invoke when a publish request omits `bundlers` in its body. The friendly
	// name lands in each layer's OCI annotation `com.armada.orbital.producer`
	// so orb's UI can show which producer made each layer. Format:
	//   "configbundle-bundler=http://localhost:8020/bundle"
	// or comma-separated for multiple bundlers. Bare URLs (no `=`) are also
	// accepted for back-compat; the name defaults to the URL host.
	BundlerURLs              []string      `envconfig:"ORBITAL_BUNDLER_URLS"                default:"configbundle-bundler=http://localhost:8020/bundle"`
	RestoreTimeout         time.Duration `envconfig:"ORBITAL_RESTORE_TIMEOUT"         default:"10m"`
	// CookieSecure controls the session cookie's Secure attribute. Default false
	// for local dev (HTTP-only). Production deploys MUST set this to "true" once
	// the ingress has TLS — AKS dev sets it explicitly to "false" today because
	// the cluster has no TLS yet (see deploy/base/deploy.yaml). The default-false
	// is dev-friendly: curl-based smoke tests and Playwright over HTTP both work
	// without per-developer env overrides.
	CookieSecure           bool          `envconfig:"ORBITAL_COOKIE_SECURE"           default:"false"`
	DGraphAlphaGRPC        string        `envconfig:"ORBITAL_DGRAPH_ALPHA_GRPC"       default:"localhost:9080"`
	DGraphZeroGRPC         string        `envconfig:"ORBITAL_DGRAPH_ZERO_GRPC"        default:"localhost:5080"`
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
		Dev:           c.Dev,
		Secure:        c.CookieSecure,
	}
}

// AdminEmailSet parses ORBITAL_ADMIN_EMAILS into a lowercase set for O(1) lookup.
func (c *Config) AdminEmailSet() map[string]struct{} {
	m := make(map[string]struct{})
	for _, e := range strings.Split(c.AdminEmails, ",") {
		e = strings.TrimSpace(strings.ToLower(e))
		if e != "" {
			m[e] = struct{}{}
		}
	}
	return m
}

// OCIConfigured returns true when the minimum OCI fields are set to enable publishing.
func (c *Config) OCIConfigured() bool {
	return c.OCIRegistry != "" && c.OCISigningKeyPath != ""
}

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
