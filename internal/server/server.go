package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq" // postgres driver for database/sql
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/bundler"
	"github.com/armada/orbital/internal/divergenceingest"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/metrics"
	orbmw "github.com/armada/orbital/internal/middleware"
	"github.com/armada/orbital/internal/oci"
	retryablehttp "github.com/hashicorp/go-retryablehttp"
	appversion "github.com/armada/orbital/internal/version"
	"github.com/armada/orbital/internal/web/data/layout"
	webtemplates "github.com/armada/orbital/web/templates/orbital"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	echoswagger "github.com/swaggo/echo-swagger"
)

type Server struct {
	cfg                *config.Config
	echo               *echo.Echo
	logger             *slog.Logger
	backupHandler      *handler.BackupHandler          // non-nil when S3 is configured; started in Start()
	divergenceIngester *divergenceingest.Ingester      // non-nil when ORBITAL_DIVERGENCE_INGEST_ENABLED=true and S3 reachable; started in Start()
}

func New(cfg *config.Config, db *ent.Client) *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	var backupHandler *handler.BackupHandler

	handler.ReconcileAdminEmails(context.Background(), db, cfg.AdminEmailSet(), logger)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(metrics.Middleware())
	e.Use(orbmw.DecodePathParams)
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

	oidcEnabled := cfg.OIDCIssuerURL != "" && cfg.OIDCClientSecret != ""
	if cfg.OIDCIssuerURL != "" && cfg.OIDCClientSecret == "" {
		logger.Warn("ORBITAL_OIDC_CLIENT_SECRET is not set — SSO login disabled")
	}

	root := e.Group(cfg.BasePath)

	// apiAuth is the common auth chain (bearer or session) for every /api/v1
	// endpoint. Built once and reused so /graphql and the rest of the
	// API surface stay in sync.
	//
	// Dev mode (cfg.Dev=true) leaves apiAuth empty so machine-to-machine
	// callers like cb-bundler can query /graphql plain-HTTP without an OAuth2
	// token. UI flows still work because the session-cookie middleware higher
	// up already populates user_id/user_email from the cookie, and the
	// per-handler authz (RequireRole on /api/v1, handler-level isMutation
	// check on /graphql) reads those directly. SSO is unaffected —
	// /auth/login and /auth/callback aren't on apiAuth. Production
	// (Dev=false) keeps strict bearer verification.
	var apiAuth []echo.MiddlewareFunc
	if cfg.OIDCIssuerURL != "" {
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
	} else {
		logger.Warn("ORBITAL_OIDC_ISSUER_URL is not set — API auth disabled")
	}

	// Default API group — dev+ required for mutating methods (POST/PUT/PATCH/DELETE).
	// RequireRole passes GET/HEAD/OPTIONS through unconditionally.
	api := root.Group("/api/v1", append(apiAuth, handler.RequireRole(db, user.RoleDev))...)

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
	gql := handler.NewGraphQL(cfg.DGraphURL, db, logger)
	s3Configured := cfg.S3Bucket != "" && cfg.S3AccessKey != "" && cfg.S3SecretKey != ""
	ociConfigured := cfg.OCIConfigured()
	if !ociConfigured {
		logger.Warn("OCI publishing not configured (ORBITAL_OCI_REGISTRY and ORBITAL_OCI_SIGNING_KEY_PATH) — publish disabled")
	}

	ui := handler.NewUI(cfg.Dev, cfg.RatelURL, cfg.IssueTrackerURL, oidcEnabled, cfg.OIDCDeviceCode, s3Configured, cfg.S3Endpoint, cfg.S3Bucket, cfg.BasePath, db)
	ui.SetOCIConfig(ociConfigured, cfg.OCIRegistry, cfg.OCIRepo)
	ui.SetExportDir(cfg.ExportDir)
	ui.SetSchemaPath(cfg.SchemaPath)
	ui.SetRestoreAvailable(true)
	ui.SetDGraphURL(cfg.DGraphURL)
	ui.SetDGraphAdminURL(cfg.DGraphAdminURL)
	ui.SetBackupCronSpec(cfg.BackupSchedule)
	root.Static("/static", "web/shared/static")
	if cfg.BasePath != "" {
		root.GET("", ui.Index)
	}
	root.GET("/", ui.Index)
	root.GET("/inventory", ui.Index)
	root.GET("/datacenters", ui.DataCenters)
	root.GET("/servers", ui.Servers)
	root.GET("/clusters", ui.Clusters)
	root.GET("/backups", ui.Backups)
	root.GET("/divergence-reports", ui.DivergenceReports)
	root.GET("/audit-log", ui.AuditLog)
	root.GET("/restore", ui.Restore)
	root.GET("/schema", ui.Schema)
	root.GET("/export", ui.Export)

	if db != nil {
		login := handler.NewLogin(db, cfg.SessionKeys(), webtemplates.LoginForm(), cfg.BasePath, logger)
		root.POST("/user/login", login.Post)
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
				cfg.OIDCDeviceCode,
			)
			if err != nil {
				logger.Error("oidc provider init failed", "err", err)
			} else {
				root.GET("/auth/login", oidc.Login)
				root.GET("/auth/callback", oidc.Callback)
				if cfg.OIDCDeviceCode {
					root.GET("/auth/device", oidc.DeviceCodeStart)
					root.POST("/auth/device/poll", oidc.DeviceCodePoll)
				}
			}
		}
	}

	dc := handler.NewDataCenter(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath)
	root.GET("/datacenters/:orbId", dc.Tab)

	srv := handler.NewServerHandler(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath)
	root.GET("/servers/:orbId", srv.Tab)

	cluster := handler.NewClusterHandler(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath,
		func(c echo.Context) layout.PageActions {
			canMutate, _ := c.Get("can_mutate").(bool)
			return layout.OrbitalActions(canMutate)
		})
	root.GET("/clusters/:orbId", cluster.Tab)

	delH := handler.NewDeleteHandler(cfg.DGraphURL, db, logger)
	root.GET("/config-items/delete-preview", delH.Preview)
	api.DELETE("/config-items/:type/:id", delH.Execute)

	if db != nil {
		exp := handler.NewExport(db, cfg.DGraphURL, cfg.DGraphScratchURL, cfg.DGraphScratchAdminURL, cfg.DGraphScratchZeroURL, cfg.ExportDir, cfg.DGraphScratchExportDir, cfg.SchemaPath, logger)
		exp.SetBasePath(cfg.BasePath)
		api.POST("/export", exp.Trigger)
		api.GET("/export/jobs", exp.List)

		api.GET("/export/jobs/:jobId", exp.Status)
		api.GET("/export/jobs/:jobId/download", exp.Download)

		ociCfg := oci.Config{
			Registry:      cfg.OCIRegistry,
			Repo:          cfg.OCIRepo,
			Username:      cfg.OCIUsername,
			Password:      cfg.OCIPassword,
			SigningKeyPath: cfg.OCISigningKeyPath,
			AllowHTTP:     cfg.OCIAllowHTTP,
		}
		retryClient := retryablehttp.NewClient()
		retryClient.RetryMax = cfg.BundlerMaxAttempts - 1
		retryClient.RetryWaitMin = time.Second
		retryClient.RetryWaitMax = 10 * time.Second
		retryClient.Logger = nil // silence default logger; errors surface via publisher logging
		bundlerOpts := []bundler.ClientOption{
			bundler.WithHTTPClient(retryClient.StandardClient()),
			bundler.WithMaxResponseBytes(cfg.BundlerMaxResponseBytes),
		}
		ociH := handler.NewOCI(db, ociCfg, cfg.DGraphScratchExportDir, logger, cfg.BundlerTimeout, cfg.BundlerURLs, bundlerOpts...)
		ociH.SetBasePath(cfg.BasePath)
		api.GET("/export/jobs/:jobId/publish-modal", ociH.PublishModal)
		api.POST("/export/jobs/:jobId/publish", ociH.Publish)
		api.DELETE("/export/jobs/:jobId", ociH.DeleteJob)
		api.GET("/oci/artifacts", ociH.ListArtifacts)

		api.GET("/oci/artifacts/:id", ociH.GetArtifact)
		api.GET("/oci/artifacts/:id/layers", ociH.ArtifactLayers)
		api.GET("/oci/public-key", ociH.PublicKey)
		api.POST("/oci/test-connection", ociH.TestConnection)
		root.GET("/publish-history", ui.EdgeDelivery)

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

		evh := handler.NewEventHandler(db, logger)
		api.GET("/audit-log", evh.List)

		uh := handler.NewUsersHandler(db, logger)
		api.GET("/users", uh.List)
		api.PUT("/users/:id/role", uh.UpdateRole)
		root.GET("/users", ui.Users)

		dh := handler.NewDivergenceHandler(db, logger, gql)
		api.GET("/divergences", dh.List)
		api.GET("/divergences/:id", dh.Get)
		api.DELETE("/divergences/:id", dh.Dismiss)
		api.PUT("/divergences/:id/resolution", dh.PutResolution)
		api.DELETE("/divergences/:id/resolution", dh.DeleteResolution)
	}

	gqlGroup.Any("/graphql", gql.Handle)
	root.GET("/swagger/*", echoswagger.WrapHandler)

	// Stub: divergence report intake (Spike 14 will implement full handling).
	api.POST("/reports", func(c echo.Context) error {
		var payload map[string]any
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		}
		logger.Info("divergence report received", "payload", payload)
		return c.JSON(http.StatusOK, map[string]string{"reportId": uuid.New().String()})
	})

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

	return &Server{
		cfg:                cfg,
		echo:               e,
		logger:             logger,
		backupHandler:      backupHandler,
		divergenceIngester: divIngester,
	}
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

