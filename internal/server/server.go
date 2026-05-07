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
	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/metrics"
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

	ui := handler.NewUI(cfg.Dev, cfg.RatelURL, cfg.IssueTrackerURL)
	e.GET("/", ui.Index)
	e.GET("/datacenters", ui.Index)
	e.GET("/backups", ui.Backups)
	e.GET("/divergence-reports", ui.DivergenceReports)
	e.GET("/audit-log", ui.AuditLog)
	e.GET("/schema", ui.Schema)
	e.GET("/export", ui.Export)

	dc := handler.NewDataCenter(cfg.DGraphURL, cfg.Dev, logger)
	e.GET("/datacenters/:id", dc.Tab)

	if db != nil {
		exp := handler.NewExport(db, cfg.DGraphURL, cfg.DGraphScratchURL, cfg.DGraphScratchAdminURL, cfg.ExportDir, cfg.DGraphScratchExportDir, cfg.SchemaPath, logger)
		e.POST("/api/v1/datacenters/:id/export", exp.Trigger)
		e.GET("/api/v1/export/jobs", exp.List)
		e.GET("/api/v1/export/jobs/:jobId", exp.Status)
		e.GET("/api/v1/export/jobs/:jobId/download", exp.Download)
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

	s.logger.Info("starting orb", "port", s.cfg.Port, "dgraph", s.cfg.DGraphURL)

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
		s.logger.Info("orb ready", "addr", ":"+s.cfg.Port)
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
