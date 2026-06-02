//go:build integration

package divergence

import (
	"context"
	"encoding/json"
	"strings"
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

	// Key format: divergence/{oci-repo}/{timestamp}.json
	if !strings.HasPrefix(key, "divergence/orbital/colo-galleon/") {
		t.Errorf("key prefix mismatch: %q", key)
	}
	if !strings.HasSuffix(key, ".json") {
		t.Errorf("key suffix mismatch: %q", key)
	}

	// Verify body is a valid Snapshot.
	body := getS3Object(t, key)
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.PublishedAt == "" {
		t.Error("PublishedAt should be set")
	}
	if len(snap.Overrides) != 1 || snap.Overrides[0].OrbID != "netbox:server-01" {
		t.Errorf("overrides mismatch: %+v", snap.Overrides)
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
	var snap Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if len(snap.Overrides) != 0 {
		t.Errorf("expected 0 overrides, got %d", len(snap.Overrides))
	}
}
