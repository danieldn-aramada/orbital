//go:build integration

package divergence

import (
	"context"
	"encoding/json"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/armada/orbital/internal/testutil"
)

func newTestPublisher(t *testing.T) *Publisher {
	t.Helper()
	ctx := context.Background()
	p, err := NewPublisher(ctx, PublisherConfig{
		Endpoint:  testutil.MinIOEndpoint(),
		Region:    testutil.TestS3Region,
		Bucket:    testutil.TestS3Bucket,
		AccessKey: testutil.TestS3AccessKey,
		SecretKey: testutil.TestS3SecretKey,
		OCIRepo:   "orbital/colo-galleon",
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	return p
}

func getS3Object(t *testing.T, key string) []byte {
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
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})
	bucket := testutil.TestS3Bucket
	out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		t.Fatalf("GetObject %s: %v", key, err)
	}
	defer out.Body.Close()
	var buf []byte
	b := make([]byte, 4096)
	for {
		n, err := out.Body.Read(b)
		buf = append(buf, b[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}

func TestPublisher_Publish_WritesToMinIO(t *testing.T) {
	testutil.EnsureTestBucket(t)
	p := newTestPublisher(t)

	entries := []OverrideEntry{
		{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
	}
	key, err := p.Publish(context.Background(), entries)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Stable-key contract: overwrite-in-place at a fixed filename per repo.
	if want := "divergence/orbital/colo-galleon/report.json"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}

	// Verify body is a valid Report.
	body := getS3Object(t, key)
	var snap Report
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if snap.PublishedAt == "" {
		t.Error("PublishedAt should be set")
	}
	if len(snap.Overrides) != 1 || snap.Overrides[0].OrbID != "netbox:server-01" {
		t.Errorf("overrides mismatch: %+v", snap.Overrides)
	}
}

// Regression guard for the Terraform-state pattern: two consecutive publishes
// must produce (a) the same key and (b) the second call's body in storage.
// Without this test, someone could reintroduce timestamped keys and every
// other test would still pass.
func TestPublisher_Publish_OverwritesAtFixedKey(t *testing.T) {
	testutil.EnsureTestBucket(t)
	p := newTestPublisher(t)
	ctx := context.Background()

	firstEntries := []OverrideEntry{
		{OrbID: "netbox:server-01", Field: "sshEnabled", IntendedValue: false, OverrideValue: true, Who: "local:admin", When: "2026-06-01T00:00:00Z"},
	}
	secondEntries := []OverrideEntry{
		{OrbID: "netbox:server-02", Field: "ipmiEnabled", IntendedValue: true, OverrideValue: false, Who: "local:admin", When: "2026-06-02T00:00:00Z"},
	}

	firstKey, err := p.Publish(ctx, firstEntries)
	if err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	secondKey, err := p.Publish(ctx, secondEntries)
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if firstKey != secondKey {
		t.Fatalf("key drift: first=%q second=%q — publish is not overwrite-in-place", firstKey, secondKey)
	}

	body := getS3Object(t, secondKey)
	var snap Report
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(snap.Overrides) != 1 || snap.Overrides[0].OrbID != "netbox:server-02" {
		t.Fatalf("second publish did not overwrite: got %+v", snap.Overrides)
	}
}

func TestPublisher_Publish_EmptyEntries(t *testing.T) {
	testutil.EnsureTestBucket(t)
	p := newTestPublisher(t)

	key, err := p.Publish(context.Background(), nil)
	if err != nil {
		t.Fatalf("Publish nil entries: %v", err)
	}

	body := getS3Object(t, key)
	var snap Report
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if len(snap.Overrides) != 0 {
		t.Errorf("expected 0 overrides, got %d", len(snap.Overrides))
	}
}
