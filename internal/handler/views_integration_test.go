//go:build integration

package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// Acceptance items 2 and 4: the endpoint returns every ConfigItem type the
// DEPLOYED schema declares, so a type added to the schema appears here with no
// code change and no restart.
func TestViews_ListsEveryTypeFromTheDeployedSchema(t *testing.T) {
	h := NewViewsHandler(NewSharedFieldSource(testutil.DGraphURL(), slog.Default()), slog.Default())

	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/views", nil), rec)
	if err := h.List(c); err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Views []configitems.View `json:"views"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Views) == 0 {
		t.Fatal("no views returned")
	}

	bySlug := map[string]configitems.View{}
	roots := []string{}
	for _, v := range body.Views {
		if v.Slug == "" || v.Type == "" || v.Label == "" {
			t.Errorf("view is missing slug/type/label: %+v", v)
		}
		// Never nil: clients iterate these without a null check.
		if v.Fields == nil || v.Tabs == nil {
			t.Errorf("%s: fields/tabs must serialise as [] not null", v.Type)
		}
		bySlug[v.Slug] = v
		if v.IsRoot {
			roots = append(roots, v.Type)
		}
	}

	// Nav is roots only — an owned child like IdracSettings is meaningless
	// outside its parent, and a nineteen-item menu helps nobody.
	//
	// Clusters are in the nav as the INTERFACE, not as EksaKubernetesCluster:
	// /clusters must show every implementation, so the implementation is
	// demoted out of the nav rather than sitting beside the interface showing
	// the same rows.
	wantRoots := map[string]bool{"DataCenter": true, "Server": true, "NetworkDevice": true, "KubernetesCluster": true}
	if len(roots) != len(wantRoots) {
		t.Errorf("nav roots = %v, want exactly %d", roots, len(wantRoots))
	}
	for _, r := range roots {
		if !wantRoots[r] {
			t.Errorf("%s is in the nav but is an owned child or is covered by an interface view", r)
		}
	}

	// The slug rule, against real type names.
	for slug, typeName := range map[string]string{
		"servers": "Server", "data-centers": "DataCenter", "ip-addresses": "IPAddress",
		"idrac-settings": "IdracSettings", "s3-syncs": "S3Sync",
	} {
		v, ok := bySlug[slug]
		if !ok {
			t.Errorf("no view at slug %q", slug)
			continue
		}
		if v.Type != typeName {
			t.Errorf("slug %q maps to %s, want %s", slug, v.Type, typeName)
		}
	}

	// Tabs link through by slug, so a client never re-derives one.
	srv := bySlug["servers"]
	var sawIdrac bool
	for _, tab := range srv.Tabs {
		if tab.Field == "idracSettings" {
			sawIdrac = true
			if tab.Slug != "idrac-settings" {
				t.Errorf("idracSettings tab slug = %q", tab.Slug)
			}
		}
	}
	if !sawIdrac {
		t.Error("Server should expose idracSettings as a relationship tab")
	}
}
