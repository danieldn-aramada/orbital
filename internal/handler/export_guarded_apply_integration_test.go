//go:build integration

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// Guarded Apply (Spike 31): POST /api/v1/export with an expectedContentHash that
// no longer matches the DC's current desired state must 409 and create NO job —
// otherwise a concurrent writer's changes would publish without ever being
// reviewed. Catches a dropped or inverted hash comparison in Trigger.
//
// The no-hash path (last-writer-wins back-compat) is covered by every other
// export integration test, which call Trigger without the field.
func TestExportTrigger_StaleContentHashConflicts(t *testing.T) {
	ctx := context.Background()
	h := newExportHandler(t)

	// The preview is what hands the operator a hash to apply with.
	e := echo.New()
	pBody, _ := json.Marshal(map[string]string{"orbId": testDcID})
	pReq := httptest.NewRequest(http.MethodPost, "/api/v1/export/preview", bytes.NewReader(pBody))
	pReq.Header.Set("Content-Type", "application/json")
	pRec := httptest.NewRecorder()
	if err := h.Preview(e.NewContext(pReq, pRec)); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if pRec.Code != http.StatusOK {
		t.Fatalf("Preview: expected 200, got %d: %s", pRec.Code, pRec.Body.String())
	}
	var preview struct {
		Current struct {
			ContentHash string `json:"contentHash"`
		} `json:"current"`
	}
	if err := json.Unmarshal(pRec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if !strings.HasPrefix(preview.Current.ContentHash, "sha256:") {
		t.Fatalf("preview did not return a usable contentHash: %q", preview.Current.ContentHash)
	}

	testDB.ExportJob.Delete().ExecX(ctx)

	// A hash the operator reviewed BEFORE someone else edited intent.
	stale := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	body, _ := json.Marshal(map[string]any{"orbId": testDcID, "expectedContentHash": stale})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	err := h.Trigger(e.NewContext(req, rec))
	if err == nil {
		t.Fatalf("expected a 409 error, got none (status %d, body %s)", rec.Code, rec.Body.String())
	}
	he, ok := err.(*echo.HTTPError)
	if !ok || he.Code != http.StatusConflict {
		t.Fatalf("expected 409 HTTPError, got %#v", err)
	}

	if n := testDB.ExportJob.Query().CountX(ctx); n != 0 {
		t.Errorf("a rejected guarded Apply must not create a job, found %d", n)
	}
}
