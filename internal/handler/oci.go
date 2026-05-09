package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/internal/oci"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// OCI handles OCI artifact publishing endpoints.
type OCI struct {
	db               *ent.Client
	publisher        *oci.Publisher
	cfg              oci.Config
	scratchExportDir string
	logger           *slog.Logger
}

// NewOCI creates an OCI handler. publisher may be nil when OCI is not configured.
func NewOCI(db *ent.Client, cfg oci.Config, scratchExportDir string, logger *slog.Logger) *OCI {
	var pub *oci.Publisher
	if cfg.Registry != "" && cfg.SigningKeyPath != "" {
		pub = oci.New(db, cfg, logger)
	}
	return &OCI{db: db, publisher: pub, cfg: cfg, scratchExportDir: scratchExportDir, logger: logger}
}

type publishResponse struct {
	ArtifactID int    `json:"artifactId"`
	Status     string `json:"status"`
	Tag        string `json:"tag"`
	Repository string `json:"repository"`
}

type artifactResponse struct {
	ID                  int     `json:"id"`
	ExportJobID         string  `json:"exportJobId"`
	DatacenterID        string  `json:"datacenterId"`
	DatacenterName      string  `json:"datacenterName"`
	Registry            string  `json:"registry"`
	Repository          string  `json:"repository"`
	Tag                 string  `json:"tag"`
	Digest              *string `json:"digest,omitempty"`
	SizeBytes           *int64  `json:"sizeBytes,omitempty"`
	Signed              bool    `json:"signed"`
	SigningKeyFingerprint *string `json:"signingKeyFingerprint,omitempty"`
	Status              string  `json:"status"`
	InitiatedAt         string  `json:"initiatedAt"`
	CompletedAt         *string `json:"completedAt,omitempty"`
	Error               *string `json:"error,omitempty"`
}

// Publish handles POST /api/v1/export/jobs/:jobId/publish
//
// @Summary     Publish export as OCI artifact
// @Description Pushes a completed export job's artifact to the configured OCI registry as a signed artifact. Returns 503 if OCI publishing is not configured, 422 if the job is not completed or its artifact file is missing.
// @Tags        oci
// @Produce     json
// @Param       jobId path string true "Export job ID"
// @Success     202 {object} publishResponse
// @Failure     404 {object} map[string]string
// @Failure     422 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/v1/export/jobs/{jobId}/publish [post]
func (h *OCI) Publish(c echo.Context) error {
	if h.publisher == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "OCI publishing is not configured (ORBITAL_OCI_REGISTRY and ORBITAL_OCI_SIGNING_KEY_PATH required)",
		})
	}

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

	if job.Status != exportjob.StatusCompleted || job.ArtifactPath == nil {
		return c.JSON(http.StatusUnprocessableEntity, map[string]string{
			"error": "export job is not in completed state or has no artifact",
		})
	}

	// Verify the artifact file still exists (not stale).
	if _, err := os.Stat(*job.ArtifactPath); os.IsNotExist(err) {
		return c.JSON(http.StatusUnprocessableEntity, map[string]string{
			"error": "export artifact file no longer exists",
		})
	}

	// Compute next tag: count existing artifacts for this DC + 1.
	count, err := h.db.RegistryArtifact.Query().
		Where(registryartifact.DatacenterID(job.DatacenterID)).
		Count(c.Request().Context())
	if err != nil {
		return fmt.Errorf("count artifacts: %w", err)
	}
	tag := oci.NextTag(count)
	repoName := oci.RepoForDC(h.cfg.Registry, h.cfg.Repo, job.DatacenterName)

	userID, _ := c.Get("user_id").(int)

	artifact, err := h.db.RegistryArtifact.Create().
		SetExportJobID(job.ID).
		SetDatacenterID(job.DatacenterID).
		SetDatacenterName(job.DatacenterName).
		SetRegistry(h.cfg.Registry).
		SetRepository(repoName).
		SetTag(tag).
		SetStatus(registryartifact.StatusPending).
		SetNillableInitiatedBy(nillableInt(userID)).
		SetInitiatedAt(time.Now()).
		Save(c.Request().Context())
	if err != nil {
		return fmt.Errorf("create artifact record: %w", err)
	}

	go h.publisher.Publish(artifact.ID, job, tag)

	return c.JSON(http.StatusAccepted, publishResponse{
		ArtifactID: artifact.ID,
		Status:     string(artifact.Status),
		Tag:        tag,
		Repository: repoName,
	})
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
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("list artifacts: %w", err)
	}

	out := make([]artifactResponse, 0, len(artifacts))
	for _, a := range artifacts {
		out = append(out, toArtifactResponse(a))
	}
	return c.JSON(http.StatusOK, out)
}

// GetArtifact handles GET /api/v1/oci/artifacts/:id
//
// @Summary     Get OCI artifact
// @Description Returns a single OCI artifact record by ID.
// @Tags        oci
// @Produce     json
// @Param       id path int true "Artifact ID"
// @Success     200 {object} artifactResponse
// @Failure     404 {object} map[string]string
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

// DeleteJob handles DELETE /api/v1/export/jobs/:jobId
//
// @Summary     Delete export job
// @Description Deletes an export job record and removes its local scratch file. Does not remove any published OCI artifacts from the registry.
// @Tags        export subgraph
// @Produce     json
// @Param       jobId path string true "Export job ID"
// @Success     204
// @Failure     404 {object} map[string]string
// @Router      /api/v1/export/jobs/{jobId} [delete]
func (h *OCI) DeleteJob(c echo.Context) error {
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

	// Remove the export zip if present.
	if job.ArtifactPath != nil {
		if removeErr := os.Remove(*job.ArtifactPath); removeErr != nil && !os.IsNotExist(removeErr) {
			h.logger.Warn("failed to remove artifact file", "path", *job.ArtifactPath, "err", removeErr)
		}
	}

	// Remove the job's scratch export directory (e.g. subgraph-exports/scratch/<jobID>/).
	scratchDir := filepath.Join(h.scratchExportDir, jobID.String())
	if removeErr := os.RemoveAll(scratchDir); removeErr != nil {
		h.logger.Warn("failed to remove scratch dir", "path", scratchDir, "err", removeErr)
	}

	if err := h.db.ExportJob.DeleteOneID(jobID).Exec(c.Request().Context()); err != nil {
		return fmt.Errorf("delete job: %w", err)
	}

	return c.NoContent(http.StatusNoContent)
}

// PublicKey handles GET /api/v1/oci/public-key
//
// @Summary     Get OCI signing public key
// @Description Returns the PEM-encoded public key corresponding to the configured OCI signing key. Used by edge consumers to verify artifact signatures.
// @Tags        oci
// @Produce     application/x-pem-file
// @Success     200
// @Failure     503 {object} map[string]string
// @Router      /api/v1/oci/public-key [get]
func (h *OCI) PublicKey(c echo.Context) error {
	if h.cfg.SigningKeyPath == "" {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "signing key not configured",
		})
	}
	pubPEM, err := oci.PublicKeyPEM(h.cfg.SigningKeyPath)
	if err != nil {
		return fmt.Errorf("load public key: %w", err)
	}
	return c.Blob(http.StatusOK, "application/x-pem-file", pubPEM)
}

// TestConnection handles POST /api/v1/oci/test-connection
//
// @Summary     Test OCI registry connection
// @Description Pings the configured OCI registry to verify credentials and reachability. Returns {"ok": true} on success or {"ok": false, "error": "..."} on failure.
// @Tags        oci
// @Produce     json
// @Success     200 {object} map[string]any
// @Failure     503 {object} map[string]string
// @Router      /api/v1/oci/test-connection [post]
func (h *OCI) TestConnection(c echo.Context) error {
	if h.cfg.Registry == "" {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "registry not configured"})
	}
	// Attempt a simple registry ping via oras registry resolution.
	// This is intentionally minimal — just validates credentials/reachability.
	err := testRegistryConnection(h.cfg.Registry, h.cfg.Username, h.cfg.Password)
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
		Repository:  a.Repository,
		Tag:         a.Tag,
		Signed:      a.Signed,
		Status:      string(a.Status),
		InitiatedAt: a.InitiatedAt.Format(time.RFC3339),
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
	return r
}

func nillableInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func testRegistryConnection(registry, username, password string) error {
	reg, err := remote.NewRegistry(registry)
	if err != nil {
		return err
	}
	cred := orasauth.Credential{Username: username, Password: password}
	reg.Client = &orasauth.Client{
		Client:     retry.DefaultClient,
		Cache:      orasauth.NewCache(),
		Credential: orasauth.StaticCredential(registry, cred),
	}
	return reg.Ping(context.Background())
}
