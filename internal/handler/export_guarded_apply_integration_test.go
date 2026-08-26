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

	// Count rather than wipe: other tests in this package publish artifacts, and
	// registry_artifacts has an FK onto export_jobs — a blanket delete would fail
	// on the constraint depending on test order.
	jobsBefore := testDB.ExportJob.Query().CountX(ctx)

	// A hash the operator reviewed BEFORE someone else edited intent.
	stale := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	body, _ := json.Marshal(map[string]any{"orbId": testDcID, "expectedContentHash": stale})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// The guard uses writeError, so it RENDERS the envelope and returns nil —
	// assert on the response, and specifically on `code`. A bare 409 would be
	// CONFLICT ("already in progress"), which clients must be able to tell apart
	// from a stale preview.
	if err := h.Trigger(e.NewContext(req, rec)); err != nil {
		t.Fatalf("Trigger returned an error instead of rendering the envelope: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Code != "MVCC_CONFLICT" {
		t.Fatalf("expected code MVCC_CONFLICT (distinguishable from an in-progress CONFLICT), got %q: %s",
			envelope.Code, rec.Body.String())
	}

	if n := testDB.ExportJob.Query().CountX(ctx); n != jobsBefore {
		t.Errorf("a rejected guarded Apply must not create a job: had %d, now %d", jobsBefore, n)
	}
}
