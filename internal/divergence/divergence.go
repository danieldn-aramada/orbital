package divergence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// OverrideEntry is a single field-level divergence between orbital's intent and
// a locally observed override. This is the canonical format orb accepts and
// orbital displays.
type OverrideEntry struct {
	OrbID         string `json:"orbId"`
	Field         string `json:"field"`
	IntendedValue any    `json:"intendedValue"`
	OverrideValue any    `json:"overrideValue"`
	Who           string `json:"who"`
	When          string `json:"when"`
}

// Snapshot is the published divergence state — the full set of currently pending
// overrides at the time of publish. Written to S3 as a single JSON file.
type Snapshot struct {
	PublishedAt string          `json:"publishedAt"`
	Overrides   []OverrideEntry `json:"overrides"`
}

// PublishRecord tracks the last successful publish.
type PublishRecord struct {
	PublishedAt time.Time `json:"publishedAt"`
	S3Key       string    `json:"s3Key"`
}

// Store manages divergence reports locally in DataDir/divergence/.
type Store struct {
	dir string
}

func NewStore(dataDir string) *Store {
	return &Store{dir: filepath.Join(dataDir, "divergence")}
}

func (s *Store) ensureDir() error {
	return os.MkdirAll(s.dir, 0o755)
}

// Save replaces the current set of pending override entries.
func (s *Store) Save(entries []OverrideEntry) error {
	if err := s.ensureDir(); err != nil {
		return fmt.Errorf("divergence store: %w", err)
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("divergence store marshal: %w", err)
	}
	return os.WriteFile(filepath.Join(s.dir, "current.json"), b, 0o644)
}

// Load returns the current set of pending override entries. Returns empty slice
// if no reports have been received yet.
func (s *Store) Load() ([]OverrideEntry, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, "current.json"))
	if os.IsNotExist(err) {
		return []OverrideEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("divergence store read: %w", err)
	}
	var entries []OverrideEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("divergence store unmarshal: %w", err)
	}
	return entries, nil
}

// SavePublishRecord writes the last-published record.
func (s *Store) SavePublishRecord(rec PublishRecord) error {
	if err := s.ensureDir(); err != nil {
		return fmt.Errorf("divergence store: %w", err)
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "published.json"), b, 0o644)
}

// LoadPublishRecord returns the last-published record, or nil if never published.
func (s *Store) LoadPublishRecord() (*PublishRecord, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, "published.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("divergence store read published: %w", err)
	}
	var rec PublishRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("divergence store unmarshal published: %w", err)
	}
	return &rec, nil
}

// Publisher writes divergence snapshots to S3.
type Publisher struct {
	client  *s3.Client
	bucket  string
	ociRepo string // used as path prefix, e.g. "orbital/colo-galleon"
}

type PublisherConfig struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	OCIRepo   string
}

func NewPublisher(ctx context.Context, cfg PublisherConfig) (*Publisher, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("divergence publisher aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = &cfg.Endpoint
			o.UsePathStyle = true
		}
	})

	return &Publisher{
		client:  client,
		bucket:  cfg.Bucket,
		ociRepo: cfg.OCIRepo,
	}, nil
}

// Publish writes a snapshot of the given entries to S3 and returns the S3 key.
func (p *Publisher) Publish(ctx context.Context, entries []OverrideEntry) (string, error) {
	now := time.Now().UTC()
	snap := Snapshot{
		PublishedAt: now.Format(time.RFC3339),
		Overrides:   entries,
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", fmt.Errorf("divergence publish marshal: %w", err)
	}

	// Key: divergence/{oci-repo}/{timestamp}.json
	// Replace slashes in oci-repo are preserved — they form a natural S3 prefix.
	ts := strings.ReplaceAll(now.Format("2006-01-02T15-04-05Z"), ":", "-")
	key := fmt.Sprintf("divergence/%s/%s.json", p.ociRepo, ts)

	_, err = p.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &p.bucket,
		Key:         &key,
		Body:        bytes.NewReader(b),
		ContentType: strPtr("application/json"),
	})
	if err != nil {
		return "", fmt.Errorf("divergence publish s3 put: %w", err)
	}
	return key, nil
}

func strPtr(s string) *string { return &s }
