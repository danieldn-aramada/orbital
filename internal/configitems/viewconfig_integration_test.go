//go:build integration

package configitems

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// Acceptance 1 — with no overlay present, every ConfigItem page renders from the
// embedded views config alone.
//
// The claim needs the DEPLOYED schema, which is the whole point: the shipped
// file and the running graph are now separate artifacts, and the only honest way
// to assert they agree is to resolve one against the other. A unit test with a
// fixture would assert the file against itself.
func TestLoadViews_EmbeddedDefaultAloneResolvesEveryView(t *testing.T) {
	gql, admin := dgraphURLs()
	types, iface, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect the deployed schema: %v", err)
	}

	cfg, hash, warnings, err := LoadViewConfig(viewsConfigPath(t), "")
	if err != nil {
		t.Fatalf("load the shipped default: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("loading with no overlay configured must warn about nothing; got %v", warnings)
	}
	if hash == "" {
		t.Error("the load must produce a content hash; the reload watch and the editor guard both key on it")
	}

	validated, dropped := cfg.Validate(types, schemaVersionLabel(t))
	if len(dropped) != 0 {
		t.Errorf("the shipped default must validate clean against the deployed schema; got:\n  %s",
			strings.Join(dropped, "\n  "))
	}

	views, err := ResolveViewsFromConfig(types, iface, validated, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// FOUR pages, and the top level of `pages:` is the menu. Every other type
	// still resolves — it renders as rows on somebody else's page — but has no
	// slug, which is what makes those rows dead rather than links to nothing.
	got := map[string]View{}
	var pages []string
	for _, v := range views {
		got[v.Type] = v
		if v.Slug != "" {
			pages = append(pages, v.Type)
		}
	}
	wantPages := map[string]bool{"DataCenter": true, "Server": true, "KubernetesCluster": true, "NetworkDevice": true}
	if len(pages) != len(wantPages) {
		t.Errorf("pages = %v, want exactly %d", pages, len(wantPages))
	}
	for _, p := range pages {
		if !wantPages[p] {
			t.Errorf("%s has a page and should not", p)
		}
	}
	for name := range types {
		v, ok := got[name]
		if !ok {
			t.Errorf("%s is in the deployed schema but resolved to nothing", name)
			continue
		}
		if v.Meta == nil {
			t.Errorf("%s has no metadata rows; every ConfigItem carries them", name)
		}
	}

	// Members have to survive the round trip, or "every page renders" is true
	// and useless. Server is the densest page and exercises every member shape
	// at once: links, tables, an inline child and a two-segment path.
	server := got["Server"]
	var links, tables, paths, editable int
	for range server.SummaryRefs {
		links++
	}
	for _, m := range server.Subgraph {
		switch {
		case strings.Contains(m.Field, "."):
			paths++
		case m.IsList:
			tables++
		}
		if m.Editable {
			editable++
		}
	}
	if links == 0 || tables == 0 || paths == 0 || editable == 0 {
		t.Errorf("Server = %d summary refs, %d tables, %d paths, %d editable; every shape must survive the round trip",
			links, tables, paths, editable)
	}

	// Acceptance 8 — nav membership comes from the views config.
	var nav []string
	for _, v := range views {
		if v.Slug != "" {
			nav = append(nav, v.Label)
		}
	}
	want := []string{"Data Centers", "Servers", "Clusters", "Network Devices"}
	if strings.Join(nav, "|") != strings.Join(want, "|") {
		t.Errorf("nav = %v, want %v", nav, want)
	}
}

// A tab or ref column's SLUG is the link target: empty when the target type has
// no page, filled when it has one. Both halves on one page, because they are one
// rule — "does its type have a page" — and a test that only checks the empty
// half passes for a resolver that never fills any slug at all.
//
// Moved here when GET /api/v1/views was deleted (2026-10-05); it was the one
// claim that endpoint's test owned and nothing else covered.
func TestResolveViews_SlugIsTheLinkTargetBothWays(t *testing.T) {
	gql, admin := dgraphURLs()
	types, iface, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	cfg, _, _, err := LoadViewConfig(viewsConfigPath(t), "")
	if err != nil {
		t.Fatal(err)
	}
	validated, _ := cfg.Validate(types, "")
	views, err := ResolveViewsFromConfig(types, iface, validated, nil)
	if err != nil {
		t.Fatal(err)
	}
	var server View
	for _, v := range views {
		if v.Type == "Server" {
			server = v
		}
	}

	var sawIdrac, sawDC bool
	for _, tab := range server.Subgraph {
		if tab.Field == "idracSettings" {
			sawIdrac = true
			if tab.Slug != "" {
				t.Errorf("IdracSettings has no page, so its tab must carry no slug; got %q", tab.Slug)
			}
		}
	}
	for _, r := range server.RefColumns {
		if r.Field == "dataCenter" {
			sawDC = true
			if r.Slug != "data-centers" {
				t.Errorf("DataCenter HAS a page, so the ref must link to it; got %q", r.Slug)
			}
		}
	}
	if !sawIdrac {
		t.Error("precondition: Server must still declare an idracSettings tab")
	}
	if !sawDC {
		t.Error("precondition: Server must still declare dataCenter as a ref column")
	}
}

// Acceptance 4 — a member path of two segments renders a table of the far type.
//
// A server's disks hang off its storage controllers, and StorageController has
// no scalars of its own, so without this the Storage tab is a list of bare names
// and the disks appear on no page at all.
func TestResolveMembers_TwoSegmentPathRendersFarType(t *testing.T) {
	gql, admin := dgraphURLs()
	types, iface, err := NewDGraphSchemaClient(gql, admin).Introspect(context.Background())
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	cfg, _, _, err := LoadViewConfig(viewsConfigPath(t), "")
	if err != nil {
		t.Fatal(err)
	}
	validated, _ := cfg.Validate(types, "")
	views, err := ResolveViewsFromConfig(types, iface, validated, nil)
	if err != nil {
		t.Fatal(err)
	}
	var server View
	for _, v := range views {
		if v.Type == "Server" {
			server = v
		}
	}
	var found *ViewTab
	for i, m := range server.Subgraph {
		if m.Field == "storageControllers.storageDevices" {
			found = &server.Subgraph[i]
		}
	}
	if found == nil {
		t.Fatalf("Server has no storageControllers.storageDevices member; got %v", memberFields(server))
	}
	if found.Type != "StorageDevice" {
		t.Errorf("the member's type is the FAR end of the path: got %q, want StorageDevice", found.Type)
	}
	if !found.IsList {
		t.Error("a path through a list relationship produces rows, so it renders as a table")
	}
	// StorageDevice has NO page, so the tab carries no slug and its rows are
	// dead. Promoting it to a page is the one change that turns them into links.
	if found.Slug != "" {
		t.Errorf("slug = %q, want empty — StorageDevice has no page, so its rows must not link", found.Slug)
	}
}

func memberFields(v View) []string {
	out := make([]string, 0, len(v.Subgraph))
	for _, m := range v.Subgraph {
		out = append(out, m.Field)
	}
	sort.Strings(out)
	return out
}
