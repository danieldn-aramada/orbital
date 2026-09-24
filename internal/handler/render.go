package handler

import (
	"html/template"

	"github.com/armada/orbital/internal/webrender"
	"github.com/labstack/echo/v4"
)

// renderHTML delegates to webrender.RenderHTML, which owns the buffer-then-write
// contract and the reasoning behind it. Kept as a package-local alias so the
// call sites in this package stay unchanged; new code in either server package
// may call webrender.RenderHTML directly.
func renderHTML(c echo.Context, tmpl *template.Template, name string, data any) error {
	return webrender.RenderHTML(c, tmpl, name, data)
}
