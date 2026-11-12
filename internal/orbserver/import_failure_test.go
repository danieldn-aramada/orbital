package orbserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/armada/orbital/internal/orb"
)

// fakeRegistry serves /tags/list for one repo and 404s everything else, so any
// import against it fails at the pull stage.
type fakeRegistry struct {
	mu   sync.Mutex
	tags []string
}

func (f *fakeRegistry) setTags(tags ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags = tags
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v2/orbital/test/tags/list" {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": "orbital/test", "tags": f.tags})
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records returns every log record whose msg equals msg.
func (b *syncBuffer) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", sc.Text())
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func newFailingImportServer(t *testing.T, autoImport bool) (*Server, *fakeRegistry, *syncBuffer) {
	t.Helper()
	t.Chdir("../..")
	reg := &fakeRegistry{}
	ts := httptest.NewServer(reg)
	t.Cleanup(ts.Close)

	cfg := testCfg(t)
	cfg.EnableOCIRegistry = true
	cfg.OCIRegistry = strings.TrimPrefix(ts.URL, "http://")
	cfg.OCIRepo = "orbital/test"
	cfg.OCIAllowHTTP = true
	cfg.AutoImportEnabled = autoImport
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logs := &syncBuffer{}
	srv.logger = slog.New(slog.NewJSONHandler(logs, nil))
	return srv, reg, logs
}

// waitForFailedImports blocks until n import failures have been logged.
func waitForFailedImports(t *testing.T, logs *syncBuffer, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(logs.records(t, "import failed")) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d failed imports; saw %d", n, len(logs.records(t, "import failed")))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func failedImportCount(t *testing.T, srv *Server, initiatedBy string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	want := fmt.Sprintf(`orb_imports_total{initiated_by=%q,status="failed"} `, initiatedBy)
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, want) {
			v, err := strconv.ParseFloat(strings.TrimPrefix(line, want), 64)
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			return v
		}
	}
	return 0
}

func TestImportFailure_LogsTagStageAndInitiator(t *testing.T) {
	srv, _, logs := newFailingImportServer(t, false)

	if !srv.startImport("v1", orb.InitiatedByManual) {
		t.Fatal("startImport refused to start")
	}
	waitForFailedImports(t, logs, 1)

	recs := logs.records(t, "import failed")
	if len(recs) != 1 {
		t.Fatalf("want exactly 1 failure log line, got %d", len(recs))
	}
	r := recs[0]
	if r["level"] != "ERROR" || r["tag"] != "v1" || r["stage"] != "pull" || r["initiated_by"] != "manual" || r["err"] == "" {
		t.Errorf("failure log line is missing tag/stage/initiator/err or not ERROR: %v", r)
	}
	if got := srv.state.snapshot().LastError; !strings.HasPrefix(got, "pull: ") {
		t.Errorf("status lastError = %q, want the pull error", got)
	}
}

func TestImportFailure_CountsInImportMetric(t *testing.T) {
	srv, _, logs := newFailingImportServer(t, false)
	before := failedImportCount(t, srv, "manual")

	srv.startImport("v1", orb.InitiatedByManual)
	waitForFailedImports(t, logs, 1)

	if got := failedImportCount(t, srv, "manual"); got != before+1 {
		t.Errorf(`orb_imports_total{status="failed",initiated_by="manual"} = %v, want %v`, got, before+1)
	}
}

func TestAutoImport_DoesNotRetryAFailedTag(t *testing.T) {
	srv, reg, logs := newFailingImportServer(t, true)
	reg.setTags("v1")

	srv.poll(context.Background())
	waitForFailedImports(t, logs, 1)
	srv.poll(context.Background())
	srv.poll(context.Background())

	if n := len(logs.records(t, "auto-import triggered")); n != 1 {
		t.Errorf("auto-import triggered %d times for a tag that failed, want 1", n)
	}
	snap := srv.state.snapshot()
	if snap.Status != "failed" || snap.LastError == "" {
		t.Errorf("status = %q lastError = %q, want the failure to stay visible", snap.Status, snap.LastError)
	}
}

func TestAutoImport_ChasesNewerTagAfterFailure(t *testing.T) {
	srv, reg, logs := newFailingImportServer(t, true)
	reg.setTags("v1")
	srv.poll(context.Background())
	waitForFailedImports(t, logs, 1)

	reg.setTags("v1", "v2")
	srv.poll(context.Background())
	waitForFailedImports(t, logs, 2)

	triggered := logs.records(t, "auto-import triggered")
	if len(triggered) != 2 || triggered[1]["tag"] != "v2" {
		t.Errorf("want a second auto-import for v2, got %v", triggered)
	}
}

func TestManualImport_RetriesAFailedTag(t *testing.T) {
	srv, reg, logs := newFailingImportServer(t, true)
	reg.setTags("v1")
	srv.poll(context.Background())
	waitForFailedImports(t, logs, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/import", strings.NewReader(`{"tag":"v1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("manual import of the failed tag: got %d %s, want 202", rec.Code, rec.Body.String())
	}
	waitForFailedImports(t, logs, 2)
}
