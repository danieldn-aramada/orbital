package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

// TestClusterTab_BackupWithRetentionRendersFullFragment guards the render-struct /
// template field-drift class that produced the 2026-07-27 "Edit button does
// nothing" bug: cluster-tab.gohtml reads .Backup.Etcd.RetentionDays from the
// backupKindTab VIEW struct (distinct from the query struct backupKindResponse).
// When retentionDays was added to the query struct + template but missed on
// backupKindTab, html/template errored mid-render and the fragment truncated
// BEFORE the edit modal (which is the last thing in the template) — the button
// rendered with no modal to open, and the failure was silent (200 + partial body).
//
// With buffered rendering, that same drift now returns an error from Tab(), so
// this test fails loudly instead of shipping a truncated page. It asserts both
// that the retention cells render (proving backupKindTab carries the field) AND
// that the edit modal is present (proving the fragment rendered to completion).
// Adding a new backup field to the template without wiring backupKindTab will
// fail this test — the intended guard.
func TestClusterTab_BackupWithRetentionRendersFullFragment(t *testing.T) {
	t.Chdir("../..")

	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"queryConfigItem": []any{
				map[string]any{
					"__typename":  "EksaKubernetesCluster",
					"id":          "0x10",
					"orbId":       "test:c1",
					"name":        "c1",
					"namespace":   "test-ns",
					"version":     1,
					"clusterType": "management",
					"backup": map[string]any{
						"id": "0x20", "orbId": "test:c1-backup", "name": "c1-backup", "namespace": "test-ns", "version": 1,
						"etcd": map[string]any{
							"id": "0x21", "orbId": "test:c1-etcd", "name": "c1-etcd", "namespace": "test-ns", "version": 1,
							"enabled": true, "schedule": "0 14 * * *", "location": "s3://etcd", "retentionDays": 7,
						},
						"velero": map[string]any{
							"id": "0x22", "orbId": "test:c1-velero", "name": "c1-velero", "namespace": "test-ns", "version": 1,
							"enabled": true, "schedule": "0 2 * * *", "location": "s3://velero", "retentionDays": 14,
						},
					},
				},
			},
		},
	})
	dgraph := newDGraphStub(t, string(body))

	// Edit=true so the edit button AND modal render (both are {{if .Actions.Edit}}-gated).
	h := NewClusterHandler(dgraph.URL, false, slog.Default(), "/app", func(echo.Context) layout.PageActions { return layout.OrbitalActions(true) })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("orbId")
	c.SetParamValues("test:c1")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab returned error — a template field likely has no match on its render struct: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	out := rec.Body.String()
	for _, want := range []string{"7 days", "14 days"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered cluster tab missing retention value %q (backupKindTab.RetentionDays not wired?)", want)
		}
	}
	// The edit modal is the last thing in cluster-tab.gohtml — its presence proves
	// the fragment rendered to completion with no mid-render truncation.
	if !strings.Contains(out, "edit-modal-cluster-") {
		t.Error("edit modal absent — fragment truncated before the modal (render-struct/template drift)")
	}
}
