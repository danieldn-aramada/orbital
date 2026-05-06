package handler

import (
	"crypto/sha256"
	"fmt"
	"html/template"
	"os"
	"time"

	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/armada/orbital/internal/web/data/page"
	webtemplates "github.com/armada/orbital/web/templates"
	"github.com/labstack/echo/v4"
)

type UI struct {
	dev       bool
	ratelURL  string
	version   string
	templates map[string]*template.Template
}

func NewUI(dev bool, ratelURL string) *UI {
	return &UI{
		dev:       dev,
		ratelURL:  ratelURL,
		version:   fmt.Sprintf("%d", time.Now().Unix()),
		templates: webtemplates.Map(),
	}
}

func (h *UI) render(c echo.Context, name string, data any) error {
	tmpl, ok := h.templates[name]
	if h.dev {
		tmpl, ok = webtemplates.Map()[name]
	}
	if !ok {
		return echo.ErrNotFound
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(c.Response().Writer, "base.gohtml", data)
}

func (h *UI) base(ratelURL string) layout.Base {
	return layout.Base{
		Head:   layout.Head{Version: h.version},
		NavBar: layout.NavBar{RatelURL: ratelURL},
	}
}

func (h *UI) Index(c echo.Context) error {
	return h.render(c, "home", page.Home{
		Base:      h.base(h.ratelURL),
		PageTitle: "Orbital",
	})
}

func (h *UI) Backups(c echo.Context) error {
	return h.render(c, "backups", page.Backups{
		Base:      h.base(h.ratelURL),
		PageTitle: "Backups",
	})
}

func (h *UI) DivergenceReports(c echo.Context) error {
	return h.render(c, "divergence-reports", page.DivergenceReports{
		Base:      h.base(h.ratelURL),
		PageTitle: "Divergence Reports",
	})
}

func (h *UI) AuditLog(c echo.Context) error {
	return h.render(c, "audit-log", page.AuditLog{
		Base:      h.base(h.ratelURL),
		PageTitle: "Audit Log",
	})
}

func (h *UI) Schema(c echo.Context) error {
	content, err := os.ReadFile("schema/schema-v1.graphql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	sum := sha256.Sum256(content)
	return h.render(c, "schema", page.Schema{
		Base:      h.base(h.ratelURL),
		PageTitle: "Schema",
		Version:   "v1",
		Checksum:  fmt.Sprintf("%x", sum[:6]),
		AppliedAt: "—",
		AppliedBy: "—",
		SDL:       string(content),
	})
}
