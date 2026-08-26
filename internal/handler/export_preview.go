package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/internal/graphdiff"
	"github.com/armada/orbital/internal/oci"
	"github.com/labstack/echo/v4"
)

const previewDisclaimer = "Desired-state delta vs the last published artifact. This is not a forecast of what the edge will apply; the edge controller decides actuation."

// previewResponse is the body returned by POST /api/v1/export/preview.
type previewResponse struct {
	OrbID                string                       `json:"orbId" example:"colo:colo-galleon"`
	DataCenterName       string                       `json:"dataCenterName" example:"colo-galleon"`
	Disclaimer           string                       `json:"disclaimer"`
	LastPublishedVersion lastPublishedMeta            `json:"lastPublishedVersion"`
	Current              currentMeta                  `json:"current"`
	Summary              graphdiff.Summary            `json:"summary"`
	ByType               map[string]graphdiff.Summary `json:"byType"`
	Changes              []*graphdiff.Change          `json:"changes"`
}

type lastPublishedMeta struct {
	State       string     `json:"state" example:"published"` // published | first_export | unavailable
	Tag         string     `json:"tag,omitempty" example:"v8"`
	Digest      string     `json:"digest,omitempty" example:"sha256:1a2b"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	ExportJobID string     `json:"exportJobId,omitempty"`
	Retrievable bool       `json:"retrievable"`
	Reason      string     `json:"reason,omitempty"`
}

type currentMeta struct {
	Source      string `json:"source" example:"blue"`
	NodeCount   int    `json:"nodeCount" example:"663"`
	ContentHash string `json:"contentHash" example:"sha256:abc"`
}

// Preview computes a read-only, per-orbId content diff between the current blue
// subgraph and the last published artifact (baseline, pulled by OCI digest).
// It performs no writes, holds no scratch lock, and never triggers an export.
//
//	@Summary		Preview export diff (desired-state delta vs last published)
//	@Description	Synchronous, read-only. Diffs the current desired state for a data center against its last published artifact. Not an apply-forecast.
//	@Tags			export
//	@Accept			json
//	@Produce		json
//	@Param			body	body		exportRequest	true	"Data center orbId to preview"
//	@Success		200		{object}	previewResponse
//	@Router			/export/preview [post]
func (h *Export) Preview(c echo.Context) error {
	var req exportRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.OrbID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "orbId is required")
	}
	ctx := c.Request().Context()

	dcName, _, namespaceName, err := h.fetchDCInfo(ctx, req.OrbID)
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "could not resolve datacenter: "+err.Error())
	}

	// Current side: the exact selection the export uses, from blue, read-only.
	nodes, err := h.fetchNamespaceSubgraph(ctx, namespaceName)
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "fetch current subgraph: "+err.Error())
	}
	current := graphdiff.NormalizeCurrent(nodes)

	resp := previewResponse{
		OrbID:          req.OrbID,
		DataCenterName: dcName,
		Disclaimer:     previewDisclaimer,
		Current:        currentMeta{Source: "blue", NodeCount: len(current), ContentHash: current.ContentHash()},
		Changes:        []*graphdiff.Change{},
	}

	// Baseline: last completed artifact for this DC.
	art, err := h.lastPublishedArtifact(ctx, req.OrbID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "look up baseline: "+err.Error())
	}
	if art == nil {
		// No prior publish — everything is an addition. Distinct from "unavailable".
		resp.LastPublishedVersion = lastPublishedMeta{State: "first_export", Retrievable: false}
		empty := graphdiff.Snapshot{}
		res := graphdiff.Compare(empty, current)
		fillPreview(&resp, res)
		return c.JSON(http.StatusOK, resp)
	}

	resp.LastPublishedVersion = lastPublishedMeta{
		State:       "published",
		Tag:         art.Tag,
		ExportJobID: art.ExportJobID.String(),
		Retrievable: true,
	}
	if art.Digest != nil {
		resp.LastPublishedVersion.Digest = *art.Digest
	}
	if art.CompletedAt != nil {
		resp.LastPublishedVersion.PublishedAt = art.CompletedAt
	}

	// Pull the baseline bytes by digest and normalize. Any failure here is a
	// 200 with baseline.state="unavailable" — never a fake full-add, never a 5xx.
	plain, perr := h.pullArtifactGraph(ctx, art)
	if perr != nil {
		resp.LastPublishedVersion.State = "unavailable"
		resp.LastPublishedVersion.Retrievable = false
		resp.LastPublishedVersion.Reason = perr.Error()
		return c.JSON(http.StatusOK, resp)
	}
	baseline, nerr := graphdiff.NormalizeExport(plain)
	if nerr != nil {
		resp.LastPublishedVersion.State = "unavailable"
		resp.LastPublishedVersion.Retrievable = false
		resp.LastPublishedVersion.Reason = "parse baseline export: " + nerr.Error()
		return c.JSON(http.StatusOK, resp)
	}

	res := graphdiff.Compare(baseline, current)
	fillPreview(&resp, res)
	return c.JSON(http.StatusOK, resp)
}

func fillPreview(resp *previewResponse, res *graphdiff.Result) {
	resp.Summary = res.Summary
	resp.ByType = res.ByType
	if res.Changes != nil {
		resp.Changes = res.Changes
	}
	// Prefer the diff's hash of the current side (identical to what we set above).
	if res.ContentHash != "" {
		resp.Current.ContentHash = res.ContentHash
	}
}

// lastPublishedArtifact returns the most recently completed RegistryArtifact for the DC
// that has a digest, or nil if none (first export).
func (h *Export) lastPublishedArtifact(ctx context.Context, dcOrbID string) (*ent.RegistryArtifact, error) {
	art, err := h.db.RegistryArtifact.Query().
		Where(
			registryartifact.DatacenterID(dcOrbID),
			registryartifact.StatusEQ(registryartifact.StatusCompleted),
			registryartifact.DigestNotNil(),
		).
		Order(registryartifact.ByCompletedAt(sql.OrderDesc())).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return art, nil
}

// pullArtifactGraph pulls the artifact by immutable digest and returns the unpacked
// (un-gzipped) native export JSON.
func (h *Export) pullArtifactGraph(ctx context.Context, art *ent.RegistryArtifact) ([]byte, error) {
	if h.ociCfg.Registry == "" {
		return nil, fmt.Errorf("OCI registry not configured")
	}
	if art.Digest == nil || *art.Digest == "" {
		return nil, fmt.Errorf("baseline artifact has no digest")
	}
	// Stored repository is the full ref ("<registry>/<prefix>/<slug>"); the puller
	// wants just the path.
	repo := strings.TrimPrefix(art.Repository, art.Registry+"/")
	pullCfg := oci.PullConfig{
		Registry:  art.Registry,
		Repo:      repo,
		Username:  h.ociCfg.Username,
		Password:  h.ociCfg.Password,
		AllowHTTP: h.ociCfg.AllowHTTP,
	}
	pulled, err := oci.Pull(ctx, pullCfg, *art.Digest)
	if err != nil {
		return nil, fmt.Errorf("pull baseline by digest: %w", err)
	}
	if len(pulled.DataGZ) == 0 {
		return nil, fmt.Errorf("baseline artifact has no data layer")
	}
	gz, err := gzip.NewReader(bytes.NewReader(pulled.DataGZ))
	if err != nil {
		return nil, fmt.Errorf("gunzip baseline: %w", err)
	}
	defer gz.Close()
	plain, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("read baseline: %w", err)
	}
	return plain, nil
}
