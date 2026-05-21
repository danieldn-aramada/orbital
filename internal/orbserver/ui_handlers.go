package orbserver

import (
	"fmt"
	"html/template"
	"time"

	appversion "github.com/armada/orbital/internal/version"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

type statusPageData struct {
	layout.Base
	PageTitle        string
	DCSlug           string
	OCIRegistry      string
	OCIRepo          string
	CurrentVersion   string
	AvailableVersion string
	HasLastImport    bool
	LastImportAt     time.Time
}

type importPageData struct {
	layout.Base
	PageTitle string
}

func (s *Server) orbBase(c echo.Context) layout.Base {
	version := fmt.Sprintf("%d", time.Now().Unix())
	return layout.Base{
		Head:        layout.Head{Version: version},
		AppVersion:  appversion.Version,
		BasePath:    "",
		CurrentPath: c.Request().URL.Path,
		UI: layout.UIConfig{
			AppName:    "Orb",
			BasePath:   "",
			Version:    version,
			EditMode:   "intent",
			ShowAuth:   false,
			APIDocPath: "/swagger/index.html",
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
		DCSlug:           s.cfg.DCSlug,
		OCIRegistry:      s.cfg.OCIRegistry,
		OCIRepo:          s.cfg.OCIRepo,
		CurrentVersion:   snap.CurrentVersion,
		AvailableVersion: snap.AvailableVersion,
	}
	if snap.LastImport != nil {
		data.HasLastImport = true
		data.LastImportAt = snap.LastImport.ImportedAt
	}
	return s.render(c, "status", data)
}

func (s *Server) importPage(c echo.Context) error {
	return s.render(c, "import", importPageData{
		Base:      s.orbBase(c),
		PageTitle: "Import Subgraph",
	})
}
