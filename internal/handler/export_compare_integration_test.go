//go:build integration

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/internal/handler"
	"github.com/armada/orbital/internal/oci"
	"github.com/armada/orbital/internal/testutil"
	"github.com/labstack/echo/v4"
)

// ── Compare end-to-end ────────────────────────────────────────────────────────
//
// Drives the REAL pipeline three times — mutate intent, export, publish — and
// then diffs the resulting artifacts against each other. This is the only test
// that proves `GET /api/v1/export/compare` detects differences end-to-end;
// unit tests cover the comparator, but nothing else exercises
// publish → OCI → pull-by-digest → normalize → compare on real bytes.
//
// The load-bearing assertion is v1→v3 (see below): the SAME field is edited
// twice, and the diff must report ONE net change (host-a → host-c), not two.
// That is the property an audit/event-stream changeset structurally cannot
// produce, and it is the whole reason the diff is a content diff.

const e2eServerA = "test-namespace:server-E2E-A"
const e2eServerB = "test-namespace:server-E2E-B"

// newPublishingExport builds an Export handler with the OCI publish callback
// wired, so Trigger runs the full export→bundle→push→sign flow rather than
// falling back to download-only mode.
func newPublishingExport(t *testing.T) *handler.Export {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..")

	ociCfg := oci.Config{
		Registry:       "localhost:5001",
		Repo:           "orbital-e2e",
		SigningKeyPath: filepath.Join(repoRoot, "deploy", "local", "cosign.key"),
		AllowHTTP:      true,
		Timeout:        5 * time.Minute,
	}
	// nil bundlers → raw-export artifact (data + schema layers only), which is
	// all the diff needs; bundling is orthogonal to the graph payload.
	ociH := handler.NewOCI(testDB, ociCfg, scratchExportDir, slog.Default(), 30*time.Second, nil)
	if !ociH.IsPublisherConfigured() {
		t.Skip("OCI publisher not configured (missing cosign key?) — skipping compare e2e")
	}

	exp := newExportHandler(t)
	exp.SetOCIConfig(ociCfg)
	exp.SetPublishFn(ociH.PublishExportedJob)
	return exp
}

// dgraphMutate runs a GraphQL mutation straight against DGraph. Deliberately not
// through orbital's proxy: this test is about the export/compare pipeline, and
// going direct keeps it independent of auth wiring.
func dgraphMutate(t *testing.T, mutation string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": mutation})
	req, err := http.NewRequest(http.MethodPost, testutil.DGraphURL(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build mutation request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("dgraph mutation: %v", err)
	}
	defer resp.Body.Close()

	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode mutation response: %v", err)
	}
	if len(out.Errors) > 0 {
		t.Fatalf("dgraph mutation error: %s", out.Errors[0].Message)
	}
}

// publishAndWait exports + publishes the DC and returns the resulting artifact's
// row ID once it reaches completed with a digest.
func publishAndWait(t *testing.T, exp *handler.Export, label string) int {
	t.Helper()
	jobID := triggerExport(t, exp, testDcID)

	if status := testutil.WaitForExportJob(t, testDB, jobID, 3*time.Minute); string(status) != "completed" {
		job, _ := testDB.ExportJob.Get(context.Background(), jobID)
		msg := ""
		if job != nil && job.Error != nil {
			msg = *job.Error
		}
		t.Fatalf("%s: export job ended %q: %s", label, status, msg)
	}

	// The publish runs inside the same async flow; poll until the artifact row
	// reaches a terminal state.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		art, err := testDB.RegistryArtifact.Query().
			Where(registryartifact.ExportJobID(jobID)).Only(context.Background())
		if err == nil {
			switch art.Status {
			case registryartifact.StatusCompleted:
				if art.Digest == nil || *art.Digest == "" {
					t.Fatalf("%s: artifact completed without a digest", label)
				}
				t.Logf("%s → artifact id=%d tag=%s digest=%s", label, art.ID, art.Tag, (*art.Digest)[:19])
				return art.ID
			case registryartifact.StatusFailed:
				e := ""
				if art.Error != nil {
					e = *art.Error
				}
				t.Fatalf("%s: publish failed: %s", label, e)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: timed out waiting for artifact to publish", label)
		}
		time.Sleep(time.Second)
	}
}

// compareResult mirrors the compare endpoint's JSON (the handler's DTO is
// unexported). Only the fields this test asserts on.
type compareResult struct {
	From struct {
		Tag string `json:"tag"`
	} `json:"from"`
	To struct {
		Tag string `json:"tag"`
	} `json:"to"`
	Summary struct {
		Added     int `json:"added"`
		Removed   int `json:"removed"`
		Modified  int `json:"modified"`
		Unchanged int `json:"unchanged"`
	} `json:"summary"`
	Changes []struct {
		OrbID  string `json:"orbId"`
		Type   string `json:"type"`
		Change string `json:"change"`
		Fields []struct {
			Field  string `json:"field"`
			Before any    `json:"before"`
			After  any    `json:"after"`
		} `json:"fields"`
	} `json:"changes"`
}

func compareArtifacts(t *testing.T, exp *handler.Export, from, to int) compareResult {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/export/compare?from=%d&to=%d", from, to), nil)
	rec := httptest.NewRecorder()
	if err := exp.Compare(e.NewContext(req, rec)); err != nil {
		t.Fatalf("compare %d→%d: %v", from, to, err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("compare %d→%d: expected 200, got %d: %s", from, to, rec.Code, rec.Body.String())
	}
	var out compareResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode compare response: %v", err)
	}
	return out
}

// findChange returns the change entry for orbId, or fails.
func findChange(t *testing.T, res compareResult, orbID string) (kind string, field string, before, after any) {
	t.Helper()
	for _, c := range res.Changes {
		if c.OrbID == orbID {
			if len(c.Fields) == 0 {
				return c.Change, "", nil, nil
			}
			return c.Change, c.Fields[0].Field, c.Fields[0].Before, c.Fields[0].After
		}
	}
	body, _ := json.Marshal(res.Changes)
	t.Fatalf("no change found for %s in %s", orbID, body)
	return "", "", nil, nil
}

func TestExportCompare_AcrossThreePublishes(t *testing.T) {
	exp := newPublishingExport(t)

	// ── v1: one server, hostname host-a ──────────────────────────────────────
	dgraphMutate(t, `mutation { addServer(input: [{
		namespace: "test-namespace"
		orbId: "`+e2eServerA+`"
		name: "e2e-a"
		version: 1
		hostname: "host-a"
		dataCenter: { orbId: "test-dc" }
	}]) { server { orbId } } }`)
	v1 := publishAndWait(t, exp, "v1 (host-a)")

	// ── v2: same server, hostname host-b ─────────────────────────────────────
	dgraphMutate(t, `mutation { updateServer(input: {
		filter: { orbId: { eq: "`+e2eServerA+`" } }
		set: { hostname: "host-b" }
	}) { server { orbId } } }`)
	v2 := publishAndWait(t, exp, "v2 (host-b)")

	// ── v3: hostname host-c AND a second server added ────────────────────────
	dgraphMutate(t, `mutation { updateServer(input: {
		filter: { orbId: { eq: "`+e2eServerA+`" } }
		set: { hostname: "host-c" }
	}) { server { orbId } } }`)
	dgraphMutate(t, `mutation { addServer(input: [{
		namespace: "test-namespace"
		orbId: "`+e2eServerB+`"
		name: "e2e-b"
		version: 1
		hostname: "host-new"
		dataCenter: { orbId: "test-dc" }
	}]) { server { orbId } } }`)
	v3 := publishAndWait(t, exp, "v3 (host-c + second server)")

	// ── consecutive: one field edit shows as exactly one modification ────────
	t.Run("v1 to v2 reports the single field edit", func(t *testing.T) {
		res := compareArtifacts(t, exp, v1, v2)
		if res.Summary.Modified != 1 || res.Summary.Added != 0 || res.Summary.Removed != 0 {
			t.Fatalf("want 1 modified / 0 added / 0 removed, got %+v", res.Summary)
		}
		kind, field, before, after := findChange(t, res, e2eServerA)
		if kind != "modified" || field != "Server.hostname" || before != "host-a" || after != "host-b" {
			t.Errorf("got %s %s: %v → %v; want modified Server.hostname: host-a → host-b", kind, field, before, after)
		}
	})

	// NOTE the modified count is 2, not 1: adding a server also mutates the
	// DataCenter's `servers` edge set. That is correct graph-diff behaviour and
	// worth pinning — a relationship change is a real change to published intent,
	// and it proves edges are diffed (by target orbId), not just scalars.
	t.Run("v2 to v3 reports the edit, the new server, and the parent edge", func(t *testing.T) {
		res := compareArtifacts(t, exp, v2, v3)
		for _, c := range res.Changes {
			t.Logf("  %s %s (%s)", c.Change, c.OrbID, c.Type)
		}
		if res.Summary.Modified != 2 || res.Summary.Added != 1 || res.Summary.Removed != 0 {
			t.Fatalf("want 2 modified / 1 added / 0 removed, got %+v", res.Summary)
		}
		if kind, field, _, _ := findChange(t, res, "test-dc"); kind != "modified" || field != "DataCenter.servers" {
			t.Errorf("data center: got %s %s; want modified DataCenter.servers (the edge set grew)", kind, field)
		}
		if kind, _, before, after := findChange(t, res, e2eServerA); kind != "modified" || before != "host-b" || after != "host-c" {
			t.Errorf("server A: got %s %v → %v; want modified host-b → host-c", kind, before, after)
		}
		if kind, _, _, _ := findChange(t, res, e2eServerB); kind != "added" {
			t.Errorf("server B: got %s, want added", kind)
		}
	})

	// THE load-bearing assertion. host-a → host-b → host-c is TWO edits, but the
	// net delta from v1 to v3 is ONE change (host-a → host-c). An audit/event
	// changeset would report two rows here and could never collapse them — this
	// is precisely why the diff is computed from content, not from the audit log.
	t.Run("v1 to v3 reports the NET delta, not the edit history", func(t *testing.T) {
		res := compareArtifacts(t, exp, v1, v3)
		// 2 modified = server hostname + the DataCenter.servers edge (see above).
		if res.Summary.Modified != 2 || res.Summary.Added != 1 || res.Summary.Removed != 0 {
			t.Fatalf("want 2 modified / 1 added / 0 removed, got %+v", res.Summary)
		}
		kind, field, before, after := findChange(t, res, e2eServerA)
		if kind != "modified" || field != "Server.hostname" || before != "host-a" || after != "host-c" {
			t.Errorf("net delta: got %s %s: %v → %v; want modified Server.hostname: host-a → host-c (NOT the intermediate host-b)",
				kind, field, before, after)
		}
		if after == "host-b" || before == "host-b" {
			t.Error("intermediate value host-b leaked into the net delta — this is an event replay, not a content diff")
		}
	})

	// Direction is load-bearing: before is the value in `from`, after in `to`.
	// A swapped argument order in the handler would invert every change and no
	// other assertion here would catch it.
	t.Run("reversing from and to inverts the diff", func(t *testing.T) {
		res := compareArtifacts(t, exp, v2, v1)
		if res.Summary.Modified != 1 || res.Summary.Added != 0 || res.Summary.Removed != 0 {
			t.Fatalf("want 1 modified / 0 added / 0 removed, got %+v", res.Summary)
		}
		if kind, _, before, after := findChange(t, res, e2eServerA); kind != "modified" || before != "host-b" || after != "host-a" {
			t.Errorf("reversed: got %s %v → %v; want modified host-b → host-a", kind, before, after)
		}
	})

	// Same artifact on both sides must be a clean zero diff.
	t.Run("comparing an artifact to itself is empty", func(t *testing.T) {
		res := compareArtifacts(t, exp, v3, v3)
		if res.Summary.Added+res.Summary.Removed+res.Summary.Modified != 0 || len(res.Changes) != 0 {
			t.Fatalf("self-compare should be empty, got %+v / %d changes", res.Summary, len(res.Changes))
		}
	})
}
