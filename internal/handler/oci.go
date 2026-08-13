package handler

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/internal/bundler"
	"github.com/armada/orbital/internal/oci"
	"github.com/armada/orbital/internal/ocitype"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// OCI handles OCI artifact publishing endpoints.
type OCI struct {
	db               *ent.Client
	publisher        *oci.Publisher
	cfg              oci.Config
	scratchExportDir string
	logger           *slog.Logger
	bundlerTimeout   time.Duration
	bundlerOpts      []bundler.ClientOption
	// defaultBundlerURLs is consulted when a publish request body omits
	// `bundlers`. Set to the in-pod sidecar URL (http://localhost:8020/bundle)
	// in deploy/base/deploy.yaml so UI publishes — which form-encode and don't
	// supply JSON — still hit the sidecar.
	defaultBundlerURLs []string
	basePath           string // URL base path for fragment-rendered hx-* attributes
}

// SetBasePath configures the URL base path used by HTML fragments rendered by this handler.
func (h *OCI) SetBasePath(bp string) { h.basePath = bp }

// IsPublisherConfigured reports whether OCI publishing is configured. Used
// by server.go to gate wiring the atomic-flow publish callback into the
// Export handler — when unconfigured, Export operates in download-only mode.
func (h *OCI) IsPublisherConfigured() bool { return h.publisher != nil }

// NewOCI creates an OCI handler. publisher may be nil when OCI is not configured.
// bundlerTimeout and bundlerOpts are applied when constructing per-request bundler clients.
// defaultBundlerURLs supplies a fallback list when the publish request omits `bundlers`.
func NewOCI(db *ent.Client, cfg oci.Config, scratchExportDir string, logger *slog.Logger, bundlerTimeout time.Duration, defaultBundlerURLs []string, bundlerOpts ...bundler.ClientOption) *OCI {
	var pub *oci.Publisher
	if cfg.Registry != "" && cfg.SigningKeyPath != "" {
		pub = oci.New(db, cfg, logger)
	}
	return &OCI{
		db:                 db,
		publisher:          pub,
		cfg:                cfg,
		scratchExportDir:   scratchExportDir,
		logger:             logger,
		bundlerTimeout:     bundlerTimeout,
		bundlerOpts:        bundlerOpts,
		defaultBundlerURLs: defaultBundlerURLs,
	}
}

type artifactResponse struct {
	ID                    int                     `json:"id"`
	ExportJobID           string                  `json:"exportJobId"`
	DatacenterID          string                  `json:"datacenterId"`
	DatacenterName        string                  `json:"datacenterName"`
	Registry              string                  `json:"registry"`
	Repository            string                  `json:"repository"`
	Tag                   string                  `json:"tag"`
	Digest                *string                 `json:"digest,omitempty"`
	SizeBytes             *int64                  `json:"sizeBytes,omitempty"`
	Signed                bool                    `json:"signed"`
	SigningKeyFingerprint *string                 `json:"signingKeyFingerprint,omitempty"`
	Status                string                  `json:"status"`
	InitiatedAt           string                  `json:"initiatedAt"`
	CompletedAt           *string                 `json:"completedAt,omitempty"`
	Error                 *string                 `json:"error,omitempty"`
	Enriched              bool                    `json:"enriched"`
	BundlerError          *string                 `json:"bundlerError,omitempty"`
	Layers                []ocitype.ArtifactLayer `json:"layers,omitempty"`
}

// PublishExportedJob runs the OCI publish half of the atomic export→publish
// flow synchronously. Called by handler.Export as its `publishFn` callback
// after the export goroutine has produced an on-disk zip. Returns non-nil
// error on any failure (bundler, push, sign); caller marks the ExportJob
// failed and cleans up the zip.
//
// This is NOT an HTTP handler — no *echo.Context. It's a package-internal
// entry point wired via server.go's SetPublishFn.
func (h *OCI) PublishExportedJob(ctx context.Context, jobID uuid.UUID) (*PublishExportedResult, error) {
	if h.publisher == nil {
		return nil, fmt.Errorf("OCI publishing is not configured")
	}

	job, err := h.db.ExportJob.Get(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("get job: %w", err)
	}
	if job.ArtifactPath == nil {
		return nil, fmt.Errorf("export job has no artifact path")
	}
	if _, err := os.Stat(*job.ArtifactPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("export artifact file no longer exists")
	}

	repoName, tag, err := h.nextTagForJob(ctx, job)
	if err != nil {
		return nil, fmt.Errorf("compute tag: %w", err)
	}

	artifact, err := h.db.RegistryArtifact.Create().
		SetExportJobID(job.ID).
		SetDatacenterID(job.DatacenterID).
		SetDatacenterName(job.DatacenterName).
		SetRegistry(h.cfg.Registry).
		SetRepository(repoName).
		SetTag(tag).
		SetStatus(registryartifact.StatusPending).
		SetInitiatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create artifact record: %w", err)
	}

	// Build bundler clients from configured defaults. Per-request bundler
	// override (which the old POST /api/v1/export/jobs/:id/publish endpoint
	// supported) is dropped — the atomic flow is UI-triggered, and the UI
	// doesn't need per-request bundler selection.
	var bundlerClients []*bundler.Client
	for _, spec := range h.defaultBundlerURLs {
		name, url := bundler.ParseSpec(spec)
		bundlerClients = append(bundlerClients, bundler.New(name, url, h.bundlerTimeout))
	}

	// Stamp the first real phase so any poller sees "bundling" or "pushing"
	// instead of the transient "pending" state.
	firstPhase := registryartifact.StatusPushing
	if len(bundlerClients) > 0 {
		firstPhase = registryartifact.StatusBundling
	}
	if _, err := h.db.RegistryArtifact.UpdateOneID(artifact.ID).SetStatus(firstPhase).Save(ctx); err == nil {
		artifact.Status = firstPhase
	}

	result, err := h.publisher.Publish(ctx, artifact.ID, job, tag, bundlerClients)
	if err != nil {
		return nil, err
	}

	return &PublishExportedResult{
		ArtifactID: artifact.ID,
		Tag:        tag,
		Digest:     result.Digest,
		SizeBytes:  result.SizeBytes,
		LayerCount: result.LayerCount,
	}, nil
}

// DeleteArtifact handles DELETE /api/v1/export/jobs/:jobId/artifact.
// Removes the on-disk zip and nullifies artifact_path on the ExportJob row;
// the job row itself stays as an audit record. Used by the UI's Retained
// Downloads section for operator-initiated cleanup of zips retained by the
// download flow.
//
// @Summary     Delete export artifact
// @Description Removes the retained zip for a download-flow export. The ExportJob row remains for audit; only the on-disk artifact and artifact_path are cleared. 404 if job or artifact does not exist.
// @Tags        export
// @Produce     json
// @Param       jobId path string true "Job ID (UUID)"
// @Success     204
// @Failure     404 {object} errorResponse
// @Router      /api/v1/export/jobs/{jobId}/artifact [delete]
func (h *OCI) DeleteArtifact(c echo.Context) error {
	jobID, err := uuid.Parse(c.Param("jobId"))
	if err != nil {
		return echo.ErrBadRequest
	}
	job, err := h.db.ExportJob.Get(c.Request().Context(), jobID)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get job: %w", err)
	}
	if job.ArtifactPath == nil {
		return echo.ErrNotFound
	}
	artifactPath := *job.ArtifactPath
	if rmErr := os.Remove(artifactPath); rmErr != nil && !os.IsNotExist(rmErr) {
		h.logger.Warn("delete artifact: remove failed", "path", artifactPath, "err", rmErr)
	}
	if _, err := h.db.ExportJob.UpdateOneID(jobID).ClearArtifactPath().Save(c.Request().Context()); err != nil {
		return fmt.Errorf("clear artifact_path: %w", err)
	}
	writeAuditEvent(h.db, h.logger, "management", actorFromContext(c), "deleteArtifact",
		[]string{"deleteArtifact"},
		nil,
		nil,
		map[string]any{"jobId": jobID.String(), "artifactPath": artifactPath},
	)
	return c.NoContent(http.StatusNoContent)
}

// ListArtifacts handles GET /api/v1/oci/artifacts
//
// @Summary     List OCI artifacts
// @Description Returns the 100 most recent OCI artifacts ordered by publish time descending.
// @Tags        oci
// @Produce     json
// @Success     200 {array} artifactResponse
// @Router      /api/v1/oci/artifacts [get]
func (h *OCI) ListArtifacts(c echo.Context) error {
	artifacts, err := h.db.RegistryArtifact.Query().
		Order(registryartifact.ByInitiatedAt(sql.OrderDesc())).
		Limit(100).
		WithExportJob().
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("list artifacts: %w", err)
	}
	if c.Request().Header.Get("HX-Request") == "true" {
		rows := make([]artifactFragRow, 0, len(artifacts))
		for _, a := range artifacts {
			rows = append(rows, toArtifactFragRow(a, h.basePath))
		}
		enrichPreviousCompleted(rows)
		tmpl, err := template.ParseFiles("web/templates/orbital/partials/artifacts-tbody.gohtml")
		if err != nil {
			return fmt.Errorf("parse artifacts fragment: %w", err)
		}
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return renderHTML(c, tmpl, "", rows)
	}
	out := make([]artifactResponse, 0, len(artifacts))
	for _, a := range artifacts {
		out = append(out, toArtifactResponse(a))
	}
	return c.JSON(http.StatusOK, out)
}

// enrichPreviousCompleted fills PreviousExportedAtRFC3339 + HasPrevious on
// each COMPLETED row. "Previous" is defined by SNAPSHOT chronology (export
// completed_at), not display order, so the "publish out of order" case
// (v2 pushed before v1 for the same DC) still produces correct diffs.
//
// For each row we scan all other completed rows in the same DC and pick
// the one whose ExportedAt is the greatest value strictly less than this
// row's ExportedAt. First publish of a DC (no earlier export) stays unset —
// the template renders an empty-state message; the mutations captured by
// the first publish are visible on the DC's own audit tab.
func enrichPreviousCompleted(rows []artifactFragRow) {
	for i := range rows {
		if rows[i].Status != "completed" || rows[i].ExportedAtRFC3339 == "" {
			continue
		}
		var bestPrev string
		for j := range rows {
			if i == j || rows[j].Status != "completed" || rows[j].ExportedAtRFC3339 == "" {
				continue
			}
			if rows[j].DatacenterOrbID == "" || rows[j].DatacenterOrbID != rows[i].DatacenterOrbID {
				continue
			}
			// Lexical compare works — RFC3339 timestamps sort chronologically.
			if rows[j].ExportedAtRFC3339 >= rows[i].ExportedAtRFC3339 {
				continue
			}
			if bestPrev == "" || rows[j].ExportedAtRFC3339 > bestPrev {
				bestPrev = rows[j].ExportedAtRFC3339
			}
		}
		if bestPrev != "" {
			rows[i].PreviousExportedAtRFC3339 = bestPrev
			rows[i].HasPrevious = true
		}
	}
}

// nextTagForJob computes the suggested next tag for the data center associated
// with an export job. Uses Repository (stable) — not DatacenterID, which is a
// DGraph internal UID that changes on reseed/restore — and takes max+1 over
// existing version numbers rather than counting rows.
//
// Filtered to StatusCompleted: failed attempts (bundler errors, push failures,
// signing errors) never reached ACR, so the tag number they tentatively
// claimed should be reusable. Counting them would burn a tag per failed retry
// and create gaps in the ACR-visible sequence (v10, v11-FAILED, v12-FAILED,
// v13 → ACR has v10 + v13 with no v11/v12). After this filter, retries reuse
// the same tag until one succeeds.
func (h *OCI) nextTagForJob(ctx context.Context, job *ent.ExportJob) (repoName, tag string, err error) {
	repoName = oci.RepoForDC(h.cfg.Registry, h.cfg.Repo, job.DatacenterName)
	existing, err := h.db.RegistryArtifact.Query().
		Where(
			registryartifact.Repository(repoName),
			registryartifact.StatusEQ(registryartifact.StatusCompleted),
		).
		Select(registryartifact.FieldTag).
		All(ctx)
	if err != nil {
		return "", "", fmt.Errorf("query artifact tags: %w", err)
	}
	return repoName, oci.NextTagAfter(existing), nil
}

// GetArtifact handles GET /api/v1/oci/artifacts/:id
//
// @Summary     Get OCI artifact
// @Description Returns a single OCI artifact record by ID.
// @Tags        oci
// @Produce     json
// @Param       id path int true "Artifact ID"
// @Success     200 {object} artifactResponse
// @Failure     404 {object} errorResponse
// @Router      /api/v1/oci/artifacts/{id} [get]
func (h *OCI) GetArtifact(c echo.Context) error {
	id := 0
	if _, err := fmt.Sscan(c.Param("id"), &id); err != nil || id == 0 {
		return echo.ErrBadRequest
	}

	a, err := h.db.RegistryArtifact.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get artifact: %w", err)
	}

	return c.JSON(http.StatusOK, toArtifactResponse(a))
}

// PublicKey handles GET /api/v1/oci/public-key
//
// @Summary     Get OCI signing public key
// @Description Returns the PEM-encoded public key corresponding to the configured OCI signing key. Used by edge consumers to verify artifact signatures.
// @Tags        oci
// @Produce     application/x-pem-file
// @Success     200
// @Failure     503 {object} errorResponse
// @Router      /api/v1/oci/public-key [get]
func (h *OCI) PublicKey(c echo.Context) error {
	if h.cfg.SigningKeyPath == "" {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "signing key not configured")
	}
	pubPEM, err := oci.PublicKeyPEM(h.cfg.SigningKeyPath)
	if err != nil {
		return fmt.Errorf("load public key: %w", err)
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="cosign.pub"`)
	return c.Blob(http.StatusOK, "application/x-pem-file", pubPEM)
}

// TestConnection handles POST /api/v1/oci/test-connection
//
// @Summary     Test OCI registry connection
// @Description Pings the configured OCI registry to verify credentials and reachability. Returns {"ok": true} on success or {"ok": false, "error": "..."} on failure.
// @Tags        oci
// @Produce     json
// @Success     200 {object} map[string]any
// @Failure     503 {object} errorResponse
// @Router      /api/v1/oci/test-connection [post]
func (h *OCI) TestConnection(c echo.Context) error {
	if h.cfg.Registry == "" {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "registry not configured")
	}
	// Attempt a simple registry ping via oras registry resolution.
	// This is intentionally minimal — just validates credentials/reachability.
	err := testRegistryConnection(h.cfg.Registry, h.cfg.Username, h.cfg.Password, h.cfg.AllowHTTP)
	if err != nil {
		return c.JSON(http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// ── helpers ───────────────────────────────────────────────────────────────────

func toArtifactResponse(a *ent.RegistryArtifact) artifactResponse {
	r := artifactResponse{
		ID:             a.ID,
		ExportJobID:    a.ExportJobID.String(),
		DatacenterID:   a.DatacenterID,
		DatacenterName: a.DatacenterName,
		Registry:       a.Registry,
		Repository:     a.Repository,
		Tag:            a.Tag,
		Signed:         a.Signed,
		Status:         string(a.Status),
		InitiatedAt:    a.InitiatedAt.Format(time.RFC3339),
	}
	if a.Digest != nil {
		r.Digest = a.Digest
	}
	if a.SizeBytes != nil {
		r.SizeBytes = a.SizeBytes
	}
	if a.SigningKeyFingerprint != nil {
		r.SigningKeyFingerprint = a.SigningKeyFingerprint
	}
	if a.CompletedAt != nil {
		s := a.CompletedAt.Format(time.RFC3339)
		r.CompletedAt = &s
	}
	if a.Error != nil {
		r.Error = a.Error
	}
	r.Enriched = a.Enriched
	if a.BundlerError != nil {
		r.BundlerError = a.BundlerError
	}
	if len(a.Layers) > 0 {
		r.Layers = a.Layers
	}
	return r
}

func testRegistryConnection(registry, username, password string, allowHTTP bool) error {
	reg, err := remote.NewRegistry(registry)
	if err != nil {
		return err
	}
	reg.PlainHTTP = allowHTTP
	cred := orasauth.Credential{Username: username, Password: password}
	reg.Client = &orasauth.Client{
		Client:     retry.DefaultClient,
		Cache:      orasauth.NewCache(),
		Credential: orasauth.StaticCredential(registry, cred),
	}
	return reg.Ping(context.Background())
}

// ArtifactLayers handles GET /api/v1/oci/artifacts/:id/layers
//
// @Summary     Get layers for an OCI artifact
// @Description Returns an HTML fragment rendering the layers modal for the given artifact.
// @Tags        oci
// @Produce     html
// @Param       id path int true "Artifact ID"
// @Success     200
// @Failure     404 {object} errorResponse
// @Router      /api/v1/oci/artifacts/{id}/layers [get]
func (h *OCI) ArtifactLayers(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return echo.ErrBadRequest
	}
	a, err := h.db.RegistryArtifact.Get(c.Request().Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return echo.ErrNotFound
		}
		return fmt.Errorf("get artifact: %w", err)
	}
	row := toArtifactFragRow(a, h.basePath)
	tmpl, err := template.ParseFiles("web/templates/orbital/partials/layers-modal.gohtml")
	if err != nil {
		return fmt.Errorf("parse layers-modal: %w", err)
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return renderHTML(c, tmpl, "layers-modal", row)
}

// ── Fragment renderer ─────────────────────────────────────────────────────────

// artifactLayerRow is a display-ready representation of a single OCI layer.
// Values are pre-formatted so the template needs no funcs.
// Digest is the full digest (e.g. sha256:abc…64chars) — the layers modal is
// the detail view, so we don't truncate.
//
// Position is the layer's index in the OCI manifest (index 0 = base, per OCI
// Image Spec §manifest.md). Preserved through the display reversal so
// operators can cross-reference a UI row with the courier zip filename
// (`layer-<position>-<producer>.<ext>`) or with `oras manifest fetch` output.
type artifactLayerRow struct {
	Position        int
	MediaType       string
	SizeDisplay     string
	Digest          string
	IsOrbitalNative bool
	Producer        string
}

type artifactFragRow struct {
	BasePath       string // for declarative HTMX hx-* attrs that need a full URL
	ID             int
	DatacenterName string
	// DatacenterOrbID is sourced from the ExportJob edge — used as the
	// `namespace` filter in the audit-log fetch on row expand.
	DatacenterOrbID string
	// datacenterNamespace is the "namespace" segment of DatacenterOrbID
	// (everything before the first ":"). Used verbatim as the `namespace=`
	// query param when building the changes-panel hx-get URL.
	DatacenterNamespace string
	Repository          string
	Tag                 string
	Digest              string
	DigestShort         string
	HasDigest           bool
	Signed              bool
	Enriched            bool
	BundlerError        string
	Status              string
	StatusClass         string
	InitiatedAt         string
	// PublishedBy is the actor (email) who triggered the export→publish,
	// sourced from the ExportJob edge's created_by. Empty for legacy rows
	// whose export job predates actor capture — the template renders "—".
	PublishedBy string
	// CompletedAtRFC3339 is the OCI-push completion time. Display-only.
	CompletedAtRFC3339 string
	// ExportedAtRFC3339 is the export_job.completed_at — the moment the
	// DGraph snapshot in this artifact was frozen. This is the correct
	// anchor for the changes-diff window: an artifact's CONTENT is
	// determined by when its subgraph was captured, not when the OCI push
	// happened (which may be hours later, out of order, etc.). Used as the
	// `until` bound on the audit-log fetch.
	ExportedAtRFC3339 string
	// PreviousExportedAtRFC3339 is the RFC3339 export_job.completed_at of
	// the latest prior completed artifact for the same DatacenterOrbID
	// whose export snapshot pre-dates this one. Latest-by-content, not
	// latest-by-publish — handles the "publish out of export order" case.
	// Empty when this is the first successful publish for this DC.
	PreviousExportedAtRFC3339 string
	// HasPrevious signals that the changes-panel row should fetch instead
	// of rendering the "first publish" empty state.
	HasPrevious bool
	Error       string
	LayerRows   []artifactLayerRow
	HasLayers   bool
}

func toArtifactFragRow(a *ent.RegistryArtifact, basePath string) artifactFragRow {
	statusClass := map[string]string{
		"pending":   "is-warning is-light",
		"pushing":   "is-info is-light",
		"completed": "is-success is-light",
		"failed":    "is-danger is-light",
	}[string(a.Status)]
	row := artifactFragRow{
		BasePath:       basePath,
		ID:             a.ID,
		DatacenterName: a.DatacenterName,
		Repository:     a.Repository,
		Tag:            a.Tag,
		Signed:         a.Signed,
		Enriched:       a.Enriched,
		Status:         string(a.Status),
		StatusClass:    statusClass,
		InitiatedAt:    a.InitiatedAt.UTC().Format("2006-01-02 15:04:05"),
	}
	// DatacenterOrbID + Namespace come from the ExportJob edge (loaded via
	// .WithExportJob() at query time). Namespace is the segment before the
	// first ":" — used as the `namespace` filter on the audit-log fetch.
	if a.Edges.ExportJob != nil && a.Edges.ExportJob.DatacenterOrbID != nil {
		row.DatacenterOrbID = *a.Edges.ExportJob.DatacenterOrbID
		if idx := strings.IndexByte(row.DatacenterOrbID, ':'); idx > 0 {
			row.DatacenterNamespace = row.DatacenterOrbID[:idx]
		}
	}
	if a.Edges.ExportJob != nil {
		row.PublishedBy = a.Edges.ExportJob.CreatedBy
	}
	if a.CompletedAt != nil {
		row.CompletedAtRFC3339 = a.CompletedAt.UTC().Format(time.RFC3339)
	}
	if a.Edges.ExportJob != nil && a.Edges.ExportJob.CompletedAt != nil {
		row.ExportedAtRFC3339 = a.Edges.ExportJob.CompletedAt.UTC().Format(time.RFC3339)
	}
	if a.Digest != nil {
		row.Digest = *a.Digest
		row.HasDigest = true
		if len(*a.Digest) > 19 {
			row.DigestShort = (*a.Digest)[:19]
		} else {
			row.DigestShort = *a.Digest
		}
	}
	if a.BundlerError != nil {
		row.BundlerError = *a.BundlerError
	}
	if a.Error != nil {
		row.Error = *a.Error
	}
	if len(a.Layers) > 0 {
		row.HasLayers = true
		row.LayerRows = make([]artifactLayerRow, 0, len(a.Layers))
		// Reverse-iterate for display: OCI stores layers base-first (index 0 =
		// data.json.gz, last index = topmost bundler layer), but the UI shows a
		// stack diagram with topmost at top and base at bottom — matching how
		// operators visualize "bundler stuff added on top of the base subgraph."
		// Position is preserved from the original manifest index so the UI's
		// Position column matches the OCI manifest AND the courier zip
		// filename (`layer-<Position>-<producer>.<ext>`).
		for i := len(a.Layers) - 1; i >= 0; i-- {
			l := a.Layers[i]
			lr := artifactLayerRow{
				Position:        i,
				MediaType:       l.MediaType,
				SizeDisplay:     fmtLayerBytes(l.SizeBytes),
				Digest:          l.Digest,
				IsOrbitalNative: l.IsOrbitalNative,
				Producer:        l.Producer,
			}
			row.LayerRows = append(row.LayerRows, lr)
		}
	}
	return row
}

// fmtLayerBytes formats a byte count as a human-readable string (e.g. "1.2 MB").
func fmtLayerBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
