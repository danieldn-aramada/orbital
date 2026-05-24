package orbserver

import (
	"encoding/json"
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

// POST /api/v1/divergence — intake endpoint for edge components (e.g. cb-controller).
// Body: JSON array of OverrideEntry — replaces the current pending set entirely.
func (s *Server) receiveDivergence(c echo.Context) error {
	var entries []divergence.OverrideEntry
	if err := json.NewDecoder(c.Request().Body).Decode(&entries); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid divergence report")
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
