package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/internal/auth"
	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/metrics"
	"github.com/armada/orbital/internal/oci"
	appversion "github.com/armada/orbital/internal/version"
	webtemplates "github.com/armada/orbital/web/templates"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoswagger "github.com/swaggo/echo-swagger"
)

type Server struct {
	cfg    *config.Config
	echo   *echo.Echo
	logger *slog.Logger
}

func New(cfg *config.Config, db *ent.Client) *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Static("/static", "web/static")

	e.Use(metrics.Middleware())
	e.GET("/metrics", metrics.Handler())

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			u, err := auth.GetUserSession(cfg.SessionKeys(), c.Request())
			c.Set("user_id", u.ID)
			c.Set("user_name", u.Name)
			c.Set("user_email", u.Email)
			c.Set("is_authn", err == nil && u.ID > 0)
			csrfToken, _ := auth.GetOrCreateCSRF(cfg.SessionKeys(), c.Request(), c.Response().Writer)
			c.Set("csrf_token", csrfToken)
			return next(c)
		}
	})

	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod:  true,
		LogURI:     true,
		LogStatus:  true,
		LogLatency: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			logger.Info("request",
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
				"latency_ms", v.Latency.Milliseconds(),
			)
			return nil
		},
	}))

	oidcEnabled := cfg.OIDCIssuerURL != "" && cfg.OIDCClientSecret != ""
	if cfg.OIDCIssuerURL != "" && cfg.OIDCClientSecret == "" {
		logger.Warn("ORBITAL_OIDC_CLIENT_SECRET is not set — SSO login disabled")
	}
	s3Configured := cfg.S3Bucket != "" && cfg.S3AccessKey != "" && cfg.S3SecretKey != ""
	ociConfigured := cfg.OCIConfigured()
	if !ociConfigured {
		logger.Warn("OCI publishing not configured (ORBITAL_OCI_REGISTRY and ORBITAL_OCI_SIGNING_KEY_PATH) — publish disabled")
	}
	ui := handler.NewUI(cfg.Dev, cfg.RatelURL, cfg.IssueTrackerURL, oidcEnabled, s3Configured, cfg.S3Endpoint, cfg.S3Bucket)
	ui.SetOCIConfig(ociConfigured, cfg.OCIRegistry, cfg.OCIRepo)
	e.GET("/", ui.Index)
	e.GET("/datacenters", ui.Index)
	e.GET("/backups", ui.Backups)
	e.GET("/divergence-reports", ui.DivergenceReports)
	e.GET("/audit-log", ui.AuditLog)
	e.GET("/schema", ui.Schema)
	e.GET("/export", ui.Export)

	if db != nil {
		login := handler.NewLogin(db, cfg.SessionKeys(), webtemplates.LoginForm())
		e.POST("/user/login", login.Post)
		e.POST("/user/logout", login.Logout)

		if oidcEnabled {
			oidc, err := handler.NewOIDC(
				context.Background(),
				db,
				cfg.SessionKeys(),
				cfg.OIDCIssuerURL,
				cfg.OIDCClientID,
				cfg.OIDCClientSecret,
				cfg.OIDCRedirectURL,
				logger,
			)
			if err != nil {
				logger.Error("oidc provider init failed", "err", err)
			} else {
				e.GET("/auth/login", oidc.Login)
				e.GET("/auth/callback", oidc.Callback)
			}
		}
	}

	dc := handler.NewDataCenter(cfg.DGraphURL, cfg.Dev, logger)
	e.GET("/datacenters/:id", dc.Tab)

	if db != nil {
		exp := handler.NewExport(db, cfg.DGraphURL, cfg.DGraphScratchURL, cfg.DGraphScratchAdminURL, cfg.ExportDir, cfg.DGraphScratchExportDir, cfg.SchemaPath, logger)
		e.POST("/api/v1/datacenters/:id/export", exp.Trigger)
		e.GET("/api/v1/export/jobs", exp.List)
		e.GET("/api/v1/export/jobs/:jobId", exp.Status)
		e.GET("/api/v1/export/jobs/:jobId/download", exp.Download)

		ociCfg := oci.Config{
			Registry:      cfg.OCIRegistry,
			Repo:          cfg.OCIRepo,
			Username:      cfg.OCIUsername,
			Password:      cfg.OCIPassword,
			SigningKeyPath: cfg.OCISigningKeyPath,
		}
		ociH := handler.NewOCI(db, ociCfg, cfg.DGraphScratchExportDir, logger)
		e.POST("/api/v1/export/jobs/:jobId/publish", ociH.Publish)
		e.DELETE("/api/v1/export/jobs/:jobId", ociH.DeleteJob)
		e.GET("/api/v1/oci/artifacts", ociH.ListArtifacts)
		e.GET("/api/v1/oci/artifacts/:id", ociH.GetArtifact)
		e.GET("/api/v1/oci/public-key", ociH.PublicKey)
		e.POST("/api/v1/oci/test-connection", ociH.TestConnection)
		e.GET("/edge-delivery", ui.EdgeDelivery)

		if !s3Configured {
			logger.Warn("S3 not configured (ORBITAL_S3_BUCKET, ORBITAL_S3_ACCESS_KEY, ORBITAL_S3_SECRET_KEY) — backup disabled")
		} else {
			bk, err := handler.NewBackupHandler(context.Background(), db, handler.BackupConfig{
				DGraphAdminURL:  cfg.DGraphAdminURL,
				DGraphExportDir: cfg.DGraphExportDir,
				SchemaPath:      cfg.SchemaPath,
				S3Endpoint:      cfg.S3Endpoint,
				S3Region:        cfg.S3Region,
				S3Bucket:        cfg.S3Bucket,
				S3AccessKey:     cfg.S3AccessKey,
				S3SecretKey:     cfg.S3SecretKey,
				S3Prefix:        cfg.S3Prefix,
				RetentionCount:  cfg.S3RetentionCount,
				Version:         appversion.Version,
			}, logger)
			if err != nil {
				logger.Error("backup handler init failed", "err", err)
			} else {
				e.POST("/api/v1/backups", bk.Trigger)
				e.GET("/api/v1/backups", bk.List)
				e.GET("/api/v1/backups/:id", bk.Status)
				e.GET("/api/v1/backups/:id/download", bk.Download)
				e.DELETE("/api/v1/backups/:id", bk.Delete)
				e.POST("/api/v1/backups/test-connection", bk.TestConnection)
			}
		}
	}

	gql := handler.NewGraphQL(cfg.DGraphURL)
	e.Any("/graphql", gql.Handle)
	e.GET("/swagger/*", echoswagger.WrapHandler)

	return &Server{
		cfg:    cfg,
		echo:   e,
		logger: logger,
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
