// Package divergenceingest polls S3 for divergence reports that orbs
// publish under divergence/<datacenter>/ prefixes, deserializes them, and
// persists current divergence entries in orbital's ent store. Entries that
// disappear from the latest report are deleted (resolved-by-disappearance).
//
// The list of data centers to poll is derived from DGraph — orbital queries
// its own DataCenter nodes (the source-of-truth for what DCs exist) and
// computes each DC's expected report prefix from the same OCI repo
// convention the publisher uses. DCs deleted from DGraph stop being polled;
// renames take effect immediately; stale publish-history rows in PostgreSQL
// don't generate phantom polls.
package divergenceingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/internal/blobstore"
	"github.com/armada/orbital/internal/divergence"
	"github.com/armada/orbital/internal/oci"
)

// Config holds the storage + polling parameters and the DGraph + OCI
// coordinates needed for DC discovery. Storage fields typically mirror the
// backup endpoint — the same bucket is shared by convention.
type Config struct {
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	PollInterval time.Duration

	// DC discovery: orbital queries its own DGraph for live DataCenter nodes
	// and computes each DC's report prefix using the same RepoForDC
	// convention as the publisher. All three must be set or discovery fails.
	DGraphURL  string // e.g. http://dgraph-blue:8080/graphql
	Registry   string // e.g. armadaeksatest.azurecr.io (matches ORBITAL_OCI_REGISTRY)
	RepoPrefix string // e.g. orbital (matches ORBITAL_OCI_REPO)
}

// Ingester polls storage for divergence reports and writes them to the ent store.
type Ingester struct {
	db     *ent.Client
	store  blobstore.Store
	logger *slog.Logger

	pollInterval time.Duration

	dgraphURL  string
	registry   string
	repoPrefix string
	httpClient *http.Client

	// lastIngestedByDC tracks the report publishedAt timestamp of the most
	// recent ingest per data center, so the poller skips reports it has
	// already processed. Populated lazily — empty map on startup means the
	// first tick re-ingests whatever is latest in storage. Guarded by mu
	// because ResetDC can mutate from an HTTP handler concurrently with the
	// poll goroutine.
	mu               sync.Mutex
	lastIngestedByDC map[string]time.Time
}

// ResetDC forgets the last-ingested timestamp for a DC, causing the next poll
// to re-process the latest S3 report fresh. Used by the orbital "clear
// divergence" break-glass: HTTP handler wipes the DB rows, then calls this so
// the ingester doesn't skip-as-already-seen.
func (i *Ingester) ResetDC(dcID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.lastIngestedByDC, dcID)
}

func New(ctx context.Context, db *ent.Client, cfg Config, logger *slog.Logger) (*Ingester, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("ingester: bucket required")
	}
	if cfg.DGraphURL == "" {
		return nil, errors.New("ingester: DGraphURL required for DC discovery")
	}
	if cfg.RepoPrefix == "" {
		return nil, errors.New("ingester: RepoPrefix required for report path computation")
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
		dgraphURL:        cfg.DGraphURL,
		registry:         cfg.Registry,
		repoPrefix:       cfg.RepoPrefix,
		httpClient:       &http.Client{Timeout: 10 * time.Second},
		lastIngestedByDC: make(map[string]time.Time),
	}, nil
}

// Start runs the poll loop until ctx is cancelled. Call as a goroutine.
// Runs one immediate poll on entry, then ticks at pollInterval.
func (i *Ingester) Start(ctx context.Context) {
	i.logger.Info("divergence ingester started", "interval", i.pollInterval.String())
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
// latest report per DC from storage, UPSERT entries, DELETE stale rows.
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

// discoverDCs queries DGraph for live DataCenter nodes and computes each one's
// report prefix. Uses oci.RepoForDC (same helper as the publisher) plus
// divergence.NormalizeRepoPath so producer and consumer share path logic.
// DCs without name or orbId are skipped defensively.
func (i *Ingester) discoverDCs(ctx context.Context) ([]dcRef, error) {
	body, err := json.Marshal(map[string]string{
		"query": "{ queryDataCenter { orbId name } }",
	})
	if err != nil {
		return nil, fmt.Errorf("marshal discovery query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.dgraphURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build discovery request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("discovery: dgraph HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data struct {
			QueryDataCenter []struct {
				OrbID string `json:"orbId"`
				Name  string `json:"name"`
			} `json:"queryDataCenter"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode discovery response: %w", err)
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("discovery: dgraph: %s", parsed.Errors[0].Message)
	}
	out := make([]dcRef, 0, len(parsed.Data.QueryDataCenter))
	for _, dc := range parsed.Data.QueryDataCenter {
		if dc.OrbID == "" || dc.Name == "" {
			continue
		}
		repoPath := divergence.NormalizeRepoPath(oci.RepoForDC(i.registry, i.repoPrefix, dc.Name))
		out = append(out, dcRef{id: dc.OrbID, repository: repoPath})
	}
	return out, nil
}

// pollDC pulls the latest report for one DC, parses it, and writes the diff
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

	var snap divergence.Report
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
	i.mu.Lock()
	last, ok := i.lastIngestedByDC[dc.id]
	i.mu.Unlock()
	if ok && !publishedAt.After(last) {
		i.logger.Debug("divergence ingester: report unchanged since last ingest",
			"dc", dc.id, "publishedAt", snap.PublishedAt)
		return nil
	}

	if err := i.applyReport(ctx, dc, publishedAt, snap.Overrides); err != nil {
		return fmt.Errorf("apply report %s: %w", latestKey, err)
	}
	i.mu.Lock()
	i.lastIngestedByDC[dc.id] = publishedAt
	i.mu.Unlock()
	i.logger.Info("divergence ingester: applied report",
		"dc", dc.id,
		"key", latestKey,
		"publishedAt", snap.PublishedAt,
		"entries", len(snap.Overrides),
	)
	return nil
}
