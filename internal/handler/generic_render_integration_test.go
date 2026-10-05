//go:build integration

package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// The generic renderer: acceptance items 5-9.
//
// The goal is that defining a type in DGraph yields /{slug} and /{slug}/{id}
// with no code, no template and no configuration. These tests therefore use
// types that have NO bespoke page — Rack, StorageDevice, IPAddress — because a
// type with a hand-written page proves nothing about a generic one.

func newGenericUI(t *testing.T) (*UI, int) {
	t.Helper()
	ui, db := newRenderUI(t)
	ui.fields = NewSharedFieldSource(testutil.DGraphURL(), ViewsSource{Path: testutil.ViewsPath()}, slog.Default())

	admin, err := db.User.Create().
		SetEmail("generic-admin@test.local").
		SetName("Generic Admin").
		SetPreferredUsername("generic-admin").
		SetRole(user.RoleAdmin).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return ui, admin.ID
}

// Every PAGE renders its list. There are four, and the top level of `pages:` is
// the menu — a type without an entry has no URL at all, which is asserted
// separately below.
func TestGeneric_ListRendersEveryPage(t *testing.T) {
	ui, adminID := newGenericUI(t)
	e := echo.New()

	// These pages carry NO heading — they mirror servers.gohtml, which
	// identifies itself by the active nav item and the URL. Identity is
	// therefore asserted on the table's data-slug/data-type, which is what
	// actually proves the right view was resolved.
	for _, tc := range []struct{ slug, wantType string }{
		{"data-centers", "DataCenter"},
		{"servers", "Server"},
		{"clusters", "KubernetesCluster"},
		{"network-devices", "NetworkDevice"},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			rec := renderAs(t, e, "/"+tc.slug, ui.Generic().List, adminID, map[string]string{"slug": tc.slug})
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, `data-slug="`+tc.slug+`"`) {
				t.Errorf("table does not carry data-slug=%q — wrong view resolved", tc.slug)
			}
			if !strings.Contains(body, `data-type="`+tc.wantType+`"`) {
				t.Errorf("table does not carry data-type=%q", tc.wantType)
			}
			if !strings.Contains(body, "</html>") {
				t.Error("render truncated")
			}
			// Either a table or an explicit empty state — never a blank page,
			// which would leave a reader unable to tell "none" from "broken".
			if !strings.Contains(body, `id="generic-table"`) &&
				!strings.Contains(body, `data-testid="empty-state"`) {
				t.Error("neither a table nor an empty state rendered")
			}
			if strings.Contains(body, `data-testid="view-unavailable"`) {
				t.Errorf("page reported unavailable: %s", body[:min(400, len(body))])
			}

			// HOUSE STRUCTURE. The generic page must look like the hand-written
			// list pages, not like a lesser one — the migration replaces those
			// with this. Mirrors shared/pages/servers.gohtml.
			for _, want := range []string{
				`class="tabs is-boxed`, // the tab bar every list page carries
				`role="tabpanel"`,      // .tab-content wrapper, which scopes .box styling
				`id="generic-table"`,   // what initGenericTable() binds DataTables to
				`<tbody>`,              // rows are server-rendered, not fetched by JS
			} {
				if !strings.Contains(body, want) {
					t.Errorf("house structure missing %q — the page will not look like the other list pages", want)
				}
			}
		})
	}
}

// Item 18: a slug nothing declares is a 404 — not an empty page, which would
// say "there are none of these" rather than "there is no such kind".
func TestGeneric_UnknownSlugIs404(t *testing.T) {
	ui, _ := newGenericUI(t)
	e := echo.New()

	// Called directly rather than through renderAs: the handler RETURNS the
	// 404 as an error for Echo's central handler to render in the envelope,
	// and renderAs treats any returned error as a test failure.
	req := httptest.NewRequest(http.MethodGet, "/no-such-kind", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("slug")
	c.SetParamValues("no-such-kind")

	err := ui.Generic().List(c)
	if err == nil {
		t.Fatalf("unknown slug should 404, got %d:\n%s", rec.Code, rec.Body.String())
	}
	he, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("want *echo.HTTPError so the central handler renders the envelope, got %T", err)
	}
	if he.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", he.Code)
	}
}

// A detail page renders the entity's fields AND its relationships — a tab strip
// for lists, a link row for singles.
//
// Rendered from the DATA CENTRE, not the rack it seeds: Rack has no page any
// more, so /racks/<id> correctly 404s. A pageless type still renders — as rows
// in the Racks tab below — which is the half this asserts.
func TestGeneric_DetailRendersFieldsAndRelationshipTabs(t *testing.T) {
	ui, adminID := newGenericUI(t)
	e := echo.New()

	seedGenericRack(t)
	const orbID = "gen-test:datacenter-1"

	// As a FRAGMENT: this route is the body a detail tab fetches, and without
	// HX-Request it 404s. There is no page to render.
	rec := renderAsFragment(t, e, "/data-centers/"+orbID, ui.Generic().Detail, adminID,
		map[string]string{"slug": "data-centers", "orbId": orbID})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `data-testid="generic-fields"`) {
		t.Error("no field table rendered")
	}
	// The rack renders as a ROW in the Racks tab, carrying its own derived
	// field — a pageless type keeps its display config.
	if !strings.Contains(body, "42") {
		t.Error("expected the rack's uHeight value 42 in the Racks tab")
	}
	if !strings.Contains(body, "Racks") {
		t.Error("expected a Racks tab — a list relationship is a table")
	}
	// A row of a PAGELESS type must not link: Rack has no URL, so there is
	// nowhere to go. That is derived from "does its type have a page", which is
	// what makes promoting a type to a page turn its rows into links with no
	// other change.
	// Rows link at `?open=`, so that is the shape a regression would produce —
	// checking for "/racks/" would now pass no matter what.
	if strings.Contains(body, "/racks?open=") || strings.Contains(body, "/racks/") {
		t.Error("Rack has no page, so a rack row must not link to one")
	}
	// Reached the END of the block. This asserted "</html>" when the route still
	// served a page; it serves only the fragment now, whose last element is the
	// audit panel — so that is what proves the render was not truncated.
	if !strings.Contains(body, `data-testid="generic-audit"`) {
		t.Error("render truncated: the audit panel closes the detail body")
	}
}

// Item 9: with the schema unreadable the page says WHY. An empty list would
// claim "there are none of these", which is a different and misleading thing.
func TestGeneric_StatesReasonWhenSchemaUnreadable(t *testing.T) {
	ui, adminID := newGenericUI(t)
	// Point the field source at something that answers but is not a schema.
	stub := newDGraphStub(t, `{"data":{"nope":1}}`)
	ui.fields = NewSharedFieldSource(stub.URL, ViewsSource{Path: testutil.ViewsPath()}, slog.Default())

	e := echo.New()
	rec := renderAs(t, e, "/servers", ui.Generic().List, adminID, map[string]string{"slug": "servers"})
	if rec.Code != http.StatusOK {
		t.Fatalf("the page should still render, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="view-unavailable"`) {
		t.Error("no unavailable notice — an empty table would read as 'there are none'")
	}
	if !strings.Contains(body, "no restart needed") {
		t.Error("the notice must say it self-heals; otherwise an operator restarts orbital for nothing")
	}
	// Save must be gone. A Save button over an editor that cannot know the
	// field set invites a write that would look like clearing every field.
	if strings.Contains(body, "-edit-submit-") {
		t.Error("Save is still rendered while the schema is unreadable")
	}
}

// The negative: when the schema resolves, NO notice appears.
//
// A warning that fires when nothing is wrong trains people to ignore it. This
// and the case above are what remains of editor_degraded_test.go, deleted
// 2026-09-26 with the last bespoke page it drove — the guarantee moved to the
// generic renderer, which is now the only path a ConfigItem page takes.
func TestGeneric_NoUnavailableNoticeWhenSchemaResolves(t *testing.T) {
	ui, adminID := newGenericUI(t)
	e := echo.New()
	rec := renderAs(t, e, "/servers", ui.Generic().List, adminID, map[string]string{"slug": "servers"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `data-testid="view-unavailable"`) {
		t.Error("unavailable notice rendered while the schema resolved fine")
	}
	if strings.Contains(body, `data-testid="editor-unavailable"`) {
		t.Error("degraded-editor notice rendered while the field list resolved fine")
	}
}

func seedGenericRack(t *testing.T) string {
	t.Helper()
	const dc = "gen-test:datacenter-1"
	const rack = "gen-test:rack-A1"
	gqlMutate(t, `mutation($input:[AddDataCenterInput!]!){ addDataCenter(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": "gen-test", "orbId": dc, "name": "generic fixture dc", "version": 1,
		}}})
	gqlMutate(t, `mutation($input:[AddRackInput!]!){ addRack(input:$input, upsert:true){ numUids } }`,
		map[string]any{"input": []any{map[string]any{
			"namespace": "gen-test", "orbId": rack, "name": "A1", "version": 1, "uHeight": 42,
			"dataCenter": map[string]any{"orbId": dc},
		}}})
	t.Cleanup(func() {
		deleteByOrbID(t, "Rack", rack)
		deleteByOrbID(t, "DataCenter", dc)
	})
	return rack
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
