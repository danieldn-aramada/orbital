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

	_ "github.com/lib/pq" // postgres driver for database/sql
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/bundler"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/metrics"
	"github.com/armada/orbital/internal/oci"
	retryablehttp "github.com/hashicorp/go-retryablehttp"
	appversion "github.com/armada/orbital/internal/version"
	webtemplates "github.com/armada/orbital/web/templates/orbital"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoswagger "github.com/swaggo/echo-swagger"
)

type Server struct {
	cfg           *config.Config
	echo          *echo.Echo
	logger        *slog.Logger
	backupHandler *handler.BackupHandler // non-nil when S3 is configured; started in Start()
}

func New(cfg *config.Config, db *ent.Client) *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	var backupHandler *handler.BackupHandler

	handler.ReconcileAdminEmails(context.Background(), db, cfg.AdminEmailSet(), logger)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(metrics.Middleware())
	e.GET("/metrics", metrics.Handler())

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			u, err := auth.GetUserSession(cfg.SessionKeys(), c.Request())
			c.Set("user_id", u.ID)
			c.Set("user_name", u.Name)
			c.Set("user_email", u.Email)
			c.Set("is_authn", err == nil && u.ID > 0)
			c.Set("can_mutate", u.ID > 0 && handler.RoleAtLeast(user.Role(u.Role), user.RoleDev))
			csrfToken, _ := auth.GetOrCreateCSRF(cfg.SessionKeys(), c.Request(), c.Response().Writer)
			c.Set("csrf_token", csrfToken)
			return next(c)
		}
	})

	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		Skipper: func(c echo.Context) bool {
			p := c.Request().URL.Path
			return strings.HasPrefix(p, "/static/") || p == "/favicon.ico" || strings.HasSuffix(p, "/auth/device/poll")
		},
		LogMethod:  true,
		LogURI:     true,
		LogStatus:  true,
		LogLatency: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			actor, _ := c.Get("user_email").(string)
			logger.Info("request",
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
				"latency_ms", v.Latency.Milliseconds(),
				"actor", actor,
			)
			return nil
		},
	}))

	oidcEnabled := cfg.OIDCIssuerURL != "" && cfg.OIDCClientSecret != ""
	if cfg.OIDCIssuerURL != "" && cfg.OIDCClientSecret == "" {
		logger.Warn("ORBITAL_OIDC_CLIENT_SECRET is not set — SSO login disabled")
	}

	root := e.Group(cfg.BasePath)

	var api *echo.Group
	if cfg.OIDCIssuerURL != "" {
		bv, err := auth.NewBearerVerifier(context.Background(), cfg.OIDCIssuerURL, cfg.OIDCClientID)
		if err != nil {
			logger.Warn("bearer verifier init failed — API auth disabled", "err", err)
			api = root.Group("/api/v1", handler.RequireRole(db, user.RoleDev))
		} else {
			api = root.Group("/api/v1", bv.RequireAuth(), handler.ResolveUser(db, cfg.AdminEmailSet()), handler.RequireRole(db, user.RoleDev))
		}
	} else {
		logger.Warn("ORBITAL_OIDC_ISSUER_URL is not set — API auth disabled")
		api = root.Group("/api/v1", handler.RequireRole(db, user.RoleDev))
	}
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
	ui.SetBackupCronSpec(cfg.BackupSchedule)
	root.Static("/static", "web/shared/static")
	if cfg.BasePath != "" {
		root.GET("", ui.Index)
	}
	root.GET("/", ui.Index)
	root.GET("/inventory", ui.Index)
	root.GET("/datacenters", ui.DataCenters)
	root.GET("/servers", ui.Servers)
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

	inv := handler.NewInventory(cfg.DGraphURL)
	api.GET("/inventory", inv.List)

	dc := handler.NewDataCenter(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath)
	root.GET("/datacenters/:id", dc.Tab)

	srv := handler.NewServerHandler(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath)
	root.GET("/servers/:id", srv.Tab)

	delH := handler.NewDeleteHandler(cfg.DGraphURL, db, logger)
	root.GET("/config-items/delete-preview", delH.Preview)
	api.DELETE("/config-items", delH.Execute)

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
		ociH := handler.NewOCI(db, ociCfg, cfg.DGraphScratchExportDir, logger, cfg.BundlerTimeout, bundlerOpts...)
		ociH.SetBasePath(cfg.BasePath)
		api.GET("/export/jobs/:jobId/publish-modal", ociH.PublishModal)
		api.POST("/export/jobs/:jobId/publish", ociH.Publish)
		api.DELETE("/export/jobs/:jobId", ociH.DeleteJob)
		api.GET("/oci/artifacts", ociH.ListArtifacts)

		api.GET("/oci/artifacts/:id", ociH.GetArtifact)
		api.GET("/oci/public-key", ociH.PublicKey)
		api.POST("/oci/test-connection", ociH.TestConnection)
		root.GET("/signed-artifacts", ui.EdgeDelivery)

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
		root.GET("/api/v1/audit-log", evh.List)

		uh := handler.NewUsersHandler(db, logger)
		api.GET("/users", uh.List)
		api.PUT("/users/:id/role", uh.UpdateRole)
		root.GET("/users", ui.Users)
	}

	gql := handler.NewGraphQL(cfg.DGraphURL, db, logger)
	root.Any("/graphql", gql.Handle)
	api.Any("/graphql", gql.Handle)
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

	return &Server{
		cfg:           cfg,
		echo:          e,
		logger:        logger,
		backupHandler: backupHandler,
	}
}

func (s *Server) Start(ctx context.Context) error {
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
