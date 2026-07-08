package orbserver

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	_ "github.com/armada/orbital/docs/orb"
	"github.com/armada/orbital/internal/divergence"
	"github.com/armada/orbital/internal/handler"
	orbmw "github.com/armada/orbital/internal/middleware"
	"github.com/armada/orbital/internal/orb"
	"github.com/armada/orbital/internal/orb/store"
	"github.com/armada/orbital/internal/orbconfig"
	"github.com/armada/orbital/internal/web/data/layout"
	orbweb "github.com/armada/orbital/web"
	orbtemplates "github.com/armada/orbital/web/templates/orb"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	echoswagger "github.com/swaggo/echo-swagger"
	"path/filepath"
)

// Server is the orb edge web server.
type Server struct {
	cfg          *orbconfig.Config
	echo         *echo.Echo
	logger       *slog.Logger
	state        *importState
	imp          *orb.Importer
	dispatcher   *orb.Dispatcher        // nil if no consumers configured
	db           *store.Client
	divStore     *divergence.Store
	divPublisher *divergence.Publisher // nil if S3 not configured
	templates    map[string]*template.Template
	webFS        fs.FS // embedded in production; os.DirFS("web") in dev for hot-reload
	devMode      bool
	version      string // stable per-restart; exposed to JS so the client can wipe stale tab state on orb restart

	// verifyCache memoizes oci.Verify results by digest for the Import Subgraph
	// tags table. Digests are content-addressed, so a hit is mathematically
	// still-valid within a pod lifetime (cosign key changes require restart).
	// Cleared on process restart (in-memory only) and on the Refresh button
	// via ?refresh=1. Values are bool (verified/not).
	verifyCache sync.Map
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

	e.Use(orbmw.DecodePathParams)

	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// RequestID must precede AccessLog so the middleware has an ID to log.
	// Honors inbound X-Request-ID header; generates one when absent.
	e.Use(echomw.RequestID())

	// HTTP access log — see docs/reference/AUDIT.md for attribute conventions.
	// Orb has no auth, so no ActorFromContext.
	e.Use(orbmw.AccessLog(orbmw.AccessLogConfig{
		Logger:         logger,
		SkipPrefixes:   []string{"/static/"},
		SkipExactPaths: []string{"/favicon.ico", "/healthz"},
	}))

	// SQLite store — required for import history + divergence persistence.
	// Fail-fast on error.
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %q: %w", cfg.DataDir, err)
	}
	dbPath := filepath.Join(cfg.DataDir, "orb.db")
	db, err := store.New(context.Background(), dbPath)
	if err != nil {
		return nil, fmt.Errorf("open orb store: %w", err)
	}
	logger.Info("orb store opened", "path", dbPath)

	state := newImportState()
	if records, err := orb.LoadHistory(context.Background(), db); err != nil {
		logger.Warn("load import history at startup failed — status page will show no last import", "err", err)
	} else {
		state.hydrateFromHistory(records)
	}

	backend := &orb.SubprocessBackend{
		AlphaGRPC: cfg.DGraphAlphaGRPC,
		ZeroGRPC:  cfg.DGraphZeroGRPC,
	}

	imp := orb.NewImporter(*cfg, logger, backend, db)
	dispatcher := orb.NewDispatcher(cfg.Consumers)
	divStore := divergence.NewStore(db)

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
		db:           db,
		divStore:     divStore,
		divPublisher: divPublisher,
		templates:    orbtemplates.Map(webFS),
		webFS:        webFS,
		devMode:      cfg.Dev,
		version:      fmt.Sprintf("%d", time.Now().Unix()),
	}

	// Seed currentVersion from history on startup.
	if history, err := orb.LoadHistory(context.Background(), db); err == nil && len(history) > 0 {
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
	dc := handler.NewDataCenter(cfg.DGraphURL, cfg.Dev, logger, "",
		func(echo.Context) layout.PageActions { return layout.OrbActions })
	e.GET("/datacenters/:orbId", dc.Tab)
	e.GET("/servers", s.serversPage)
	srv := handler.NewServerHandler(cfg.DGraphURL, cfg.Dev, logger, "",
		func(echo.Context) layout.PageActions { return layout.OrbActions })
	e.GET("/servers/:orbId", srv.Tab)
	e.GET("/clusters", s.clustersPage)
	// Reuse orbital's ClusterHandler — the same DGraph query + render path,
	// with orb-specific PageActions injected (read-only, no audit tab). This
	// is the model for collapsing the rest of the DC/Server parallel impls.
	cluster := handler.NewClusterHandler(cfg.DGraphURL, cfg.Dev, logger, "",
		func(echo.Context) layout.PageActions { return layout.OrbActions })
	e.GET("/clusters/:orbId", cluster.Tab)
	e.GET("/divergence", s.divergencePage)
	e.GET("/publish-history", s.publishHistoryPage)
	e.GET("/import-history", s.importHistoryPage)

	// GraphQL proxy — browser-side DataTables calls go here.
	// Registered at /graphql (not /api/v1/graphql) — GraphQL is not URL-versioned,
	// per convention (GitHub, GitLab, NetBox, Apollo). See CLAUDE.md.
	gql := handler.NewGraphQL(cfg.DGraphURL, nil, logger)
	e.Any("/graphql", gql.Handle)

	// API.
	api := e.Group("/api/v1")
	api.POST("/import/subgraph", s.importSubgraph)
	api.POST("/import/artifact", s.importArtifact)
	api.GET("/import/status", s.importStatus)
	api.GET("/import/history", s.importHistory)
	api.GET("/import/history/:tag/layers", s.importHistoryLayers)
	if cfg.EnableOCIRegistry {
		api.POST("/import", s.triggerImport)
		api.GET("/import/tags", s.importTags)
	}
	api.POST("/divergence", s.receiveDivergence)
	api.GET("/divergence", s.getDivergence)
	api.POST("/divergence/publish", s.publishDivergence)
	api.GET("/divergence/publish-history", s.publishHistory)
	api.POST("/divergence/test-connection", s.testDivergenceConnection)

	return s, nil
}

// Start begins the polling loop (OCI source only) then starts the HTTP server.
func (s *Server) Start(ctx context.Context) error {
	// Log configured consumer URLs for operator visibility. We deliberately do
	// NOT probe them — orb's core (intent serving, divergence intake) does not
	// depend on consumer reachability at boot, and probing creates a startup
	// race. Misconfigured URLs surface at dispatch time with the failing URL
	// in the response.
	if len(s.cfg.Consumers) == 0 {
		s.logger.Info("ORB_CONSUMERS not configured — imported bundler layers will not be dispatched")
	} else {
		names := make([]string, 0, len(s.cfg.Consumers))
		for _, c := range s.cfg.Consumers {
			names = append(names, fmt.Sprintf("%s=%s", c.Name, c.URL))
		}
		s.logger.Info("ORB_CONSUMERS configured", "consumers", names)
	}

	if s.cfg.EnableOCIRegistry {
		go s.pollLoop(ctx)
	}

	if s.divPublisher != nil && s.cfg.DivergencePublishSchedule != "" {
		sched := NewDivergenceScheduler(s.divStore, s.divPublisher, s.cfg.DivergencePublishSchedule, s.logger)
		go sched.Start(ctx)
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
