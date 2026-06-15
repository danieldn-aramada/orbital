package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestDivergenceReports_HX_ReturnsFragment ensures the Refresh button's AJAX
// pattern works: when the handler sees HX-Request: true, the response must be
// just the {{define "divergence-content"}} block — no <html>, no <main>, no
// surrounding layout chrome. This is what the JS swaps into #divergence-content.
func TestDivergenceReports_HX_ReturnsFragment(t *testing.T) {
	t.Chdir("../..")

	h := &UI{dev: true, basePath: ""}
	c, rec := newUIEchoCtx(http.MethodGet, "/divergence-reports")
	c.Request().Header.Set("HX-Request", "true")

	if err := h.DivergenceReports(c); err != nil {
		t.Fatalf("DivergenceReports: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="divergence-batch-form"`) {
		t.Errorf("expected form in HX fragment; body:\n%s", body)
	}
	for _, mustNotContain := range []string{"<html", "<main", `id="divergence-content"`} {
		if strings.Contains(body, mustNotContain) {
			t.Errorf("HX fragment must not contain %q; body:\n%s", mustNotContain, body)
		}
	}
}

// TestDivergenceReports_FullPage_ReturnsLayout verifies the non-HX path still
// renders the full page with surrounding layout — so the user can hit the URL
// directly in a browser and get a usable page.
func TestDivergenceReports_FullPage_ReturnsLayout(t *testing.T) {
	t.Chdir("../..")

	h := &UI{dev: true, basePath: ""}
	c, rec := newUIEchoCtx(http.MethodGet, "/divergence-reports")
	// The page hides everything behind {{if not .IsAuthn}}; mark the context
	// authenticated so the table block renders.
	c.Set("is_authn", true)
	c.Set("user_id", 1)

	if err := h.DivergenceReports(c); err != nil {
		t.Fatalf("DivergenceReports: %v", err)
	}
	body := rec.Body.String()
	for _, want := range []string{"<html", `id="divergence-content"`, `id="btn-refresh-divergence"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected full page to contain %q; body excerpt:\n%s", want, body[:min(400, len(body))])
		}
	}
}

func newUIEchoCtx(method, path string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}
