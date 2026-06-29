//go:build integration

package handler_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/testutil"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

func TestDataCenterPage_RendersExpectedElements(t *testing.T) {
	t.Chdir("../..")

	userID := createTestUser(t, "dc-render@test.com", user.RoleAdmin)

	// app-version badge appears on all UI pages; use Export (simple, no extra setup)
	ui := handler.NewUI(
		false, "", "",
		false, false,
		false,
		"", "",
		"",
		testDB,
		slog.Default(),
	)
	ui.SetDGraphURL(testutil.DGraphURL())

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("is_authn", true)
	c.Set("user_id", userID)

	if err := ui.Export(c); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	html := rec.Body.String()
	if !strings.Contains(html, `data-testid="app-version"`) {
		t.Error("expected app-version badge to be present")
	}
	if !strings.Contains(html, "Orbital") {
		t.Error("expected app-version badge to contain 'Orbital'")
	}
}

func TestDataCenterTab_RendersSeededDCData(t *testing.T) {
	t.Chdir("../..")

	// Use the orbId seeded by SeedMinimalE (called in setupExportSuite TestMain)
	h := handler.NewDataCenter(
		testutil.DGraphURL(),
		false,
		slog.Default(),
		"",
		func(echo.Context) layout.PageActions { return layout.OrbitalActions(true) },
	)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/datacenters/"+testDcID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("orbId")
	c.SetParamValues(testDcID)

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	html := rec.Body.String()
	for _, want := range []string{
		"Data Center Summary",
		"Test DC",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected HTML to contain %q", want)
		}
	}
}
