package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/bundler"
	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/divergenceingest"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/metrics"
	orbmw "github.com/armada/orbital/internal/middleware"
	"github.com/armada/orbital/internal/oci"
	appversion "github.com/armada/orbital/internal/version"
	"github.com/armada/orbital/internal/web/data/layout"
	webtemplates "github.com/armada/orbital/web/templates/orbital"
	retryablehttp "github.com/hashicorp/go-retryablehttp"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	_ "github.com/lib/pq" // postgres driver for database/sql
	echoswagger "github.com/swaggo/echo-swagger"
	"golang.org/x/time/rate"
)

type Server struct {
	cfg                *config.Config
	echo               *echo.Echo
	logger             *slog.Logger
	backupHandler      *handler.BackupHandler     // non-nil when S3 is configured; started in Start()
	divergenceIngester *divergenceingest.Ingester // non-nil when ORBITAL_DIVERGENCE_INGEST_ENABLED=true and S3 reachable; started in Start()
}

func New(cfg *config.Config, db *ent.Client) (*Server, error) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	var backupHandler *handler.BackupHandler

	handler.ReconcileAdminEmails(context.Background(), db, cfg.AdminEmailSet(), logger)
	handler.ReconcileStaleJobs(context.Background(), db, logger)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	// Render every returned error as the errorResponse envelope (one shape, with
	// a machine code) instead of Echo's default {"message": ...}.
	// See docs/reference/ERROR-RESPONSES.md.
	e.HTTPErrorHandler = handler.ErrorHandler

	e.Use(metrics.Middleware())
	// Bound every inbound request body before any handler reads it. Without
	// this, io.ReadAll on /graphql (graphql.go) is an unbounded allocation =
	// trivial memory-exhaustion DoS. Applied globally: orbital has no
	// file-upload endpoints, so a generous cap breaks nothing. (audit S.7)
	e.Use(echomw.BodyLimit(cfg.MaxRequestBody))
	e.Use(orbmw.DecodePathParams)

	// Rate limiting (audit S.12) — opt-in via ORBITAL_RATE_LIMIT_ENABLED, so
	// local dev, e2e, and the AKS-dev smoke suite are never throttled;
	// production enables it explicitly. Per-IP token buckets, in-memory
	// (orbital is single-replica). Denials return a 429 that the central
	// ErrorHandler renders as the standard envelope (code RATE_LIMITED), with a
	// Retry-After header. A tighter bucket is attached to POST /user/login
	// below to slow credential brute-force. loginRateLimiter stays nil (and the
	// login route registers without it) when the feature is off.
	var loginRateLimiter echo.MiddlewareFunc
	if cfg.RateLimitEnabled {
		denyHandler := func(c echo.Context, _ string, _ error) error {
			c.Response().Header().Set("Retry-After", "1")
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded — too many requests; retry after a moment")
		}
		newLimiter := func(rps int) echo.MiddlewareFunc {
			return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
				Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
					Rate:  rate.Limit(rps),
					Burst: rps * 2,
				}),
				DenyHandler: denyHandler,
			})
		}
		// General per-IP limiter for the whole surface, skipping the Prometheus
		// scrape endpoint, K8s probe, and static assets so scrapers/probes are
		// never throttled.
		e.Use(echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
			Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
				Rate:  rate.Limit(cfg.RateLimitRPS),
				Burst: cfg.RateLimitRPS * 2,
			}),
			DenyHandler: denyHandler,
			Skipper: func(c echo.Context) bool {
				p := c.Request().URL.Path
				return p == "/healthz" || p == "/metrics" || strings.HasPrefix(p, cfg.BasePath+"/static/")
			},
		}))
		loginRateLimiter = newLimiter(cfg.LoginRateLimitRPS)
		logger.Info("rate limiting enabled", "general_rps", cfg.RateLimitRPS, "login_rps", cfg.LoginRateLimitRPS)
	}
	e.GET("/metrics", metrics.Handler())
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			u, err := auth.GetUserSession(cfg.SessionKeys(), c.Request())
			c.Set("user_id", u.ID)
			c.Set("user_name", u.Name)
			c.Set("user_email", u.Email)
			c.Set("is_authn", err == nil && u.ID > 0)
			c.Set("can_mutate", u.ID > 0 && handler.RoleAtLeast(user.Role(u.Role), user.RoleDev))
			csrfToken, _ := auth.GetOrCreateCSRF(cfg.SessionKeys(), c.Request(), c.Response())
			c.Set("csrf_token", csrfToken)
			return next(c)
		}
	})

	// RequestID must precede AccessLog so the middleware has an ID to log.
	// Honors inbound X-Request-ID header; generates one when absent.
	e.Use(echomw.RequestID())

	// HTTP access log — see docs/reference/AUDIT.md for the attribute conventions.
	// Orbital includes `actor` (extracted from session); orb does not.
	e.Use(orbmw.AccessLog(orbmw.AccessLogConfig{
		Logger:         logger,
		SkipPrefixes:   []string{"/static/"},
		SkipExactPaths: []string{"/favicon.ico", "/healthz"},
		SkipSuffixes:   []string{"/auth/device/poll"},
		ActorFromContext: func(c echo.Context) string {
			actor, _ := c.Get("user_email").(string)
			return actor
		},
	}))

	externalJWTMode := cfg.AuthMode == "external-jwt"
	oidcEnabled := cfg.OIDCIssuerURL != "" && cfg.OIDCClientSecret != ""
	if cfg.OIDCIssuerURL != "" && cfg.OIDCClientSecret == "" {
		logger.Warn("ORBITAL_OIDC_CLIENT_SECRET is not set — SSO login disabled")
	}

	root := e.Group(cfg.BasePath)

	// apiAuth is the common auth chain (bearer or session) for every /api/v1
	// endpoint. Built once and reused so /graphql and the rest of the
	// API surface stay in sync.
	//
	// Three modes:
	//   - external-jwt (ORBITAL_AUTH_MODE=external-jwt): API/GraphQL requests
	//     accept a bearer signed by ORBITAL_JWT_ISSUER (assigned
	//     ORBITAL_JWT_DEFAULT_ROLE via context) OR a session cookie (role
	//     resolved from the DB). The session fallback keeps orbital's own UI
	//     usable — humans sign in via local/OIDC login; AEP's proxied calls
	//     carry a bearer. Login routes stay registered (oidcEnabled unchanged).
	//     See AUTH.md § External JWT mode.
	//   - Dev (cfg.Dev=true): apiAuth stays empty so machine-to-machine
	//     callers like cb-bundler can query /graphql plain-HTTP. Session
	//     middleware still populates user info for the UI.
	//   - Production OIDC (Dev=false, OIDCIssuerURL set): strict bearer
	//     verification + user resolution against PostgreSQL.
	var apiAuth []echo.MiddlewareFunc
	switch {
	case externalJWTMode:
		// Build the AAD bearer verifier as a fallback so internal service
		// callers (in-pod cb-bundler, AAD client-credentials) keep working.
		// external-jwt ADDS Keycloak-user acceptance; it must not remove the
		// AAD service-token path the bundler's publish callback depends on.
		var fallback *auth.BearerVerifier
		if cfg.OIDCIssuerURL != "" {
			if bv, err := auth.NewBearerVerifier(context.Background(), cfg.OIDCIssuerURL, cfg.OIDCClientID, cfg.AppTokenAllowedAppIDs); err != nil {
				logger.Warn("external-jwt: AAD fallback verifier init failed — internal service callers (bundler) will fail auth", "err", err)
			} else {
				fallback = bv
			}
		}
		ejv, err := auth.NewExternalJWTVerifier(context.Background(), auth.ExternalJWTConfig{
			IssuerURL:   cfg.JWTIssuer,
			Audience:    cfg.JWTAudience,
			ClientID:    cfg.JWTClientID,
			DefaultRole: cfg.JWTDefaultRole,
			Fallback:    fallback,
		})
		if err != nil {
			logger.Error("external-jwt verifier init failed — API auth disabled", "err", err)
		} else {
			logger.Warn("ORBITAL_AUTH_MODE=external-jwt — Keycloak bearers (issuer "+cfg.JWTIssuer+") map to role "+cfg.JWTDefaultRole+"; other issuers fall back to AAD bearer auth. Intended for demo/dev; do not use in production without per-user role mapping.",
				"issuer", cfg.JWTIssuer, "audience", cfg.JWTAudience, "client_id", cfg.JWTClientID, "aad_fallback", fallback != nil)
			apiAuth = []echo.MiddlewareFunc{ejv.RequireAuth(), handler.ResolveUser(db, cfg.AdminEmailSet())}
		}
	case cfg.OIDCIssuerURL != "":
		bv, err := auth.NewBearerVerifier(context.Background(), cfg.OIDCIssuerURL, cfg.OIDCClientID, cfg.AppTokenAllowedAppIDs)
		if err != nil {
			logger.Warn("bearer verifier init failed — API auth disabled", "err", err)
		} else if cfg.Dev {
			logger.Warn("ORBITAL_DEV=true — bearer verification on /api/v1 and /graphql is BYPASSED; session-cookie auth remains. Production must set ORBITAL_DEV=false.")
			// apiAuth stays nil — session middleware sets user info for UI;
			// unauthenticated callers (cb-bundler) pass through to handlers
			// which decide based on operation type (mutations require user_id).
		} else {
			apiAuth = []echo.MiddlewareFunc{bv.RequireAuth(), handler.ResolveUser(db, cfg.AdminEmailSet())}
		}
	default:
		logger.Warn("ORBITAL_OIDC_ISSUER_URL is not set — API auth disabled")
	}

	// Single authoritative summary of the effective auth posture, logged
	// unconditionally and stated positively. Operators must never have to
	// infer the active mode from the presence/absence of a mode-specific
	// banner above — an empty apiAuth means unauthenticated requests are
	// accepted, which is the most dangerous state and must be loud.
	authMode := "oidc"
	switch {
	case externalJWTMode:
		authMode = "external-jwt"
	case !oidcEnabled:
		authMode = "none"
	}
	if len(apiAuth) == 0 {
		logger.Warn("auth: API AUTHENTICATION DISABLED — /graphql and /api/v1 accept unauthenticated requests; only session-identity mutations are gated",
			"mode", authMode, "enabled", false, "dev", cfg.Dev)
	} else {
		logger.Info("auth: API authentication enabled", "mode", authMode, "enabled", true)
	}

	// Fail-closed in production. An empty apiAuth means /graphql and /api/v1
	// accept unauthenticated requests — acceptable only in dev (cfg.Dev), where
	// bearer auth is intentionally bypassed (see the switch above). In
	// production this state must abort startup rather than silently degrade to
	// no-auth, whatever the cause: OIDC discovery unreachable at boot, a
	// verifier-init error, or an unset issuer. The preceding WARN carries the
	// specific reason. (audit S.16)
	if !cfg.Dev && len(apiAuth) == 0 {
		return nil, fmt.Errorf("refusing to start: API authentication is disabled in production (ORBITAL_DEV=false) — ensure ORBITAL_OIDC_ISSUER_URL is set and OIDC discovery is reachable at startup")
	}

	// Default API group — dev+ required for mutating methods (POST/PUT/PATCH/DELETE).
	// RequireRole passes GET/HEAD/OPTIONS through unconditionally.
	api := root.Group("/api/v1", append(apiAuth, handler.RequireRole(db, user.RoleDev))...)

	// Read-only API group — auth required, but NO route-level RoleDev check, so
	// readonly callers can run read endpoints that happen to be POST-shaped
	// (e.g. export preview, which takes an orbId in the body per the "orbId is
	// never a path segment" convention). Mirrors the GraphQL group's rationale:
	// a POST read must not be gated as a mutation.
	apiReadonly := root.Group("/api/v1", apiAuth...)

	// GraphQL group — auth required, but NO route-level role check. GraphQL
	// queries use POST, so RequireRole(RoleDev) would wrongly block readonly
	// callers from running reads. Mutation authorization is enforced at the
	// handler (graphql.go), which calls RoleAtLeast(role, RoleDev) when
	// isMutation(query) returns true. Registered at /graphql (under BasePath)
	// not /api/v1/graphql — GraphQL is not URL-versioned, per convention
	// (GitHub, GitLab, NetBox, Apollo). See CLAUDE.md Settled Decisions.
	gqlGroup := root.Group("", apiAuth...)
	// Constructed once at outer scope so the DivergenceHandler can borrow it
	// for the Accept-mutation dispatch (see internal/handler/divergence.go).
	gql := handler.NewGraphQL(cfg.DGraphURL, db, logger, cfg.InlineSelectorReject)
	s3Configured := cfg.S3Bucket != "" && cfg.S3AccessKey != "" && cfg.S3SecretKey != ""
	ociConfigured := cfg.OCIConfigured()
	if !ociConfigured {
		logger.Warn("OCI publishing not configured (ORBITAL_OCI_REGISTRY and ORBITAL_OCI_SIGNING_KEY_PATH) — publish disabled")
	}

	ui := handler.NewUI(cfg.Dev, cfg.RatelURL, cfg.IssueTrackerURL, oidcEnabled, cfg.OAuth2DeviceCode, s3Configured, cfg.S3Endpoint, cfg.S3Bucket, cfg.BasePath, db, logger)
	ui.SetOCIConfig(ociConfigured, cfg.OCIRegistry, cfg.OCIRepo)
	ui.SetExportDir(cfg.ExportDir)
	ui.SetSchemaPath(cfg.SchemaPath)
	ui.SetDGraphURL(cfg.DGraphURL)
	ui.SetDGraphAdminURL(cfg.DGraphAdminURL)
	ui.SetBackupCronSpec(cfg.BackupSchedule)
	// Aggressive caching for versioned static assets. head.gohtml's import
	// map (see web/templates/shared/layouts/head.gohtml) rewrites every ES
	// module URL to a ?v={{.Version}} variant on each deploy, so responses
	// carrying a `v` query param are safe to cache forever — the URL itself
	// changes when the version does. Un-versioned requests keep default
	// cache semantics (last-modified based revalidation). This is what
	// prevents "old browser cache serves stale shared.js after a deploy
	// added new exports" — the class caught by v0.0.23 → v0.0.24 hand-off.
	staticCacheMW := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.QueryParam("v") != "" {
				c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			return next(c)
		}
	}
	root.Group("/static", staticCacheMW).Static("/", "web/shared/static")
	if cfg.BasePath != "" {
		root.GET("", ui.Index)
	}
	root.GET("/", ui.Index)
	root.GET("/inventory", ui.Index)
	root.GET("/datacenters", ui.DataCenters)
	root.GET("/servers", ui.Servers)
	root.GET("/clusters", ui.Clusters)
	root.GET("/network", ui.NetworkDevices)
	root.GET("/backups", ui.Backups)
	root.GET("/divergence-reports", ui.DivergenceReports)
	root.GET("/audit-log", ui.AuditLog)
	root.GET("/change-requests", ui.ChangeRequests)
	root.GET("/change-requests/:id", ui.ChangeRequestDetail)
	root.GET("/approval-policies", ui.ApprovalPolicies)
	root.GET("/restore", ui.Restore)
	root.GET("/schema", ui.Schema)
	root.GET("/export", ui.Export)

	// Hoisted so the divergence ingester (constructed later) can be wired into
	// the divergence handler after both exist.
	var divHandler *handler.DivergenceHandler

	if db != nil {
		login := handler.NewLogin(db, cfg.SessionKeys(), webtemplates.LoginForm(), cfg.BasePath, logger)
		if loginRateLimiter != nil {
			root.POST("/user/login", login.Post, loginRateLimiter)
		} else {
			root.POST("/user/login", login.Post)
		}
		root.POST("/user/logout", login.Logout)

		if oidcEnabled {
			oidc, err := handler.NewOIDC(
				context.Background(),
				db,
				cfg.SessionKeys(),
				cfg.OIDCIssuerURL,
				cfg.OIDCClientID,
				cfg.OIDCClientSecret,
				cfg.OIDCRedirectURL,
				cfg.BasePath,
				logger,
				cfg.AdminEmailSet(),
				cfg.OAuth2DeviceCode,
			)
			if err != nil {
				logger.Error("oidc provider init failed", "err", err)
			} else {
				root.GET("/auth/login", oidc.Login)
				root.GET("/auth/callback", oidc.Callback)
				if cfg.OAuth2DeviceCode {
					root.GET("/auth/device", oidc.DeviceCodeStart)
					root.POST("/auth/device/poll", oidc.DeviceCodePoll)
				}
			}
		}
	}

	dc := handler.NewDataCenter(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath,
		func(c echo.Context) layout.PageActions {
			canMutate, _ := c.Get("can_mutate").(bool)
			return layout.OrbitalActions(canMutate)
		})
	root.GET("/datacenters/:orbId", dc.Tab)

	srv := handler.NewServerHandler(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath,
		func(c echo.Context) layout.PageActions {
			canMutate, _ := c.Get("can_mutate").(bool)
			return layout.OrbitalActions(canMutate)
		})
	root.GET("/servers/:orbId", srv.Tab)

	cluster := handler.NewClusterHandler(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath,
		func(c echo.Context) layout.PageActions {
			canMutate, _ := c.Get("can_mutate").(bool)
			return layout.OrbitalActions(canMutate)
		})
	root.GET("/clusters/:orbId", cluster.Tab)

	networkDevice := handler.NewNetworkDeviceHandler(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath,
		func(c echo.Context) layout.PageActions {
			canMutate, _ := c.Get("can_mutate").(bool)
			return layout.OrbitalActions(canMutate)
		})
	root.GET("/network/:orbId", networkDevice.Tab)

	delH := handler.NewDeleteHandler(cfg.DGraphURL, db, logger)
	root.GET("/config-items/delete-preview", delH.Preview)
	api.DELETE("/config-items/:type/:id", delH.Execute)

	if db != nil {
		exp := handler.NewExport(db, cfg.DGraphURL, cfg.DGraphScratchURL, cfg.DGraphScratchAdminURL, cfg.DGraphScratchZeroURL, cfg.ExportDir, cfg.DGraphScratchExportDir, cfg.SchemaPath, logger)
		exp.SetBasePath(cfg.BasePath)
		exp.SetTimeout(cfg.ExportTimeout)
		api.POST("/export", exp.Trigger)
		api.GET("/export/jobs", exp.List)

		api.GET("/export/jobs/:jobId", exp.Status)
		api.GET("/export/jobs/:jobId/download", exp.Download)
		apiReadonly.POST("/export/preview", exp.Preview)
		apiReadonly.GET("/export/compare", exp.Compare)

		ociCfg := oci.Config{
			Registry:       cfg.OCIRegistry,
			Repo:           cfg.OCIRepo,
			Username:       cfg.OCIUsername,
			Password:       cfg.OCIPassword,
			SigningKeyPath: cfg.OCISigningKeyPath,
			Timeout:        cfg.OCIPublishTimeout,
			AllowHTTP:      cfg.OCIAllowHTTP,
		}
		exp.SetOCIConfig(ociCfg)
		retryClient := retryablehttp.NewClient()
		retryClient.RetryMax = cfg.BundlerMaxAttempts - 1
		retryClient.RetryWaitMin = time.Second
		retryClient.RetryWaitMax = 10 * time.Second
		retryClient.Logger = nil // silence default logger; errors surface via publisher logging
		bundlerOpts := []bundler.ClientOption{
			bundler.WithHTTPClient(retryClient.StandardClient()),
			bundler.WithMaxResponseBytes(cfg.BundlerMaxResponseBytes),
		}
		exp.SetBundlers(cfg.BundlerURLs, cfg.BundlerTimeout, bundlerOpts...)
		ociH := handler.NewOCI(db, ociCfg, cfg.DGraphScratchExportDir, logger, cfg.BundlerTimeout, cfg.BundlerURLs, bundlerOpts...)
		ociH.SetBasePath(cfg.BasePath)
		// Wire the atomic-flow publish callback into the Export handler ONLY
		// when OCI publishing is configured. When unconfigured, publishFn
		// stays nil and Export.Trigger auto-infers download-only mode.
		if ociH.IsPublisherConfigured() {
			exp.SetPublishFn(ociH.PublishExportedJob)
		}
		api.DELETE("/export/jobs/:jobId/artifact", ociH.DeleteArtifact)
		api.GET("/oci/artifacts", ociH.ListArtifacts)

		api.GET("/oci/artifacts/:id", ociH.GetArtifact)
		api.GET("/oci/artifacts/:id/layers", ociH.ArtifactLayers)
		api.GET("/oci/public-key", ociH.PublicKey)
		api.POST("/oci/test-connection", ociH.TestConnection)
		root.GET("/publish-history", ui.EdgeDelivery)
		root.GET("/publish-history/compare", ui.PublishHistoryCompare)

		if !s3Configured {
			logger.Warn("S3 not configured (ORBITAL_S3_BUCKET, ORBITAL_S3_ACCESS_KEY, ORBITAL_S3_SECRET_KEY) — backup disabled")
		} else {
			var rawDB *sql.DB
			if rdb, err := sql.Open("postgres", cfg.DatabaseURL); err != nil {
				logger.Warn("raw sql.DB open failed — advisory lock disabled", "err", err)
			} else {
				rawDB = rdb
			}

			bk, err := handler.NewBackupHandler(context.Background(), db, handler.BackupConfig{
				DGraphAdminURL:    cfg.DGraphAdminURL,
				DGraphExportDir:   cfg.DGraphExportDir,
				SchemaPath:        cfg.SchemaPath,
				S3Endpoint:        cfg.S3Endpoint,
				S3Region:          cfg.S3Region,
				S3Bucket:          cfg.S3Bucket,
				S3AccessKey:       cfg.S3AccessKey,
				S3SecretKey:       cfg.S3SecretKey,
				S3Prefix:          cfg.S3Prefix,
				RetentionDays:     cfg.BackupRetentionDays,
				RetentionMinCount: cfg.BackupRetentionMinCount,
				Version:           appversion.Version,
				RawDB:             rawDB,
				CronSpec:          cfg.BackupSchedule,
				Timeout:           cfg.BackupTimeout,
			}, logger)
			if err != nil {
				logger.Error("backup handler init failed", "err", err)
			} else {
				api.POST("/backup", bk.Trigger)
				api.GET("/backup/jobs", bk.List)

				api.GET("/backup/jobs/:jobId", bk.Status)
				api.GET("/backup/jobs/:jobId/download", bk.Download)
				api.DELETE("/backup/jobs/:jobId", bk.Delete)
				api.POST("/backup/test-connection", bk.TestConnection)
				backupHandler = bk
			}

			rh, err := handler.NewRestoreHandler(context.Background(), db, handler.RestoreConfig{
				S3Endpoint:      cfg.S3Endpoint,
				S3Region:        cfg.S3Region,
				S3Bucket:        cfg.S3Bucket,
				S3AccessKey:     cfg.S3AccessKey,
				S3SecretKey:     cfg.S3SecretKey,
				DGraphAdminURL:  cfg.DGraphAdminURL,
				DGraphAlphaGRPC: cfg.DGraphAlphaGRPC,
				DGraphZeroGRPC:  cfg.DGraphZeroGRPC,
				SchemaPath:      cfg.SchemaPath,
				RestoreTimeout:  cfg.RestoreTimeout,
			}, handler.NewSubprocessRestoreBackend(), logger)
			if err != nil {
				logger.Error("restore handler init failed", "err", err)
			} else {
				api.GET("/restore/jobs", rh.List)

				api.GET("/restore/jobs/:jobId", rh.Status)
				api.POST("/restore", rh.Trigger)
			}
		}

		evh := handler.NewEventHandler(db, logger, cfg.BasePath)
		api.GET("/audit-log", evh.List)

		uh := handler.NewUsersHandler(db, logger)
		api.GET("/users", uh.List)
		api.PUT("/users/:id/role", uh.UpdateRole)
		root.GET("/users", ui.Users)

		dh := handler.NewDivergenceHandler(db, logger, gql)
		divHandler = dh
		api.GET("/divergences", dh.List)
		api.GET("/divergences/:id", dh.Get)
		api.DELETE("/divergences/:id", dh.Dismiss)
		api.DELETE("/divergences", dh.ClearByDC)
		api.PUT("/divergences/:id/resolution", dh.PutResolution)
		api.DELETE("/divergences/:id/resolution", dh.DeleteResolution)

		// Change requests. Reads go on apiReadonly so a readonly caller can
		// review the queue and the diff; every state transition is dev+ via the
		// `api` group, and the finer rules (author != approver, who may merge)
		// are enforced in the handler because they depend on the request.
		//
		// Policy administration is admin-only: a dev who could edit the policy
		// governing their own change could disable the gate instead of passing
		// it. RequireRole(RoleAdmin) on the mutating verbs; the read and the
		// resolve convenience stay dev-visible so a client can label its save
		// button without being an admin.
		crh := handler.NewChangeRequest(db, gql, cfg.DGraphURL, logger)
		// A gated divergence Accept opens a change request instead of mutating.
		dh.SetChangeRequests(crh)
		apiReadonly.GET("/change-requests", crh.ListChangeRequests)
		apiReadonly.GET("/change-requests/:id", crh.GetChangeRequest)
		apiReadonly.GET("/change-requests/:id/diff", crh.GetChangeRequestDiff)
		api.POST("/change-requests", crh.CreateChangeRequest)
		api.PATCH("/change-requests/:id", crh.AmendChangeRequest)
		api.POST("/change-requests/:id/approve", crh.ApproveChangeRequest)
		api.POST("/change-requests/:id/reject", crh.RejectChangeRequest)
		api.POST("/change-requests/:id/merge", crh.MergeChangeRequest)
		api.POST("/change-requests/:id/close", crh.CloseChangeRequest)

		// Spike 36 session 2 installs the write gate. Until then a declared
		// policy records intent without enforcing it, so say so at startup —
		// an operator inheriting a configured deployment must not discover it
		// from a mutation that should have been refused and was not.
		crh.WarnUnenforcedPolicies(context.Background())

		adminAPI := root.Group("/api/v1", append(apiAuth, handler.RequireRole(db, user.RoleAdmin))...)
		apiReadonly.GET("/approval-policies", crh.ListApprovalPolicies)
		apiReadonly.GET("/approval-policies/resolve", crh.ResolveApprovalPolicy)
		adminAPI.POST("/approval-policies", crh.CreateApprovalPolicy)
		adminAPI.PATCH("/approval-policies/:id", crh.UpdateApprovalPolicy)
		adminAPI.DELETE("/approval-policies/:id", crh.DeleteApprovalPolicy)
	}

	gqlGroup.Any("/graphql", gql.Handle)
	root.GET("/swagger/*", echoswagger.WrapHandler)

	var divIngester *divergenceingest.Ingester
	if cfg.DivergenceIngestEnabled && cfg.S3Bucket != "" {
		ig, err := divergenceingest.New(context.Background(), db, divergenceingest.Config{
			Endpoint:     cfg.S3Endpoint,
			Region:       cfg.S3Region,
			Bucket:       cfg.S3Bucket,
			AccessKey:    cfg.S3AccessKey,
			SecretKey:    cfg.S3SecretKey,
			PollInterval: cfg.DivergencePollInterval,
			DGraphURL:    cfg.DGraphURL,
			Registry:     cfg.OCIRegistry,
			RepoPrefix:   cfg.OCIRepo,
		}, logger)
		if err != nil {
			logger.Warn("divergence ingester init failed — ingest disabled", "err", err)
		} else {
			divIngester = ig
		}
	}

	// Wire the ingester into the divergence handler so ClearByDC can reset
	// the in-memory idempotency tracker after wiping DB rows. Either may be
	// nil (DB-less mode, S3 not configured); SetIngester handles nil safely.
	if divHandler != nil && divIngester != nil {
		divHandler.SetIngester(divIngester)
	}

	return &Server{
		cfg:                cfg,
		echo:               e,
		logger:             logger,
		backupHandler:      backupHandler,
		divergenceIngester: divIngester,
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	// Log configured bundler URLs for operator visibility. We deliberately do
	// NOT probe them — orbital's core (intent reads, GraphQL, UI, audit) does
	// not depend on cb-bundler, and probing creates a startup race with the
	// in-pod sidecar that caused unnecessary restarts. Misconfigured URLs
	// surface clearly at publish time with the failing URL in the response.
	if len(s.cfg.BundlerURLs) == 0 {
		s.logger.Info("ORBITAL_BUNDLER_URLS not configured — publish will skip bundler layers")
	} else {
		s.logger.Info("ORBITAL_BUNDLER_URLS configured", "urls", s.cfg.BundlerURLs)
	}

	errCh := make(chan error, 1)

	s.logger.Info("starting orbital", "port", s.cfg.Port, "dgraph", s.cfg.DGraphURL)

	go func() {
		if err := s.echo.Start(":" + s.cfg.Port); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Give the server a moment to bind and log ready, then wait.
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	default:
		s.logger.Info("orbital ready", "addr", ":"+s.cfg.Port)
	}

	if s.backupHandler != nil {
		go s.backupHandler.StartScheduler(ctx)
	}

	if s.divergenceIngester != nil {
		go s.divergenceIngester.Start(ctx)
	}

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
	}

	s.logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	if err := s.echo.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return nil
}
