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
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/internal/blobstore"
	"github.com/armada/orbital/internal/divergence"
)

// Config holds the storage + polling parameters. Storage fields typically
// mirror the backup endpoint — the same bucket is shared by convention.
type Config struct {
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	PollInterval time.Duration
}

// Ingester polls storage for divergence snapshots and writes them to the ent store.
type Ingester struct {
	db     *ent.Client
	store  blobstore.Store
	logger *slog.Logger

	pollInterval time.Duration

	// lastIngestedByDC tracks the snapshot publishedAt timestamp of the most
	// recent ingest per data center, so the poller skips snapshots it has
	// already processed. Populated lazily — empty map on startup means the
	// first tick re-ingests whatever is latest in storage (idempotent because
	// of the (dc_orb_id, entry_orb_id, field) unique key — UPSERT preserves
	// first_seen_at).
	lastIngestedByDC map[string]time.Time
}

func New(ctx context.Context, db *ent.Client, cfg Config, logger *slog.Logger) (*Ingester, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("ingester: bucket required")
	}
	store, err := blobstore.New(ctx, blobstore.Config{
		Endpoint:  cfg.Endpoint,
		Region:    cfg.Region,
		Bucket:    cfg.Bucket,
		AccessKey: cfg.AccessKey,
		SecretKey: cfg.SecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("ingester: %w", err)
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Minute
	}
	return &Ingester{
		db:               db,
		store:            store,
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
// latest snapshot per DC from storage, UPSERT entries, DELETE stale rows.
//
// Observability: emits a DEBUG log per tick listing every prefix that will be
// polled. This is the single greppable surface for "why isn't my divergence
// landing?" — without it, a path-prefix mismatch (the bug class that motivated
// this package's existence) is silently invisible. Operators can enable DEBUG
// when troubleshooting; in normal operation the line is filtered out.
func (i *Ingester) Poll(ctx context.Context) error {
	dcs, err := i.discoverDCs(ctx)
	if err != nil {
		return fmt.Errorf("discover datacenters: %w", err)
	}
	if len(dcs) == 0 {
		i.logger.Debug("divergence ingester: no DCs discovered (no completed registry artifacts yet)")
		return nil
	}
	prefixes := make([]string, 0, len(dcs))
	for _, dc := range dcs {
		prefixes = append(prefixes, divergence.PrefixForRepo(dc.repository))
	}
	i.logger.Debug("divergence ingester: polling",
		"dc_count", len(dcs),
		"prefixes", prefixes)
	for _, dc := range dcs {
		if err := i.pollDC(ctx, dc); err != nil {
			// Per-DC failure is logged, not fatal — other DCs continue.
			i.logger.Warn("divergence ingester: DC poll failed", "dc", dc.id, "repo", dc.repository, "err", err)
		}
	}
	return nil
}

// dcRef is one (datacenter, repository) pair derived from RegistryArtifact.
// The repository is the storage prefix component orb writes under.
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
		// Normalize via the shared helper so producer (orb) and consumer
		// (this ingester) can't drift on the S3 path convention. See
		// internal/divergence/path.go for the canonical encoding.
		repoPath := divergence.NormalizeRepoPath(r.Repository)
		key := r.DatacenterID + "|" + repoPath
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, dcRef{id: r.DatacenterID, repository: repoPath})
	}
	return out, nil
}

// pollDC pulls the latest snapshot for one DC, parses it, and writes the diff
// (UPSERT new/changed entries, DELETE entries no longer present).
func (i *Ingester) pollDC(ctx context.Context, dc dcRef) error {
	prefix := divergence.PrefixForRepo(dc.repository)
	keys, err := i.store.List(ctx, prefix)
	if err != nil {
		return fmt.Errorf("list %s: %w", prefix, err)
	}
	if len(keys) == 0 {
		return nil // nothing to ingest yet
	}

	// Filenames are RFC3339-with-colons-replaced timestamps, lexicographically
	// sortable. Latest key wins.
	jsonKeys := make([]string, 0, len(keys))
	for _, k := range keys {
		if strings.HasSuffix(k, ".json") {
			jsonKeys = append(jsonKeys, k)
		}
	}
	if len(jsonKeys) == 0 {
		return nil
	}
	sort.Strings(jsonKeys)
	latestKey := jsonKeys[len(jsonKeys)-1]

	body, err := i.store.Get(ctx, latestKey)
	if err != nil {
		return fmt.Errorf("get %s: %w", latestKey, err)
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
	// DEBUG-log the skip so operators can distinguish "ingestion working,
	// nothing new" from "ingestion broken, no logs at all."
	if last, ok := i.lastIngestedByDC[dc.id]; ok && !publishedAt.After(last) {
		i.logger.Debug("divergence ingester: snapshot unchanged since last ingest",
			"dc", dc.id, "publishedAt", snap.PublishedAt)
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
