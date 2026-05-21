//go:build integration

package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	testMinIOEndpoint = "http://localhost:9000"
	testMinIOUser     = "minioadmin"
	testMinIOPassword = "minioadmin"
	TestS3Bucket      = "orbital-test"
	TestS3Region      = "us-east-1"
	TestS3AccessKey   = testMinIOUser
	TestS3SecretKey   = testMinIOPassword

	// TestOCIRegistry is the OCI registry address in the test stack.
	TestOCIRegistry = "localhost:5001"
)

// MinIOEndpoint returns the MinIO endpoint for the test stack.
func MinIOEndpoint() string {
	if v := os.Getenv("TEST_MINIO_ENDPOINT"); v != "" {
		return v
	}
	return testMinIOEndpoint
}

// EnsureTestBucketE creates the test S3 bucket if it doesn't already exist.
// Use in TestMain (which has no *testing.T).
func EnsureTestBucketE() error {
	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(TestS3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(testMinIOUser, testMinIOPassword, "")),
	)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}
	endpoint := MinIOEndpoint()
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})

	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: strPtr(TestS3Bucket),
	})
	if err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		var exists *types.BucketAlreadyExists
		if !errors.As(err, &owned) && !errors.As(err, &exists) {
			return fmt.Errorf("create bucket: %w", err)
		}
	}
	return nil
}

// EnsureTestBucket creates the test S3 bucket if it doesn't already exist.
// Call once in TestMain before any tests that use backup or OCI publish.
func EnsureTestBucket(t *testing.T) {
	t.Helper()

	client := newTestS3Client(t)
	ctx := context.Background()

	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: strPtr(TestS3Bucket),
	})
	if err != nil {
		// BucketAlreadyOwnedByYou and BucketAlreadyExists are fine — bucket exists from a prior run.
		var owned *types.BucketAlreadyOwnedByYou
		var exists *types.BucketAlreadyExists
		if !errors.As(err, &owned) && !errors.As(err, &exists) {
			t.Fatalf("EnsureTestBucket: %v", err)
		}
	}
}

// EmptyTestBucket deletes all objects in the test bucket.
// Call in TestMain or t.Cleanup to leave a clean slate between runs.
func EmptyTestBucket(t *testing.T) {
	t.Helper()

	client := newTestS3Client(t)
	ctx := context.Background()

	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: strPtr(TestS3Bucket),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			t.Fatalf("EmptyTestBucket list: %v", err)
		}
		for _, obj := range page.Contents {
			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: strPtr(TestS3Bucket),
				Key:    obj.Key,
			}); err != nil {
				t.Fatalf("EmptyTestBucket delete %s: %v", *obj.Key, err)
			}
		}
	}
}

func newTestS3Client(t *testing.T) *s3.Client {
	t.Helper()

	ctx := context.Background()
	endpoint := MinIOEndpoint()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(TestS3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(testMinIOUser, testMinIOPassword, "")),
	)
	if err != nil {
		t.Fatalf("newTestS3Client load config: %v", err)
	}

	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})
}

func strPtr(s string) *string { return &s }
