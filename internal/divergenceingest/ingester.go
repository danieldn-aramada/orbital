// Package divergenceingest polls S3 for divergence snapshots that orbs
// publish under divergence/<datacenter>/ prefixes, deserializes them, and
// persists current divergence entries in orbital's ent store. Entries that
// disappear from the latest snapshot are deleted (resolved-by-disappearance).
//
// The list of data centers to poll is derived from orbital's own
// RegistryArtifact records — orbital only polls for DCs it has previously
// published bundles for. No external registry of orbs needed.
package divergenceingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/internal/divergence"
)

// Config holds the S3 + polling parameters. S3 fields typically mirror the
// backup S3 endpoint — the same bucket is shared by convention.
type Config struct {
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	PollInterval time.Duration
}

// Ingester polls S3 for divergence snapshots and writes them to the ent store.
type Ingester struct {
	db     *ent.Client
	s3     *s3.Client
	bucket string
	logger *slog.Logger

	pollInterval time.Duration

	// lastIngestedByDC tracks the snapshot publishedAt timestamp of the most
	// recent ingest per data center, so the poller skips snapshots it has
	// already processed. Populated lazily — empty map on startup means the
	// first tick re-ingests whatever is latest in S3 (idempotent because of
	// the (dc_orb_id, entry_orb_id, field) unique key — UPSERT preserves
	// first_seen_at).
	lastIngestedByDC map[string]time.Time
}

func New(ctx context.Context, db *ent.Client, cfg Config, logger *slog.Logger) (*Ingester, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("ingester: bucket required")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		// MinIO + many S3-compatible stores don't emit x-amz-checksum-* response
		// headers, which makes the SDK's default "WhenSupported" mode log warnings
		// on every Get/List. WhenRequired matches the pre-2025 behavior.
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)
	if err != nil {
		return nil, fmt.Errorf("ingester aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = &cfg.Endpoint
			o.UsePathStyle = true
		}
	})
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Minute
	}
	return &Ingester{
		db:               db,
		s3:               client,
		bucket:           cfg.Bucket,
		logger:           logger,
		pollInterval:     cfg.PollInterval,
		lastIngestedByDC: make(map[string]time.Time),
	}, nil
}

// Start runs the poll loop until ctx is cancelled. Call as a goroutine.
// Runs one immediate poll on entry, then ticks at pollInterval.
func (i *Ingester) Start(ctx context.Context) {
	i.logger.Info("divergence ingester started", "interval", i.pollInterval)
	if err := i.Poll(ctx); err != nil {
		i.logger.Warn("divergence ingester: initial poll failed", "err", err)
	}
	ticker := time.NewTicker(i.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			i.logger.Info("divergence ingester stopped")
			return
		case <-ticker.C:
			if err := i.Poll(ctx); err != nil {
				i.logger.Warn("divergence ingester: poll failed", "err", err)
			}
		}
	}
}

// Poll runs one ingest cycle: discover DCs from RegistryArtifact, pull the
// latest snapshot per DC from S3, UPSERT entries, DELETE stale rows.
func (i *Ingester) Poll(ctx context.Context) error {
	dcs, err := i.discoverDCs(ctx)
	if err != nil {
		return fmt.Errorf("discover datacenters: %w", err)
	}
	for _, dc := range dcs {
		if err := i.pollDC(ctx, dc); err != nil {
			// Per-DC failure is logged, not fatal — other DCs continue.
			i.logger.Warn("divergence ingester: DC poll failed", "dc", dc.id, "repo", dc.repository, "err", err)
		}
	}
	return nil
}

// dcRef is one (datacenter, repository) pair derived from RegistryArtifact.
// The repository is the S3 prefix component orb writes under.
type dcRef struct {
	id         string // dc_orb_id (e.g. "colo:colo-galleon")
	repository string // OCI repo (e.g. "orbital/colo-galleon") — orb publishes under divergence/<repository>/
}

func (i *Ingester) discoverDCs(ctx context.Context) ([]dcRef, error) {
	rows, err := i.db.RegistryArtifact.Query().
		Where(registryartifact.StatusEQ(registryartifact.StatusCompleted)).
		Select(registryartifact.FieldDatacenterID, registryartifact.FieldRepository).
		All(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var out []dcRef
	for _, r := range rows {
		key := r.DatacenterID + "|" + r.Repository
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, dcRef{id: r.DatacenterID, repository: r.Repository})
	}
	return out, nil
}

// pollDC pulls the latest snapshot for one DC, parses it, and writes the diff
// (UPSERT new/changed entries, DELETE entries no longer present).
func (i *Ingester) pollDC(ctx context.Context, dc dcRef) error {
	prefix := "divergence/" + dc.repository + "/"
	list, err := i.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: &i.bucket,
		Prefix: &prefix,
	})
	if err != nil {
		return fmt.Errorf("list %s: %w", prefix, err)
	}
	if len(list.Contents) == 0 {
		return nil // nothing to ingest yet
	}

	// Filenames are RFC3339-with-colons-replaced timestamps, lexicographically
	// sortable. Latest key wins.
	keys := make([]string, 0, len(list.Contents))
	for _, obj := range list.Contents {
		if obj.Key != nil && strings.HasSuffix(*obj.Key, ".json") {
			keys = append(keys, *obj.Key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	latestKey := keys[len(keys)-1]

	get, err := i.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &i.bucket,
		Key:    &latestKey,
	})
	if err != nil {
		return fmt.Errorf("get %s: %w", latestKey, err)
	}
	defer get.Body.Close()
	body, err := io.ReadAll(get.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", latestKey, err)
	}

	var snap divergence.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return fmt.Errorf("decode %s: %w", latestKey, err)
	}
	publishedAt, err := time.Parse(time.RFC3339, snap.PublishedAt)
	if err != nil {
		return fmt.Errorf("parse publishedAt %q from %s: %w", snap.PublishedAt, latestKey, err)
	}

	// Idempotency: skip if we already ingested this publishedAt for this DC.
	if last, ok := i.lastIngestedByDC[dc.id]; ok && !publishedAt.After(last) {
		return nil
	}

	if err := i.applySnapshot(ctx, dc, publishedAt, snap.Overrides); err != nil {
		return fmt.Errorf("apply snapshot %s: %w", latestKey, err)
	}
	i.lastIngestedByDC[dc.id] = publishedAt
	i.logger.Info("divergence ingester: applied snapshot",
		"dc", dc.id,
		"key", latestKey,
		"publishedAt", snap.PublishedAt,
		"entries", len(snap.Overrides),
	)
	return nil
}
