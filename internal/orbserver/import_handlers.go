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

	snap := s.state.snapshot()
	if snap.Status == "running" {
		return c.JSON(http.StatusConflict, map[string]string{"error": "import already running"})
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

		artifact, err := oci.Pull(ctx, pullCfg, req.Tag)
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
			// With the broadcast Dispatcher, every layer produces a result.
			// Role classification is now driven by the response:
			//   - 2xx → "dispatched" (some consumer accepted it)
			//   - non-2xx → "unknown" (no consumer claimed it)
			for i := range results {
				dr := results[i]
				role := orb.LayerRoleUnknown
				if dr.StatusCode >= 200 && dr.StatusCode < 300 {
					role = orb.LayerRoleDispatched
				} else {
					dispatchErrors++
				}
				layerRecords = append(layerRecords, orb.LayerRecord{
					MediaType: dr.MediaType,
					Role:      role,
					Producer:  producerFor(dr.MediaType),
					SizeBytes: artifact.LayerSizes[dr.MediaType],
					Digest:    artifact.LayerDigests[dr.MediaType],
					Dispatch:  &dr,
				})
			}
		}

		// Status reflects the WHOLE import: graph apply + extra-layer dispatch.
		// "done" only when both phases fully succeed. "partial" when graph
		// applied but at least one dispatch failed — operator action likely
		// needed. ("failed" is reserved for the graph-apply phase failing
		// upstream, handled by setFailed above.)
		status := "done"
		if dispatchErrors > 0 {
			status = "partial"
		}
		if len(layerRecords) > 0 || status != "done" {
			if err := orb.FinalizeLastHistory(s.cfg.DataDir, layerRecords, status); err != nil {
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

	return c.JSON(http.StatusAccepted, map[string]string{"status": "started", "tag": req.Tag})
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
	pullCfg := oci.PullConfig{
		Registry:  s.cfg.OCIRegistry,
		Repo:      s.cfg.OCIRepo,
		Username:  s.cfg.OCIUsername,
		Password:  s.cfg.OCIPassword,
		AllowHTTP: s.cfg.OCIAllowHTTP,
	}
	allTags, err := oci.ListTags(ctx, pullCfg)
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

	if c.Request().Header.Get("HX-Request") == "true" {
		var rows []orbTagFragRow
		for _, t := range allTags {
			if strings.HasSuffix(t, ".sig") {
				continue
			}
			row := orbTagFragRow{Name: t}
			meta, err := oci.ResolveTag(ctx, pullCfg, t)
			if err != nil {
				s.logger.Warn("resolve tag failed", "tag", t, "err", err)
				rows = append(rows, row)
				continue
			}
			row.Size = fmtOrbTagSize(meta.TotalSize)
			row.Digest = meta.Digest
			row.HasDigest = meta.Digest != ""
			if result, err := oci.Verify(ctx, verifyCfg, repoRef, meta.Digest, s.logger); err == nil {
				row.Verified = result.Verified
			}
			rows = append(rows, row)
		}
		tmpl, err := template.ParseFiles("web/templates/orb/partials/orb-tags-tbody.gohtml")
		if err != nil {
			return fmt.Errorf("parse orb tags fragment: %w", err)
		}
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return tmpl.Execute(c.Response(), rows)
	}

	// JSON path uses the same descending-version order as the HTMX rows so
	// callers don't have to re-sort.
	var infos []tagInfo
	for _, t := range allTags {
		// Skip cosign signature tags — not importable artifacts.
		if strings.HasSuffix(t, ".sig") {
			continue
		}
		info := tagInfo{Name: t}
		meta, err := oci.ResolveTag(ctx, pullCfg, t)
		if err != nil {
			s.logger.Warn("resolve tag failed", "tag", t, "err", err)
			infos = append(infos, info)
			continue
		}
		info.SizeBytes = meta.TotalSize
		info.Digest = meta.Digest
		result, err := oci.Verify(ctx, verifyCfg, repoRef, meta.Digest, s.logger)
		if err == nil {
			info.Verified = result.Verified
		}
		infos = append(infos, info)
	}
	return c.JSON(http.StatusOK, map[string][]tagInfo{"tags": infos})
}

// @Summary     Import history
// @Description Returns the rolling history of completed and failed imports from disk.
// @Tags        import
// @Produce     json
// @Success     200 {array}  orb.ImportRecord
// @Router      /api/v1/import/history [get]
func (s *Server) importHistory(c echo.Context) error {
	records, err := orb.LoadHistory(s.cfg.DataDir)
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
	records, err := orb.LoadHistory(s.cfg.DataDir)
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
	reversed := make([]orb.LayerRecord, len(match.Layers))
	for i, l := range match.Layers {
		reversed[len(match.Layers)-1-i] = l
	}
	viewModel := struct {
		Tag    string
		Layers []orb.LayerRecord
	}{
		Tag:    match.Tag,
		Layers: reversed,
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
		var dispatchErrors int
		if len(extraLayers) > 0 && s.dispatcher != nil {
			results := s.dispatcher.Dispatch(ctx, extraLayers, tag, "", importID)
			dispatched := make(map[string]bool, len(results))
			for i := range results {
				dr := results[i]
				role := orb.LayerRoleUnknown
				if dr.StatusCode >= 200 && dr.StatusCode < 300 {
					role = orb.LayerRoleDispatched
				} else {
					dispatchErrors++
				}
				layerRecords = append(layerRecords, orb.LayerRecord{
					MediaType: dr.MediaType,
					Role:      role,
					Dispatch:  &dr,
				})
				dispatched[dr.MediaType] = true
			}
			for mt := range extraLayers {
				if !dispatched[mt] {
					dispatchErrors++
					layerRecords = append(layerRecords, orb.LayerRecord{
						MediaType: mt,
						Role:      orb.LayerRoleUnknown,
					})
				}
			}
		}

		// Same status rollup as the OCI-poll path: "partial" if graph applied
		// but at least one dispatch failed.
		status := "done"
		if dispatchErrors > 0 {
			status = "partial"
		}
		if len(layerRecords) > 0 || status != "done" {
			if err := orb.FinalizeLastHistory(s.cfg.DataDir, layerRecords, status); err != nil {
				s.logger.Warn("finalize history failed", "err", err)
			}
		}

		s.state.setDone(orb.ImportRecord{
			Tag:        tag,
			ImportedAt: time.Now().UTC(),
			Status:     status,
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
