package handler

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
)

// errorResponse is the standard body for orbital-authored (non-DGraph) error
// responses. Swagger-documented — external services consume it, so the field
// names are a contract. See docs/reference/ERROR-RESPONSES.md.
type errorResponse struct {
	Error      string `json:"error"          example:"dev or admin role required for mutations"`
	Code       string `json:"code"           example:"FORBIDDEN"`
	HTTPStatus int    `json:"httpStatus"     example:"403"`
	Hint       string `json:"hint,omitempty" example:"Ask an admin to grant you the dev role."`
	DocURL     string `json:"docUrl,omitempty"`
}

// Stable machine-readable error codes. UPPER_SNAKE, unique, and stable once
// shipped — clients branch on these, never on the human message. Registry and
// semantics live in docs/reference/ERROR-RESPONSES.md; add there when adding one.
const (
	CodeUnauthenticated      = "UNAUTHENTICATED"
	CodeForbidden            = "FORBIDDEN"
	CodeNotFound             = "NOT_FOUND"
	CodeBadUserInput         = "BAD_USER_INPUT"
	CodeVariableFormRequired = "VARIABLE_FORM_REQUIRED"
	CodeContentTooLarge      = "CONTENT_TOO_LARGE"
	CodeRateLimited          = "RATE_LIMITED"
	CodeConflict             = "CONFLICT"
	CodeMVCCConflict         = "MVCC_CONFLICT"
	CodeUnavailable          = "UNAVAILABLE"
	CodeInternal             = "INTERNAL"
)

// writeError writes the errorResponse envelope directly. Use it where a handler
// writes its own response instead of returning an error to ErrorHandler, or where
// it needs a specific code/hint — notably the shared GraphQL proxy guards, which
// run on BOTH orbital and orb and so must not depend on orbital's ErrorHandler
// being registered. The status line and the httpStatus body field come from the
// same argument and cannot drift.
func writeError(c echo.Context, status int, code, msg, hint string) error {
	return c.JSON(status, errorResponse{Error: msg, Code: code, HTTPStatus: status, Hint: hint})
}

// ErrorHandler is orbital's echo.HTTPErrorHandler. It renders every returned error
// as the errorResponse envelope so orbital speaks one error shape instead of
// Echo's default {"message": ...}. Registered on the Echo instance in
// internal/server. DGraph-native errors[] never reach here: the GraphQL proxy
// writes those through directly and returns nil.
func ErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	status := http.StatusInternalServerError
	code := CodeInternal
	var msg string

	switch e := err.(type) {
	case *echo.HTTPError:
		status = e.Code
		code = codeForStatus(status)
		if m, ok := e.Message.(string); ok {
			msg = m
		}
	default:
		// Unexpected/raw error: never leak internals to the caller. Log the real
		// error against the request id and return a generic 500.
		slog.Default().Error("unhandled request error",
			"request.id", c.Response().Header().Get(echo.HeaderXRequestID),
			"path", c.Request().URL.Path,
			"err", err,
		)
	}

	if msg == "" {
		msg = http.StatusText(status)
	}

	var writeErr error
	if c.Request().Method == http.MethodHead {
		writeErr = c.NoContent(status)
	} else {
		writeErr = c.JSON(status, errorResponse{Error: msg, Code: code, HTTPStatus: status})
	}
	if writeErr != nil {
		slog.Default().Error("error handler failed to write response",
			"request.id", c.Response().Header().Get(echo.HeaderXRequestID), "err", writeErr)
	}
}

// codeForStatus maps an HTTP status to the default machine code for errors that
// arrive without an explicit one — bare echo.HTTPError from handlers and Echo's
// own routing/binding errors (404, 405, 400). Specific codes (e.g. MVCC_CONFLICT)
// are set explicitly at the raising site via writeError.
func codeForStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return CodeUnauthenticated
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeConflict
	case http.StatusRequestEntityTooLarge:
		return CodeContentTooLarge
	case http.StatusTooManyRequests:
		return CodeRateLimited
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return CodeUnavailable
	}
	if status >= 500 {
		return CodeInternal
	}
	return CodeBadUserInput
}
