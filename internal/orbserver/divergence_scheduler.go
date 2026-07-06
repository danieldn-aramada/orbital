package orbserver

import (
	"context"
	"log/slog"
	"sync"
	"time"

	cron "github.com/robfig/cron/v3"

	"github.com/armada/orbital/internal/divergence"
)

// DivergenceScheduler fires orb's divergence publish on a cron schedule.
// Empty cron spec = disabled (manual publish only). Mirrors the backup
// scheduler pattern in internal/handler/backup.go.
type DivergenceScheduler struct {
	store     *divergence.Store
	publisher *divergence.Publisher // nil → publishing disabled regardless of schedule
	cronSpec  string                // empty → disabled
	logger    *slog.Logger

	mu      sync.Mutex
	cronJob *cron.Cron
}

func NewDivergenceScheduler(store *divergence.Store, publisher *divergence.Publisher, cronSpec string, logger *slog.Logger) *DivergenceScheduler {
	return &DivergenceScheduler{
		store:     store,
		publisher: publisher,
		cronSpec:  cronSpec,
		logger:    logger,
	}
}

// divergenceCronParser matches the backup scheduler's parser.
var divergenceCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Start fires any missed run, then runs the cron scheduler until ctx is
// cancelled. Call as a goroutine. No-ops if cron spec is empty or publisher
// is nil (S3 not configured).
func (s *DivergenceScheduler) Start(ctx context.Context) {
	if s.cronSpec == "" || s.publisher == nil {
		return
	}

	if _, err := divergenceCronParser.Parse(s.cronSpec); err != nil {
		s.logger.Error("invalid ORB_DIVERGENCE_PUBLISH_SCHEDULE — scheduler disabled", "spec", s.cronSpec, "err", err)
		return
	}

	if s.isMissedRun(ctx) {
		s.logger.Info("divergence scheduler: catch-up firing missed publish")
		s.fire(ctx)
	}

	s.mu.Lock()
	c := cron.New(cron.WithLocation(time.UTC))
	if _, err := c.AddFunc(s.cronSpec, func() { s.fire(context.Background()) }); err != nil {
		s.logger.Warn("divergence scheduler: failed to add cron func", "spec", s.cronSpec, "err", err)
		s.mu.Unlock()
		return
	}
	c.Start()
	s.cronJob = c
	s.mu.Unlock()
	s.logger.Info("divergence scheduler started", "spec", s.cronSpec)

	<-ctx.Done()

	s.mu.Lock()
	if s.cronJob != nil {
		s.cronJob.Stop()
		s.cronJob = nil
	}
	s.mu.Unlock()
}

// fire publishes the current set to S3. No orb-side content dedup: orbital's
// ingester (`internal/divergenceingest/store.go` applyReport) is the only
// side that can correctly tell a state change from a no-op, because it knows
// which divergences have been resolved and pruned. Cost is one extra S3
// object per cb-controller heartbeat when nothing's changed — orbital
// short-circuits those to a timestamp touch with no DB churn.
func (s *DivergenceScheduler) fire(ctx context.Context) {
	entries, err := s.store.Load()
	if err != nil {
		s.logger.Error("divergence scheduler: load failed", "err", err)
		return
	}

	key, err := s.publisher.Publish(ctx, entries)
	if err != nil {
		s.logger.Error("divergence scheduler: publish failed", "err", err)
		return
	}

	if err := s.store.SavePublishRow(divergence.PublishRecord{
		PublishedAt: time.Now().UTC(),
		S3Key:       key,
	}, publishDCOrbID(entries), entries); err != nil {
		s.logger.Warn("divergence scheduler: save publish record failed", "err", err)
	}
	s.logger.Info("divergence scheduler: published", "s3Key", key, "entries", len(entries))
}

// isMissedRun returns true if a scheduled publish should have occurred since
// the last successful publish but didn't (e.g. orb was down). Mirrors
// internal/handler/backup.go::isMissedRun.
func (s *DivergenceScheduler) isMissedRun(ctx context.Context) bool {
	schedule, err := divergenceCronParser.Parse(s.cronSpec)
	if err != nil {
		return false
	}
	last, err := s.store.LoadPublishRecord()
	if err != nil || last == nil {
		// Never published — fire once on startup as the first run.
		return true
	}
	return schedule.Next(last.PublishedAt).Before(time.Now())
}
