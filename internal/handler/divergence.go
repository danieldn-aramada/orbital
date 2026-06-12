package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/divergenceresolution"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// DivergenceHandler exposes orbital's divergence ingestion + resolution API.
// Orbital ingests snapshots from S3 (via internal/divergenceingest) and
// stores them in DivergenceEntry. Cloud admins resolve entries via Accept
// (mutate orbital intent to match the override and record the decision),
// Force (cb-bundler reads pending forces, emits spec.takeover[]), or
// Ignore (record decision; entry stays visible tagged "ignored").
//
// gql is used by Accept to dispatch the `update{Type}` mutation through the
// existing GraphQL audit path. May be nil in tests that don't exercise Accept.
type DivergenceHandler struct {
	db     *ent.Client
	logger *slog.Logger
	gql    *GraphQL
}

func NewDivergenceHandler(db *ent.Client, logger *slog.Logger, gql *GraphQL) *DivergenceHandler {
	return &DivergenceHandler{db: db, logger: logger, gql: gql}
}

// entryItem is the wire shape returned by the List endpoint and used by the UI.
type entryItem struct {
	ID            string          `json:"id"`
	DCOrbID       string          `json:"dcOrbId"`
	EntryOrbID    string          `json:"entryOrbId"`
	Field         string          `json:"field"`
	IntendedValue json.RawMessage `json:"intendedValue,omitempty"`
	OverrideValue json.RawMessage `json:"overrideValue,omitempty"`
	Who           string          `json:"who"`
	FirstSeenAt   string          `json:"firstSeenAt"`
	LastSeenAt    string          `json:"lastSeenAt"`

	// Resolution is the current decision on this entry, if any. Nil when un-resolved.
	Resolution *resolutionItem `json:"resolution,omitempty"`
}

type resolutionItem struct {
	ID         string `json:"id"`
	Action     string `json:"action"` // "accept" | "force" | "ignore"
	Actor      string `json:"actor"`
	DecidedAt  string `json:"decidedAt"`
	CbConsumed bool   `json:"cbConsumed"`
}

// List handles GET /api/v1/divergence — returns all current divergence
// entries with their resolutions (if any). Open to readonly callers.
func (h *DivergenceHandler) List(c echo.Context) error {
	ctx := c.Request().Context()
	entries, err := h.db.DivergenceEntry.Query().
		Order(ent.Desc(divergenceentry.FieldLastSeenAt)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("list divergence entries: %w", err)
	}
	out := make([]entryItem, 0, len(entries))
	for _, e := range entries {
		item := entryItem{
			ID:            e.ID.String(),
			DCOrbID:       e.DcOrbID,
			EntryOrbID:    e.EntryOrbID,
			Field:         e.Field,
			IntendedValue: e.IntendedValue,
			OverrideValue: e.OverrideValue,
			Who:           e.Who,
			FirstSeenAt:   e.FirstSeenAt.UTC().Format(time.RFC3339),
			LastSeenAt:    e.LastSeenAt.UTC().Format(time.RFC3339),
		}
		// Find current resolution if any.
		res, err := h.db.DivergenceResolution.Query().
			Where(
				divergenceresolution.EntryOrbID(e.EntryOrbID),
				divergenceresolution.Field(e.Field),
			).
			Only(ctx)
		if err == nil {
			item.Resolution = &resolutionItem{
				ID:         res.ID.String(),
				Action:     string(res.Action),
				Actor:      res.Actor,
				DecidedAt:  res.DecidedAt.UTC().Format(time.RFC3339),
				CbConsumed: res.CbConsumed,
			}
		} else if !ent.IsNotFound(err) {
			h.logger.Warn("query resolution failed", "orbId", e.EntryOrbID, "field", e.Field, "err", err)
		}
		out = append(out, item)
	}
	return c.JSON(http.StatusOK, out)
}

// Accept handles POST /api/v1/divergence/:id/accept.
// Records the admin decision. Does NOT auto-mutate orbital intent — the
// admin updates intent via the normal UI flow for the relevant ConfigItem.
// cb-bundler ignores accept rows when building bundles.
func (h *DivergenceHandler) Accept(c echo.Context) error {
	return h.recordResolution(c, divergenceresolution.ActionAccept)
}

// Force handles POST /api/v1/divergence/:id/force.
// cb-bundler queries pending un-consumed force resolutions when building the
// next bundle and emits them as spec.takeover[] entries on the ConfigBundle CR.
func (h *DivergenceHandler) Force(c echo.Context) error {
	return h.recordResolution(c, divergenceresolution.ActionForce)
}

// Ignore handles POST /api/v1/divergence/:id/ignore.
// No downstream effect; UI tags the entry as "ignored."
func (h *DivergenceHandler) Ignore(c echo.Context) error {
	return h.recordResolution(c, divergenceresolution.ActionIgnore)
}

// recordResolution is the shared implementation for the three resolution
// endpoints. Finds the DivergenceEntry by ID, dispatches the side-effect (for
// Accept: a GraphQL mutation that updates orbital intent), then upserts a
// DivergenceResolution row (REPLACE semantics: re-deciding overwrites the
// previous decision). If the side-effect fails, NO resolution row is written.
func (h *DivergenceHandler) recordResolution(c echo.Context, action divergenceresolution.Action) error {
	ctx := c.Request().Context()
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	entry, err := h.db.DivergenceEntry.Get(ctx, id)
	if ent.IsNotFound(err) {
		return echo.NewHTTPError(http.StatusNotFound, "divergence entry not found")
	}
	if err != nil {
		return fmt.Errorf("get entry: %w", err)
	}

	actor := actorFromContext(c)
	if actor == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "actor required")
	}

	// Accept dispatches a mutation BEFORE recording the resolution. On failure,
	// the resolution is not written so the entry stays visible as pending.
	if action == divergenceresolution.ActionAccept {
		if err := h.dispatchAcceptMutation(ctx, entry, actor); err != nil {
			return err
		}
	}

	// REPLACE: delete any existing resolution for this (orbId, field), insert new.
	_, err = h.db.DivergenceResolution.Delete().
		Where(
			divergenceresolution.EntryOrbID(entry.EntryOrbID),
			divergenceresolution.Field(entry.Field),
		).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete prior resolution: %w", err)
	}
	res, err := h.db.DivergenceResolution.Create().
		SetEntryOrbID(entry.EntryOrbID).
		SetField(entry.Field).
		SetAction(action).
		SetActor(actor).
		SetDecidedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create resolution: %w", err)
	}

	writeAuditEvent(h.db, h.logger, "management", actor, "resolveDivergence",
		[]string{string(action)},
		[]string{"DivergenceEntry"},
		[]string{entry.EntryOrbID + ":" + entry.Field},
		map[string]any{
			"entryId":    entry.ID.String(),
			"action":     string(action),
			"orbId":      entry.EntryOrbID,
			"field":      entry.Field,
			"dcOrbId":    entry.DcOrbID,
		},
	)

	return c.JSON(http.StatusOK, resolutionItem{
		ID:         res.ID.String(),
		Action:     string(res.Action),
		Actor:      res.Actor,
		DecidedAt:  res.DecidedAt.UTC().Format(time.RFC3339),
		CbConsumed: res.CbConsumed,
	})
}

// dispatchAcceptMutation issues `update{TypeName}(filter:{orbId},set:{field:value})`
// through the GraphQL handler so the mutation lands in audit alongside any
// user-driven mutation. Returns an HTTPError suitable for the caller to
// propagate verbatim — bad input is 422 (missing type) or 502 (DGraph failure).
func (h *DivergenceHandler) dispatchAcceptMutation(ctx context.Context, entry *ent.DivergenceEntry, actor string) error {
	if entry.TypeName == "" {
		// Legacy entry ingested before mapping started carrying type info.
		// The admin must update intent manually until the next snapshot
		// re-ingests the entry with a type.
		return echo.NewHTTPError(http.StatusUnprocessableEntity,
			"divergence entry is missing type info; update intent manually for this field, or wait for the next snapshot")
	}
	if h.gql == nil {
		// Defensive: server.go must wire gql into DivergenceHandler.
		return echo.NewHTTPError(http.StatusInternalServerError, "graphql dispatcher not configured")
	}
	if len(entry.OverrideValue) == 0 {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "override value is empty; cannot mutate intent")
	}

	var overrideVal any
	if err := json.Unmarshal(entry.OverrideValue, &overrideVal); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, fmt.Sprintf("invalid override value: %v", err))
	}

	// DGraph generates {Type}Filter and {Type}Patch input types from the @id
	// schema. Sending filter + set as GraphQL variables (not inlined) lets the
	// audit-log expanded row render the same "Input" block as user-driven
	// mutations — so the cloud admin can see what was changed.
	mutation := fmt.Sprintf(
		`mutation AcceptDivergence($filter: %sFilter!, $set: %sPatch!) { update%s(input: {filter: $filter, set: $set}) { numUids } }`,
		entry.TypeName, entry.TypeName, entry.TypeName,
	)
	variables := map[string]any{
		"filter": map[string]any{"orbId": map[string]any{"eq": entry.EntryOrbID}},
		"set":    map[string]any{entry.Field: overrideVal},
	}
	if _, err := h.gql.DispatchMutation(ctx, actor, mutation, variables); err != nil {
		h.logger.Warn("accept-divergence mutation failed",
			"orbId", entry.EntryOrbID, "field", entry.Field, "type", entry.TypeName, "err", err)
		return echo.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("failed to update intent: %v", err))
	}
	return nil
}

// pendingForceItem is the wire shape returned to cb-bundler for the
// "what takeover entries do I need to emit in the next bundle" query.
type pendingForceItem struct {
	ID    string `json:"id"`
	OrbID string `json:"orbId"`
	Field string `json:"field"`
}

// PendingForce handles GET /api/v1/divergence/resolutions/pending-force.
// Returns un-consumed force resolutions in JSON. cb-bundler queries this
// when building a bundle to emit spec.takeover[] entries.
func (h *DivergenceHandler) PendingForce(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := h.db.DivergenceResolution.Query().
		Where(
			divergenceresolution.ActionEQ(divergenceresolution.ActionForce),
			divergenceresolution.CbConsumed(false),
		).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query pending forces: %w", err)
	}
	out := make([]pendingForceItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, pendingForceItem{
			ID:    r.ID.String(),
			OrbID: r.EntryOrbID,
			Field: r.Field,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// MarkConsumed handles POST /api/v1/divergence/resolutions/:id/consumed.
// cb-bundler calls this after successfully pushing a bundle that incorporated
// the takeover entry for this resolution.
func (h *DivergenceHandler) MarkConsumed(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	now := time.Now().UTC()
	_, err = h.db.DivergenceResolution.UpdateOneID(id).
		SetCbConsumed(true).
		SetCbConsumedAt(now).
		Save(ctx)
	if ent.IsNotFound(err) {
		return echo.NewHTTPError(http.StatusNotFound, "resolution not found")
	}
	if err != nil {
		return fmt.Errorf("mark consumed: %w", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"consumedAt": now.Format(time.RFC3339)})
}
