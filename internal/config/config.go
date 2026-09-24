package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
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
	RateLimitEnabled  bool   `envconfig:"ORBITAL_RATE_LIMIT_ENABLED"   default:"false"`
	RateLimitRPS      int    `envconfig:"ORBITAL_RATE_LIMIT_RPS"       default:"40"`
	LoginRateLimitRPS int    `envconfig:"ORBITAL_LOGIN_RATE_LIMIT_RPS" default:"5"`
	DGraphURL         string `envconfig:"DGRAPH_URL"                      default:"http://localhost:8080/graphql"`
	DGraphAdminURL    string `envconfig:"DGRAPH_ADMIN_URL"                default:"http://localhost:8080/admin"`
	RatelURL          string `envconfig:"RATEL_URL"                       default:"http://localhost:8000"`
	IssueTrackerURL   string `envconfig:"ORBITAL_ISSUE_TRACKER_URL"       default:"https://dev.azure.com/armadasystems/Commander/_workitems/create/Bug?[System.AreaPath]=Commander\\Edge\\Edge Platform"`
	// Dev means "a developer is running this", not "auth is off". It enables
	// template hot-reload (handlers re-parse .gohtml per request) and permits
	// the placeholder session HMAC key. API auth is NOT its business — see
	// APIAuthEnabled, which defaults to !Dev only to preserve the historical
	// coupling.
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
	DGraphScratchExportDir  string `envconfig:"DGRAPH_SCRATCH_EXPORT_DIR"       default:"./.local/exports/scratch"`
	SchemaPath              string `envconfig:"ORBITAL_SCHEMA_PATH"             default:"schema/schema.graphql"`
	SessionHMACKey          string `envconfig:"ORBITAL_SESSION_HMAC_KEY"        default:"local-dev-hmac-key-change-in-prod"` // must be changed in prod
	SessionEncryptionKey    string `envconfig:"ORBITAL_SESSION_ENCRYPTION_KEY"  default:"local-dev-enc-key-32-bytes-pad!!"`  // must be exactly 32 bytes for AES-256; empty disables cookie encryption
	DGraphExportDir         string `envconfig:"DGRAPH_EXPORT_DIR"               default:"./.local/exports/blue"`             // host-side mount of /dgraph/export on blue alpha
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
	// OIDC identifiers have NO defaults on purpose: a tenant or client id baked
	// in as a default silently points someone else's deployment at OUR identity
	// provider. They are public values, not secrets, so this is about wrong
	// defaults rather than disclosure. Empty disables SSO (server.go gates on
	// OIDCIssuerURL and OIDCClientSecret both being set) and the password login
	// still works, so `make run-orbital` needs no setup. For local SSO, copy
	// deploy/local/orbital.env.example to deploy/local/orbital.env — the
	// Makefile sources it when present.
	//
	// ⚠️ ORBITAL_DEV=true does NOT bypass bearer auth once ORBITAL_AUTH_PROVIDERS
	// is set — providers are the SOLE source of bearer verification (auth v2,
	// 2026-09-22), so /api/v1 and /graphql return 401 to an unauthenticated
	// caller even with dev=true. This comment claimed the opposite until
	// 2026-09-23, and cb-bundler was configured against that claim: locally it
	// needs real Keycloak client credentials, not a dev-mode bypass. The Dev
	// bypass survives only when NO providers are configured.
	OIDCIssuerURL string `envconfig:"ORBITAL_OIDC_ISSUER_URL"         default:""`
	OIDCClientID  string `envconfig:"ORBITAL_OIDC_CLIENT_ID"          default:""`
	// OIDCDisplayName names the identity provider on the sign-in button. The
	// adopter's IdP is theirs, so orbital must not hardcode a vendor: the
	// default is provider-neutral and an operator sets "Okta", "Keycloak",
	// "Entra ID" or whatever their users recognise. Follows ArgoCD's
	// `oidc.config.name` and Grafana's generic-OAuth `name`.
	OIDCDisplayName string `envconfig:"ORBITAL_OIDC_DISPLAY_NAME" default:"SSO"`
	// OIDCIconURL optionally points at an image for the sign-in button — an
	// adopter's own asset, mounted or hosted by them. Orbital deliberately
	// ships NO vendor logos: "Sign in with Microsoft" and its Google equivalent
	// are specified brand treatments, and redistributing those marks in an
	// open-source repo hands every adopter a trademark obligation orbital has no
	// standing to take on. Empty renders a neutral glyph.
	OIDCIconURL      string `envconfig:"ORBITAL_OIDC_ICON_URL" default:""`
	OIDCClientSecret string `envconfig:"ORBITAL_OIDC_CLIENT_SECRET"      default:""`
	OIDCRedirectURL  string `envconfig:"ORBITAL_OIDC_REDIRECT_URL"       default:"http://localhost:8001/auth/callback"`
	AdminEmails      string `envconfig:"ORBITAL_ADMIN_EMAILS"            default:"admin@armada.ai"` // comma-separated emails promoted to admin on first OIDC login
	// AuthProviders is the multi-provider bearer config. When set it is the SOLE
	// source of bearer verification, superseding the single-issuer bearer path.
	// ORBITAL_OIDC_* keeps driving the browser login flow, where orbital is an
	// OAuth client rather than a resource server — those are different jobs and
	// only the second one moves here.
	AuthProviders AuthProviders `envconfig:"ORBITAL_AUTH_PROVIDERS"`

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
	BundlerURLs []string `envconfig:"ORBITAL_BUNDLER_URLS"                default:"configbundle-bundler=http://localhost:8020/bundle"`
	// Job leases (HA). The staleness threshold does NOT need to exceed job
	// duration — that is the difference between a refreshed heartbeat and a
	// static claim timestamp; it need only exceed one interval plus the worst
	// plausible pause. Wider than controller-runtime's 15s lease because a
	// false positive kills a live restore, where a controller merely re-elects.
	// OrphanGrace applies ONLY to jobs with no heartbeat at all — those left
	// running by the deploy that introduced leases, which under a rolling
	// update may still be executing in the outgoing pod.
	// MigrationLockTimeout bounds how long a replica waits for another
	// replica's schema migration. The wait is legitimate — the holder is
	// migrating — so this is generous; exceeding it is reported as a
	// diagnosable error rather than a hang. See internal/db.Migrate.
	MigrationLockTimeout time.Duration `envconfig:"ORBITAL_MIGRATION_LOCK_TIMEOUT" default:"5m"`

	JobHeartbeatInterval time.Duration `envconfig:"ORBITAL_JOB_HEARTBEAT_INTERVAL" default:"10s"`
	JobStaleAfter        time.Duration `envconfig:"ORBITAL_JOB_STALE_AFTER"        default:"60s"`
	JobOrphanGrace       time.Duration `envconfig:"ORBITAL_JOB_ORPHAN_GRACE"       default:"1h"`

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

	// APIAuthEnabledRaw is the unparsed ORBITAL_API_AUTH_ENABLED. Declared as a
	// struct tag (not read via os.LookupEnv) so the generated settings table in
	// docs/reference/CONFIG.md still sees it; empty means unset, which is the
	// third state the hierarchy below needs. Read APIAuthEnabled, never this.
	APIAuthEnabledRaw string `envconfig:"ORBITAL_API_AUTH_ENABLED"`

	// APIAuthEnabled decides whether bearer verification is installed on
	// /api/v1 and /graphql. Hierarchical, never an independent boolean
	// (docs/reference/CONFIG.md): unset it follows !Dev, which is exactly the
	// historical behaviour; ORBITAL_API_AUTH_ENABLED set explicitly wins.
	// Resolved once in New() so no read site re-derives it from Dev — that is
	// how one site ends up disagreeing with another.
	APIAuthEnabled bool
	// apiAuthExplicit records whether ORBITAL_API_AUTH_ENABLED was set at all.
	// An explicit false must switch auth off in every auth mode; an unset
	// value must not change any mode's existing behaviour.
	apiAuthExplicit bool
	// apiAuthSource names the setting that decided APIAuthEnabled, so the
	// startup log states it rather than leaving an operator to infer it.
	apiAuthSource string
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
	if err := cfg.AuthProviders.Validate(); err != nil {
		return nil, err
	}
	if cfg.DBUseAzMI {
		if cfg.DBHost == "" || cfg.DBUser == "" || cfg.DBName == "" {
			return nil, fmt.Errorf("ORBITAL_DB_USE_AZ_MI=true requires ORBITAL_DB_HOST, ORBITAL_DB_USER, ORBITAL_DB_NAME")
		}
	}
	cfg.APIAuthEnabled = !cfg.Dev
	cfg.apiAuthSource = "ORBITAL_DEV"
	if raw := cfg.APIAuthEnabledRaw; raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("ORBITAL_API_AUTH_ENABLED must be a boolean, got %q", raw)
		}
		cfg.APIAuthEnabled = enabled
		cfg.apiAuthExplicit = true
		cfg.apiAuthSource = "ORBITAL_API_AUTH_ENABLED"
	}
	cfg.sessionKeys = auth.NewSessionKeys(cfg.SessionHMACKey, cfg.SessionEncryptionKey, cfg.Dev, cfg.CookieSecure)
	return &cfg, nil
}

func (c *Config) SessionKeys() auth.SessionKeys {
	return c.sessionKeys
}

// APIAuthSource names the env var that decided APIAuthEnabled.
func (c *Config) APIAuthSource() string { return c.apiAuthSource }

// APIAuthExplicitlyDisabled reports an operator deliberately setting
// ORBITAL_API_AUTH_ENABLED=false. Distinct from APIAuthEnabled being false by
// inheritance from Dev: an explicit false switches auth off in every auth mode,
// an inherited one only preserves the historical OIDC-mode bypass.
func (c *Config) APIAuthExplicitlyDisabled() bool {
	return c.apiAuthExplicit && !c.APIAuthEnabled
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
