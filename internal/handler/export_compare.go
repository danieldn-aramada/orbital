package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/internal/graphdiff"
	"github.com/labstack/echo/v4"
)

// artifactRef identifies one side of a comparison.
type artifactRef struct {
	ID             int        `json:"id" example:"42"`
	DataCenterName string     `json:"dataCenterName" example:"colo-galleon"`
	Tag            string     `json:"tag" example:"v2"`
	Digest         string     `json:"digest" example:"sha256:81147a187a8f"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
}

// compareResponse is the body returned by GET /api/v1/export/compare.
//
// Sides are named `from` and `to` (POSIX `diff from-file to-file`, `git diff
// <from>..<to>`) so the direction of every change is unambiguous: `fields[].before`
// is the value in `from`, `after` is the value in `to`. Document-level from/to over
// field-level before/after mirrors how git layers file-level a/b over line-level -/+.
type compareResponse struct {
	From       artifactRef                  `json:"from"`
	To         artifactRef                  `json:"to"`
	Disclaimer string                       `json:"disclaimer"`
	Summary    graphdiff.Summary            `json:"summary"`
	ByType     map[string]graphdiff.Summary `json:"byType"`
	Changes    []*graphdiff.Change          `json:"changes"`
}

const compareDisclaimer = "Desired-state delta between two published artifacts. This is not a forecast of what the edge applied; the edge controller decides actuation."

// Compare diffs two published artifacts for the same data center.
//
// Both sides are pulled by immutable digest and run through the SAME normalizer
// (they are both DGraph native exports), which makes this strictly simpler than
// the preview — that one reconciles a native export against a live DQL result.
//
//	@Summary		Compare two published artifacts
//	@Description	Returns the desired-state delta between two published OCI artifacts of the same data center. Read-only; both artifacts are pulled by immutable digest. Not an apply-forecast.
//	@Tags			export
//	@Produce		json
//	@Param			from	query		int	true	"Artifact ID to diff FROM (the earlier side)"
//	@Param			to		query		int	true	"Artifact ID to diff TO (the later side)"
//	@Success		200		{object}	compareResponse
//	@Router			/api/v1/export/compare [get]
func (h *Export) Compare(c echo.Context) error {
	ctx := c.Request().Context()

	from, err := h.artifactByIDParam(ctx, c.QueryParam("from"), "from")
	if err != nil {
		return err
	}
	to, err := h.artifactByIDParam(ctx, c.QueryParam("to"), "to")
	if err != nil {
		return err
	}
	// Comparing across data centers is meaningless — every orbId differs, so the
	// diff would report the entire graph as removed+added.
	if from.DatacenterID != to.DatacenterID {
		return echo.NewHTTPError(http.StatusBadRequest,
			"from and to must belong to the same data center (got "+from.DatacenterID+" and "+to.DatacenterID+")")
	}

	fromSnap, err := h.artifactSnapshot(ctx, from, "from")
	if err != nil {
		return err
	}
	toSnap, err := h.artifactSnapshot(ctx, to, "to")
	if err != nil {
		return err
	}

	res := graphdiff.Compare(fromSnap, toSnap)
	resp := compareResponse{
		From:       toArtifactRef(from),
		To:         toArtifactRef(to),
		Disclaimer: compareDisclaimer,
		Summary:    res.Summary,
		ByType:     res.ByType,
		Changes:    res.Changes,
	}
	if resp.Changes == nil {
		resp.Changes = []*graphdiff.Change{}
	}
	return c.JSON(http.StatusOK, resp)
}

// artifactByIDParam resolves and validates one side's artifact ID. side is "from"
// or "to", used only for error messages.
func (h *Export) artifactByIDParam(ctx context.Context, raw, side string) (*ent.RegistryArtifact, error) {
	if raw == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, side+" is required")
	}
	id := 0
	if _, perr := fmt.Sscan(raw, &id); perr != nil || id <= 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, side+" is not a valid artifact id")
	}
	art, err := h.db.RegistryArtifact.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, echo.NewHTTPError(http.StatusNotFound, side+" artifact not found")
	}
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "look up "+side+" artifact: "+err.Error())
	}
	if art.Digest == nil || *art.Digest == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest,
			side+" artifact has no digest — it never completed a publish and its bytes are not retrievable")
	}
	return art, nil
}

// artifactSnapshot pulls one artifact's graph by digest and normalizes it.
// Unlike the preview — where an unretrievable side degrades to a 200 with a state
// flag because there is still a current-state story to tell — a comparison with a
// missing side has nothing to return, so this is a hard error.
func (h *Export) artifactSnapshot(ctx context.Context, art *ent.RegistryArtifact, side string) (graphdiff.Snapshot, error) {
	raw, err := h.pullArtifactGraph(ctx, art)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadGateway,
			"could not retrieve "+side+" artifact ("+art.Tag+"): "+err.Error())
	}
	snap, err := graphdiff.NormalizeExport(raw)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadGateway,
			"could not parse "+side+" artifact ("+art.Tag+"): "+err.Error())
	}
	return snap, nil
}

func toArtifactRef(a *ent.RegistryArtifact) artifactRef {
	ref := artifactRef{
		ID:             a.ID,
		DataCenterName: a.DatacenterName,
		Tag:            a.Tag,
		PublishedAt:    a.CompletedAt,
	}
	if a.Digest != nil {
		ref.Digest = *a.Digest
	}
	return ref
}
