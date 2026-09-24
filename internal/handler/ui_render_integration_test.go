//go:build integration

package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// newRenderUI builds a UI handler wired the way server.go wires it, against the
// isolated orbital_test database.
//
// Chdir to the repo root FIRST: webtemplates.Map() parses with CWD-relative
// paths ("web/templates/..."), so a template that moved or was deleted fails
// here rather than at the assertion.
func newRenderUI(t *testing.T) (*UI, *ent.Client) {
	t.Helper()
	t.Chdir("../..")

	db := testutil.NewTestDB(t)
	ui := NewUI(
		false,                // dev
		"http://ratel.test",  // ratelURL
		"http://issues.test", // issueTrackerURL
		false,                // oidcEnabled
		true,                 // backupEnabled
		"object-store.test",  // s3Endpoint
		"orbital-test",       // s3Bucket
		"",                   // basePath
		db,
		slog.Default(),
	)
	ui.SetSchemaPath("schema/schema.graphql")
	ui.SetDGraphURL(testutil.DGraphURL())
	ui.SetDGraphAdminURL(testutil.DGraphAdminURL())
	return ui, db
}

// renderAs calls a UI page handler with an authenticated session.
//
// Authentication is not incidental. Several pages wrap their entire body in
// {{if not .IsAuthn}}<login gate>{{else}}<the page>{{end}}, so an unauthenticated
// render exercises only the gate — it would return 200 with a perfectly intact
// login notice while the real page body was broken. Rendering as a logged-in
// admin is what puts the page content itself under test.
func renderAs(t *testing.T, e *echo.Echo, path string, fn func(echo.Context) error, userID int, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if userID > 0 {
		c.Set("is_authn", true)
		c.Set("user_id", userID)
		c.Set("user_name", "Render Test")
		c.Set("user_email", "render@test.local")
		c.Set("csrf_token", "render-test-csrf")
	}
	if len(params) > 0 {
		names := make([]string, 0, len(params))
		values := make([]string, 0, len(params))
		for k, v := range params {
			names = append(names, k)
			values = append(values, v)
		}
		c.SetParamNames(names...)
		c.SetParamValues(values...)
	}
	if err := fn(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return rec
}

// TestOrbitalPages_AllPathsReturn200 is orbital's counterpart to orb's
// TestOrbPages_AllPathsReturn200, which had no equivalent here until 2026-09-23.
//
// It guards template/render-struct drift across every orbital UI route: a page
// whose template references a field its render struct lacks fails the render,
// and renderHTML turns that into a real error instead of a truncated 200.
// Deleting or renaming a template that a parse set still names fails here too.
//
// The </html> assertion is the anti-truncation half — see
// TestOrbitalPages_CompleteBodyNotTruncated for why a status check alone is not
// enough to catch the failure mode this suite exists for.
func TestOrbitalPages_AllPathsReturn200(t *testing.T) {
	ui, db := newRenderUI(t)
	e := echo.New()

	admin, err := db.User.Create().
		SetEmail("render-admin@test.local").
		SetName("Render Admin").
		SetPreferredUsername("render-admin").
		SetRole(user.RoleAdmin).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create admin user: %v", err)
	}

	pages := []struct {
		path   string
		fn     func(echo.Context) error
		params map[string]string
		wantIn string
	}{
		{"/", ui.Index, nil, "<title>Orbital</title>"},
		{"/inventory", ui.Index, nil, "<title>Orbital</title>"},
		{"/datacenters", ui.DataCenters, nil, "Data Centers"},
		{"/servers", ui.Servers, nil, "Servers"},
		{"/clusters", ui.Clusters, nil, "Clusters"},
		{"/network", ui.NetworkDevices, nil, "Network Devices"},
		{"/backups", ui.Backups, nil, "Backups"},
		{"/divergence-reports", ui.DivergenceReports, nil, "Divergence Reports"},
		{"/audit-log", ui.AuditLog, nil, "Audit Log"},
		{"/change-requests", ui.ChangeRequests, nil, "Change Requests"},
		{"/change-requests/cr-1", ui.ChangeRequestDetail, map[string]string{"id": "cr-1"}, "Change Request"},
		{"/approval-policies", ui.ApprovalPolicies, nil, "Approval Policies"},
		{"/restore", ui.Restore, nil, "Restore Graph"},
		{"/schema", ui.Schema, nil, "Schema"},
		{"/export", ui.Export, nil, "Export Subgraph"},
		{"/publish-history", ui.EdgeDelivery, nil, "Publish History"},
		{"/publish-history/compare", ui.PublishHistoryCompare, nil, "Compare Artifacts"},
		{"/users", ui.Users, nil, "Users"},
	}

	for _, p := range pages {
		t.Run(p.path, func(t *testing.T) {
			rec := renderAs(t, e, p.path, p.fn, admin.ID, p.params)
			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, p.wantIn) {
				t.Errorf("expected body to contain %q", p.wantIn)
			}
			if !strings.Contains(body, "</html>") {
				t.Errorf("body has no closing </html> — the render truncated (%d bytes)", len(body))
			}
		})
	}
}

// TestOrbitalPages_LoginGateWhenUnauthenticated pins the other branch: pages
// that gate their body on .IsAuthn must show the gate, not the content, to a
// caller with no session. Rendering only the authenticated path would let the
// gate rot unnoticed.
func TestOrbitalPages_LoginGateWhenUnauthenticated(t *testing.T) {
	ui, _ := newRenderUI(t)
	e := echo.New()

	gated := []struct {
		path string
		fn   func(echo.Context) error
	}{
		{"/servers", ui.Servers},
		{"/clusters", ui.Clusters},
		{"/datacenters", ui.DataCenters},
	}

	for _, p := range gated {
		t.Run(p.path, func(t *testing.T) {
			rec := renderAs(t, e, p.path, p.fn, 0, nil)
			if rec.Code != http.StatusOK {
				t.Errorf("expected 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `data-target="login-modal"`) {
				t.Error("expected the login gate for an unauthenticated caller")
			}
			if !strings.Contains(body, "</html>") {
				t.Error("body has no closing </html> — the render truncated")
			}
		})
	}
}
