package orbserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/armada/orbital/internal/divergence"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

type divergencePageData struct {
	layout.Base
	PageTitle    string
	Entries      []divergence.OverrideEntry
	LastPublish  *divergence.PublishRecord
	S3Configured bool
}

func (s *Server) divergencePage(c echo.Context) error {
	entries, _ := s.divStore.Load()
	rec, _ := s.divStore.LoadPublishRecord()
	return s.render(c, "divergence", divergencePageData{
		Base:         s.orbBase(c),
		PageTitle:    "Divergence Report",
		Entries:      entries,
		LastPublish:  rec,
		S3Configured: s.divPublisher != nil,
	})
}

// intakeOverride is one entry in the divergence intake payload. cb-controller
// sends K8s field paths; orb translates them to {orbId, field} using the
// stored mapping for the given bundle digest.
type intakeOverride struct {
	Path          string `json:"path"`
	IntendedValue any    `json:"intendedValue"`
	OverrideValue any    `json:"overrideValue"`
	Who           string `json:"who"`
	When          string `json:"when"`
}

// intakePayload is the new payload shape accepted by POST /api/v1/divergence.
// The bundleDigest selects which mapping orb uses to translate paths into
// canonical {orbId, field} entries. Replace-not-merge: the array represents
// the FULL current divergence set; entries not included are considered
// resolved by disappearance.
type intakePayload struct {
	BundleDigest string           `json:"bundleDigest"`
	Overrides    []intakeOverride `json:"overrides"`
}

// POST /api/v1/divergence — intake endpoint for edge components (e.g. cb-controller).
// Accepts K8s-path-keyed overrides plus the bundle digest. Orb looks up the
// matching mapping, translates each path into a canonical {orbId, field} entry,
// and saves the canonical set (replace-not-merge).
//
// Returns 422 when the bundleDigest has no matching mapping stored — caller
// should retry on a later tick (typically after orb has imported the matching
// bundle and persisted its mapping layer).
func (s *Server) receiveDivergence(c echo.Context) error {
	var payload intakePayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid divergence report")
	}
	if payload.BundleDigest == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bundleDigest is required")
	}

	mapping, err := s.mappingStore.Load(payload.BundleDigest)
	if err != nil {
		s.logger.Warn("divergence intake: mapping not found", "digest", payload.BundleDigest, "err", err)
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "unknown bundleDigest: "+payload.BundleDigest)
	}

	entries := make([]divergence.OverrideEntry, 0, len(payload.Overrides))
	for _, ov := range payload.Overrides {
		orbID, field, typeName, resolveErr := mapping.Resolve(ov.Path)
		if resolveErr != nil {
			s.logger.Warn("divergence intake: resolve failed", "path", ov.Path, "digest", payload.BundleDigest, "err", resolveErr)
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("path %q: %v", ov.Path, resolveErr))
		}
		entries = append(entries, divergence.OverrideEntry{
			OrbID:         orbID,
			Field:         field,
			Type:          typeName,
			IntendedValue: ov.IntendedValue,
			OverrideValue: ov.OverrideValue,
			Who:           ov.Who,
			When:          ov.When,
		})
	}

	if err := s.divStore.Save(entries); err != nil {
		s.logger.Error("divergence store save failed", "err", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store report")
	}
	return c.JSON(http.StatusOK, map[string]int{"stored": len(entries)})
}

// GET /api/v1/divergence — returns current pending entries.
func (s *Server) getDivergence(c echo.Context) error {
	entries, err := s.divStore.Load()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load reports")
	}
	return c.JSON(http.StatusOK, entries)
}

// POST /api/v1/divergence/publish — aggregates pending entries into a snapshot and writes to S3.
func (s *Server) publishDivergence(c echo.Context) error {
	if s.divPublisher == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "S3 not configured")
	}
	entries, err := s.divStore.Load()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load reports")
	}
	key, err := s.divPublisher.Publish(c.Request().Context(), entries)
	if err != nil {
		s.logger.Error("divergence publish failed", "err", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "publish failed")
	}
	rec := divergence.PublishRecord{
		PublishedAt: time.Now().UTC(),
		S3Key:       key,
	}
	if err := s.divStore.SavePublishRecord(rec); err != nil {
		s.logger.Warn("failed to save publish record", "err", err)
	}
	s.logger.Info("divergence report published", "key", key, "entries", len(entries))
	return c.JSON(http.StatusOK, map[string]string{"key": key})
}
