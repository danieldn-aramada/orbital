package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"log/slog"

	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

// A reachable edit target MUST carry an OCC version.
//
// "Reachable" means the handler's edit-data tree actually contains the target's
// path, so the JSON editor renders it and a user can change it. An unreachable
// target is inert: getByPath returns undefined, `changed` is false, and
// configitem-editor.js never builds a mutation for it.
//
// That distinction is the whole point of this test, and it is what made the
// current state hard to read. `BuildEditTargets` emits StorageDevice,
// NetworkInterface (Server) and Rack (DataCenter) targets that carry NO version
// and a registry-derived orbId nobody overrode with a real one — but they are
// unreachable, so nothing can be edited unguarded today.
//
// The day someone adds `storageControllers` to editFields to make storage
// editable, those targets go live: unguarded, and pointed at an orbId that
// probably does not exist, which upserts a phantom entity instead of editing
// the real one. Two silent failures at once. This test fires on that change.
//
// Version is opt-in server-side — "an absent client is indistinguishable from
// one that declined to use it" (configitem-editor.js) — which is exactly why it
// needs a test rather than a code review. MVCC was already off for every UI
// edit for two and a half months after a refactor dropped the parameter, and
// nothing failed.

var (
	editDataRe    = regexp.MustCompile(`(?s)id="[a-z-]+-edit-data-[^"]*">(.*?)</script>`)
	editTargetsRe = regexp.MustCompile(`(?s)id="[a-z-]+-edit-targets-[^"]*">(.*?)</script>`)
)

type renderedTarget struct {
	Kind    string   `json:"kind"`
	OrbID   string   `json:"orbId"`
	Path    []string `json:"path"`
	Version int      `json:"version"`
}

// reachable reports whether the edit tree actually contains this target's path,
// mirroring getByPath + the `changed` comparison in configitem-editor.js: an
// absent OR null node yields no change and therefore no mutation.
func reachable(editData map[string]any, path []string) bool {
	if len(path) == 0 {
		return true // the root is always rendered
	}
	var cur any = editData
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = m[seg]
		if !ok {
			return false
		}
	}
	return cur != nil
}

func assertTargetInvariant(t *testing.T, page, html string) {
	t.Helper()
	dm := editDataRe.FindStringSubmatch(html)
	tm := editTargetsRe.FindStringSubmatch(html)
	if dm == nil || tm == nil {
		t.Fatalf("%s: could not find the edit-data/edit-targets blobs in the rendered page", page)
	}
	var editData map[string]any
	if err := json.Unmarshal([]byte(dm[1]), &editData); err != nil {
		t.Fatalf("%s: decode edit data: %v", page, err)
	}
	var targets []renderedTarget
	if err := json.Unmarshal([]byte(tm[1]), &targets); err != nil {
		t.Fatalf("%s: decode edit targets: %v", page, err)
	}
	if len(targets) == 0 {
		t.Fatalf("%s: no edit targets rendered — the assertions below would be vacuous", page)
	}

	var checked int
	for _, tg := range targets {
		if !reachable(editData, tg.Path) {
			continue // inert: the editor cannot produce a mutation for it
		}
		checked++
		if tg.Version <= 0 {
			t.Errorf("%s: target %s (path %v, orbId %q) is EDITABLE but carries no version — "+
				"a concurrent edit to it would be silently overwritten. Stamp it with "+
				"configitems.StampEditTargetVersion, and make sure the page query selects `version`.",
				page, tg.Kind, tg.Path, tg.OrbID)
		}
	}
	if checked == 0 {
		t.Errorf("%s: no reachable targets found — the invariant checked nothing", page)
	}
}

func TestEditTargets_EveryEditableEntityCarriesAVersion(t *testing.T) {
	t.Chdir("../..")

	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"getServer": map[string]any{
				"id": "0x2", "name": "srv-01", "orbId": "test-ns:server-ABC123",
				"hostname": "srv-01.example.com", "namespace": "test-ns",
				"serviceTag": "ABC123", "version": 4,
				"rack":       map[string]any{"id": "0x3", "name": "rack-a"},
				"dataCenter": map[string]any{"id": "0x1", "name": "Test DC"},
				"idracSettings": map[string]any{
					"orbId": "test-ns:idrac-ABC123", "firmwareVersion": "7.0.0", "version": 2,
				},
				"serverMaintenance": map[string]any{
					"orbId": "test-ns:server-maintenance-ABC123", "enabled": true, "version": 7,
				},
				"storageControllers": []any{
					map[string]any{"orbId": "test:srv-01-ctrl-0", "name": "PERC H755"},
				},
			},
		},
	})
	dgraph := newDGraphStub(t, string(body))
	h := NewServerHandler(dgraph.URL, false, slog.Default(), "/app",
		func(echo.Context) layout.PageActions { return layout.OrbitalActions(true) })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x2")
	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	assertTargetInvariant(t, "Server", rec.Body.String())
}

// renderTab is the shared harness: stub DGraph, render one tab fragment with
// edit actions enabled (the modal — and therefore the targets blob — is gated
// on {{if .Actions.Edit}}), and hand back the HTML.
func renderTab(t *testing.T, dgraphResp string, mk func(url string) func(echo.Context) error, param string) string {
	t.Helper()
	dgraph := newDGraphStub(t, dgraphResp)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id", "orbId")
	c.SetParamValues(param, param)
	if err := mk(dgraph.URL)(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	return rec.Body.String()
}

func editActions(echo.Context) layout.PageActions { return layout.OrbitalActions(true) }

func TestEditTargets_DataCenterEveryEditableEntityCarriesAVersion(t *testing.T) {
	t.Chdir("../..")
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"getDataCenter": map[string]any{
		"id": "0x1", "orbId": "test-ns:dc-1", "name": "dc-1", "namespace": "test-ns", "version": 3,
		"racks": []any{map[string]any{"orbId": "test-ns:rack-a", "name": "rack-a"}},
	}}})
	html := renderTab(t, string(body), func(u string) func(echo.Context) error {
		return NewDataCenter(u, false, slog.Default(), "/app", editActions).Tab
	}, "test-ns:dc-1")
	assertTargetInvariant(t, "DataCenter", html)
}

func TestEditTargets_ClusterEveryEditableEntityCarriesAVersion(t *testing.T) {
	t.Chdir("../..")
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"queryConfigItem": []any{map[string]any{
		"__typename": "EksaKubernetesCluster",
		"id":         "0x9", "orbId": "test-ns:cluster-1", "name": "cluster-1",
		"namespace": "test-ns", "version": 5,
		"backup": map[string]any{
			"id": "0xa", "orbId": "test-ns:cluster-1-backup", "name": "b", "namespace": "test-ns", "version": 2,
			"etcd":   map[string]any{"id": "0xb", "orbId": "test-ns:cluster-1-etcdbackup", "name": "e", "namespace": "test-ns", "version": 2},
			"velero": map[string]any{"id": "0xc", "orbId": "test-ns:cluster-1-velerobackup", "name": "v", "namespace": "test-ns", "version": 2},
			"s3Sync": map[string]any{"id": "0xd", "orbId": "test-ns:cluster-1-s3sync", "name": "s", "namespace": "test-ns", "version": 2},
		},
	}}}})
	html := renderTab(t, string(body), func(u string) func(echo.Context) error {
		return NewClusterHandler(u, false, slog.Default(), "/app", editActions).Tab
	}, "test-ns:cluster-1")
	assertTargetInvariant(t, "KubernetesCluster", html)
}

func TestEditTargets_NetworkDeviceEveryEditableEntityCarriesAVersion(t *testing.T) {
	t.Chdir("../..")
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"getNetworkDevice": map[string]any{
		"id": "0x5", "orbId": "test-ns:network-device-SW1", "name": "SW1",
		"namespace": "test-ns", "version": 6,
	}}})
	html := renderTab(t, string(body), func(u string) func(echo.Context) error {
		return NewNetworkDeviceHandler(u, false, slog.Default(), "/app", editActions).Tab
	}, "test-ns:network-device-SW1")
	assertTargetInvariant(t, "NetworkDevice", html)
}
