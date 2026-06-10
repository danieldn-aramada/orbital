package orbserver

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/armada/orbital/docs/orb"
	"github.com/armada/orbital/internal/divergence"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/orb"
	"github.com/armada/orbital/internal/orbconfig"
	orbweb "github.com/armada/orbital/web"
	orbtemplates "github.com/armada/orbital/web/templates/orb"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoswagger "github.com/swaggo/echo-swagger"
)

// Server is the orb edge web server.
type Server struct {
	cfg          *orbconfig.Config
	echo         *echo.Echo
	logger       *slog.Logger
	state        *importState
	imp          *orb.Importer
	dispatcher   *orb.Dispatcher        // nil if no consumers configured
	divStore     *divergence.Store
	divPublisher *divergence.Publisher // nil if S3 not configured
	templates    map[string]*template.Template
	webFS        fs.FS // embedded in production; os.DirFS("web") in dev for hot-reload
	devMode      bool
	version      string // stable per-restart; exposed to JS so the client can wipe stale tab state on orb restart
}

// templateMap rebuilds the template map — used in dev mode for hot reload.
func (s *Server) templateMap() map[string]*template.Template {
	return orbtemplates.Map(s.webFS)
}

// New creates an orb Server. All routes are registered here.
func New(cfg *orbconfig.Config) (*Server, error) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))

	// Dev mode reads templates from disk on each request for hot-reload.
	// Production uses the embedded FS baked into the binary.
	var webFS fs.FS
	if cfg.Dev {
		webFS = os.DirFS("web")
	} else {
		webFS = orbweb.FS
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// HTTP access log — attribute names follow OpenTelemetry semantic
	// conventions for HTTP server. See docs/reference/AUDIT.md for the convention
	// document. Kept in sync with orbital's logger in internal/server/server.go.
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		Skipper: func(c echo.Context) bool {
			p := c.Request().URL.Path
			return strings.HasPrefix(p, "/static/") || p == "/favicon.ico"
		},
		LogMethod:    true,
		LogURI:       true,
		LogStatus:    true,
		LogLatency:   true,
		LogRemoteIP:  true,
		LogUserAgent: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			logger.Info("request",
				"http.request.method", v.Method,
				"url.path", v.URI,
				"http.response.status_code", v.Status,
				"client.address", v.RemoteIP,
				"user_agent.original", v.UserAgent,
				"duration_ms", v.Latency.Milliseconds(),
			)
			return nil
		},
	}))

	state := newImportState()

	var backend orb.DGraphBackend
	if cfg.Backend == "k8s" {
		backend = &orb.SubprocessBackend{
			AlphaGRPC: cfg.DGraphAlphaGRPC,
			ZeroGRPC:  cfg.DGraphZeroGRPC,
		}
	} else {
		backend = &orb.DockerBackend{ContainerName: cfg.DGraphContainerName}
	}

	imp := orb.NewImporter(*cfg, logger, backend)
	dispatcher := orb.NewDispatcher(cfg.Consumers)
	divStore := divergence.NewStore(cfg.DataDir)

	var divPublisher *divergence.Publisher
	if cfg.S3Endpoint != "" && cfg.S3Bucket != "" {
		var err error
		divPublisher, err = divergence.NewPublisher(context.Background(), divergence.PublisherConfig{
			Endpoint:  cfg.S3Endpoint,
			Region:    cfg.S3Region,
			Bucket:    cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			OCIRepo:   cfg.OCIRepo,
		})
		if err != nil {
			logger.Warn("divergence S3 publisher init failed — publish disabled", "err", err)
		}
	}

	s := &Server{
		cfg:          cfg,
		echo:         e,
		logger:       logger,
		state:        state,
		imp:          imp,
		dispatcher:   dispatcher,
		divStore:     divStore,
		divPublisher: divPublisher,
		templates:    orbtemplates.Map(webFS),
		webFS:        webFS,
		devMode:      cfg.Dev,
		version:      fmt.Sprintf("%d", time.Now().Unix()),
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

	// Static assets — served from embedded FS in production, live disk in dev.
	staticFS := echo.MustSubFS(webFS, "shared/static")
	e.StaticFS("/static", staticFS)

	// UI pages.
	e.GET("/", s.statusPage)
	e.GET("/status", s.statusPage)
	e.GET("/import", s.importPage)
	e.GET("/inventory", s.inventoryPage)
	e.GET("/schema", s.schemaPage)
	e.GET("/datacenter", s.dcPage)
	e.GET("/datacenters/:id", s.dcTab)
	e.GET("/servers", s.serversPage)
	e.GET("/servers/:id", s.srvTab)
	e.GET("/divergence", s.divergencePage)
	e.GET("/import-history", s.importHistoryPage)

	// API.
	api := e.Group("/api/v1")

	// GraphQL proxy — browser-side DataTables calls go here.
	// Registered at /api/v1/graphql to match the path shared.js expects.
	gql := handler.NewGraphQL(cfg.DGraphURL, nil, logger)
	api.Any("/graphql", gql.Handle)
	api.POST("/import/subgraph", s.importSubgraph)
	api.POST("/import/artifact", s.importArtifact)
	api.GET("/import/status", s.importStatus)
	api.GET("/import/history", s.importHistory)
	if cfg.EnableOCIRegistry {
		api.POST("/import", s.triggerImport)
		api.GET("/import/tags", s.importTags)
	}
	inv := handler.NewInventory(cfg.DGraphURL)
	api.GET("/inventory", inv.List)
	api.POST("/divergence", s.receiveDivergence)
	api.GET("/divergence", s.getDivergence)
	api.POST("/divergence/publish", s.publishDivergence)

	return s, nil
}

// Start begins the polling loop (OCI source only) then starts the HTTP server.
func (s *Server) Start(ctx context.Context) error {
	if s.cfg.EnableOCIRegistry {
		go s.pollLoop(ctx)
	}

	s.logger.Info("starting orb", "port", s.cfg.Port)
	srv := &http.Server{
		Addr:    ":" + s.cfg.Port,
		Handler: s.echo,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		s.logger.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := srv.Shutdown(shutCtx)
		s.logger.Info("shutdown complete")
		return err
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}
}
