//go:build integration

package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// The audit log refuses an over-cap orbId filter instead of truncating it.
//
// It used to truncate — `orbIDFilter = orbIDFilter[:maxOrbIDs]`, silently, with
// a 200. That made the Server audit tab lossy on ordinary data: the tab sends
// the server's whole owned subtree, a populated server in the seeded namespace
// has 35 orbIds, and the cap was 32. Three children were dropped from every
// query, and "no events for that disk" is indistinguishable from "that disk was
// never asked about".
//
// This is the regression that announces itself least — nothing errors, nothing
// looks wrong, and the answer is simply narrower than the question. Only a test
// that sends more than the cap catches it.
func TestAuditLog_OverTheOrbIDCapIsRefusedNotTruncated(t *testing.T) {
	db := testutil.NewTestDB(t)
	h := &AuditHandler{db: db, logger: slog.Default()}

	query := func(n int) string {
		parts := make([]string, 0, n)
		for i := 0; i < n; i++ {
			parts = append(parts, "orbId=colo:probe-"+strconv.Itoa(i))
		}
		return "/api/v1/audit-log?" + strings.Join(parts, "&")
	}

	// At the cap: served normally.
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, query(maxOrbIDFilter), nil), rec)
	if err := h.List(c); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("at the cap: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// One over: refused, with the envelope, naming the count and the limit.
	rec = httptest.NewRecorder()
	c = echo.New().NewContext(httptest.NewRequest(http.MethodGet, query(maxOrbIDFilter+1), nil), rec)
	if err := h.List(c); err != nil {
		t.Fatalf("over the cap: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — the filter was silently truncated: %s", rec.Code, rec.Body.String())
	}
	var errBody errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if errBody.Code != CodeBadUserInput || errBody.Hint == "" {
		t.Errorf("envelope = %+v, want %s with a hint", errBody, CodeBadUserInput)
	}
	if !strings.Contains(errBody.Error, strconv.Itoa(maxOrbIDFilter+1)) {
		t.Errorf("error %q does not say how many were sent, so the caller cannot tell how far over it is", errBody.Error)
	}
}
