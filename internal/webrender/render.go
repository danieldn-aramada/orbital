// Package webrender holds the one HTML template-rendering path shared by
// orbital's and orb's HTTP handlers.
//
// It exists as its own package because the helper has to be reachable from two
// server packages that must not import each other: internal/handler (orbital)
// and internal/orbserver (orb). While it lived unexported in internal/handler,
// orb could not call it, so every orb-owned page rendered straight into the
// response — the exact failure this helper prevents, live across orb's whole
// UI, for a structural reason rather than an oversight.
package webrender

import (
	"bytes"
	"fmt"
	"html/template"

	"github.com/labstack/echo/v4"
)

// RenderHTML executes an html/template into a buffer FIRST, then writes the
// result to the response — the canonical Go pattern for serving templates.
//
// html/template streams output as it renders and only returns an error after
// partial output may already be written (see the html/template docs). Executing
// directly into c.Response() therefore commits a 200 + truncated body on any
// mid-render error (e.g. a template referencing a field the render struct lacks).
// Echo's error handler cannot undo a committed response, so the failure is
// silent: truncated HTML, no 500, nothing logged. This exact drift produced a
// "button does nothing" bug (missing modal after a truncated cluster fragment).
//
// Buffering keeps the response uncommitted until render succeeds: on error
// nothing is written and the error propagates to a real 500 that Echo logs; on
// success the full body is written in one shot. Pass name="" to run the root
// template (Execute), or a defined-block name to run ExecuteTemplate. Write
// through c.Response() (not .Writer) so Echo's response Size counter stays
// accurate for the access log.
//
// Callers must NOT set Content-Type themselves — this sets it after a
// successful render, so a failed render leaves the response untouched.
func RenderHTML(c echo.Context, tmpl *template.Template, name string, data any) error {
	var buf bytes.Buffer
	var err error
	if name == "" {
		err = tmpl.Execute(&buf, data)
	} else {
		err = tmpl.ExecuteTemplate(&buf, name, data)
	}
	if err != nil {
		return fmt.Errorf("render template %q: %w", name, err)
	}
	// Lowercase "utf-8" matches the codebase convention (and Go's own
	// http.DetectContentType output) so content-type assertions stay stable.
	c.Response().Header().Set(echo.HeaderContentType, "text/html; charset=utf-8")
	_, err = buf.WriteTo(c.Response())
	return err
}
