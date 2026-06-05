package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/handler"
	"github.com/labstack/echo/v4"
)

func TestUpdateRole_InvalidID(t *testing.T) {
	h := handler.NewUsersHandler(nil, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/notanid/role",
		strings.NewReader(`{"role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("notanid")

	err := h.UpdateRole(c)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 HTTPError, got: %v", err)
	}
}

func TestUpdateRole_InvalidRole(t *testing.T) {
	h := handler.NewUsersHandler(nil, nil)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/1/role",
		strings.NewReader(`{"role":"superuser"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("1")

	err := h.UpdateRole(c)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 HTTPError, got: %v", err)
	}
}

func TestUpdateRole_AllRolesAccepted(t *testing.T) {
	for _, role := range []string{"readonly", "dev", "admin"} {
		h := handler.NewUsersHandler(nil, nil)
		e := echo.New()

		req := httptest.NewRequest(http.MethodPut, "/api/v1/users/1/role",
			strings.NewReader(`{"role":"`+role+`"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("1")

		err := h.UpdateRole(c)
		// nil db — passes role validation, then fails because db is nil (can't fetch user)
		// The important thing: it must NOT return a bad-request error for valid role names.
		if err != nil {
			httpErr, ok := err.(*echo.HTTPError)
			if ok && httpErr.Code == http.StatusBadRequest {
				t.Errorf("role=%q: got unexpected 400, role should be valid", role)
			}
		}
	}
}
