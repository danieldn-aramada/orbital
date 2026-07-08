package orbserver

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/armada/orbital/internal/oci"
	"github.com/armada/orbital/internal/ocitype"
	"github.com/armada/orbital/internal/orb"
	"github.com/labstack/echo/v4"
)

// @Summary     Trigger import
// @Description Starts an async OCI artifact pull and DGraph import for the requested tag. Returns 409 if an import is already running.
// @Tags        import
// @Accept      json
// @Produce     json
// @Param       body body object true "Import request" SchemaExample({"tag":"v3"})
// @Success     202 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /api/v1/import [post]
func (s *Server) triggerImport(c echo.Context) error {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := c.Bind(&req); err != nil || req.Tag == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "tag is required"})
	}
	if !s.startImport(req.Tag, orb.InitiatedByManual) {
		return c.JSON(http.StatusConflict, map[string]string{"error": "import already running"})
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "started", "tag": req.Tag})
}

// startImport kicks off the pull/verify/import goroutine for the given tag.
// Returns false if an import is already running (state machine gate) — caller
// should surface that as a 409 (HTTP) or a skipped-poll-cycle (auto). The
// initiatedBy value is persisted on the ImportRecord row for audit.
func (s *Server) startImport(tag, initiatedBy string) bool {
	if s.state.snapshot().Status == "running" {
		return false
	}
	s.state.setRunning()

	go func() {
		ctx := context.Background()

		pullCfg := oci.PullConfig{
			Registry:  s.cfg.OCIRegistry,
			Repo:      s.cfg.OCIRepo,
			Username:  s.cfg.OCIUsername,
			Password:  s.cfg.OCIPassword,
			AllowHTTP: s.cfg.OCIAllowHTTP,
		}

		artifact, err := oci.Pull(ctx, pullCfg, tag)
		if err != nil {
			s.state.setFailed("pull: " + err.Error())
			return
		}

		verifyCfg := oci.VerifyConfig{
			PublicKeyPath: s.cfg.OCIPublicKeyPath,
			AllowHTTP:     s.cfg.OCIAllowHTTP,
		}
		repoRef := s.cfg.OCIRegistry + "/" + s.cfg.OCIRepo
		result, err := oci.Verify(ctx, verifyCfg, repoRef, artifact.Digest, s.logger)
		if err != nil {
			s.state.setFailed("verify: " + err.Error())
			return
		}
		s.logger.Info("cosign verification", "result", result.Message)

		verification := orb.VerificationUnverified
		if result.Verified {
			verification = orb.VerificationVerified
		}
		meta := orb.ImportMeta{
			Tag:          artifact.Tag,
			Digest:       artifact.Digest,
			DCOrbID:      artifact.Annotations["com.armada.orbital.datacenter-id"],
			ExportJobID:  artifact.Annotations["com.armada.orbital.export-job-id"],
			CreatedAt:    time.Now().UTC(),
			Verification: verification,
			InitiatedBy:  initiatedBy,
		}

		if err := s.imp.Import(ctx, artifact.DataGZ, artifact.SchemaGZ, meta); err != nil {
			s.state.setFailed("import: " + err.Error())
			return
		}

		// Best-effort consumer dispatch for all non-graph layers. Every extra
		// layer is treated identically — orb has no built-in knowledge of any
		// specific media type; consumers register themselves via ORB_CONSUMERS.
		// Producer attribution comes from the OCI manifest's per-layer
		// annotations (com.armada.orbital.producer), declared by whoever
		// assembled the artifact (orbital).
		producerFor := func(mediaType string) string {
			if ann := artifact.LayerAnnotations[mediaType]; ann != nil {
				return ann[ocitype.AnnotationProducer]
			}
			return ""
		}
		var layerRecords []orb.LayerRecord
		var dispatchErrors int
		importID := newImportID()
		if len(artifact.ExtraLayers) > 0 && s.dispatcher != nil {
			results := s.dispatcher.Dispatch(ctx, artifact.ExtraLayers, meta.Tag, meta.Digest, importID)
			// LayerRoleDispatched means a dispatch was attempted; Dispatch.Error
			// records the outcome. LayerRoleUnknown is reserved for layers skipped
			// entirely (no consumers configured at all).
			for i := range results {
				dr := results[i]
				if dr.StatusCode < 200 || dr.StatusCode >= 300 {
					dispatchErrors++
				}
				layerRecords = append(layerRecords, orb.LayerRecord{
					MediaType: dr.MediaType,
					Role:      orb.LayerRoleDispatched,
					Producer:  producerFor(dr.MediaType),
					SizeBytes: artifact.LayerSizes[dr.MediaType],
					Digest:    artifact.LayerDigests[dr.MediaType],
					Dispatch:  &dr,
				})
			}
		}

		// "partial" when the graph applied but at least one consumer dispatch
		// failed — operator may need to retry. "failed" is reserved for
		// graph-apply failures (handled by setFailed above).
		status := "done"
		if dispatchErrors > 0 {
			status = "partial"
		}
		if len(layerRecords) > 0 || status != "done" {
			if err := orb.FinalizeLastHistory(ctx, s.db, layerRecords, status); err != nil {
				s.logger.Warn("finalize history failed", "err", err)
			}
		}

		s.state.setDone(orb.ImportRecord{
			Tag:          meta.Tag,
			Digest:       meta.Digest,
			DCOrbID:      meta.DCOrbID,
			ExportJobID:  meta.ExportJobID,
			ImportedAt:   time.Now().UTC(),
			Status:       status,
			Verification: verification,
		})
	}()

	return true
}

// @Summary     Import status
// @Description Returns the current import state snapshot including status, current version, and last import record.
// @Tags        import
// @Produce     json
// @Success     200 {object} importSnapshot
// @Router      /api/v1/import/status [get]
func (s *Server) importStatus(c echo.Context) error {
	return c.JSON(http.StatusOK, s.state.snapshot())
}

type tagInfo struct {
	Name      string `json:"name"`
	Verified  bool   `json:"verified"`
	SizeBytes int64  `json:"sizeBytes"`
	Digest    string `json:"digest"`
}

// @Summary     List import tags
// @Description Lists available OCI artifact tags from the configured registry for this data center, enriched with signature verification status and artifact size.
// @Tags        import
// @Produce     json
// @Success     200 {object} map[string][]tagInfo
// @Router      /api/v1/import/tags [get]
func (s *Server) importTags(c echo.Context) error {
	ctx := c.Request().Context()
	reqStart := time.Now()
	pullCfg := oci.PullConfig{
		Registry:  s.cfg.OCIRegistry,
		Repo:      s.cfg.OCIRepo,
		Username:  s.cfg.OCIUsername,
		Password:  s.cfg.OCIPassword,
		AllowHTTP: s.cfg.OCIAllowHTTP,
	}

	// ?refresh=1 clears the verify cache before processing. Wired to the
	// Refresh button so operators have an explicit "trust nothing, re-verify
	// everything" affordance without needing to restart orb (which also
	// clears the cache, but is heavier). Any truthy value except "0"/"false".
	refreshed := false
	if r := c.QueryParam("refresh"); r != "" && r != "0" && r != "false" {
		s.verifyCache.Clear()
		refreshed = true
	}

	listStart := time.Now()
	allTags, err := oci.ListTags(ctx, pullCfg)
	listElapsed := time.Since(listStart)
	if err != nil {
		s.logger.Warn("list tags failed", "err", err)
		if c.Request().Header.Get("HX-Request") == "true" {
			c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
			_, err = c.Response().Write([]byte(`<tr><td colspan="5" class="has-text-grey">No versions available.</td></tr>`))
			return err
		}
		return c.JSON(http.StatusOK, map[string][]tagInfo{"tags": {}})
	}

	verifyCfg := oci.VerifyConfig{
		PublicKeyPath: s.cfg.OCIPublicKeyPath,
		AllowHTTP:     s.cfg.OCIAllowHTTP,
	}
	repoRef := s.cfg.OCIRegistry + "/" + s.cfg.OCIRepo

	allTags = sortTagsByVersionDesc(allTags)

	// Filter .sig BEFORE pagination so limit/offset count only importable
	// tags, matching what the operator actually sees rendered.
	realTags := make([]string, 0, len(allTags))
	for _, t := range allTags {
		if !strings.HasSuffix(t, ".sig") {
			realTags = append(realTags, t)
		}
	}
	total := len(realTags)
	limit, offset := parseTagsPagination(c)
	pageTags := realTags[min(offset, total):min(offset+limit, total)]

	// Verify only what's on the visible page. Combined with the digest cache,
	// cold-load cost is bounded by page-size, not total tag count.
	rvStart := time.Now()
	resolved := s.resolveAndVerifyTags(ctx, pageTags, pullCfg, verifyCfg, repoRef)
	rvElapsed := time.Since(rvStart)

	// Diagnostic log to prove or disprove cache effectiveness. Hits/misses
	// are counted inside verifyWithCache via the returned counters below.
	s.logger.Info("import tags served",
		"tags_total", total,
		"page_size", limit,
		"refreshed", refreshed,
		"list_took", listElapsed.String(),
		"resolve_verify_took", rvElapsed.String(),
		"total_took", time.Since(reqStart).String(),
	)

	if c.Request().Header.Get("HX-Request") == "true" {
		rows := make([]orbTagFragRow, 0, len(resolved))
		for _, r := range resolved {
			rows = append(rows, orbTagFragRow{
				Name:      r.name,
				Size:      fmtOrbTagSize(r.sizeBytes),
				Digest:    r.digest,
				HasDigest: r.digest != "",
				Verified:  r.verified,
			})
		}
		data := orbTagsContentData{
			Rows:       rows,
			Total:      total,
			Limit:      limit,
			Offset:     offset,
			FirstRow:   offset + 1,
			LastRow:    offset + len(rows),
			HasPrev:    offset > 0,
			HasNext:    offset+len(rows) < total,
			PrevOffset: max(0, offset-limit),
			NextOffset: offset + limit,
		}
		if len(rows) == 0 {
			data.FirstRow = 0
		}
		tmpl, err := template.ParseFiles("web/templates/orb/partials/orb-tags-content.gohtml")
		if err != nil {
			return fmt.Errorf("parse orb tags fragment: %w", err)
		}
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return tmpl.Execute(c.Response(), data)
	}

	// JSON path uses the same descending-version order as the HTMX rows so
	// callers don't have to re-sort. Pagination metadata mirrors what the
	// existing publish-history endpoint returns.
	infos := make([]tagInfo, 0, len(resolved))
	for _, r := range resolved {
		infos = append(infos, tagInfo{
			Name:      r.name,
			SizeBytes: r.sizeBytes,
			Digest:    r.digest,
			Verified:  r.verified,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"tags":   infos,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// parseTagsPagination extracts limit/offset with sane bounds. Default 10 rows
// per page — small enough to keep cold-cache load fast (10 tags at 8-worker
// parallel ≈ one batch of Verify calls). Max 200 caps worst-case work per
// request. Negative or non-numeric values fall back to defaults.
func parseTagsPagination(c echo.Context) (limit, offset int) {
	limit = 10
	if v, err := strconv.Atoi(c.QueryParam("limit")); err == nil && v > 0 {
		limit = v
		if limit > 200 {
			limit = 200
		}
	}
	if v, err := strconv.Atoi(c.QueryParam("offset")); err == nil && v > 0 {
		offset = v
	}
	return
}

// resolvedTag carries per-tag metadata gathered from the registry. Fields
// stay zero-valued on any lookup error so the row still renders (the UI
// treats an empty digest as "unknown"); the error is logged, not surfaced.
type resolvedTag struct {
	name      string
	sizeBytes int64
	digest    string
	verified  bool
}

// resolveAndVerifyTags fans out ResolveTag + Verify per tag across a bounded
// worker pool, preserving the input order. Caller is responsible for
// filtering out .sig tags — this function verifies every tag it's given.
//
// Sequential fetches at 33+ tags took 10-20s of wall time; the crypto step
// dominates because sig blobs are fetched fresh each time. Parallel with 8
// workers collapses that to bounded-by-slowest-tag.
//
// ResolveTag stays UNCACHED — tag pointers can move (re-push, wipe+republish)
// so fresh manifest resolution per call is required to learn the current
// digest. Verify results ARE cached by digest via s.verifyCache: given a
// stable public key, Verify(digest, key) is a deterministic function of
// immutable inputs (digest is content-addressed), so a hit is mathematically
// still-valid within the pod lifetime.
func (s *Server) resolveAndVerifyTags(
	ctx context.Context,
	tags []string,
	pullCfg oci.PullConfig,
	verifyCfg oci.VerifyConfig,
	repoRef string,
) []resolvedTag {
	results := make([]resolvedTag, len(tags))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var hits, misses atomic.Int64
	var resolveTotal, verifyTotal atomic.Int64 // nanoseconds
	for i, t := range tags {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t string) {
			defer wg.Done()
			defer func() { <-sem }()
			row := resolvedTag{name: t}
			resolveStart := time.Now()
			meta, err := oci.ResolveTag(ctx, pullCfg, t)
			resolveTotal.Add(int64(time.Since(resolveStart)))
			if err != nil {
				s.logger.Warn("resolve tag failed", "tag", t, "err", err)
				results[i] = row
				return
			}
			row.sizeBytes = meta.TotalSize
			row.digest = meta.Digest
			verifyStart := time.Now()
			hit := false
			row.verified, hit = s.verifyWithCache(ctx, verifyCfg, repoRef, meta.Digest)
			verifyTotal.Add(int64(time.Since(verifyStart)))
			if hit {
				hits.Add(1)
			} else {
				misses.Add(1)
			}
			results[i] = row
		}(i, t)
	}
	wg.Wait()
	s.logger.Info("resolve+verify tag batch",
		"tags", len(tags),
		"cache_hits", hits.Load(),
		"cache_misses", misses.Load(),
		"resolve_cpu_sum", time.Duration(resolveTotal.Load()).String(),
		"verify_cpu_sum", time.Duration(verifyTotal.Load()).String(),
	)
	return results
}

// verifyWithCache consults s.verifyCache before falling back to oci.Verify.
// Empty digest short-circuits to false (nothing to verify). Verify errors
// are logged upstream by oci.Verify and result in false; NOT cached, so a
// transient registry hiccup won't stick as "unverified" forever.
// Returns (verified, wasCacheHit) so callers can count hit rate.
func (s *Server) verifyWithCache(ctx context.Context, verifyCfg oci.VerifyConfig, repoRef, digest string) (bool, bool) {
	if digest == "" {
		return false, false
	}
	if v, ok := s.verifyCache.Load(digest); ok {
		return v.(bool), true
	}
	result, err := oci.Verify(ctx, verifyCfg, repoRef, digest, s.logger)
	if err != nil {
		return false, false
	}
	s.verifyCache.Store(digest, result.Verified)
	return result.Verified, false
}

// @Summary     Import history
// @Description Returns the rolling history of completed and failed imports from disk.
// @Tags        import
// @Produce     json
// @Success     200 {array}  orb.ImportRecord
// @Router      /api/v1/import/history [get]
func (s *Server) importHistory(c echo.Context) error {
	records, err := orb.LoadHistory(c.Request().Context(), s.db)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if records == nil {
		records = []orb.ImportRecord{}
	}
	return c.JSON(http.StatusOK, records)
}

// @Summary     Import layers modal
// @Description Returns an HTML fragment rendering the layers modal for an import-history record, found by tag (newest match wins).
// @Tags        import
// @Produce     html
// @Param       tag path string true "Import tag"
// @Success     200
// @Failure     404 {object} map[string]string
// @Router      /api/v1/import/history/{tag}/layers [get]
func (s *Server) importHistoryLayers(c echo.Context) error {
	tag := c.Param("tag")
	if tag == "" {
		return echo.ErrBadRequest
	}
	records, err := orb.LoadHistory(c.Request().Context(), s.db)
	if err != nil {
		return fmt.Errorf("load history: %w", err)
	}
	// History is appended chronologically; iterate newest-first to match the page.
	var match *orb.ImportRecord
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Tag == tag {
			match = &records[i]
			break
		}
	}
	if match == nil {
		return echo.ErrNotFound
	}
	tmpl, err := template.ParseFiles("web/templates/orb/partials/layers-modal.gohtml")
	if err != nil {
		return fmt.Errorf("parse layers-modal: %w", err)
	}
	// Reverse for display (stack diagram: topmost at top, base at bottom) but
	// keep the original OCI manifest position on each row so operators can
	// cross-reference the UI with the courier zip filename
	// (`layer-<position>-<producer>.<ext>`) or with `oras manifest fetch`.
	// See docs/reference/OCI.md.
	type layerRow struct {
		orb.LayerRecord
		Position int
	}
	rows := make([]layerRow, len(match.Layers))
	for i, l := range match.Layers {
		rows[len(match.Layers)-1-i] = layerRow{LayerRecord: l, Position: i}
	}
	viewModel := struct {
		Tag    string
		Layers []layerRow
	}{
		Tag:    match.Tag,
		Layers: rows,
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.ExecuteTemplate(c.Response(), "layers-modal", viewModel)
}

// newImportID generates a random UUID-format string for import correlation.
func newImportID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// sortTagsByVersionDesc sorts orbital's `v<N>`-style tags numerically descending,
// pushing any non-version tag (e.g. "latest") to the end. Without this, lex sort
// orders v10 between v1 and v2 because "1" < "2" character-wise.
func sortTagsByVersionDesc(tags []string) []string {
	out := make([]string, len(tags))
	copy(out, tags)
	sort.SliceStable(out, func(i, j int) bool {
		ni, oki := parseVersionTag(out[i])
		nj, okj := parseVersionTag(out[j])
		switch {
		case oki && okj:
			return ni > nj
		case oki:
			return true // version tags before non-version tags
		case okj:
			return false
		default:
			return out[i] < out[j] // stable lex among non-version tags
		}
	})
	return out
}

// parseVersionTag returns the integer N for a "v<N>" tag (e.g. "v10" → 10),
// or false if the tag doesn't fit the convention.
func parseVersionTag(tag string) (int, bool) {
	if len(tag) < 2 || tag[0] != 'v' {
		return 0, false
	}
	n, err := strconv.Atoi(tag[1:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// @Summary     Import OCI artifact bundle
// @Description Accepts a zip bundle (data.json.gz + schema.gz + optional layers.json + layer blobs)
//
//	and runs the full import pipeline: DGraph import followed by best-effort consumer dispatch.
//	Always registered regardless of ORB_ENABLE_OCI_REGISTRY. Consumer dispatch only occurs
//	if ORB_CONSUMERS is configured and layers.json is present in the zip.
//
// @Tags        import
// @Accept      multipart/form-data
// @Produce     json
// @Param       bundle formData file true "Zip archive containing data.json.gz, schema.gz, and optionally layers.json + layer blobs"
// @Success     202 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /api/v1/import/artifact [post]
func (s *Server) importArtifact(c echo.Context) error {
	if snap := s.state.snapshot(); snap.Status == "running" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "import already running"})
	}

	fh, err := c.FormFile("bundle")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "bundle file is required"})
	}
	f, err := fh.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "could not open upload"})
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "could not read upload"})
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "not a valid zip archive"})
	}

	type layerEntry struct {
		MediaType string `json:"mediaType"`
		Filename  string `json:"filename"`
	}
	var layerManifest []layerEntry

	// First pass: parse layers.json if present.
	for _, zf := range zr.File {
		if zf.Name != "layers.json" {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read layers.json"})
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if err := json.Unmarshal(b, &layerManifest); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid layers.json: " + err.Error()})
		}
		break
	}

	// Build filename → mediaType lookup from layers.json.
	filenameToMediaType := make(map[string]string, len(layerManifest))
	for _, e := range layerManifest {
		filenameToMediaType[e.Filename] = e.MediaType
	}

	// Second pass: extract all files.
	var dataGZ, schemaGZ []byte
	extraLayers := make(map[string][]byte)
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read " + zf.Name})
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		switch zf.Name {
		case "data.json.gz":
			dataGZ = data
		case "schema.gz":
			schemaGZ = data
		case "layers.json":
			// already processed
		default:
			if mt, ok := filenameToMediaType[zf.Name]; ok {
				extraLayers[mt] = data
			}
		}
	}

	if len(dataGZ) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "zip must contain data.json.gz"})
	}
	if len(schemaGZ) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "zip must contain schema.gz"})
	}

	tag := fmt.Sprintf("artifact-%s", time.Now().UTC().Format("20060102-150405"))
	importID := newImportID()
	s.state.setRunning()

	go func() {
		ctx := context.Background()
		meta := orb.ImportMeta{
			Tag:          tag,
			CreatedAt:    time.Now().UTC(),
			Verification: orb.VerificationNotApplicable,
		}
		if err := s.imp.Import(ctx, dataGZ, schemaGZ, meta); err != nil {
			s.state.setFailed("artifact import: " + err.Error())
			return
		}

		// Best-effort consumer dispatch for all extra layers. Every extra
		// layer is treated identically — orb has no built-in knowledge of any
		// specific media type; consumers register themselves via ORB_CONSUMERS.
		var layerRecords []orb.LayerRecord
		if len(extraLayers) > 0 && s.dispatcher != nil {
			results := s.dispatcher.Dispatch(ctx, extraLayers, tag, "", importID)
			dispatched := make(map[string]bool, len(results))
			for i := range results {
				dr := results[i]
				// Dispatch is best-effort: LayerRoleDispatched records that a
				// dispatch was attempted regardless of the consumer's response.
				// Consumer failures are surfaced in Dispatch.Error but do not
				// degrade the import status — the graph apply succeeded.
				layerRecords = append(layerRecords, orb.LayerRecord{
					MediaType: dr.MediaType,
					Role:      orb.LayerRoleDispatched,
					Dispatch:  &dr,
				})
				dispatched[dr.MediaType] = true
			}
			for mt := range extraLayers {
				if !dispatched[mt] {
					layerRecords = append(layerRecords, orb.LayerRecord{
						MediaType: mt,
						Role:      orb.LayerRoleUnknown,
					})
				}
			}
		}

		// Consumer dispatch is best-effort: status is always "done" when the
		// graph apply succeeds. Consumer errors are visible in layer records.
		if len(layerRecords) > 0 {
			if err := orb.FinalizeLastHistory(ctx, s.db, layerRecords, "done"); err != nil {
				s.logger.Warn("finalize history failed", "err", err)
			}
		}

		s.state.setDone(orb.ImportRecord{
			Tag:        tag,
			ImportedAt: time.Now().UTC(),
			Status:     "done",
		})
	}()

	return c.JSON(http.StatusAccepted, map[string]string{"status": "started", "tag": tag, "importId": importID})
}

// @Summary     Import subgraph bundle
// @Description Accepts a zip bundle (data.json.gz + schema.gz) and imports it into local DGraph. Source-agnostic: use for courier (direct upload), ConfigBundle Controller delivery, or any caller with a subgraph zip.
// @Tags        import
// @Accept      multipart/form-data
// @Produce     json
// @Param       bundle formData file true "Zip archive containing data.json.gz and schema.gz"
// @Success     202 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Router      /api/v1/import/subgraph [post]
// importSubgraph reads the entire zip into memory before extracting. At peak it holds
// the raw zip + the extracted data.json.gz simultaneously (~2× zip size). This is
// acceptable for typical single-DC subgraphs (1–10 MB compressed). If subgraph size
// grows significantly, rework to: save upload to a temp file, use zip.OpenReader, and
// stream data.json.gz directly to the scratch path — eliminating the double-copy.
func (s *Server) importSubgraph(c echo.Context) error {
	if snap := s.state.snapshot(); snap.Status == "running" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "import already running"})
	}

	fh, err := c.FormFile("bundle")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "bundle file is required"})
	}

	f, err := fh.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "could not open upload"})
	}
	defer f.Close()

	raw, err := io.ReadAll(f)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "could not read upload"})
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "not a valid zip archive"})
	}

	var dataGZ, schemaGZ []byte
	for _, zf := range zr.File {
		switch zf.Name {
		case "data.json.gz":
			rc, err := zf.Open()
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read data.json.gz"})
			}
			dataGZ, _ = io.ReadAll(rc)
			rc.Close()
		case "schema.gz":
			rc, err := zf.Open()
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read schema.gz"})
			}
			schemaGZ, _ = io.ReadAll(rc)
			rc.Close()
		}
	}

	if len(dataGZ) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "zip must contain data.json.gz"})
	}
	if len(schemaGZ) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "zip must contain schema.gz"})
	}

	tag := fmt.Sprintf("courier-%s", time.Now().UTC().Format("20060102-150405"))
	s.state.setRunning()

	go func() {
		meta := orb.ImportMeta{
			Tag:          tag,
			CreatedAt:    time.Now().UTC(),
			Verification: orb.VerificationNotApplicable,
		}
		if err := s.imp.Import(context.Background(), dataGZ, schemaGZ, meta); err != nil {
			s.state.setFailed("courier import: " + err.Error())
			return
		}

		s.state.setDone(orb.ImportRecord{
			Tag:          meta.Tag,
			ImportedAt:   time.Now().UTC(),
			Status:       "done",
			Verification: orb.VerificationNotApplicable,
		})
	}()

	return c.JSON(http.StatusAccepted, map[string]string{"status": "started", "tag": tag})
}

// ── Fragment renderer ─────────────────────────────────────────────────────────

type orbTagFragRow struct {
	Name      string
	Verified  bool
	Digest    string
	HasDigest bool
	Size      string
}

// orbTagsContentData is the shape passed to the orb-tags-content fragment.
// Rows is the current page's slice; the pagination fields drive the nav
// footer rendered inside the same fragment (same-swap-target as the rows,
// so nav clicks re-render both together, mirroring publish-history).
type orbTagsContentData struct {
	Rows       []orbTagFragRow
	Total      int
	Limit      int
	Offset     int
	FirstRow   int  // 1-indexed for display
	LastRow    int  // 1-indexed for display
	HasPrev    bool
	HasNext    bool
	PrevOffset int
	NextOffset int
	BasePath   string
}

func fmtOrbTagSize(n int64) string {
	if n <= 0 {
		return "—"
	}
	if n < 1048576 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1048576)
}

// importTagRows handles GET /api/v1/import/tags/rows
// Returns an HTML fragment of the orb tags tbody, newest first.
