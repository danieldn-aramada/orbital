// Package blobstore is a thin abstraction over S3-compatible object stores
// (MinIO, real AWS S3, etc.) and Azure Blob Storage. It exists to keep the
// `.blob.core.windows.net` routing decision in one place — callers ask for a
// Store and don't care which backend serves their reads and writes.
//
// All operations are keyed by string; the underlying "bucket" or "container"
// is fixed at construction time. Endpoint format determines backend:
//
//   - Contains ".blob.core.windows.net" → Azure Blob via azblob SDK (SharedKey auth)
//   - Otherwise                          → AWS SDK v2 against an S3-compatible endpoint
package blobstore

import (
	"context"
	"io"
	"strings"
	"time"
)

// Store is the storage backend interface. Implementations are S3-compatible
// or Azure-native; callers don't distinguish.
type Store interface {
	// Put writes body at key. contentType may be empty to skip the header.
	Put(ctx context.Context, key string, body io.Reader, contentType string) error

	// Get returns the body of the object at key.
	Get(ctx context.Context, key string) ([]byte, error)

	// List returns object keys under the given prefix, in arbitrary order.
	List(ctx context.Context, prefix string) ([]string, error)

	// Delete removes the object at key. Missing-object behavior is backend-
	// defined: S3 returns success, Azure returns an error. Callers that want
	// idempotent delete should tolerate either outcome.
	Delete(ctx context.Context, key string) error

	// PresignURL returns a time-limited GET URL for the object at key.
	PresignURL(ctx context.Context, key string, ttl time.Duration) (string, error)

	// Ping verifies the bucket/container is reachable with current credentials.
	// Used as a readiness check at startup.
	Ping(ctx context.Context) error
}

// Config is the shape callers pass in. The "S3" naming reflects the env vars
// they consume (ORBITAL_S3_*); Azure backends interpret AccessKey as the
// storage account name and SecretKey as the account key.
type Config struct {
	Endpoint  string
	Region    string // ignored by Azure backend
	Bucket    string // = container, for Azure
	AccessKey string // = storage account name, for Azure
	SecretKey string // = storage account key, for Azure; ignored when UseAzureMI
	UseAzureMI bool
}

// New constructs a Store, sniffing the endpoint to pick the backend.
// Returns an error if the backend's client construction fails.
func New(ctx context.Context, cfg Config) (Store, error) {
	if strings.Contains(cfg.Endpoint, ".blob.core.windows.net") {
		return newAzureStore(cfg.Endpoint, cfg.AccessKey, cfg.SecretKey, cfg.Bucket, cfg.UseAzureMI)
	}
	return newS3Store(ctx, cfg)
}
