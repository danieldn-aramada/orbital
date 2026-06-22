//go:build integration

package divergenceingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	_ "github.com/lib/pq"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/divergenceresolution"
	"github.com/armada/orbital/ent/migrate"
	"github.com/armada/orbital/internal/divergence"
	"github.com/armada/orbital/internal/divergenceingest"
	"github.com/armada/orbital/internal/testutil"
)

var testDB *ent.Client

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		log.Fatalf("divergenceingest integration test setup: %v", err)
	}
	os.Exit(m.Run())
}

func setup() error {
	var err error
	testDB, err = ent.Open("postgres", testutil.TestDatabaseURL())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	if err := testDB.Schema.Create(context.Background(), migrate.WithDropColumn(true)); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	if err := testutil.TruncateAllE(); err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	if err := testutil.EnsureTestBucketE(); err != nil {
		return fmt.Errorf("ensure test bucket: %w", err)
	}
	if err := testutil.ResetDGraphE(testutil.DGraphAdminURL(), schemaPath()); err != nil {
		return fmt.Errorf("reset dgraph: %w", err)
	}
	return nil
}

// schemaPath locates the schema file relative to this test file. Avoids relying
// on cwd, which differs between `go test ./...` and direct package runs.
func schemaPath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "schema", "schema.graphql")
}

// resetState wipes the DB and S3 bucket so each test starts from scratch.
func resetState(t *testing.T) {
	t.Helper()
	if err := testutil.TruncateAllE(); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	testutil.EmptyTestBucket(t)
	if err := testutil.ResetDGraphE(testutil.DGraphAdminURL(), schemaPath()); err != nil {
		t.Fatalf("reset dgraph: %v", err)
	}
}

// seedDC creates a Namespace + DataCenter in DGraph with the given orbId and
// name. The ingester discovers DCs by querying queryDataCenter, then computes
// each one's report prefix via oci.RepoForDC — so name must slugify to the
// expected repo path component (e.g. "colo-galleon" → repo "orbital/colo-galleon").
func seedDC(t *testing.T, orbID, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	post := func(query string) {
		body, _ := json.Marshal(map[string]string{"query": query})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, testutil.DGraphURL(), bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build dgraph request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("dgraph request: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var r struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(raw, &r)
		if len(r.Errors) > 0 {
			t.Fatalf("dgraph: %s — query: %s", r.Errors[0].Message, query)
		}
	}

	post(`mutation { addNamespace(input: [{ name: "test-ns" }]) { namespace { id } } }`)
	post(fmt.Sprintf(`mutation { addDataCenter(input: [{
		orbId: %q
		name: %q
		namespace: "test-ns"
		version: 1
	}]) { dataCenter { id } } }`, orbID, name))
}

// putReport writes a report JSON to s3://orbital-test/divergence/<repo>/<ts>.json
// and returns the key. Uses keyTime as the filename component (RFC3339 with colons
// preserved — the ingester only requires lexicographic sortability).
func putReport(t *testing.T, repo string, snap divergence.Report, keyTime string) string {
	t.Helper()
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	key := fmt.Sprintf("divergence/%s/%s.json", repo, keyTime)
	client := newS3(t)
	bucket := testutil.TestS3Bucket
	_, err = client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		t.Fatalf("PutObject %s: %v", key, err)
	}
	return key
}

func newS3(t *testing.T) *s3.Client {
	t.Helper()
	ctx := context.Background()
	endpoint := testutil.MinIOEndpoint()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(testutil.TestS3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(testutil.TestS3AccessKey, testutil.TestS3SecretKey, "")),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})
}

func newIngester(t *testing.T) *divergenceingest.Ingester {
	t.Helper()
	ctx := context.Background()
	ing, err := divergenceingest.New(ctx, testDB, divergenceingest.Config{
		Endpoint:     testutil.MinIOEndpoint(),
		Region:       testutil.TestS3Region,
		Bucket:       testutil.TestS3Bucket,
		AccessKey:    testutil.TestS3AccessKey,
		SecretKey:    testutil.TestS3SecretKey,
		PollInterval: time.Hour, // we only call Poll() directly
		DGraphURL:    testutil.DGraphURL(),
		Registry:     testutil.TestOCIRegistry,
		RepoPrefix:   "orbital",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("ingester New: %v", err)
	}
	return ing
}

func listEntries(t *testing.T, dcID string) []*ent.DivergenceEntry {
	t.Helper()
	rows, err := testDB.DivergenceEntry.Query().
		Where(divergenceentry.DcOrbID(dcID)).
		Order(ent.Asc(divergenceentry.FieldField)).
		All(context.Background())
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	return rows
}

func TestPoll_IngestsLatestReport(t *testing.T) {
	resetState(t)
	const dcID, repo, dcName = "netbox:colo-galleon", "orbital/colo-galleon", "colo-galleon"
	seedDC(t, dcID, dcName)

	// Two reports — only the latest should be applied.
	older := divergence.Report{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	latest := divergence.Report{
		PublishedAt: "2026-06-02T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", Type: "IdracSettings", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
			{OrbID: "netbox:server-02", Field: "powerLimit", Type: "Server", IntendedValue: 500, OverrideValue: 750, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
		},
	}
	putReport(t, repo, older, "2026-06-01T000000Z")
	putReport(t, repo, latest, "2026-06-02T000000Z")

	ing := newIngester(t)
	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	rows := listEntries(t, dcID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 entries from latest report, got %d", len(rows))
	}
	if rows[0].Field != "powerLimit" || rows[0].EntryOrbID != "netbox:server-02" {
		t.Errorf("row[0] mismatch: %+v", rows[0])
	}
	if rows[0].TypeName != "Server" {
		t.Errorf("row[0] type_name: got %q, want %q", rows[0].TypeName, "Server")
	}
	if rows[1].Field != "sshEnabled" || rows[1].EntryOrbID != "netbox:server-01" {
		t.Errorf("row[1] mismatch: %+v", rows[1])
	}
	if rows[1].TypeName != "IdracSettings" {
		t.Errorf("row[1] type_name: got %q, want %q", rows[1].TypeName, "IdracSettings")
	}
}

func TestPoll_DeletesEntriesAbsentFromLatestReport(t *testing.T) {
	resetState(t)
	const dcID, repo, dcName = "netbox:colo-galleon", "orbital/colo-galleon", "colo-galleon"
	seedDC(t, dcID, dcName)

	// First report: two entries.
	first := divergence.Report{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
			{OrbID: "netbox:server-02", Field: "powerLimit", IntendedValue: 500, OverrideValue: 750, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	putReport(t, repo, first, "2026-06-01T000000Z")

	ing := newIngester(t)
	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll #1: %v", err)
	}
	if got := len(listEntries(t, dcID)); got != 2 {
		t.Fatalf("after first poll: expected 2 entries, got %d", got)
	}

	// Second report: server-02 entry gone — should be deleted.
	second := divergence.Report{
		PublishedAt: "2026-06-02T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
		},
	}
	putReport(t, repo, second, "2026-06-02T000000Z")

	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll #2: %v", err)
	}
	rows := listEntries(t, dcID)
	if len(rows) != 1 {
		t.Fatalf("after second poll: expected 1 entry, got %d", len(rows))
	}
	if rows[0].EntryOrbID != "netbox:server-01" {
		t.Errorf("surviving entry mismatch: %+v", rows[0])
	}
}

func TestPoll_PreservesFirstSeenOnRepeatedEntry(t *testing.T) {
	resetState(t)
	const dcID, repo, dcName = "netbox:colo-galleon", "orbital/colo-galleon", "colo-galleon"
	seedDC(t, dcID, dcName)

	first := divergence.Report{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	putReport(t, repo, first, "2026-06-01T000000Z")

	ing := newIngester(t)
	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll #1: %v", err)
	}
	before := listEntries(t, dcID)
	if len(before) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(before))
	}
	firstSeen := before[0].FirstSeenAt

	// Re-emit the same entry under a newer report — first_seen_at must not move.
	second := divergence.Report{
		PublishedAt: "2026-06-05T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-05T00:00:00Z"},
		},
	}
	putReport(t, repo, second, "2026-06-05T000000Z")

	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll #2: %v", err)
	}
	after := listEntries(t, dcID)
	if len(after) != 1 {
		t.Fatalf("expected 1 entry after second poll, got %d", len(after))
	}
	if !after[0].FirstSeenAt.Equal(firstSeen) {
		t.Errorf("first_seen_at changed: was %v now %v", firstSeen, after[0].FirstSeenAt)
	}
	if !after[0].LastSeenAt.After(firstSeen) {
		t.Errorf("last_seen_at should advance: was %v now %v", firstSeen, after[0].LastSeenAt)
	}
}

// TestPoll_SupersedesPriorResolutionsOnContentChange pins ADR 012: when a new
// report's content (set of orbId/field/override tuples) differs from what
// orbital has stored for the DC, all prior entries AND their resolutions are
// dropped. The operator must re-decide every row in the new report.
//
// Regression class: a future change that decides to "merge" reports
// (keeping resolutions for unchanged tuples) reintroduces the inferrability
// bug — two identical-looking rows could end up with different decision
// states without on-screen explanation.
func TestPoll_SupersedesPriorResolutionsOnContentChange(t *testing.T) {
	resetState(t)
	ctx := context.Background()
	const dcID, repo, dcName = "netbox:colo-galleon", "orbital/colo-galleon", "colo-galleon"
	seedDC(t, dcID, dcName)

	// First report: 2 entries. Operator resolves both.
	first := divergence.Report{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", Type: "IdracSettings", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
			{OrbID: "netbox:server-01", Field: "ipmiEnabled", Type: "IdracSettings", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	putReport(t, repo, first, "2026-06-01T000000Z")
	ing := newIngester(t)
	if err := ing.Poll(ctx); err != nil {
		t.Fatalf("Poll #1: %v", err)
	}
	for _, f := range []string{"sshEnabled", "ipmiEnabled"} {
		if _, err := testDB.DivergenceResolution.Create().
			SetEntryOrbID("netbox:server-01").
			SetField(f).
			SetAction(divergenceresolution.ActionAccept).
			SetActor("admin@test.com").
			SetDecidedAt(time.Now().UTC()).
			Save(ctx); err != nil {
			t.Fatalf("seed %s resolution: %v", f, err)
		}
	}

	// Second report has content-different shape (one entry has changed override).
	// Under ADR 012 supersede: BOTH resolutions are dropped — operator must re-decide.
	second := divergence.Report{
		PublishedAt: "2026-06-02T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", Type: "IdracSettings", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
			{OrbID: "netbox:server-01", Field: "ipmiEnabled", Type: "IdracSettings", IntendedValue: false, OverrideValue: false, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
		},
	}
	putReport(t, repo, second, "2026-06-02T000000Z")
	if err := ing.Poll(ctx); err != nil {
		t.Fatalf("Poll #2: %v", err)
	}

	// Entries: 2, all fresh (first_seen advanced because supersede wiped + inserted).
	rows := listEntries(t, dcID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 entries after supersede, got %d", len(rows))
	}
	expectedFirstSeen, _ := time.Parse(time.RFC3339, "2026-06-02T00:00:00Z")
	for _, r := range rows {
		if !r.FirstSeenAt.Equal(expectedFirstSeen) {
			t.Errorf("supersede must reset first_seen_at; row %s.%s has %v, want %v",
				r.EntryOrbID, r.Field, r.FirstSeenAt, expectedFirstSeen)
		}
	}

	// Resolutions: both gone — including the sshEnabled one whose tuple was unchanged.
	// Full supersede is the principle; partial preservation is explicitly rejected.
	resolutionCount, err := testDB.DivergenceResolution.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count resolutions: %v", err)
	}
	if resolutionCount != 0 {
		t.Errorf("expected ALL resolutions dropped on supersede, got %d", resolutionCount)
	}
}

// TestPoll_PreservesResolutionsOnIdenticalContent pins the no-op path of
// ADR 012: when a new report's content matches what orbital has stored, the
// ingester touches timestamps but doesn't drop resolutions. Operator's
// decisions persist as long as orb keeps publishing the same divergence.
func TestPoll_PreservesResolutionsOnIdenticalContent(t *testing.T) {
	resetState(t)
	ctx := context.Background()
	const dcID, repo, dcName = "netbox:colo-galleon", "orbital/colo-galleon", "colo-galleon"
	seedDC(t, dcID, dcName)

	report := func(when string) divergence.Report {
		return divergence.Report{
			PublishedAt: when,
			Overrides: []divergence.OverrideEntry{
				{OrbID: "netbox:server-01", Field: "sshEnabled", Type: "IdracSettings", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: when},
			},
		}
	}
	putReport(t, repo, report("2026-06-01T00:00:00Z"), "2026-06-01T000000Z")
	ing := newIngester(t)
	if err := ing.Poll(ctx); err != nil {
		t.Fatalf("Poll #1: %v", err)
	}

	// Operator resolves.
	if _, err := testDB.DivergenceResolution.Create().
		SetEntryOrbID("netbox:server-01").
		SetField("sshEnabled").
		SetAction(divergenceresolution.ActionReject).
		SetActor("admin@test.com").
		SetDecidedAt(time.Now().UTC()).
		Save(ctx); err != nil {
		t.Fatalf("seed resolution: %v", err)
	}

	// Identical content republished — resolution must survive.
	putReport(t, repo, report("2026-06-02T00:00:00Z"), "2026-06-02T000000Z")
	if err := ing.Poll(ctx); err != nil {
		t.Fatalf("Poll #2: %v", err)
	}

	resolutionCount, err := testDB.DivergenceResolution.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count resolutions: %v", err)
	}
	if resolutionCount != 1 {
		t.Errorf("identical content must preserve resolutions, got %d", resolutionCount)
	}

	// last_seen_at should advance (touch path), first_seen_at preserved.
	rows := listEntries(t, dcID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(rows))
	}
	expectedFirstSeen, _ := time.Parse(time.RFC3339, "2026-06-01T00:00:00Z")
	if !rows[0].FirstSeenAt.Equal(expectedFirstSeen) {
		t.Errorf("first_seen_at must not move on identical content; got %v, want %v",
			rows[0].FirstSeenAt, expectedFirstSeen)
	}
	expectedLastSeen, _ := time.Parse(time.RFC3339, "2026-06-02T00:00:00Z")
	if !rows[0].LastSeenAt.Equal(expectedLastSeen) {
		t.Errorf("last_seen_at must advance to latest publish; got %v, want %v",
			rows[0].LastSeenAt, expectedLastSeen)
	}
}

func TestPoll_NoArtifactsMeansNoPoll(t *testing.T) {
	resetState(t)
	const repo = "orbital/colo-galleon"
	// No RegistryArtifact rows seeded — discoverDCs returns empty.

	// Even with a report present, no DC = no ingest.
	snap := divergence.Report{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	putReport(t, repo, snap, "2026-06-01T000000Z")

	ing := newIngester(t)
	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rows, err := testDB.DivergenceEntry.Query().All(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 entries when no DC discovered, got %d", len(rows))
	}
}
