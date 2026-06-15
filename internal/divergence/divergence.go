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

	"github.com/armada/orbital/internal/blobstore"
)

// OverrideEntry is a single field-level divergence between orbital's intent and
// a locally observed override. This is the canonical format orb accepts and
// orbital displays.
//
// IntendedAtVersion is populated by orb's intake handler from orb's local
// DGraph at the moment the report arrives — producers (cb-controller, etc.)
// do not need to send it. It anchors the observation to the ConfigItem
// version orb believed intent was at when this report was observed, which
// orbital uses for MVCC race detection at Accept time.
type OverrideEntry struct {
	OrbID             string `json:"orbId"`
	Field             string `json:"field"`
	Type              string `json:"type,omitempty"` // orbital GraphQL type name (e.g. "Server"); empty for legacy producers
	IntendedValue     any    `json:"intendedValue"`
	OverrideValue     any    `json:"overrideValue"`
	IntendedAtVersion *int   `json:"intendedAtVersion,omitempty"`
	Who               string `json:"who"`
	When              string `json:"when"`
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
	return writeAtomic(filepath.Join(s.dir, "current.json"), b)
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
	return writeAtomic(filepath.Join(s.dir, "published.json"), b)
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

// Publisher writes divergence snapshots to S3-compatible or Azure Blob storage.
// The choice of backend is hidden behind blobstore.Store.
type Publisher struct {
	store   blobstore.Store
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
	store, err := blobstore.New(ctx, blobstore.Config{
		Endpoint:  cfg.Endpoint,
		Region:    cfg.Region,
		Bucket:    cfg.Bucket,
		AccessKey: cfg.AccessKey,
		SecretKey: cfg.SecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("divergence publisher: %w", err)
	}
	return &Publisher{store: store, ociRepo: cfg.OCIRepo}, nil
}

// Publish writes a snapshot of the given entries to storage and returns the key.
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

	// Slashes in oci-repo become natural prefix separators inside SnapshotKey.
	ts := strings.ReplaceAll(now.Format("2006-01-02T15-04-05Z"), ":", "-")
	key := SnapshotKey(p.ociRepo, ts)

	if err := p.store.Put(ctx, key, bytes.NewReader(b), "application/json"); err != nil {
		return "", fmt.Errorf("divergence publish put: %w", err)
	}
	return key, nil
}

// writeAtomic writes data to path via a tmp+rename so a crash mid-write can't
// leave a truncated/corrupt file. POSIX rename is atomic on the same filesystem.
// Mirrors the same helper in the orb package; consolidate to a shared package
// if a third copy ever shows up.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
