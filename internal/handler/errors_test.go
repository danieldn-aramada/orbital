package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestErrorHandler pins the central error handler's contract: every error becomes
// an errorResponse, the status→code default table, the httpStatus/status-line
// mirror invariant, and — the security-sensitive one — that a raw (unexpected)
// error is genericized to 500 INTERNAL without leaking its message.
func TestErrorHandler(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		method     string
		wantStatus int
		wantCode   string
		wantMsg    string // exact `error` field; "" = skip
		noLeak     string // substring that must NOT appear in the body; "" = skip
	}{
		{"forbidden", echo.NewHTTPError(http.StatusForbidden, "nope"), http.MethodPost, 403, CodeForbidden, "nope", ""},
		{"not found", echo.NewHTTPError(http.StatusNotFound, "missing"), http.MethodGet, 404, CodeNotFound, "missing", ""},
		{"conflict default code", echo.NewHTTPError(http.StatusConflict, "busy"), http.MethodPost, 409, CodeConflict, "busy", ""},
		{"service unavailable", echo.NewHTTPError(http.StatusServiceUnavailable, "down"), http.MethodPost, 503, CodeUnavailable, "down", ""},
		{"bad gateway → unavailable", echo.NewHTTPError(http.StatusBadGateway, "upstream"), http.MethodPost, 502, CodeUnavailable, "upstream", ""},
		{"unauthorized", echo.NewHTTPError(http.StatusUnauthorized, "who"), http.MethodPost, 401, CodeUnauthenticated, "who", ""},
		{"bad request default", echo.NewHTTPError(http.StatusBadRequest, "bad"), http.MethodPost, 400, CodeBadUserInput, "bad", ""},
		{"content too large", echo.NewHTTPError(http.StatusRequestEntityTooLarge, "Request Entity Too Large"), http.MethodPost, 413, CodeContentTooLarge, "Request Entity Too Large", ""},
		{"too many requests", echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded"), http.MethodPost, 429, CodeRateLimited, "rate limit exceeded", ""},
		{"echo sentinel", echo.ErrNotFound, http.MethodGet, 404, CodeNotFound, "", ""},
		{"raw error genericized, no leak", errors.New("secret dsn user:pw@db leaked"), http.MethodPost, 500, CodeInternal, "", "secret dsn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(tt.method, "/x", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			ErrorHandler(tt.err, c)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status line: got %d, want %d", rec.Code, tt.wantStatus)
			}
			var body errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not errorResponse JSON: %v — %s", err, rec.Body.String())
			}
			if body.Code != tt.wantCode {
				t.Errorf("code: got %q, want %q", body.Code, tt.wantCode)
			}
			if body.HTTPStatus != tt.wantStatus {
				t.Errorf("httpStatus body field: got %d, want %d (must mirror the status line)", body.HTTPStatus, tt.wantStatus)
			}
			if tt.wantMsg != "" && body.Error != tt.wantMsg {
				t.Errorf("error: got %q, want %q", body.Error, tt.wantMsg)
			}
			if tt.noLeak != "" && strings.Contains(rec.Body.String(), tt.noLeak) {
				t.Errorf("body leaked internal detail %q: %s", tt.noLeak, rec.Body.String())
			}
		})
	}
}

// TestErrorHandler_HeadHasNoBody verifies a HEAD request gets the status but no
// body (RFC 7231), matching Echo's default handler behavior.
func TestErrorHandler_HeadHasNoBody(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodHead, "/x", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	ErrorHandler(echo.NewHTTPError(http.StatusNotFound, "missing"), c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD response must have an empty body, got %q", rec.Body.String())
	}
}
