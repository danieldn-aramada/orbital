package orbserver

import (
	"context"
	"time"

	"github.com/armada/orbital/internal/oci"
)

// pollLoop runs on a background goroutine for the lifetime of the server.
// Every PollInterval it checks Zot for the latest tag and sets availableVersion
// if a newer artifact exists. The operator triggers the actual import manually.
func (s *Server) pollLoop(ctx context.Context) {
	s.logger.Info("poller started", "interval", s.cfg.PollInterval, "dc_slug", s.cfg.DCSlug)
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	// Poll immediately on startup so the status page shows current state right away.
	s.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("poller stopped")
			return
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

func (s *Server) poll(ctx context.Context) {
	tags, err := oci.ListTags(ctx, oci.PullConfig{
		Registry:  s.cfg.OCIRegistry,
		Repo:      s.cfg.OCIRepo,
		DCSlug:    s.cfg.DCSlug,
		Username:  s.cfg.OCIUsername,
		Password:  s.cfg.OCIPassword,
		AllowHTTP: s.cfg.OCIAllowHTTP,
	})
	if err != nil {
		s.logger.Warn("poll: list tags failed", "err", err)
		s.state.setAvailable("") // clear stale available version on error
		return
	}

	if len(tags) == 0 {
		s.state.setAvailable("")
		return
	}

	latest := tags[len(tags)-1]
	snap := s.state.snapshot()

	if latest != snap.CurrentVersion {
		s.logger.Info("new version available", "available", latest, "current", snap.CurrentVersion)
		s.state.setAvailable(latest)
	} else {
		s.state.setAvailable("") // already up to date
	}
}
