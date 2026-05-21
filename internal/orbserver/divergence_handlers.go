package orbserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/armada/orbital/internal/orb"
	"github.com/labstack/echo/v4"
)

type divergenceReport struct {
	DCSlug    string         `json:"dcSlug"`
	OrbID     string         `json:"orbId"`
	Version   string         `json:"version"`
	ReportedAt time.Time     `json:"reportedAt"`
	Overrides []orb.Override `json:"overrides"`
}

// @Summary     Publish divergence report
// @Description Sends a divergence report containing local overrides to orbital's report intake API. Returns 502 if orbital is unreachable.
// @Tags        divergence
// @Produce     json
// @Success     200 {object} map[string]any
// @Failure     502 {object} map[string]string
// @Router      /divergence/publish [post]
func (s *Server) postPublishReport(c echo.Context) error {
	overrides, err := orb.LoadOverrides(s.cfg.DataDir)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load overrides"})
	}

	snap := s.state.snapshot()

	report := divergenceReport{
		DCSlug:     s.cfg.DCSlug,
		OrbID:      s.cfg.DCSlug,
		Version:    snap.CurrentVersion,
		ReportedAt: time.Now().UTC(),
		Overrides:  overrides,
	}

	body, _ := json.Marshal(report)

	if s.cfg.OrbitalURL == "" {
		s.logger.Warn("ORBITAL_URL not configured — divergence report not sent")
		return c.JSON(http.StatusOK, map[string]string{"status": "skipped", "reason": "orbital URL not configured"})
	}

	resp, err := http.Post(s.cfg.OrbitalURL+"/api/v1/reports", "application/json", bytes.NewReader(body))
	if err != nil {
		s.logger.Warn("failed to publish divergence report", "err", err)
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "failed to reach orbital: " + err.Error()})
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var result map[string]any
	json.Unmarshal(raw, &result)

	s.logger.Info("divergence report published", "dc", s.cfg.DCSlug, "overrides", len(overrides))
	return c.JSON(http.StatusOK, result)
}
