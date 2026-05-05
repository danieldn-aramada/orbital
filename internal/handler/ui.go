package handler

import (
	"html/template"

	"github.com/armada/orbital/internal/web/data/page"
	webtemplates "github.com/armada/orbital/web/templates"
	"github.com/labstack/echo/v4"
)

type UI struct {
	dev       bool
	templates map[string]*template.Template
}

func NewUI(dev bool) *UI {
	return &UI{
		dev:       dev,
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

func (h *UI) Index(c echo.Context) error {
	return h.render(c, "home", page.Home{PageTitle: "Orbital"})
}
