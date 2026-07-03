package orbserver

import (
	"encoding/json"
	"html/template"
	"time"

	"github.com/armada/orbital/internal/dgraphschema"
	appversion "github.com/armada/orbital/internal/version"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

type statusPageData struct {
	layout.Base
	PageTitle        string
	HasData          bool   // true after a successful import — DC identity is known
	DCName           string // name of the imported data center, derived from DGraph
	OCIEnabled       bool   // ORB_ENABLE_OCI_REGISTRY — gates pull-mode messaging on empty state
	OCIRegistry      string
	OCIRepo          string
	CurrentVersion   string
	AvailableVersion string
	HasLastImport    bool
	LastImportAt     time.Time
}

const queryActiveDC = `{ queryDataCenter { name } }`

type importPageData struct {
	layout.Base
	PageTitle   string
	OCIEnabled  bool
	OCIRegistry string // host of the registry orb polls (e.g. "zot.local:5000")
	OCIRepo     string // repo path orb polls (e.g. "orbital/colo-galleon")
}

type inventoryPageData struct {
	layout.Base
	PageTitle string
}

type schemaPageData struct {
	layout.Base
	PageTitle string
	SDL       string
}

func (s *Server) orbBase(c echo.Context) layout.Base {
	path := c.Request().URL.Path
	return layout.Base{
		Head:        layout.Head{Version: s.version},
		AppVersion:  appversion.Version,
		BasePath:    "",
		CurrentPath: path,
		UI: layout.UIConfig{
			AppName:      "Orb",
			BasePath:     "",
			Version:      s.version,
			ShowAuth:     false,
			APIDocPath:      "/swagger/index.html",
			GraphQLPath:     "/graphql",
			AuditPanelLimit: layout.AuditPanelDefaultLimit,
			MenuSections:    s.buildOrbMenuSections(path),
		},
	}
}

func (s *Server) buildOrbMenuSections(path string) []layout.MenuSection {
	return []layout.MenuSection{
		{
			Title: "Orb",
			Icon:  "fa-solid fa-satellite-dish",
			Color: "has-text-info",
			Items: []layout.MenuItem{
				{Label: "Status", Href: "/", Active: path == "/" || path == "/status"},
			},
		},
		{
			Title: "Config Items",
			Icon:  "fa-solid fa-diagram-project",
			Color: "has-text-primary",
			Items: []layout.MenuItem{
				{Label: "Inventory", Href: "/inventory", Active: path == "/inventory"},
				{Label: "Data Center", Href: "/datacenter", Active: path == "/datacenter"},
				{Label: "Servers", Href: "/servers", Active: path == "/servers"},
				{Label: "Clusters", Href: "/clusters", Active: path == "/clusters"},
				{Label: "Schema Version", Href: "/schema", Active: path == "/schema"},
			},
		},
		{
			Title: "Sync",
			Icon:  "fa-solid fa-download",
			Color: "has-text-warning",
			Items: []layout.MenuItem{
				{Label: "Import Subgraph", Href: "/import", Active: path == "/import"},
				{Label: "Import History", Href: "/import-history", Active: path == "/import-history"},
			},
		},
		{
			Title: "Divergence",
			Icon:  "fa-solid fa-code-branch",
			Color: "has-text-danger",
			Items: []layout.MenuItem{
				{Label: "Divergence Report", Href: "/divergence", Active: path == "/divergence"},
			},
		},
	}
}

func (s *Server) render(c echo.Context, name string, data any) error {
	var tmpl *template.Template
	if s.devMode {
		tmpl = s.templateMap()[name]
	} else {
		tmpl = s.templates[name]
	}
	if tmpl == nil {
		return echo.ErrNotFound
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(c.Response(), "base.gohtml", data); err != nil {
		s.logger.Error("template render failed", "name", name, "err", err)
		return err
	}
	return nil
}

// renderFragment executes a single named {{define}} block within a page's
// parse set. Used for HX-Request swaps where the caller wants one chunk of
// the page back, not the full layout. Mirrors handler.UI.renderFragment.
func (s *Server) renderFragment(c echo.Context, page, fragment string, data any) error {
	var tmpl *template.Template
	if s.devMode {
		tmpl = s.templateMap()[page]
	} else {
		tmpl = s.templates[page]
	}
	if tmpl == nil {
		return echo.ErrNotFound
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(c.Response(), fragment, data)
}

func (s *Server) statusPage(c echo.Context) error {
	snap := s.state.snapshot()
	b := s.orbBase(c)
	data := statusPageData{
		Base:             b,
		PageTitle:        "Status",
		OCIEnabled:       s.cfg.EnableOCIRegistry,
		OCIRegistry:      s.cfg.OCIRegistry,
		OCIRepo:          s.cfg.OCIRepo,
		CurrentVersion:   snap.CurrentVersion,
		AvailableVersion: snap.AvailableVersion,
	}
	if snap.LastImport != nil {
		data.HasLastImport = true
		data.LastImportAt = snap.LastImport.ImportedAt
	}
	// Derive DC identity from the imported graph. After a successful import there
	// is exactly one DataCenter node (import is sudo: drop_all + full reload).
	if raw, err := s.dgraphQuery(queryActiveDC, nil); err == nil {
		var result struct {
			Data struct {
				QueryDataCenter []struct {
					Name string `json:"name"`
				} `json:"queryDataCenter"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &result) == nil && len(result.Data.QueryDataCenter) > 0 {
			data.DCName = result.Data.QueryDataCenter[0].Name
			data.HasData = true
		}
	}
	return s.render(c, "status", data)
}

func (s *Server) importPage(c echo.Context) error {
	return s.render(c, "import", importPageData{
		Base:        s.orbBase(c),
		PageTitle:   "Import Subgraph",
		OCIEnabled:  s.cfg.EnableOCIRegistry,
		OCIRegistry: s.cfg.OCIRegistry,
		OCIRepo:     s.cfg.OCIRepo,
	})
}

func (s *Server) inventoryPage(c echo.Context) error {
	return s.render(c, "inventory", inventoryPageData{
		Base:      s.orbBase(c),
		PageTitle: "Config Items",
	})
}

type clustersPageData struct {
	layout.Base
	PageTitle string
}

func (s *Server) clustersPage(c echo.Context) error {
	return s.render(c, "clusters", clustersPageData{
		Base:      s.orbBase(c),
		PageTitle: "Clusters",
	})
}

// schemaPage queries orb's local DGraph for the active GraphQL schema and
// renders it. Single source of truth — if DGraph was wiped, the page
// correctly shows "no schema" instead of a stale sidecar copy.
func (s *Server) schemaPage(c echo.Context) error {
	sdl, _ := dgraphschema.Active(c.Request().Context(), s.cfg.DGraphAdminURL)
	// Errors are non-fatal — empty SDL renders as the "Awaiting import" state,
	// which is the right UX whether DGraph is unreachable or simply empty.
	return s.render(c, "schema", schemaPageData{
		Base:      s.orbBase(c),
		PageTitle: "Schema",
		SDL:       sdl,
	})
}
