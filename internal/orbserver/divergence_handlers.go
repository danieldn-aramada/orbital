package orbserver

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/armada/orbital/internal/divergence"
	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

// publishHistoryPageSize caps rows per page on the publish-history section.
// 25 fits comfortably above the fold and matches the import-history page's
// implicit rolling window. Callers can request smaller via ?limit; larger
// requests are clamped.
const publishHistoryPageSize = 25

type divergencePageData struct {
	layout.Base
	PageTitle      string
	Entries        []divergence.OverrideEntry
	LastPublish    *divergence.PublishRecord
	S3Configured   bool
	S3Endpoint     string
	S3Bucket       string
	PublishHistory publishHistoryView
}

// publishHistoryView is the render-model for the Publish History page. FirstRow
// and LastRow are 1-indexed row numbers within the full dataset (e.g. "26-50 of
// 100"), computed from Offset + len(Rows) so the template can render a range
// indicator alongside pagination without duplicating math.
type publishHistoryView struct {
	Rows       []divergence.PublishHistoryRow
	Total      int
	Limit      int
	Offset     int
	FirstRow   int
	LastRow    int
	NextOffset int
	PrevOffset int
	HasNext    bool
	HasPrev    bool
}

func (s *Server) divergencePage(c echo.Context) error {
	entries, _ := s.divStore.Load()
	rec, _ := s.divStore.LoadPublishRecord()
	data := divergencePageData{
		Base:         s.orbBase(c),
		PageTitle:    "Publish Report",
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

// publishHistoryPage renders the dedicated Publish History page. Full-page
// initial load + HX-Request pagination swaps into #publish-history-content
// with just the inner fragment.
func (s *Server) publishHistoryPage(c echo.Context) error {
	history := s.loadPublishHistoryView(c)
	data := divergencePageData{
		Base:           s.orbBase(c),
		PageTitle:      "Publish History",
		PublishHistory: history,
	}
	if c.Request().Header.Get("HX-Request") == "true" {
		return s.renderFragment(c, "publish-history", "publish-history-content", data)
	}
	return s.render(c, "publish-history", data)
}

// loadPublishHistoryView reads pagination query params (limit, offset) and
// returns the rendered view model. Errors are logged and treated as an empty
// history — a broken history query should not blank the whole divergence page.
func (s *Server) loadPublishHistoryView(c echo.Context) publishHistoryView {
	limit, offset := parsePublishHistoryParams(c)
	rows, total, err := s.divStore.LoadPublishHistory("", limit, offset)
	if err != nil {
		s.logger.Warn("load publish history failed", "err", err)
		return publishHistoryView{Limit: limit, Offset: offset}
	}
	next := offset + limit
	prev := max(offset-limit, 0)
	first := 0
	last := 0
	if len(rows) > 0 {
		first = offset + 1
		last = offset + len(rows)
	}
	return publishHistoryView{
		Rows:       rows,
		Total:      total,
		Limit:      limit,
		Offset:     offset,
		FirstRow:   first,
		LastRow:    last,
		NextOffset: next,
		PrevOffset: prev,
		HasNext:    next < total,
		HasPrev:    offset > 0,
	}
}

func parsePublishHistoryParams(c echo.Context) (int, int) {
	limit := publishHistoryPageSize
	offset := 0
	if v := c.QueryParam("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := c.QueryParam("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
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
//
// @Summary     Receive divergence report
// @Description Producer-agnostic intake. Accepts an orbital-native divergence set (replace-not-merge: the array is the full current state). Validates structural correctness; orbital-domain validity is checked downstream on ingestion.
// @Tags        divergence
// @Accept      json
// @Produce     json
// @Param       body body  intakePayload true "Override entries"
// @Success     200  {object} map[string]int
// @Failure     400  {object} map[string]string
// @Failure     500  {object} map[string]string
// @Router      /api/v1/divergence [post]
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
//
// @Summary     Get pending divergence entries
// @Description Returns the current pending divergence set held by orb. This is the same set that the next `publish` call would aggregate into a report.
// @Tags        divergence
// @Produce     json
// @Success     200 {array} divergence.OverrideEntry
// @Failure     500 {object} map[string]string
// @Router      /api/v1/divergence [get]
func (s *Server) getDivergence(c echo.Context) error {
	entries, err := s.divStore.Load()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load reports")
	}
	return c.JSON(http.StatusOK, entries)
}

// POST /api/v1/divergence/publish — aggregates pending entries into a report and writes to S3.
//
// @Summary     Publish divergence report
// @Description Aggregates the current pending entries into a snapshot and writes it to the configured object store (S3 or Azure Blob). Returns the storage key. 503 if no object store is configured; 409 if there are no entries to publish (orbital reconciles from non-empty publishes only).
// @Tags        divergence
// @Produce     json
// @Success     200 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Failure     500 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/v1/divergence/publish [post]
func (s *Server) publishDivergence(c echo.Context) error {
	if s.divPublisher == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "Object store not configured")
	}
	entries, err := s.divStore.Load()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load reports")
	}
	// Refuse to publish an empty report — it would pollute S3 with files
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
	if err := s.divStore.SavePublishRow(rec, publishDCOrbID(entries), entries); err != nil {
		s.logger.Warn("failed to save publish record", "err", err)
	}
	s.logger.Info("divergence report published", "key", key, "entries", len(entries))
	return c.JSON(http.StatusOK, map[string]string{"key": key})
}

// GET /api/v1/divergence/publish-history — history of divergence publishes.
//
// Newest-first. HTMX callers get an HTML fragment for the section; JSON
// callers get the same rows as a JSON array with pagination metadata.
//
// @Summary     List published divergence reports
// @Description Returns history of divergence reports published to object storage, newest first. Supports pagination via limit (max 200) and offset. HX-Request callers receive an HTML fragment for direct swap into the divergence page.
// @Tags        divergence
// @Produce     json
// @Param       limit  query int false "Rows per page (default 25, max 200)"
// @Param       offset query int false "Row offset (default 0)"
// @Success     200 {object} map[string]any
// @Router      /api/v1/divergence/publish-history [get]
func (s *Server) publishHistory(c echo.Context) error {
	view := s.loadPublishHistoryView(c)
	if c.Request().Header.Get("HX-Request") == "true" {
		// Fragment path: render the same section rendered inline by the page.
		return s.renderFragment(c, "publish-history", "publish-history-content", divergencePageData{
			Base:           s.orbBase(c),
			PublishHistory: view,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"rows":   view.Rows,
		"total":  view.Total,
		"limit":  view.Limit,
		"offset": view.Offset,
	})
}

// POST /api/v1/divergence/test-connection
//
// Verifies the configured S3/Azure Blob target is reachable. Mirrors
// orbital's BackupHandler.TestConnection pattern: HX-Request callers get a
// single-span HTML fragment ready to swap into a result slot; other callers
// get JSON. Returns 503 when S3 is not configured so the same gating used by
// publish surfaces consistently.
//
// @Summary     Test divergence object store connection
// @Description Pings the configured object store (S3 or Azure Blob) used for divergence report publishing. Returns JSON `{ok, error?}` by default, or an inline HTML span when `HX-Request: true` is set. 503 if no object store is configured.
// @Tags        divergence
// @Produce     json
// @Success     200 {object} map[string]any
// @Failure     503 {object} map[string]string
// @Router      /api/v1/divergence/test-connection [post]
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

// publishDCOrbID derives a data-center orbId from the published entry set.
// Every entry carries an orbId of the form "<namespace>:<name>"; the namespace
// prefix is the DC handle for orbital's lookups. Empty entries → empty DC.
func publishDCOrbID(entries []divergence.OverrideEntry) string {
	if len(entries) == 0 {
		return ""
	}
	id := entries[0].OrbID
	for i := 0; i < len(id); i++ {
		if id[i] == ':' {
			return id[:i]
		}
	}
	return ""
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
