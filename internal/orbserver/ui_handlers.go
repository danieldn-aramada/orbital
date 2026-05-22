package orbserver

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"github.com/armada/orbital/internal/orb"
	appversion "github.com/armada/orbital/internal/version"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

type statusPageData struct {
	layout.Base
	PageTitle        string
	HasData          bool   // true after a successful import — DC identity is known
	DCName           string // name of the imported data center, derived from DGraph
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
	PageTitle string
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
	version := fmt.Sprintf("%d", time.Now().Unix())
	path := c.Request().URL.Path
	return layout.Base{
		Head:        layout.Head{Version: version},
		AppVersion:  appversion.Version,
		BasePath:    "",
		CurrentPath: path,
		UI: layout.UIConfig{
			AppName:      "Orb",
			BasePath:     "",
			Version:      version,
			ShowAuth:     false,
			APIDocPath:   "/swagger/index.html",
			MenuSections: s.buildOrbMenuSections(path),
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
	if err := tmpl.ExecuteTemplate(c.Response().Writer, "base.gohtml", data); err != nil {
		s.logger.Error("template render failed", "name", name, "err", err)
		return err
	}
	return nil
}

func (s *Server) statusPage(c echo.Context) error {
	snap := s.state.snapshot()
	b := s.orbBase(c)
	data := statusPageData{
		Base:             b,
		PageTitle:        "Status",
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
		Base:      s.orbBase(c),
		PageTitle: "Import Subgraph",
	})
}

func (s *Server) inventoryPage(c echo.Context) error {
	return s.render(c, "inventory", inventoryPageData{
		Base:      s.orbBase(c),
		PageTitle: "Config Items",
	})
}

func (s *Server) schemaPage(c echo.Context) error {
	schemaPath := filepath.Join(s.cfg.DataDir, orb.SchemaFile)
	sdl := ""
	if data, err := os.ReadFile(schemaPath); err == nil {
		sdl = string(data)
	}
	return s.render(c, "schema", schemaPageData{
		Base:      s.orbBase(c),
		PageTitle: "Schema",
		SDL:       sdl,
	})
}
