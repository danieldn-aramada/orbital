//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// Acceptance 11 — a save whose view changed since the editor opened is refused,
// and the dropped entity's fields are untouched.
//
// The hazard, from spike 38 and undiminished: configitem-editor.js decides a
// field was CLEARED by diffing the open-time snapshot against the edited tree.
// A member removed from a view while someone has an editor open therefore reads
// as "the user cleared this", and the save emits a `remove` for an entity nobody
// touched. The ConfigMap model narrows the window — a view change arrives on
// reload, not on a live write — but reload happens in place, so the window is
// real.
//
// "Untouched" is asserted where it is actually decided: the DGraph stub counts
// requests, and a refused save must produce ZERO. Asserting field values after
// the fact would prove the same thing one layer further from the cause, and
// would pass if the write had been made and then reverted.
func TestEditorSave_RefusedWhenViewVersionMovedAndDropsNothing(t *testing.T) {
	// A counting proxy in front of the real cluster: reads pass through, so the
	// resolver introspects a genuine deployed schema, and writes are COUNTED and
	// never forwarded. The test therefore asserts against real schema resolution
	// while touching no data.
	var writes atomic.Int32
	dgraph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("mutation")) {
			writes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":{"updateServer":{"server":[{"orbId":"colo:server-A"}]}}}`)) //nolint:errcheck
			return
		}
		target := testutil.DGraphURL()
		if bytes.Contains(body, []byte("getGQLSchema")) {
			target = testutil.DGraphAdminURL()
		}
		resp, err := http.Post(target, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, resp.Body) //nolint:errcheck
	}))
	t.Cleanup(dgraph.Close)

	viewsPath := filepath.Join(t.TempDir(), "views.yaml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(viewsPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("views:\n  Server:\n    members:\n      - { path: idracSettings, editable: true }\n")

	sf := NewSharedFieldSource(dgraph.URL, ViewsSource{Path: viewsPath, CheckEvery: time.Nanosecond}, slog.Default())
	h := NewGraphQL(dgraph.URL, nil, slog.Default(), false, WithFieldSource(sf))

	const mutation = `mutation Save($orbId: String!, $set: ServerPatch!) {
	  updateServer(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { server { orbId } }
	}`
	save := func(t *testing.T, declaredHash string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"query":     mutation,
			"variables": map[string]any{"orbId": "colo:server-A", "set": map[string]any{"hostname": "h1"}},
		})
		req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if declaredHash != "" {
			req.Header.Set(viewsHashHeader, declaredHash)
		}
		rec := httptest.NewRecorder()
		if err := h.Handle(echo.New().NewContext(req, rec)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		return rec
	}

	// The hash the modal would have been stamped with. Resolving it is also what
	// primes the resolver, so the comparison below is against a known value and
	// not against "".
	if _, err := sf.ViewSet(context.Background()); err != nil {
		t.Fatalf("resolve views: %v", err)
	}
	opened := sf.ViewsHash()
	if opened == "" {
		t.Fatal("the resolver must publish a views hash; the guard is inert without one")
	}

	t.Run("a save under the current view goes through", func(t *testing.T) {
		before := writes.Load()
		if rec := save(t, opened); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — the guard must not block an unchanged view: %s", rec.Code, rec.Body)
		}
		if writes.Load() == before {
			t.Error("the mutation must reach DGraph when the view has not moved")
		}
	})

	t.Run("a save under a view that has since changed is refused, and writes nothing", func(t *testing.T) {
		// The member is dropped — exactly the change that makes the editor's
		// diff read as a deletion.
		write("views:\n  Server:\n    members: []\n")

		before := writes.Load()
		rec := save(t, opened)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 — the request was correct when composed, "+
				"so the answer is reload-and-retry, not malformed-request. body: %s", rec.Code, rec.Body)
		}
		if got := writes.Load(); got != before {
			t.Errorf("the refused save reached DGraph %d time(s); nothing may be written", got-before)
		}

		var env map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("the refusal must use the error envelope: %v (%s)", err, rec.Body)
		}
		if env["code"] != CodeConflict {
			t.Errorf("code = %v, want %s — clients branch on this", env["code"], CodeConflict)
		}
		if h, _ := env["hint"].(string); !strings.Contains(strings.ToLower(h), "reload") {
			t.Errorf("hint = %q; it must say what to do, and the answer is to reload", h)
		}
	})

	t.Run("a caller that declares no view is not blocked", func(t *testing.T) {
		// The header is sent only by configitem-editor.js. An API client,
		// orbctl or AEP emitting an explicit `remove` is doing it deliberately,
		// and the hazard guarded here is the editor's diff — not the mutation.
		before := writes.Load()
		if rec := save(t, ""); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — a header-less caller must not be gated on a UI concern: %s",
				rec.Code, rec.Body)
		}
		if writes.Load() == before {
			t.Error("a header-less write must reach DGraph")
		}
	})
}
