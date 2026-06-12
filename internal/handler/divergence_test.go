package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

func TestDivergenceAccept_InvalidID(t *testing.T) {
	h := handler.NewDivergenceHandler(nil, nil, nil)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence/notanid/accept", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("notanid")

	if err := h.Accept(c); err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	} else if echoErr, ok := err.(*echo.HTTPError); !ok || echoErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %v", err)
	}
}

func TestDivergenceForce_InvalidID(t *testing.T) {
	h := handler.NewDivergenceHandler(nil, nil, nil)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence/abc/force", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("abc")

	err := h.Force(c)
	if err == nil {
		t.Fatal("expected error for invalid UUID")
	}
	if echoErr, ok := err.(*echo.HTTPError); !ok || echoErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %v", err)
	}
}

func TestDivergenceMarkConsumed_InvalidID(t *testing.T) {
	h := handler.NewDivergenceHandler(nil, nil, nil)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/divergence/resolutions/bad/consumed", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("bad")

	err := h.MarkConsumed(c)
	if echoErr, ok := err.(*echo.HTTPError); !ok || echoErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %v", err)
	}
}
