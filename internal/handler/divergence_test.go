package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

// These unit tests cover the parameter-validation branches of the divergence
// handlers — the only paths that don't require a real DB. Behavior tests live
// in the integration suite (`divergence_integration_test.go`, behind the
// `integration` build tag).

func newCtx(method, path string, body string, idParam string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if idParam != "" {
		c.SetParamNames("id")
		c.SetParamValues(idParam)
	}
	return c, rec
}

func TestPutResolution_InvalidID(t *testing.T) {
	h := handler.NewDivergenceHandler(nil, nil, nil)
	c, _ := newCtx(http.MethodPut, "/api/v1/divergences/notanid/resolution", `{"action":"accept"}`, "notanid")
	err := h.PutResolution(c)
	if echoErr, ok := err.(*echo.HTTPError); !ok || echoErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %v", err)
	}
}

func TestPutResolution_InvalidAction(t *testing.T) {
	h := handler.NewDivergenceHandler(nil, nil, nil)
	c, _ := newCtx(http.MethodPut, "/api/v1/divergences/00000000-0000-0000-0000-000000000000/resolution", `{"action":"explode"}`, "00000000-0000-0000-0000-000000000000")
	err := h.PutResolution(c)
	if echoErr, ok := err.(*echo.HTTPError); !ok || echoErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %v", err)
	}
}

func TestPutResolution_MalformedBody(t *testing.T) {
	h := handler.NewDivergenceHandler(nil, nil, nil)
	c, _ := newCtx(http.MethodPut, "/api/v1/divergences/00000000-0000-0000-0000-000000000000/resolution", `{not-json`, "00000000-0000-0000-0000-000000000000")
	err := h.PutResolution(c)
	if echoErr, ok := err.(*echo.HTTPError); !ok || echoErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %v", err)
	}
}

func TestDeleteResolution_InvalidID(t *testing.T) {
	h := handler.NewDivergenceHandler(nil, nil, nil)
	c, _ := newCtx(http.MethodDelete, "/api/v1/divergences/bad/resolution", "", "bad")
	err := h.DeleteResolution(c)
	if echoErr, ok := err.(*echo.HTTPError); !ok || echoErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %v", err)
	}
}

func TestGet_InvalidID(t *testing.T) {
	h := handler.NewDivergenceHandler(nil, nil, nil)
	c, _ := newCtx(http.MethodGet, "/api/v1/divergences/bad", "", "bad")
	err := h.Get(c)
	if echoErr, ok := err.(*echo.HTTPError); !ok || echoErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %v", err)
	}
}
