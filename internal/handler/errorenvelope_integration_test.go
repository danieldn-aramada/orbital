//go:build integration

package handler_test

import (
	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

// renderErr mirrors what Echo does in production for a handler that signals
// failure by RETURNING an *echo.HTTPError rather than writing a body itself:
// the central handler.ErrorHandler — wired as `e.HTTPErrorHandler` in
// internal/server — renders the JSON envelope.
//
// These tests invoke handlers directly, which bypasses Echo's router and
// therefore never runs HTTPErrorHandler. Without this the recorder stays empty
// and every `rec.Code`/envelope assertion fails, which is exactly how the
// handler integration suite ended up permanently red: the unit tests were
// migrated when handlers moved to returning HTTPError (see the note in
// authz_test.go) and the integration tests were missed.
//
// Use for assertions about the RESPONSE a client sees. If a test means to
// assert on the returned error itself, check it directly instead.
func renderErr(c echo.Context, err error) {
	if err != nil {
		handler.ErrorHandler(err, c)
	}
}
