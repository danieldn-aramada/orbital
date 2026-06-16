package orbserver

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
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
	S3Endpoint   string
	S3Bucket     string
}

func (s *Server) divergencePage(c echo.Context) error {
	entries, _ := s.divStore.Load()
	rec, _ := s.divStore.LoadPublishRecord()
	data := divergencePageData{
		Base:         s.orbBase(c),
		PageTitle:    "Divergence Report",
		Entries:      entries,
		LastPublish:  rec,
		S3Configured: s.divPublisher != nil,
		S3Endpoint:   s.cfg.S3Endpoint,
		S3Bucket:     s.cfg.S3Bucket,
	}
	// HX-Request callers (the Refresh button) get just the table fragment.
	if c.Request().Header.Get("HX-Request") == "true" {
		return s.renderFragment(c, "divergence", "divergence-content", data)
	}
	return s.render(c, "divergence", data)
}

// intakePayload is the wire shape accepted by POST /api/v1/divergence.
// Producers translate their native vocabulary into orbital-native before
// posting; orb does not interpret producer-specific concepts. Replace-not-
// merge: the array represents the FULL current divergence set.
//
// See docs/reference/DIVERGENCE-INTAKE.md for the canonical contract.
type intakePayload struct {
	Overrides []divergence.OverrideEntry `json:"overrides"`
}

// POST /api/v1/divergence — producer-agnostic intake.
//
// Accepts an orbital-native divergence set. Validates structural correctness
// (presence of required fields, parseable timestamp) and stores. Does not
// validate orbital-domain semantics (real orbId, real type, real field).
//
// For each override, orb looks up the ConfigItem's current `version` from its
// local DGraph (which is the imported-bundle view that the producer observed
// against) and stamps it into IntendedAtVersion. Orb's DGraph is read-only
// outside of `orb import`, so the version captured here matches what the
// producer observed; producers don't need to send version themselves.
//
// Lookup failures (orbId not in local DGraph, DGraph unreachable, version
// field null) leave IntendedAtVersion as nil. Orbital's Accept handler
// degrades to a value-based stale check when version is missing.
func (s *Server) receiveDivergence(c echo.Context) error {
	var payload intakePayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid divergence report")
	}
	for i, ov := range payload.Overrides {
		if err := validateOverrideEntry(ov); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("overrides[%d]: %v", i, err))
		}
	}

	// Version capture. Batched by orbId so a report with N fields on the same
	// ConfigItem makes one lookup, not N. Failures degrade silently — leaves
	// IntendedAtVersion nil for that override.
	ctx := c.Request().Context()
	versions := make(map[string]*int, len(payload.Overrides))
	for _, ov := range payload.Overrides {
		key := ov.Type + "|" + ov.OrbID
		if _, seen := versions[key]; seen {
			continue
		}
		v, err := divergence.FetchCurrentVersion(ctx, s.cfg.DGraphURL, ov.Type, ov.OrbID)
		if err != nil {
			s.logger.Warn("orb divergence intake: version lookup failed",
				"orbId", ov.OrbID, "type", ov.Type, "err", err)
			versions[key] = nil
		} else {
			versions[key] = v
		}
	}
	for i := range payload.Overrides {
		ov := &payload.Overrides[i]
		key := ov.Type + "|" + ov.OrbID
		ov.IntendedAtVersion = versions[key]
	}

	if err := s.divStore.Save(payload.Overrides); err != nil {
		s.logger.Error("divergence store save failed", "err", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store report")
	}
	return c.JSON(http.StatusOK, map[string]int{"stored": len(payload.Overrides)})
}

// validateOverrideEntry enforces presence and structural rules for a single
// intake entry. Orbital-domain validity (real orbId/type/field) is checked
// downstream by orbital on ingestion.
func validateOverrideEntry(ov divergence.OverrideEntry) error {
	if ov.OrbID == "" {
		return fmt.Errorf("orbId is required")
	}
	if ov.Field == "" {
		return fmt.Errorf("field is required")
	}
	if ov.Type == "" {
		return fmt.Errorf("type is required")
	}
	if ov.IntendedValue == nil {
		return fmt.Errorf("intendedValue is required")
	}
	if ov.OverrideValue == nil {
		return fmt.Errorf("overrideValue is required")
	}
	if ov.Who == "" {
		return fmt.Errorf("who is required")
	}
	if ov.When == "" {
		return fmt.Errorf("when is required")
	}
	if _, err := time.Parse(time.RFC3339, ov.When); err != nil {
		return fmt.Errorf("when must be RFC3339: %w", err)
	}
	return nil
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
		return echo.NewHTTPError(http.StatusServiceUnavailable, "Object store not configured")
	}
	entries, err := s.divStore.Load()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load reports")
	}
	// Refuse to publish an empty snapshot — it would pollute S3 with files
	// that contain no information. Orbital reconciles state from non-empty
	// publishes; absence of new keys does not need to be communicated.
	if len(entries) == 0 {
		return echo.NewHTTPError(http.StatusConflict, "nothing to publish")
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

// POST /api/v1/divergence/test-connection
//
// Verifies the configured S3/Azure Blob target is reachable. Mirrors
// orbital's BackupHandler.TestConnection pattern: HX-Request callers get a
// single-span HTML fragment ready to swap into a result slot; other callers
// get JSON. Returns 503 when S3 is not configured so the same gating used by
// publish surfaces consistently.
func (s *Server) testDivergenceConnection(c echo.Context) error {
	if s.divPublisher == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "Object store not configured")
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
	defer cancel()
	err := s.divPublisher.Ping(ctx)
	if c.Request().Header.Get("HX-Request") == "true" {
		return renderTestConnectionFragment(c, err)
	}
	if err != nil {
		return c.JSON(http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// renderTestConnectionFragment writes the inline HTML span shown next to a
// "Test Connection" button. Mirrors the orbital handler-package helper of the
// same shape; kept inline (not a template file) because the markup is trivial
// and lives at the same boundary in both apps.
func renderTestConnectionFragment(c echo.Context, pingErr error) error {
	if pingErr != nil {
		return c.HTML(http.StatusOK, `<span class="has-text-danger"><i class="fa-solid fa-circle-xmark"></i> `+template.HTMLEscapeString(pingErr.Error())+`</span>`)
	}
	return c.HTML(http.StatusOK, `<span class="has-text-success"><i class="fa-solid fa-circle-check"></i> Connected</span>`)
}
