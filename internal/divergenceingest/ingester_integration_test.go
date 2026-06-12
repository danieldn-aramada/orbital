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
	"os"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/migrate"
	"github.com/armada/orbital/ent/registryartifact"
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
	return nil
}

// resetState wipes the DB and S3 bucket so each test starts from scratch.
func resetState(t *testing.T) {
	t.Helper()
	if err := testutil.TruncateAllE(); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	testutil.EmptyTestBucket(t)
}

// seedDC registers a RegistryArtifact in StatusCompleted so the ingester
// will discover this datacenter on poll.
func seedDC(t *testing.T, dcID, repo string) {
	t.Helper()
	ctx := context.Background()
	_, err := testDB.RegistryArtifact.Create().
		SetExportJobID(uuid.New()).
		SetDatacenterID(dcID).
		SetDatacenterName(dcID).
		SetRegistry(testutil.TestOCIRegistry).
		SetRepository(repo).
		SetTag("latest").
		SetStatus(registryartifact.StatusCompleted).
		SetInitiatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed registry artifact: %v", err)
	}
}

// putSnapshot writes a snapshot JSON to s3://orbital-test/divergence/<repo>/<ts>.json
// and returns the key. Uses keyTime as the filename component (RFC3339 with colons
// preserved — the ingester only requires lexicographic sortability).
func putSnapshot(t *testing.T, repo string, snap divergence.Snapshot, keyTime string) string {
	t.Helper()
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
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

func TestPoll_IngestsLatestSnapshot(t *testing.T) {
	resetState(t)
	const dcID, repo = "netbox:colo-galleon", "orbital/colo-galleon"
	seedDC(t, dcID, repo)

	// Two snapshots — only the latest should be applied.
	older := divergence.Snapshot{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	latest := divergence.Snapshot{
		PublishedAt: "2026-06-02T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", Type: "IdracSettings", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
			{OrbID: "netbox:server-02", Field: "powerLimit", Type: "Server", IntendedValue: 500, OverrideValue: 750, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
		},
	}
	putSnapshot(t, repo, older, "2026-06-01T000000Z")
	putSnapshot(t, repo, latest, "2026-06-02T000000Z")

	ing := newIngester(t)
	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	rows := listEntries(t, dcID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 entries from latest snapshot, got %d", len(rows))
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

func TestPoll_DeletesEntriesAbsentFromLatest(t *testing.T) {
	resetState(t)
	const dcID, repo = "netbox:colo-galleon", "orbital/colo-galleon"
	seedDC(t, dcID, repo)

	// First snapshot: two entries.
	first := divergence.Snapshot{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
			{OrbID: "netbox:server-02", Field: "powerLimit", IntendedValue: 500, OverrideValue: 750, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	putSnapshot(t, repo, first, "2026-06-01T000000Z")

	ing := newIngester(t)
	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll #1: %v", err)
	}
	if got := len(listEntries(t, dcID)); got != 2 {
		t.Fatalf("after first poll: expected 2 entries, got %d", got)
	}

	// Second snapshot: server-02 entry gone — should be deleted.
	second := divergence.Snapshot{
		PublishedAt: "2026-06-02T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
		},
	}
	putSnapshot(t, repo, second, "2026-06-02T000000Z")

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
	const dcID, repo = "netbox:colo-galleon", "orbital/colo-galleon"
	seedDC(t, dcID, repo)

	first := divergence.Snapshot{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	putSnapshot(t, repo, first, "2026-06-01T000000Z")

	ing := newIngester(t)
	if err := ing.Poll(context.Background()); err != nil {
		t.Fatalf("Poll #1: %v", err)
	}
	before := listEntries(t, dcID)
	if len(before) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(before))
	}
	firstSeen := before[0].FirstSeenAt

	// Re-emit the same entry under a newer snapshot — first_seen_at must not move.
	second := divergence.Snapshot{
		PublishedAt: "2026-06-05T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-05T00:00:00Z"},
		},
	}
	putSnapshot(t, repo, second, "2026-06-05T000000Z")

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

func TestPoll_NoArtifactsMeansNoPoll(t *testing.T) {
	resetState(t)
	const repo = "orbital/colo-galleon"
	// No RegistryArtifact rows seeded — discoverDCs returns empty.

	// Even with a snapshot present, no DC = no ingest.
	snap := divergence.Snapshot{
		PublishedAt: "2026-06-01T00:00:00Z",
		Overrides: []divergence.OverrideEntry{
			{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
		},
	}
	putSnapshot(t, repo, snap, "2026-06-01T000000Z")

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
