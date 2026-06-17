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
	s.logger.Info("poller started", "interval", s.cfg.PollInterval)
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
		Username:  s.cfg.OCIUsername,
		Password:  s.cfg.OCIPassword,
		AllowHTTP: s.cfg.OCIAllowHTTP,
	})
	if err != nil {
		// Transient error: keep last-known availableVersion so the banner doesn't
		// disappear mid-publish. lastPollErr captures the reason for diagnostics.
		s.logger.Warn("poll: list tags failed", "err", err)
		s.state.notePollError(err)
		return
	}

	// Pick the highest version tag (`v<N>`), ignoring moving tags ("latest"),
	// cosign signature tags (`sha256-<hash>.sig`), and any other non-numeric
	// tags. Distribution-Spec registries return /tags/list in
	// implementation-defined order (Zot alphabetical, others insertion order),
	// so we can't trust the raw slice — pick by parsed version number.
	//
	// Comparison is numeric per parseVersionTag: v10 > v9 > v3 > v2 > v1.
	//
	// CurrentVersion is also expected to be `v<N>` (set by Import after a
	// successful pull). Comparison is also numeric: only flag a tag as "newer"
	// if its parsed version is strictly greater than current's. This avoids
	// flagging an older version that happens to differ as a string (e.g., if
	// orb was manually rolled back to v2 and v3 still exists in the registry,
	// we'd want to surface v3 again so the operator can re-import).
	highest, highestN, hasHighest := highestVersionTag(tags)
	if !hasHighest {
		// Registry has no v<N> tags — nothing to offer as "newer."
		s.state.setAvailable("")
		return
	}

	snap := s.state.snapshot()
	currentN, currentOK := parseVersionTag(snap.CurrentVersion)
	switch {
	case !currentOK:
		// Current is empty (never imported) or non-version — any version is newer.
		s.logger.Info("new version available", "available", highest, "current", snap.CurrentVersion)
		s.state.setAvailable(highest)
	case highestN > currentN:
		s.logger.Info("new version available", "available", highest, "current", snap.CurrentVersion)
		s.state.setAvailable(highest)
	default:
		// highestN <= currentN: orb is at or ahead of the registry's highest tag.
		s.state.setAvailable("")
	}
}

// highestVersionTag scans tags for the highest `v<N>` and returns it with its
// parsed integer. Returns ("", 0, false) when no version tag is present.
// Non-version tags (`latest`, signatures, custom names) are ignored.
func highestVersionTag(tags []string) (string, int, bool) {
	var bestTag string
	var bestN int
	var found bool
	for _, t := range tags {
		n, ok := parseVersionTag(t)
		if !ok {
			continue
		}
		if !found || n > bestN {
			bestTag = t
			bestN = n
			found = true
		}
	}
	return bestTag, bestN, found
}
