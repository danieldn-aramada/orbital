package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/armada/orbital/internal/auth"
	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Port            string        `envconfig:"ORBITAL_PORT"                    default:"8001"`
	ShutdownTimeout time.Duration `envconfig:"ORBITAL_SHUTDOWN_TIMEOUT"        default:"10s"`
	// MaxRequestBody caps every inbound request body (Echo BodyLimit) — the
	// guard against the unbounded io.ReadAll on /graphql (audit S.7). Generous
	// by default because orbital's bodies are small JSON (GraphQL queries, job
	// triggers) and there are no file-upload endpoints; seeding POSTs directly
	// to DGraph, not through orbital. Tighten via env if a deployment wants a
	// stricter ceiling. Echo humanized size string (e.g. "10M", "512K").
	MaxRequestBody string `envconfig:"ORBITAL_MAX_REQUEST_BODY" default:"10M"`
	// Rate limiting (audit S.12). Opt-in — OFF by default so local dev, e2e,
	// and the AKS-dev smoke suite are never throttled; production enables it
	// with ORBITAL_RATE_LIMIT_ENABLED=true. Per-IP token buckets, in-memory
	// (orbital runs single-replica — see ROADMAP HA note). RateLimitRPS is the
	// sustained per-IP request/sec for the whole surface; LoginRateLimitRPS is
	// a tighter bucket on POST /user/login to slow credential brute-force.
	// Burst = 2×RPS. Behind a proxy, per-IP fairness needs c.RealIP() to
	// resolve the true client via X-Forwarded-For (Istio sets it).
	RateLimitEnabled      bool   `envconfig:"ORBITAL_RATE_LIMIT_ENABLED"   default:"false"`
	RateLimitRPS          int    `envconfig:"ORBITAL_RATE_LIMIT_RPS"       default:"40"`
	LoginRateLimitRPS     int    `envconfig:"ORBITAL_LOGIN_RATE_LIMIT_RPS" default:"5"`
	DGraphURL             string `envconfig:"DGRAPH_URL"                      default:"http://localhost:8080/graphql"`
	DGraphAdminURL        string `envconfig:"DGRAPH_ADMIN_URL"                default:"http://localhost:8080/admin"`
	RatelURL              string `envconfig:"RATEL_URL"                       default:"http://localhost:8000"`
	IssueTrackerURL       string `envconfig:"ORBITAL_ISSUE_TRACKER_URL"       default:"https://dev.azure.com/armadasystems/Commander/_workitems/create/Bug?[System.AreaPath]=Commander\\Edge\\Edge Platform"`
	Dev                   bool   `envconfig:"ORBITAL_DEV"                     default:"true"`
	LogLevel              string `envconfig:"ORBITAL_LOG_LEVEL"               default:"info"`
	DGraphScratchURL      string `envconfig:"DGRAPH_SCRATCH_URL"              default:"http://localhost:8081/graphql"`
	DGraphScratchAdminURL string `envconfig:"DGRAPH_SCRATCH_ADMIN_URL"        default:"http://localhost:8081/admin"`
	DGraphScratchZeroURL  string `envconfig:"DGRAPH_SCRATCH_ZERO_URL"         default:"http://localhost:6081"`
	DatabaseURL           string `envconfig:"DATABASE_URL"                    default:"postgres://orbital:orbital-local-dev-secret@localhost:5432/orbital?sslmode=disable"`
	// Discrete DB fields. Set DBUseAzMI to authenticate with the pod's workload
	// identity instead of a password: the Entra token becomes the password, minted
	// per connection. When DBUseAzMI is false these are ignored and DATABASE_URL is
	// used as-is, which is what local dev and air-gapped deployments do.
	DBUseAzMI               bool   `envconfig:"ORBITAL_DB_USE_AZ_MI"            default:"false"`
	DBHost                  string `envconfig:"ORBITAL_DB_HOST"                 default:""`
	DBPort                  int    `envconfig:"ORBITAL_DB_PORT"                 default:"5432"`
	DBUser                  string `envconfig:"ORBITAL_DB_USER"                 default:""`
	DBName                  string `envconfig:"ORBITAL_DB_NAME"                 default:""`
	DBSSLMode               string `envconfig:"ORBITAL_DB_SSLMODE"              default:"require"`
	ExportDir               string `envconfig:"ORBITAL_EXPORT_DIR"              default:"./subgraph-exports"`
	DGraphScratchExportDir  string `envconfig:"DGRAPH_SCRATCH_EXPORT_DIR"       default:"/tmp/orbital-test-scratch"`
	SchemaPath              string `envconfig:"ORBITAL_SCHEMA_PATH"             default:"schema/schema.graphql"`
	SessionHMACKey          string `envconfig:"ORBITAL_SESSION_HMAC_KEY"        default:"local-dev-hmac-key-change-in-prod"` // must be changed in prod
	SessionEncryptionKey    string `envconfig:"ORBITAL_SESSION_ENCRYPTION_KEY"  default:"local-dev-enc-key-32-bytes-pad!!"`  // must be exactly 32 bytes for AES-256; empty disables cookie encryption
	DGraphExportDir         string `envconfig:"DGRAPH_EXPORT_DIR"               default:"/tmp/orbital-test-blue"`            // host-side mount of /dgraph/export on blue alpha
	S3Endpoint              string `envconfig:"ORBITAL_S3_ENDPOINT"             default:"http://localhost:9000"`
	S3Region                string `envconfig:"ORBITAL_S3_REGION"               default:"us-east-1"`
	S3Bucket                string `envconfig:"ORBITAL_S3_BUCKET"               default:"orbital"`
	S3AccessKey             string `envconfig:"ORBITAL_S3_ACCESS_KEY"           default:"minioadmin"`
	S3SecretKey             string `envconfig:"ORBITAL_S3_SECRET_KEY"           default:"minioadmin"`
	S3Prefix                string `envconfig:"ORBITAL_S3_PREFIX"                default:"backups/"` // optional path prefix within the bucket
	S3RetentionCount        int    `envconfig:"ORBITAL_S3_RETENTION_COUNT"       default:"0"`        // deprecated: use ORBITAL_BACKUP_RETENTION_MIN_COUNT
	BackupRetentionDays     int    `envconfig:"ORBITAL_BACKUP_RETENTION_DAYS"    default:"14"`       // delete backups older than N days; 0 = no time-based pruning
	BackupRetentionMinCount int    `envconfig:"ORBITAL_BACKUP_RETENTION_MIN_COUNT" default:"3"`      // always keep at least N backups regardless of age
	BackupSchedule          string `envconfig:"ORBITAL_BACKUP_SCHEDULE"    default:""`               // cron expression for in-process scheduler (e.g. "0 8 * * *" = midnight PT); empty = disabled
	// DivergenceIngestEnabled toggles the S3 poller that ingests divergence
	// snapshots published by orbs. Defaults on so `make run-orbital` picks up
	// snapshots seeded by scripts/seed-divergence-s3.sh without extra env vars.
	// Production AKS overrides interval via deploy/base/deploy.yaml.
	DivergenceIngestEnabled bool          `envconfig:"ORBITAL_DIVERGENCE_INGEST_ENABLED" default:"true"`
	DivergencePollInterval  time.Duration `envconfig:"ORBITAL_DIVERGENCE_POLL_INTERVAL"  default:"10s"`
	// InlineSelectorReject rejects single-entity update mutations that pass their
	// selector/set inline instead of as GraphQL variables — the shape the proxy
	// can't stamp version/updatedAt/updatedBy into. Default on; a kill switch for
	// ops if an unanticipated caller surfaces. See docs/reference/ERROR-RESPONSES.md.
	InlineSelectorReject bool `envconfig:"ORBITAL_INLINE_SELECTOR_REJECT" default:"true"`

	// ChangeControlEnabled controls whether orbital's change-control feature
	// EXISTS: the Change Requests queue, the Approval Policies admin page, their
	// REST endpoints, and the approval gate.
	//
	// It earns a toggle because the feature is actively HARMFUL to some
	// adopters, not merely unused by them: a team running their own change
	// management (ServiceNow, an internal process) would have two systems
	// answering "was this change approved", and anyone using orbital's flow
	// makes that change invisible to their org's audit. Nav clutter alone would
	// not justify a toggle.
	//
	// Turning it off HIDES the surface. It does not delete anything: existing
	// change requests, approvals and policies stay in PostgreSQL untouched and
	// reappear if it is switched back on. Do NOT use it to "reset" the feature.
	//
	// With this off the approval gate never runs either, whatever policies
	// remain in the database — enforcing writes while offering no way to propose
	// a change would be a state with no coherent meaning.
	//
	// There is deliberately NO global "enforcement on/off" setting beside this.
	// Every comparable policy engine puts enforcement on the POLICY — Kyverno's
	// `validationFailureAction`, Gatekeeper's `enforcementAction`, Sentinel's
	// enforcement level, GitHub rulesets' enforcement status — because stopping
	// ONE misbehaving policy is what an operator actually needs, and a global
	// switch is the blunt version of it. Disabling a policy is always reachable:
	// policy administration writes PostgreSQL, never DGraph, so it is never
	// itself gated.
	ChangeControlEnabled bool `envconfig:"ORBITAL_CHANGE_CONTROL_ENABLED" default:"true"`
	// OIDCIssuerURL defaults to the Azure AD tenant URL so the SSO login button
	// is available in `make run-orbital` for daily UI work (provided the user
	// also sets ORBITAL_OIDC_CLIENT_SECRET). In dev mode (ORBITAL_DEV=true),
	// bearer auth on /api/v1 + /graphql is bypassed at the middleware layer
	// (see internal/server/server.go), so machine-to-machine callers like
	// cb-bundler can query without an OAuth2 token. Production (Dev=false)
	// enforces bearer auth strictly.
	OIDCIssuerURL    string `envconfig:"ORBITAL_OIDC_ISSUER_URL"         default:"https://login.microsoftonline.com/8f231c2a-9551-4b40-be17-5b24afe5e890/v2.0"`
	OIDCClientID     string `envconfig:"ORBITAL_OIDC_CLIENT_ID"          default:"5fc832f6-843e-4207-93dd-b3c3a77c06f2"`
	OIDCClientSecret string `envconfig:"ORBITAL_OIDC_CLIENT_SECRET"      default:""`
	OIDCRedirectURL  string `envconfig:"ORBITAL_OIDC_REDIRECT_URL"       default:"http://localhost:8001/auth/callback"`
	OAuth2DeviceCode bool   `envconfig:"ORBITAL_OAUTH2_DEVICE_CODE"      default:"true"` // enables device code flow for browser SSO; set false to use Authorization Code + PKCE (requires publicly resolvable redirect URI). RFC 8628 — OAuth 2.0, not OIDC despite living next to ORBITAL_OIDC_* settings.
	// AppTokenAllowedAppIDs gates which app-only (client-credentials) bearer
	// tokens orbital accepts on /api/v1 and /graphql. Defaults to allowing
	// only the orbital app itself (in-pod cb-bundler authenticates as the
	// orbital app via client credentials). Set explicitly to widen for other
	// internal callers, or empty to allow any AAD app token bound to the
	// orbital audience. See docs/reference/AUTH.md § App Caller Authorization.
	AppTokenAllowedAppIDs []string `envconfig:"ORBITAL_APP_TOKEN_ALLOWED_APPIDS" default:"5fc832f6-843e-4207-93dd-b3c3a77c06f2"`
	AdminEmails           string   `envconfig:"ORBITAL_ADMIN_EMAILS"            default:"admin@armada.ai"` // comma-separated emails promoted to admin on first OIDC login
	// AuthMode selects the authentication stack. Empty (default) keeps today's
	// behavior — session cookie + optional OIDC bearer per OIDCIssuerURL. Set to
	// "external-jwt" to trust bearer tokens issued by an external OIDC provider
	// (e.g. AEP's Keycloak client) instead of orbital's own login flow. See
	// docs/reference/AUTH.md § External JWT Mode.
	AuthMode                string        `envconfig:"ORBITAL_AUTH_MODE"               default:""`
	JWTIssuer               string        `envconfig:"ORBITAL_JWT_ISSUER"              default:""`      // e.g. https://keycloak.example.com/realms/foo
	JWTAudience             string        `envconfig:"ORBITAL_JWT_AUDIENCE"            default:""`      // expected `aud` claim
	JWTClientID             string        `envconfig:"ORBITAL_JWT_CLIENT_ID"           default:""`      // required `azp` claim — the trust anchor when aud is a generic default like "account"
	JWTDefaultRole          string        `envconfig:"ORBITAL_JWT_DEFAULT_ROLE"        default:"admin"` // role every valid token maps to: readonly | dev | admin
	OCIRegistry             string        `envconfig:"ORBITAL_OCI_REGISTRY"            default:"localhost:5001"`
	OCIRepo                 string        `envconfig:"ORBITAL_OCI_REPO"                default:"orbital"`
	OCIUsername             string        `envconfig:"ORBITAL_OCI_USERNAME"            default:""`
	OCIPassword             string        `envconfig:"ORBITAL_OCI_PASSWORD"            default:""`
	OCIAllowHTTP            bool          `envconfig:"ORBITAL_OCI_ALLOW_HTTP"          default:"true"`                    // set false in prod (TLS registry)
	OCISigningKeyPath       string        `envconfig:"ORBITAL_OCI_SIGNING_KEY_PATH"    default:"deploy/local/cosign.key"` // run: cosign generate-key-pair
	BasePath                string        `envconfig:"ORBITAL_BASE_PATH"                    default:""`
	BundlerTimeout          time.Duration `envconfig:"ORBITAL_BUNDLER_TIMEOUT"             default:"30s"`      // per-attempt HTTP timeout; per-request URLs supplied in publish body
	BundlerMaxAttempts      int           `envconfig:"ORBITAL_BUNDLER_MAX_ATTEMPTS"        default:"3"`        // total attempts (1 initial + N-1 retries)
	BundlerMaxResponseBytes int64         `envconfig:"ORBITAL_BUNDLER_MAX_RESPONSE_BYTES"  default:"10485760"` // 10 MB
	// BundlerURLs is the comma-separated list of bundler `name=url` entries to
	// invoke when a publish request omits `bundlers` in its body. The friendly
	// name lands in each layer's OCI annotation `com.armada.orbital.producer`
	// so orb's UI can show which producer made each layer. Format:
	//   "configbundle-bundler=http://localhost:8020/bundle"
	// or comma-separated for multiple bundlers. Bare URLs (no `=`) are also
	// accepted for back-compat; the name defaults to the URL host.
	BundlerURLs       []string      `envconfig:"ORBITAL_BUNDLER_URLS"                default:"configbundle-bundler=http://localhost:8020/bundle"`
	RestoreTimeout    time.Duration `envconfig:"ORBITAL_RESTORE_TIMEOUT"         default:"10m"`
	ExportTimeout     time.Duration `envconfig:"ORBITAL_EXPORT_TIMEOUT"          default:"30m"`
	BackupTimeout     time.Duration `envconfig:"ORBITAL_BACKUP_TIMEOUT"          default:"30m"`
	OCIPublishTimeout time.Duration `envconfig:"ORBITAL_OCI_PUBLISH_TIMEOUT"     default:"10m"`
	// CookieSecure controls the session cookie's Secure attribute. Default false
	// for local dev (HTTP-only). Production deploys MUST set this to "true" once
	// the ingress has TLS — AKS dev sets it explicitly to "false" today because
	// the cluster has no TLS yet (see deploy/base/deploy.yaml). The default-false
	// is dev-friendly: curl-based smoke tests and Playwright over HTTP both work
	// without per-developer env overrides.
	CookieSecure    bool   `envconfig:"ORBITAL_COOKIE_SECURE"           default:"false"`
	DGraphAlphaGRPC string `envconfig:"ORBITAL_DGRAPH_ALPHA_GRPC"       default:"localhost:9080"`
	DGraphZeroGRPC  string `envconfig:"ORBITAL_DGRAPH_ZERO_GRPC"        default:"localhost:5080"`

	sessionKeys auth.SessionKeys // built once in New(); returned by SessionKeys()
}

func New() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	if cfg.SessionEncryptionKey != "" && len(cfg.SessionEncryptionKey) != 32 {
		return nil, fmt.Errorf("ORBITAL_SESSION_ENCRYPTION_KEY must be exactly 32 bytes for AES-256, got %d", len(cfg.SessionEncryptionKey))
	}
	if !cfg.Dev && cfg.SessionHMACKey == "local-dev-hmac-key-change-in-prod" {
		return nil, fmt.Errorf("ORBITAL_SESSION_HMAC_KEY must be set to a secret value in production (ORBITAL_DEV=false)")
	}
	if cfg.AuthMode == "external-jwt" {
		if cfg.JWTIssuer == "" || cfg.JWTAudience == "" || cfg.JWTClientID == "" {
			return nil, fmt.Errorf("ORBITAL_AUTH_MODE=external-jwt requires ORBITAL_JWT_ISSUER, ORBITAL_JWT_AUDIENCE, ORBITAL_JWT_CLIENT_ID")
		}
		switch cfg.JWTDefaultRole {
		case "readonly", "dev", "admin":
		default:
			return nil, fmt.Errorf("ORBITAL_JWT_DEFAULT_ROLE must be one of readonly|dev|admin, got %q", cfg.JWTDefaultRole)
		}
	} else if cfg.AuthMode != "" {
		return nil, fmt.Errorf("ORBITAL_AUTH_MODE must be empty or \"external-jwt\", got %q", cfg.AuthMode)
	}
	if cfg.DBUseAzMI {
		if cfg.DBHost == "" || cfg.DBUser == "" || cfg.DBName == "" {
			return nil, fmt.Errorf("ORBITAL_DB_USE_AZ_MI=true requires ORBITAL_DB_HOST, ORBITAL_DB_USER, ORBITAL_DB_NAME")
		}
	}
	cfg.sessionKeys = auth.NewSessionKeys(cfg.SessionHMACKey, cfg.SessionEncryptionKey, cfg.Dev, cfg.CookieSecure)
	return &cfg, nil
}

func (c *Config) SessionKeys() auth.SessionKeys {
	return c.sessionKeys
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

// DatabaseDSN returns the connection string the pool is built from.
//
// Under managed identity it is assembled from the discrete fields with an EMPTY
// password — internal/db's BeforeConnect hook fills that per connection with a
// freshly minted Entra token. Otherwise DATABASE_URL is returned unchanged, which
// keeps local dev and air-gapped deployments on a static password.
func (c *Config) DatabaseDSN() string {
	if !c.DBUseAzMI {
		return c.DatabaseURL
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.User(c.DBUser),
		Host:     fmt.Sprintf("%s:%d", c.DBHost, c.DBPort),
		Path:     "/" + c.DBName,
		RawQuery: "sslmode=" + c.DBSSLMode,
	}
	return u.String()
}
