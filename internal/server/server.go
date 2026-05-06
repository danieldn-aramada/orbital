package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/armada/orbital/internal/config"
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoswagger "github.com/swaggo/echo-swagger"
)

type Server struct {
	cfg    *config.Config
	echo   *echo.Echo
	logger *slog.Logger
}

func New(cfg *config.Config) *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Static("/static", "web/static")

	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogMethod:  true,
		LogURI:     true,
		LogStatus:  true,
		LogLatency: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			logger.Info("request", // logger captured from New() scope
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
				"latency_ms", v.Latency.Milliseconds(),
			)
			return nil
		},
	}))

	ui := handler.NewUI(cfg.Dev, cfg.RatelURL)
	e.GET("/", ui.Index)
	e.GET("/datacenters", ui.Index)
	e.GET("/backups", ui.Backups)
	e.GET("/divergence-reports", ui.DivergenceReports)
	e.GET("/audit-log", ui.AuditLog)
	e.GET("/schema", ui.Schema)

	dc := handler.NewDataCenter(cfg.DGraphURL, cfg.Dev, logger)
	e.GET("/datacenters/:id", dc.Tab)

	exp := handler.NewExport(cfg.DGraphAdminURL)
	e.POST("/api/v1/export", exp.Trigger)

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
