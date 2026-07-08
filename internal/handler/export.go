package handler

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/ent/restorejob"
	"github.com/armada/orbital/internal/bundler"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// PublishExportedResult is returned by the atomic-flow publish callback when
// the OCI push succeeds. Enriches the single `export` audit event without a
// second DB round-trip.
type PublishExportedResult struct {
	ArtifactID int
	Tag        string
	Digest     string
	SizeBytes  int64
	LayerCount int
}

// PublishExportedFunc runs the publish half of the atomic export → publish
// flow synchronously. Called from Export.runExport when download=false AND
// OCI is configured. Returns non-nil error on any failure (bundler, push,
// sign); the caller marks the ExportJob failed and cleans up the zip.
type PublishExportedFunc func(ctx context.Context, jobID uuid.UUID, actor string) (*PublishExportedResult, error)

type Export struct {
	db                    *ent.Client
	dgraphURL             string // blue GraphQL
	dgraphScratchURL      string // scratch GraphQL
	dgraphScratchAdminURL string // scratch admin
	dgraphScratchZeroURL  string // scratch Zero HTTP (for UID lease bump)
	exportDir             string // where final zips are written
	scratchExportDir      string // host-side mount of /dgraph/export in scratch container
	schemaPath            string // path to the GraphQL schema file
	logger                *slog.Logger
	basePath              string        // URL base path for fragment-rendered hx-* attributes
	timeout               time.Duration // max duration for the async export goroutine
	// Bundler settings for Download's on-the-fly bundle assembly. When set,
	// Download calls each configured bundler and packages its layers alongside
	// data.json.gz + schema.gz into a courier-ready zip. When empty, Download
	// falls back to streaming the raw export zip (data + schema only).
	defaultBundlerURLs []string
	bundlerTimeout     time.Duration
	bundlerOpts        []bundler.ClientOption
	// publishFn is the atomic-flow publish callback. nil = OCI not configured
	// → server infers download-only mode. Set by server.go once the OCI
	// handler exists (handler → oci → handler cycle is avoided by injecting
	// the closure rather than a handler ref).
	publishFn PublishExportedFunc
}

// SetBasePath configures the URL base path used by HTML fragments rendered by this handler.
func (h *Export) SetBasePath(bp string) { h.basePath = bp }

// SetTimeout configures the maximum duration for the async export goroutine.
func (h *Export) SetTimeout(d time.Duration) { h.timeout = d }

// SetBundlers wires the same bundler config that OCI publish uses so Download
// can call bundlers on the fly and package the result as a courier-ready zip.
// urls is a slice of "name=url" specs (parsed via bundler.ParseSpec). Empty urls
// keeps the plain-zip download behavior.
func (h *Export) SetBundlers(urls []string, timeout time.Duration, opts ...bundler.ClientOption) {
	h.defaultBundlerURLs = urls
	h.bundlerTimeout = timeout
	h.bundlerOpts = opts
}

// SetPublishFn wires the atomic-flow publish callback. Called from server.go
// after the OCI handler exists. When unset, Export operates in
// download-only mode and any request with download=false is treated as
// download=true (server-inferred; matches "orbital-w/o-OCI" deployment).
func (h *Export) SetPublishFn(fn PublishExportedFunc) {
	h.publishFn = fn
}

func NewExport(db *ent.Client, dgraphURL, dgraphScratchURL, dgraphScratchAdminURL, dgraphScratchZeroURL, exportDir, scratchExportDir, schemaPath string, logger *slog.Logger) *Export {
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		logger.Warn("could not create export dir", "dir", exportDir, "err", err)
	}
	if err := os.MkdirAll(scratchExportDir, 0o755); err != nil {
		logger.Warn("could not create scratch export dir", "dir", scratchExportDir, "err", err)
	}
	return &Export{
		db:                    db,
		dgraphURL:             dgraphURL,
		dgraphScratchURL:      dgraphScratchURL,
		dgraphScratchAdminURL: dgraphScratchAdminURL,
		dgraphScratchZeroURL:  dgraphScratchZeroURL,
		exportDir:             exportDir,
		scratchExportDir:      scratchExportDir,
		schemaPath:            schemaPath,
		logger:                logger,
	}
}

type triggerResponse struct {
	JobID  string `json:"id"`
	Status string `json:"status"`
}

type statusResponse struct {
	JobID       string  `json:"id"`
	DataCenter  string  `json:"dataCenter"`
	Status      string  `json:"status"`
	// Phase surfaces the fine-grained step the atomic goroutine is on, for
	// clients that want to render a per-step progress list. Coarse `status`
	// stays authoritative for terminal-state gating. Values: pending,
	// exporting, bundling, pushing, signing, completed, failed. Empty
	// only if derivation raced with the initial insert.
	Phase       string  `json:"phase,omitempty"`
	Published   bool    `json:"published"`
	CreatedBy   string  `json:"createdBy"`
	Error       *string `json:"error,omitempty"`
	CompletedAt *string `json:"completedAt,omitempty"`
	CreatedAt   string  `json:"createdAt"`
}

// Trigger handles POST /api/v1/export
//
// @Summary     Trigger subgraph export
// @Description Triggers an async atomic export of the data center's configuration subgraph. When download=false (default) AND OCI is configured, the export chains into an OCI publish and the zip is discarded on completion. When download=true (or OCI is not configured), the export runs in download-only mode and the zip is retained on disk for the client to fetch via GET /api/v1/export/jobs/{jobId}/download. Returns 202 with a job ID immediately; poll GET /api/v1/export/jobs/{jobId} until terminal state. Returns 409 if an export or restore is already in progress.
// @Tags        export
// @Accept      json
// @Produce     json
// @Param       body body object true "Export request" SchemaExample({"orbId":"alaska:dc-01","download":false})
// @Success     202 {object} triggerResponse
// @Failure     409 {object} map[string]string
// @Router      /api/v1/export [post]
func (h *Export) Trigger(c echo.Context) error {
	var req struct {
		OrbID    string `json:"orbId"`
		Download bool   `json:"download"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.OrbID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "orbId is required")
	}
	datacenterID := req.OrbID

	// Server-inferred download mode when OCI is not configured — orbital
	// deployed without an OCI registry only supports the download flow.
	// Client stays deployment-agnostic (same request body regardless of
	// whether OCI is wired up on the server).
	download := req.Download
	if !download && h.publishFn == nil {
		download = true
	}

	dcName, dcOrbID, _, err := h.fetchDCInfo(c.Request().Context(), datacenterID)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "could not resolve datacenter: " + err.Error()})
	}

	// Scratch DGraph is shared — only one export can run at a time across all data centers.
	existing, err := h.db.ExportJob.Query().
		Where(exportjob.StatusIn(exportjob.StatusPending, exportjob.StatusRunning)).
		First(c.Request().Context())
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("check existing job: %w", err)
	}
	if existing != nil {
		return c.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("export already in progress (id: %s)", existing.ID),
			"id":    existing.ID.String(),
		})
	}

	existingRestore, err := h.db.RestoreJob.Query().
		Where(restorejob.StatusIn(restorejob.StatusPending, restorejob.StatusRunning)).
		First(c.Request().Context())
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("check restore jobs: %w", err)
	}
	if existingRestore != nil {
		return c.JSON(http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("restore in progress (id: %s)", existingRestore.ID),
		})
	}

	actor := actorFromContext(c)
	var actorPtr *string
	if actor != "" {
		actorPtr = &actor
	}

	job, err := h.db.ExportJob.Create().
		SetDatacenterID(datacenterID).
		SetDatacenterName(dcName).
		SetDatacenterOrbID(dcOrbID).
		SetStatus(exportjob.StatusPending).
		SetNillableCreatedBy(actorPtr).
		Save(c.Request().Context())
	if err != nil {
		return fmt.Errorf("create export job: %w", err)
	}

	// Audit event fires from runExport on completion — the atomic flow
	// produces ONE `export` event with the actual outcome (mode, publishedToOCI)
	// rather than a per-phase trail.
	go h.runExport(job.ID, download, actor, dcOrbID)

	return c.JSON(http.StatusAccepted, triggerResponse{
		JobID:  job.ID.String(),
		Status: string(job.Status),
	})
}

// List handles GET /api/v1/export/jobs
//
// @Summary     List export jobs
// @Description Returns the 50 most recent export jobs ordered by creation time.
// @Tags        export
// @Produce     json
// @Success     200 {array} statusResponse
// @Router      /api/v1/export/jobs [get]
func (h *Export) List(c echo.Context) error {
	jobs, err := h.db.ExportJob.Query().
		Order(exportjob.ByCreatedAt(sql.OrderDesc())).
		Limit(50).
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}

	// Detect stale jobs: completed jobs whose artifact file no longer exists.
	for _, job := range jobs {
		if job.Status == exportjob.StatusCompleted && job.ArtifactPath != nil {
			if _, statErr := os.Stat(*job.ArtifactPath); os.IsNotExist(statErr) {
				h.db.ExportJob.UpdateOneID(job.ID). //nolint:errcheck
					SetStatus(exportjob.StatusStale).
					Save(c.Request().Context())
				job.Status = exportjob.StatusStale
			}
		}
	}

	// Build a set of job IDs that have at least one published artifact.
	publishedJobIDs := map[uuid.UUID]bool{}
	if len(jobs) > 0 {
		jobIDs := make([]uuid.UUID, 0, len(jobs))
		for _, j := range jobs {
			jobIDs = append(jobIDs, j.ID)
		}
		if artifactRows, err := h.db.RegistryArtifact.Query().
			Where(registryartifact.ExportJobIDIn(jobIDs...)).
			Select(registryartifact.FieldExportJobID).
			All(c.Request().Context()); err == nil {
			for _, a := range artifactRows {
				publishedJobIDs[a.ExportJobID] = true
			}
		}
	}

	if c.Request().Header.Get("HX-Request") == "true" {
		ociConfigured := c.QueryParam("ociConfigured") == "true"
		// UI fragment renders ONLY retained-download rows: completed jobs
		// with a retained zip on disk AND no linked RegistryArtifact
		// (i.e. download-flow, not publish-flow). The full list is still
		// available via JSON API for troubleshooting.
		rows := make([]exportJobFragRow, 0, len(jobs))
		for _, job := range jobs {
			if publishedJobIDs[job.ID] {
				continue // publish-flow — audit history lives at /publish-history
			}
			if job.ArtifactPath == nil {
				continue // no retained zip
			}
			if job.Status != exportjob.StatusCompleted {
				continue // in-progress or failed — nothing to download
			}
			rows = append(rows, toExportJobFragRow(job, false))
		}
		tmpl, err := template.ParseFiles("web/templates/orbital/partials/export-jobs-tbody.gohtml")
		if err != nil {
			return fmt.Errorf("parse export fragment: %w", err)
		}
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return tmpl.Execute(c.Response(), exportJobsFragData{Rows: rows, OCIConfigured: ociConfigured, BasePath: h.basePath})
	}

	out := make([]statusResponse, 0, len(jobs))
	for _, job := range jobs {
		r := statusResponse{
			JobID:      job.ID.String(),
			DataCenter: job.DatacenterName,
			Status:     string(job.Status),
			Published:  publishedJobIDs[job.ID],
			CreatedAt:  job.CreatedAt.Format(time.RFC3339),
		}
		r.CreatedBy = job.CreatedBy
		if job.Error != nil {
			r.Error = job.Error
		}
		if job.CompletedAt != nil {
			s := job.CompletedAt.Format(time.RFC3339)
			r.CompletedAt = &s
		}
		out = append(out, r)
	}
	return c.JSON(http.StatusOK, out)
}

// Status handles GET /api/v1/export/jobs/:jobId
//
// @Summary     Get export job status
// @Description Returns the current status of an export job.
// @Tags        export
// @Produce     json
// @Param       jobId path string true "Job ID (UUID)"
// @Success     200 {object} statusResponse
// @Failure     404
// @Router      /api/v1/export/jobs/{jobId} [get]
func (h *Export) Status(c echo.Context) error {
	id, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}

	job, err := h.db.ExportJob.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get job: %w", err)
	}

	// Derive published from registry_artifacts (same convention as the list
	// endpoint at line 255). Without this the single-job endpoint always
	// returns published=false even after a successful atomic publish, breaking
	// clients that need to distinguish "export done but publish pending" from
	// "atomic export+publish complete".
	//
	// We fetch (not just Exist) so we can read the artifact's phase for the
	// UI's per-step progress renderer — the atomic goroutine transitions this
	// through pending → bundling → pushing → signing → completed as work
	// progresses (see internal/oci/publisher.go setPhase calls).
	artifact, err := h.db.RegistryArtifact.Query().
		Where(registryartifact.ExportJobID(job.ID)).
		First(c.Request().Context())
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("fetch artifact: %w", err)
	}

	resp := statusResponse{
		JobID:      job.ID.String(),
		DataCenter: job.DatacenterName,
		Status:     string(job.Status),
		Phase:      derivePhase(job, artifact),
		Published:  artifact != nil,
		CreatedBy:  job.CreatedBy,
		CreatedAt:  job.CreatedAt.Format(time.RFC3339),
	}
	if job.Error != nil {
		resp.Error = job.Error
	}
	if job.CompletedAt != nil {
		s := job.CompletedAt.Format(time.RFC3339)
		resp.CompletedAt = &s
	}

	return c.JSON(http.StatusOK, resp)
}

// Download handles GET /api/v1/export/jobs/:jobId/download.
//
// When no bundlers are configured, streams the raw export zip
// (data.json.gz + schema.gz) directly from disk — cheap and identical to the
// pre-bundler behavior.
//
// When bundlers ARE configured, calls each on the fly and packages the result
// as a courier-ready zip: data.json.gz + schema.gz + one file per bundler
// layer + layers.json (media-type manifest). This zip is directly consumable
// by orb's POST /api/v1/import/artifact endpoint, closing the courier flow
// for air-gapped modular data centers.
//
// Design note: this endpoint does NOT go through the OCI registry or Cosign.
// Courier trust model is "operator physically walked this in" — signature
// verification is a separate feature (not yet implemented). If bundlers change
// output between publish and download, the download reflects current graph
// state, not the historical published bytes. See docs/reference/OCI.md.
//
// @Summary     Download export artifact
// @Description Downloads a zip containing data.json.gz + schema.gz. When bundlers are configured, also includes bundler-produced layers + layers.json — the exact format orb's /import/artifact accepts.
// @Tags        export
// @Produce     application/zip
// @Param       jobId path string true "Job ID (UUID)"
// @Success     200
// @Failure     404
// @Failure     502
// @Router      /api/v1/export/jobs/{jobId}/download [get]
func (h *Export) Download(c echo.Context) error {
	id, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}

	job, err := h.db.ExportJob.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get job: %w", err)
	}

	if job.Status != exportjob.StatusCompleted || job.ArtifactPath == nil {
		return echo.ErrNotFound
	}

	filename := fmt.Sprintf("%s-%s.zip", job.DatacenterName, job.ID)
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	// Fast path: no bundlers configured → stream the raw zip from disk.
	// Same bytes the current Download has always produced.
	if len(h.defaultBundlerURLs) == 0 {
		f, err := os.Open(*job.ArtifactPath)
		if err != nil {
			return fmt.Errorf("open artifact: %w", err)
		}
		defer f.Close()
		return c.Stream(http.StatusOK, "application/zip", f)
	}

	// Bundler path: assemble a courier-ready bundle. Extract raw payload,
	// call each bundler, package everything into a new zip.
	if job.DatacenterOrbID == nil {
		h.logger.Warn("bundler-aware download skipped: export job missing DatacenterOrbID", "jobId", job.ID)
		f, err := os.Open(*job.ArtifactPath)
		if err != nil {
			return fmt.Errorf("open artifact: %w", err)
		}
		defer f.Close()
		return c.Stream(http.StatusOK, "application/zip", f)
	}

	dataGZ, schemaGZ, err := readRawExportZip(*job.ArtifactPath)
	if err != nil {
		return fmt.Errorf("read raw export: %w", err)
	}

	req := bundler.Request{OrbID: *job.DatacenterOrbID}
	layers, err := h.callBundlers(c.Request().Context(), req)
	if err != nil {
		h.logger.Error("bundler-aware download failed", "jobId", job.ID, "err", err)
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "bundler call failed: " + err.Error()})
	}

	zipBytes, err := buildCourierZip(dataGZ, schemaGZ, layers)
	if err != nil {
		return fmt.Errorf("build courier zip: %w", err)
	}

	return c.Blob(http.StatusOK, "application/zip", zipBytes)
}

// callBundlers invokes every configured bundler and returns the combined
// layers in a stable order (bundler order → layer order within each result).
// Each layer is stamped with its bundler's friendly name via l.Producer.
func (h *Export) callBundlers(ctx context.Context, req bundler.Request) ([]bundler.Layer, error) {
	var all []bundler.Layer
	for _, spec := range h.defaultBundlerURLs {
		name, url := bundler.ParseSpec(spec)
		client := bundler.New(name, url, h.bundlerTimeout, h.bundlerOpts...)
		result, err := client.Enrich(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for i := range result.Layers {
			result.Layers[i].Producer = name
		}
		all = append(all, result.Layers...)
	}
	return all, nil
}

// readRawExportZip extracts data.json.gz + schema.gz from the export's on-disk
// zip. Same shape as oci.extractZip but re-implemented locally to avoid a
// handler → oci import (oci already depends on handler in some paths, and
// extractZip is a tiny helper).
func readRawExportZip(zipPath string) (dataGZ, schemaGZ []byte, err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		switch zf.Name {
		case "data.json.gz":
			rc, err := zf.Open()
			if err != nil {
				return nil, nil, fmt.Errorf("open data.json.gz: %w", err)
			}
			dataGZ, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("read data.json.gz: %w", err)
			}
		case "schema.gz":
			rc, err := zf.Open()
			if err != nil {
				return nil, nil, fmt.Errorf("open schema.gz: %w", err)
			}
			schemaGZ, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, nil, fmt.Errorf("read schema.gz: %w", err)
			}
		}
	}
	if len(dataGZ) == 0 {
		return nil, nil, fmt.Errorf("export zip missing data.json.gz")
	}
	if len(schemaGZ) == 0 {
		return nil, nil, fmt.Errorf("export zip missing schema.gz")
	}
	return dataGZ, schemaGZ, nil
}

// courierLayerEntry mirrors the layers.json shape orb's /import/artifact expects
// (see internal/orbserver/import_handlers.go:410). Filename is unique per zip;
// mediaType is opaque to orbital — set by the bundler, consumed by orb's
// consumer dispatch.
type courierLayerEntry struct {
	MediaType string `json:"mediaType"`
	Filename  string `json:"filename"`
	// Producer is a display-only hint. Not consumed by orb today; documented in
	// the zip for operator debugging.
	Producer string `json:"producer,omitempty"`
}

// ociBundlerLayerStart is the OCI manifest position where bundler-produced
// layers begin. Positions 0 and 1 are always reserved for data.json.gz and
// schema.gz respectively (see oci.buildLayerMeta). Keep this in sync with
// that function's layout — the courier zip's filename numbering depends on it.
const ociBundlerLayerStart = 2

// buildCourierZip assembles the exact zip shape orb's /import/artifact accepts:
// data.json.gz + schema.gz + layers.json + one file per layer.
//
// Bundler layer filenames follow OCI Image Spec ordering:
//
//	layer-<oci-position>-<producer>.<ext>
//
// where <oci-position> matches the layer's index in the OCI manifest
// (positions 0/1 are data.json.gz + schema.gz; bundler layers start at 2)
// and <ext> is derived from the layer's media type (`.yaml` for `+yaml`,
// `.json` for `+json`, `.bin` fallback).
//
// Aligning zip filenames with OCI positions lets operators cross-reference
// between the layers modal (which shows the same position) and the zip
// contents without arithmetic. See docs/reference/OCI.md.
func buildCourierZip(dataGZ, schemaGZ []byte, layers []bundler.Layer) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	writeFile := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		return nil
	}

	if err := writeFile("data.json.gz", dataGZ); err != nil {
		return nil, err
	}
	if err := writeFile("schema.gz", schemaGZ); err != nil {
		return nil, err
	}

	manifest := make([]courierLayerEntry, 0, len(layers))
	for i, l := range layers {
		producer := l.Producer
		if producer == "" {
			producer = "unknown"
		}
		filename := fmt.Sprintf("layer-%d-%s%s", ociBundlerLayerStart+i, sanitizeForFilename(producer), extensionForMediaType(l.MediaType))
		if err := writeFile(filename, l.Data); err != nil {
			return nil, err
		}
		manifest = append(manifest, courierLayerEntry{
			MediaType: l.MediaType,
			Filename:  filename,
			Producer:  l.Producer,
		})
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal layers.json: %w", err)
	}
	if err := writeFile("layers.json", manifestJSON); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return buf.Bytes(), nil
}

// extensionForMediaType derives a file extension from a media type's structured
// syntax suffix (RFC 6838 §4.2.8). The suffix is the part after '+' — e.g.
// "application/vnd.armada.configbundle+yaml" → ".yaml". Media types without
// a suffix or with an unrecognized suffix fall back to ".bin".
//
// Extension is display-only for operator UX. The authoritative content type
// remains in layers.json; orb dispatches by media type, not filename.
func extensionForMediaType(mt string) string {
	plus := strings.LastIndex(mt, "+")
	if plus < 0 {
		return ".bin"
	}
	// Trim any parameters after ';' (e.g. "+yaml; charset=utf-8").
	suffix := strings.ToLower(strings.TrimSpace(mt[plus+1:]))
	if semi := strings.Index(suffix, ";"); semi >= 0 {
		suffix = strings.TrimSpace(suffix[:semi])
	}
	switch suffix {
	case "yaml", "yml":
		return ".yaml"
	case "json":
		return ".json"
	case "xml":
		return ".xml"
	case "gzip":
		return ".gz"
	case "zip":
		return ".zip"
	default:
		return ".bin"
	}
}

// sanitizeForFilename keeps producer names filesystem-safe. Alphanumerics + '-'
// pass through; everything else becomes '-'. Empty input → "unknown".
func sanitizeForFilename(s string) string {
	if s == "" {
		return "unknown"
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b = append(b, c)
		default:
			b = append(b, '-')
		}
	}
	return string(b)
}

// runExport is the async goroutine that drives the atomic export workflow.
// download=true means "export only, retain zip for download." download=false
// means "export then publish to OCI, discard zip." Emits a single `export`
// audit event on success, capturing mode + publishedToOCI. On failure, the
// zip is deleted (never retained through a failed atomic flow).
func (h *Export) runExport(jobID uuid.UUID, download bool, actor string, dcOrbID string) {
	timeout := h.timeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	log := h.logger.With("jobId", jobID, "download", download)

	// retainZip is true only when the whole atomic flow succeeded AND the
	// mode was download. Publish-mode success also cleans up the zip
	// (OCI has authoritative bytes; no reason to keep a local copy).
	var retainZip bool
	defer func() {
		if retainZip {
			return
		}
		// Cleanup: remove zip on disk + clear ArtifactPath. Fetches the row
		// once, ignores not-found (job may have been deleted mid-flight).
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		job, err := h.db.ExportJob.Get(cleanupCtx, jobID)
		if err != nil {
			return
		}
		if job.ArtifactPath != nil {
			if rmErr := os.Remove(*job.ArtifactPath); rmErr != nil && !os.IsNotExist(rmErr) {
				log.Warn("cleanup: remove zip failed", "path", *job.ArtifactPath, "err", rmErr)
			}
			if _, upErr := h.db.ExportJob.UpdateOneID(jobID).ClearArtifactPath().Save(cleanupCtx); upErr != nil {
				log.Warn("cleanup: clear artifact_path failed", "err", upErr)
			}
		}
	}()

	if _, err := h.db.ExportJob.UpdateOneID(jobID).
		SetStatus(exportjob.StatusRunning).
		SetStartedAt(time.Now()).
		Save(ctx); err != nil {
		log.Error("failed to mark job running", "err", err)
		return
	}

	if err := h.doExport(ctx, jobID, log); err != nil {
		log.Error("export failed", "err", err)
		h.markFailed(ctx, jobID, err.Error())
		return
	}

	// Export phase complete — zip is on disk, ArtifactPath is set, job.status
	// is Completed. Now branch on mode.
	if download {
		// Download flow: emit audit event, retain zip, done.
		h.emitExportEvent(actor, dcOrbID, jobID, "download", nil)
		retainZip = true
		return
	}

	// Publish flow: chain into the OCI push. publishFn is non-nil here —
	// Trigger already inferred download=true when it was nil.
	result, err := h.publishFn(ctx, jobID, actor)
	if err != nil {
		log.Error("publish failed after export", "err", err)
		h.markFailed(ctx, jobID, "publish: "+err.Error())
		// retainZip stays false → defer removes the zip. No half-states.
		return
	}

	h.emitExportEvent(actor, dcOrbID, jobID, "publish", result)
	// retainZip stays false → defer removes the zip. OCI has the bytes.
}

// markFailed writes the terminal failure state for the ExportJob row.
func (h *Export) markFailed(ctx context.Context, jobID uuid.UUID, errStr string) {
	h.db.ExportJob.UpdateOneID(jobID). //nolint:errcheck
						SetStatus(exportjob.StatusFailed).
						SetError(errStr).
						Save(ctx)
}

// derivePhase collapses ExportJob + RegistryArtifact state into a single
// user-facing phase string for the /export progress UI. Coarse job.Status
// is authoritative for terminal transitions; per-step phase only refines
// the running window into exporting → bundling → pushing → signing.
//
//   pending    → job created, goroutine not yet running
//   exporting  → job.status=running AND no artifact yet (DGraph export step)
//   bundling|pushing|signing → artifact.status mirrors the atomic publish leg
//   completed  → job.status=completed
//   failed     → job.status=failed (error carries the phase context in text)
func derivePhase(job *ent.ExportJob, artifact *ent.RegistryArtifact) string {
	switch job.Status {
	case exportjob.StatusCompleted:
		return "completed"
	case exportjob.StatusFailed:
		return "failed"
	case exportjob.StatusPending:
		return "pending"
	case exportjob.StatusRunning:
		if artifact == nil {
			return "exporting"
		}
		switch artifact.Status {
		case registryartifact.StatusBundling:
			return "bundling"
		case registryartifact.StatusPushing:
			return "pushing"
		case registryartifact.StatusSigning:
			return "signing"
		case registryartifact.StatusCompleted:
			// Artifact done but ExportJob not yet marked completed — very
			// narrow race. Report the coarse view.
			return "signing"
		default:
			// StatusPending on the artifact means the record was inserted
			// but no phase has been stamped yet — that hand-off happens in
			// PublishExportedJob within milliseconds of creation.
			return "exporting"
		}
	}
	return ""
}

// emitExportEvent records the successful atomic-flow outcome to the audit
// log. Single event per user action (Option 2 from the audit-events design
// discussion). Publish-mode fills the OCI fields; download-mode leaves them
// zero-valued but sets mode + publishedToOCI=false.
func (h *Export) emitExportEvent(actor, dcOrbID string, jobID uuid.UUID, mode string, publishResult *PublishExportedResult) {
	details := map[string]any{
		"exportJobId":     jobID.String(),
		"mode":            mode,
		"publishedToOCI":  publishResult != nil,
		"datacenterOrbId": dcOrbID,
	}
	if publishResult != nil {
		details["artifactId"] = publishResult.ArtifactID
		details["tag"] = publishResult.Tag
		details["ociDigest"] = publishResult.Digest
		details["sizeBytes"] = publishResult.SizeBytes
		details["layerCount"] = publishResult.LayerCount
	}
	var resourceIDs []string
	if dcOrbID != "" {
		resourceIDs = []string{dcOrbID}
	}
	writeAuditEvent(h.db, h.logger,
		"management", actor, "export",
		[]string{"export"},
		[]string{"DataCenter"},
		resourceIDs,
		details,
	)
}

func (h *Export) doExport(ctx context.Context, jobID uuid.UUID, log *slog.Logger) error {
	job, err := h.db.ExportJob.Get(ctx, jobID)
	if err != nil {
		return fmt.Errorf("get job: %w", err)
	}

	// 1. Resolve namespace name from DC ID
	log.Info("resolving DC namespace")
	_, _, namespaceName, err := h.fetchDCInfo(ctx, job.DatacenterID)
	if err != nil {
		return fmt.Errorf("fetch DC info: %w", err)
	}

	// 2. Query the full namespace subgraph from blue via DQL.
	// Uses has(ConfigItem.namespace) + uid_in to find every node in the namespace
	// regardless of type, then expand(_all_) to get all predicates without
	// enumerating schema types. New ConfigItem types are automatically included.
	log.Info("querying namespace subgraph from blue DGraph", "namespace", namespaceName)
	nodes, err := h.fetchNamespaceSubgraph(ctx, namespaceName)
	if err != nil {
		return fmt.Errorf("fetch namespace subgraph: %w", err)
	}
	log.Info("subgraph fetched", "nodes", len(nodes))
	if len(nodes) == 0 {
		return fmt.Errorf("namespace %q has no nodes in blue DGraph — nothing to export", namespaceName)
	}

	// 3. Wipe scratch so stale data from a previous export cannot bleed into this one.
	log.Info("wiping scratch DGraph")
	if err := h.wipeScratch(ctx); err != nil {
		return fmt.Errorf("wipe scratch: %w", err)
	}

	// 4. Apply schema to scratch so GraphQL layer is aware of all types before
	// loading data. Safe to run after a manual wipe.
	log.Info("applying schema to scratch DGraph")
	if err := h.applyScratchSchema(ctx); err != nil {
		return fmt.Errorf("apply scratch schema: %w", err)
	}

	// 5. Bump scratch Zero's UID lease to cover the highest UID in the subgraph,
	// then load the subgraph into scratch preserving original UIDs from blue.
	log.Info("loading subgraph into scratch DGraph")
	if err := h.bumpScratchUIDLease(ctx, nodes); err != nil {
		return fmt.Errorf("bump scratch UID lease: %w", err)
	}
	if err := h.loadSubgraphIntoScratch(ctx, nodes); err != nil {
		return fmt.Errorf("load subgraph into scratch: %w", err)
	}

	// 6. Create a per-job directory under the scratch export mount.
	// Host path:      scratchExportDir/<jobID>/
	// Container path: /dgraph/export/<jobID>/   (passed as destination in the export mutation)
	// This gives each job an isolated, clearly-labelled output directory.
	jobScratchDir := filepath.Join(h.scratchExportDir, jobID.String())
	if err := os.MkdirAll(jobScratchDir, 0o755); err != nil {
		return fmt.Errorf("create job scratch dir: %w", err)
	}

	jobContainerDir := "/dgraph/export/" + jobID.String()

	// 7. Trigger native DGraph export mutation on scratch, scoped to the job's directory.
	log.Info("triggering native DGraph export on scratch", "destination", jobContainerDir)
	if err := h.triggerScratchExport(ctx, jobContainerDir); err != nil {
		return fmt.Errorf("trigger scratch export: %w", err)
	}

	// 8. Find the exported json.gz written by DGraph into the job's directory.
	log.Info("locating exported data file", "dir", jobScratchDir)
	dataGZPath, err := h.findScratchExport(jobScratchDir)
	if err != nil {
		return fmt.Errorf("find scratch export: %w", err)
	}
	log.Info("found exported file", "path", dataGZPath)

	// 9. Read data.json.gz (already gzipped by DGraph — do not re-gzip)
	dataGZ, err := os.ReadFile(dataGZPath)
	if err != nil {
		return fmt.Errorf("read data.json.gz: %w", err)
	}

	// 10. Read and gzip the schema
	schemaBytes, err := os.ReadFile(h.schemaPath)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	schemaGZ, err := gzipBytes(schemaBytes)
	if err != nil {
		return fmt.Errorf("gzip schema: %w", err)
	}

	// 11. Write zip archive
	zipPath := filepath.Join(h.exportDir, fmt.Sprintf("orbital-export-%s.zip", jobID))
	if err := writeZip(zipPath, dataGZ, schemaGZ, nil, nil); err != nil {
		return fmt.Errorf("write zip: %w", err)
	}
	log.Info("artifact written", "path", zipPath)

	// 12. Mark completed
	_, err = h.db.ExportJob.UpdateOneID(jobID).
		SetStatus(exportjob.StatusCompleted).
		SetArtifactPath(zipPath).
		SetCompletedAt(time.Now()).
		Save(ctx)
	return err
}

// ── DGraph helpers ────────────────────────────────────────────────────────────

// dqlBase derives the DQL HTTP root from a GraphQL URL.
// e.g. http://localhost:8080/graphql → http://localhost:8080
func dqlBase(graphqlURL string) string {
	return strings.TrimSuffix(graphqlURL, "/graphql")
}

// fetchDCInfo queries blue GraphQL for the DC name, orbId, and its namespace.
// datacenterOrbID must be the orbId of the data center (e.g. "alaska:dc-01").
func (h *Export) fetchDCInfo(ctx context.Context, datacenterOrbID string) (name, orbID, namespaceName string, err error) {
	query := fmt.Sprintf(`{ getDataCenter(orbId: %q) { name orbId namespace } }`, datacenterOrbID)
	body, _ := json.Marshal(map[string]string{"query": query})
	resp, err := http.Post(h.dgraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			GetDataCenter struct {
				Name      string `json:"name"`
				OrbID     string `json:"orbId"`
				Namespace string `json:"namespace"`
			} `json:"getDataCenter"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", "", err
	}
	dc := result.Data.GetDataCenter
	if dc.Name == "" {
		return "", "", "", fmt.Errorf("data center %q not found in DGraph", datacenterOrbID)
	}
	return dc.Name, dc.OrbID, dc.Namespace, nil
}

// fetchUIDPredicates queries the DGraph schema and returns all predicate names
// whose type is uid. These must be listed explicitly in DQL queries — expand(_all_)
// only returns scalar predicates.
func (h *Export) fetchUIDPredicates(ctx context.Context) ([]string, error) {
	payload := map[string]string{"query": "schema {}"}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(dqlBase(h.dgraphURL)+"/query", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("schema query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("schema query failed (%d): %s", resp.StatusCode, b)
	}
	var result struct {
		Data struct {
			Schema []struct {
				Predicate string `json:"predicate"`
				Type      string `json:"type"`
			} `json:"schema"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode schema response: %w", err)
	}
	var preds []string
	for _, s := range result.Data.Schema {
		if s.Type == "uid" {
			preds = append(preds, s.Predicate)
		}
	}
	return preds, nil
}

// fetchNamespaceSubgraph retrieves every node in the target namespace from blue
// using two DQL result blocks that are merged in Go:
//
//  1. "items" — expand(_all_) for scalar predicates. DGraph silently drops UID
//     predicates from expand(_all_) when they form cycles (which all edges in our
//     schema do), so scalars and edges must be fetched separately.
//
//  2. "edges" — explicit listing of every UID-type predicate with { uid } sub-
//     selection. The predicate list is derived from the live DGraph schema so new
//     edge types are included automatically without code changes.
//
// The two result sets are merged by UID before being sent to scratch as a single
// DQL mutation, ensuring UIDs, scalar predicates, and edges are all written in one
// pass.
func (h *Export) fetchNamespaceSubgraph(ctx context.Context, namespaceName string) ([]map[string]any, error) {
	uidPreds, err := h.fetchUIDPredicates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch uid predicates: %w", err)
	}

	var edgeLines strings.Builder
	for _, p := range uidPreds {
		fmt.Fprintf(&edgeLines, "\t\t\t%s { uid }\n", p)
	}

	dql := fmt.Sprintf(`{
		ns(func: type(Namespace)) @filter(eq(Namespace.name, %q)) { uid dgraph.type expand(_all_) }
		items(func: eq(ConfigItem.namespace, %q)) {
			uid
			dgraph.type
			expand(_all_)
		}
		edges(func: eq(ConfigItem.namespace, %q)) {
			uid
			%s
		}
	}`, namespaceName, namespaceName, namespaceName, edgeLines.String())

	payload := map[string]string{"query": dql}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(dqlBase(h.dgraphURL)+"/query", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dql query: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dql query failed (%d): %s", resp.StatusCode, b)
	}

	var result struct {
		Data struct {
			Ns    []map[string]any `json:"ns"`
			Items []map[string]any `json:"items"`
			Edges []map[string]any `json:"edges"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode dql response: %w", err)
	}

	if len(result.Data.Ns) == 0 {
		return nil, fmt.Errorf("namespace %q not found in DGraph", namespaceName)
	}

	// Merge edge predicates into the corresponding scalar nodes by UID.
	edgesByUID := make(map[string]map[string]any, len(result.Data.Edges))
	for _, e := range result.Data.Edges {
		if uid, ok := e["uid"].(string); ok {
			edgesByUID[uid] = e
		}
	}
	for _, node := range result.Data.Items {
		uid, ok := node["uid"].(string)
		if !ok {
			continue
		}
		if edges, ok := edgesByUID[uid]; ok {
			for k, v := range edges {
				if k == "uid" {
					continue
				}
				node[k] = v
			}
		}
	}

	nodes := make([]map[string]any, 0, 1+len(result.Data.Items))
	nodes = append(nodes, result.Data.Ns...)
	nodes = append(nodes, result.Data.Items...)
	return nodes, nil
}

// loadSubgraphIntoScratch inserts all nodes into scratch via DQL mutate.
// Original UIDs from blue are preserved in the mutation so that relationships
// remain intact and the resulting DGraph export has stable UIDs for orb import.
func (h *Export) loadSubgraphIntoScratch(ctx context.Context, nodes []map[string]any) error {
	payload := map[string]any{"set": nodes}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	mutateURL := dqlBase(h.dgraphScratchURL) + "/mutate?commitNow=true"
	h.logger.Info("posting subgraph to scratch", "url", mutateURL, "nodes", len(nodes))
	resp, err := http.Post(mutateURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dql mutate: %w", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	h.logger.Info("scratch mutate response", "status", resp.StatusCode, "body", string(b))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("dql mutate failed (%d): %s", resp.StatusCode, b)
	}
	var mutResp struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &mutResp); err == nil && len(mutResp.Errors) > 0 {
		return fmt.Errorf("dql mutate error: %s", mutResp.Errors[0].Message)
	}
	return nil
}

// bumpScratchUIDLease advances the scratch Zero's UID lease to be at least as
// large as the highest UID present in the subgraph. Without this, DGraph rejects
// mutations that use UIDs higher than the current lease.
func (h *Export) bumpScratchUIDLease(ctx context.Context, nodes []map[string]any) error {
	var maxUID uint64
	for _, node := range nodes {
		if uid, ok := node["uid"].(string); ok {
			v, err := strconv.ParseUint(strings.TrimPrefix(uid, "0x"), 16, 64)
			if err == nil && v > maxUID {
				maxUID = v
			}
		}
	}
	if maxUID == 0 {
		return nil
	}
	assignURL := fmt.Sprintf("%s/assign?what=uids&num=%d", strings.TrimSuffix(h.dgraphScratchZeroURL, "/"), maxUID+1000)
	h.logger.Info("bumping scratch UID lease", "maxUID", maxUID, "url", assignURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assignURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("assign uids: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("assign uids failed (%d): %s", resp.StatusCode, b)
	}
	return nil
}

func (h *Export) wipeScratch(ctx context.Context) error {
	alterURL := dqlBase(h.dgraphScratchURL) + "/alter"
	resp, err := http.Post(alterURL, "application/json", strings.NewReader(`{"drop_all": true}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("alter failed (%d): %s", resp.StatusCode, b)
	}
	return nil
}

func (h *Export) applyScratchSchema(ctx context.Context) error {
	schemaBytes, err := os.ReadFile(h.schemaPath)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	schemaURL := strings.TrimSuffix(h.dgraphScratchAdminURL, "/") + "/schema"
	resp, err := http.Post(schemaURL, "application/octet-stream", bytes.NewReader(schemaBytes))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("schema apply failed (%d): %s", resp.StatusCode, b)
	}
	return nil
}

func (h *Export) triggerScratchExport(ctx context.Context, destination string) error {
	mutation := fmt.Sprintf(`{"query": "mutation { export(input: { format: \"json\", destination: \"%s\" }) { response { code message } } }"}`, destination)
	resp, err := http.Post(h.dgraphScratchAdminURL, "application/json", strings.NewReader(mutation))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("export mutation failed (%d): %s", resp.StatusCode, b)
	}
	h.logger.Info("scratch export mutation response", "body", string(b))
	return nil
}

// findScratchExport walks dir and returns the first *.json.gz file.
// DGraph writes to a timestamped subdirectory under the destination path.
// Retries for up to 15 seconds since DGraph may flush the file slightly after the mutation returns.
func (h *Export) findScratchExport(dir string) (string, error) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var found string
		var seen []string
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				seen = append(seen, path)
				if strings.HasSuffix(path, ".json.gz") {
					found = path
					return filepath.SkipAll
				}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		h.logger.Info("scratch export dir contents", "dir", dir, "files", seen)
		if found != "" {
			return found, nil
		}
		time.Sleep(1 * time.Second)
	}
	return "", fmt.Errorf("no json.gz found in %s after export", dir)
}

// ── Archive helpers ───────────────────────────────────────────────────────────

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeZip(path string, dataGZ, dqlSchemaGZ, gqlSchemaGZ, manifestJSON []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"manifest.json", manifestJSON},
		{"data.json.gz", dataGZ},
		{"schema.gz", dqlSchemaGZ},
		{"gql_schema.gz", gqlSchemaGZ},
	} {
		if entry.data == nil {
			continue
		}
		w, err := zw.Create(entry.name)
		if err != nil {
			return err
		}
		if _, err := w.Write(entry.data); err != nil {
			return err
		}
	}
	return nil
}

// ── Fragment renderer ─────────────────────────────────────────────────────────

type exportJobFragRow struct {
	JobID       string
	DataCenter  string
	Status      string
	StatusClass string
	Published   bool
	CreatedAt   string
	CreatedBy   string
	CompletedAt string
	CanDownload bool
	CanPublish  bool
}

type exportJobsFragData struct {
	Rows          []exportJobFragRow
	OCIConfigured bool
	BasePath      string
}

func toExportJobFragRow(job *ent.ExportJob, published bool) exportJobFragRow {
	statusClass := map[string]string{
		"pending":   "is-warning is-light",
		"running":   "is-info is-light",
		"completed": "is-success is-light",
		"failed":    "is-danger is-light",
		"stale":     "is-light",
	}[string(job.Status)]
	completedAt := "—"
	if job.CompletedAt != nil {
		completedAt = job.CompletedAt.UTC().Format("2006-01-02 15:04:05")
	}
	return exportJobFragRow{
		JobID:       job.ID.String(),
		DataCenter:  job.DatacenterName,
		Status:      string(job.Status),
		StatusClass: statusClass,
		Published:   published,
		CreatedAt:   job.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
		CreatedBy:   job.CreatedBy,
		CompletedAt: completedAt,
		CanDownload: job.Status == exportjob.StatusCompleted && job.ArtifactPath != nil,
		CanPublish:  job.Status == exportjob.StatusCompleted && job.ArtifactPath != nil,
	}
}

// ListRows handles GET /api/v1/export/jobs/rows
// Returns an HTML fragment containing the export jobs tbody rows.
