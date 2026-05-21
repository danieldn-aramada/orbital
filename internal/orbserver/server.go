package orbserver

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/armada/orbital/docs/orb"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/orb"
	"github.com/armada/orbital/internal/orbconfig"
	orbtemplates "github.com/armada/orbital/web/orb/templates"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoswagger "github.com/swaggo/echo-swagger"
)

// Server is the orb edge web server.
type Server struct {
	cfg       *orbconfig.Config
	echo      *echo.Echo
	logger    *slog.Logger
	state     *importState
	imp       *orb.Importer
	templates map[string]*template.Template
	devMode   bool
}

// templateMap rebuilds the template map from disk — used in dev mode for hot reload.
func (s *Server) templateMap() map[string]*template.Template {
	return orbtemplates.Map()
}

// New creates an orb Server. All routes are registered here.
func New(cfg *orbconfig.Config) *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

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

	state := newImportState()
	imp := orb.NewImporter(*cfg, logger)

	s := &Server{
		cfg:       cfg,
		echo:      e,
		logger:    logger,
		state:     state,
		imp:       imp,
		templates: orbtemplates.Map(),
		devMode:   cfg.LogLevel == "debug",
	}

	// Seed currentVersion from history on startup.
	if history, err := orb.LoadHistory(cfg.DataDir); err == nil && len(history) > 0 {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Status == "done" {
				state.currentVersion = history[i].Tag
				break
			}
		}
		if state.currentVersion != "" {
			logger.Info("restored current version from history", "version", state.currentVersion)
		}
	}

	// Swagger UI.
	e.GET("/swagger/*", echoswagger.WrapHandler)

	// GraphQL proxy — browser-side DataTables calls go here.
	gql := handler.NewGraphQL(cfg.DGraphURL, nil, logger)
	e.Any("/graphql", gql.Handle)

	// Static assets.
	e.Static("/static", "web/shared/static")

	// UI pages.
	e.GET("/", s.statusPage)
	e.GET("/status", s.statusPage)
	e.GET("/import", s.importPage)
	e.GET("/datacenter", s.dcPage)
	e.GET("/datacenters/:id", s.dcTab)
	e.GET("/servers", s.serversPage)
	e.GET("/servers/:id", s.srvTab)
	e.GET("/divergence", s.divergencePage)
	e.GET("/import-history", s.importHistoryPage)

	// API.
	api := e.Group("/api/v1")
	api.POST("/import", s.triggerImport)
	api.POST("/import/upload", s.uploadImport)
	api.GET("/import/status", s.importStatus)
	api.GET("/import/tags", s.importTags)
	api.GET("/import/history", s.importHistory)
	api.POST("/overrides", s.postOverride)
	api.GET("/overrides", s.getOverrides)
	api.POST("/divergence/publish", s.postPublishReport)

	return s
}

// Start begins the polling loop then starts the HTTP server.
func (s *Server) Start(ctx context.Context) error {
	go s.pollLoop(ctx)

	s.logger.Info("starting orb", "port", s.cfg.Port, "dc_slug", s.cfg.DCSlug)
	srv := &http.Server{
		Addr:    ":" + s.cfg.Port,
		Handler: s.echo,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}
}
