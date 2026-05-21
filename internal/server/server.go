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
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoswagger "github.com/swaggo/echo-swagger"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

	root := e.Group(cfg.BasePath)

	var api *echo.Group
	if cfg.OIDCIssuerURL != "" {
		bv, err := auth.NewBearerVerifier(context.Background(), cfg.OIDCIssuerURL, cfg.OIDCClientID)
		if err != nil {
			logger.Warn("bearer verifier init failed — API auth disabled", "err", err)
			api = root.Group("/api/v1")
		} else {
			api = root.Group("/api/v1", bv.RequireAuth())
		}
	} else {
		logger.Warn("ORBITAL_OIDC_ISSUER_URL is not set — API auth disabled")
		api = root.Group("/api/v1")
	}
	s3Configured := cfg.S3Bucket != "" && cfg.S3AccessKey != "" && cfg.S3SecretKey != ""
	ociConfigured := cfg.OCIConfigured()
	if !ociConfigured {
		logger.Warn("OCI publishing not configured (ORBITAL_OCI_REGISTRY and ORBITAL_OCI_SIGNING_KEY_PATH) — publish disabled")
	}

	// Detect in-cluster k8s — restore via kubectl exec requires this.
	var k8sCfg *rest.Config
	var k8sClient kubernetes.Interface
	k8sAvailable := false
	if inClusterCfg, err := rest.InClusterConfig(); err == nil {
		if kc, err := kubernetes.NewForConfig(inClusterCfg); err == nil {
			k8sCfg = inClusterCfg
			k8sClient = kc
			k8sAvailable = true
		} else {
			logger.Warn("k8s client init failed — restore from stored backup disabled", "err", err)
		}
	} else {
		logger.Warn("not running in-cluster — restore from stored backup disabled")
	}

	ui := handler.NewUI(cfg.Dev, cfg.RatelURL, cfg.IssueTrackerURL, oidcEnabled, s3Configured, cfg.S3Endpoint, cfg.S3Bucket, cfg.BasePath)
	ui.SetOCIConfig(ociConfigured, cfg.OCIRegistry, cfg.OCIRepo)
	ui.SetExportDir(cfg.ExportDir)
	ui.SetSchemaPath(cfg.SchemaPath)
	ui.SetK8sAvailable(k8sAvailable)
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
		login := handler.NewLogin(db, cfg.SessionKeys(), webtemplates.LoginForm(), cfg.BasePath)
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
			)
			if err != nil {
				logger.Error("oidc provider init failed", "err", err)
			} else {
				root.GET("/auth/login", oidc.Login)
				root.GET("/auth/callback", oidc.Callback)
			}
		}
	}

	inv := handler.NewInventory(cfg.DGraphURL)
	api.GET("/inventory", inv.List)

	dc := handler.NewDataCenter(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath)
	root.GET("/datacenters/:id", dc.Tab)

	srv := handler.NewServerHandler(cfg.DGraphURL, cfg.Dev, logger, cfg.BasePath)
	root.GET("/servers/:id", srv.Tab)

	if db != nil {
		exp := handler.NewExport(db, cfg.DGraphURL, cfg.DGraphScratchURL, cfg.DGraphScratchAdminURL, cfg.DGraphScratchZeroURL, cfg.ExportDir, cfg.DGraphScratchExportDir, cfg.SchemaPath, logger)
		api.POST("/datacenters/:id/export", exp.Trigger)
		api.GET("/export/jobs", exp.List)
		api.GET("/export/jobs/:jobId", exp.Status)
		api.GET("/export/jobs/:jobId/download", exp.Download)

		ociCfg := oci.Config{
			Registry:      cfg.OCIRegistry,
			Repo:          cfg.OCIRepo,
			Username:      cfg.OCIUsername,
			Password:      cfg.OCIPassword,
			SigningKeyPath: cfg.OCISigningKeyPath,
		}
		ociH := handler.NewOCI(db, ociCfg, cfg.DGraphScratchExportDir, logger)
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
				api.POST("/backups", bk.Trigger)
				api.GET("/backups", bk.List)
				api.GET("/backups/:id", bk.Status)
				api.GET("/backups/:id/download", bk.Download)
				api.DELETE("/backups/:id", bk.Delete)
				api.POST("/backups/test-connection", bk.TestConnection)
			}

			rh, err := handler.NewRestoreHandler(context.Background(), db, handler.RestoreConfig{
				S3Endpoint:      cfg.S3Endpoint,
				S3Region:        cfg.S3Region,
				S3Bucket:        cfg.S3Bucket,
				S3AccessKey:     cfg.S3AccessKey,
				S3SecretKey:     cfg.S3SecretKey,
				DGraphAdminURL:  cfg.DGraphAdminURL,
				DGraphNamespace: cfg.DGraphNamespace,
				DGraphAlphaGRPC: cfg.DGraphAlphaGRPC,
				DGraphZeroGRPC:  cfg.DGraphZeroGRPC,
				SchemaPath:      cfg.SchemaPath,
				RestoreDir:      cfg.RestoreDir,
				RestoreTimeout:  cfg.RestoreTimeout,
			}, k8sClient, k8sCfg, logger)
			if err != nil {
				logger.Error("restore handler init failed", "err", err)
			} else {
				api.GET("/restore", rh.List)
				api.GET("/restore/:id", rh.Status)
				if k8sAvailable {
					api.POST("/restore", rh.Trigger)
				}
			}
		}

		evh := handler.NewEventHandler(db, logger)
		root.GET("/api/v1/events", evh.List)
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
