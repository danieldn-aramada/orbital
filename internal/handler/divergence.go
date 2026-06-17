package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/divergenceresolution"
	"github.com/armada/orbital/internal/divergence"
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
	IntendedValue json.RawMessage `json:"intendedValue,omitempty" swaggertype:"object"`
	OverrideValue json.RawMessage `json:"overrideValue,omitempty" swaggertype:"object"`
	Who           string          `json:"who"`
	FirstSeenAt   string          `json:"firstSeenAt"`
	LastSeenAt    string          `json:"lastSeenAt"`

	// Resolution is the current decision on this entry, if any. Nil when un-resolved.
	Resolution *resolutionItem `json:"resolution,omitempty"`
}

type resolutionItem struct {
	ID        string `json:"id"`
	Action    string `json:"action"` // "accept" | "reject" | "ignore"
	Actor     string `json:"actor"`
	DecidedAt string `json:"decidedAt"`
}

// List handles GET /api/v1/divergences.
//
// Returns the divergence collection with each entry's resolution embedded.
// Filterable by query params:
//
//	?action=accept&action=reject   resolution action — repeatable for OR. Entries without a resolution are excluded when this filter is set.
//	?dc=colo:colo-galleon          dc_orb_id exact match.
//
// Two canonical query shapes for the deployment layer:
//   - "force takeover":  ?action=accept&action=reject  → spec.takeover[]
//   - "disengaged":      ?action=ignore                 → spec omissions
//
// UI feed (the divergence-reports page) calls with no params and groups
// client-side by dc_orb_id.
//
// @Summary  List divergences with their resolution
// @Tags     divergence
// @Produce  json
// @Param    action      query []string false "Filter by resolution action; repeatable for OR" Enums(accept,reject,ignore)
// @Param    dc          query string   false "Filter by dc_orb_id"
// @Success  200 {array} entryItem
// @Router   /api/v1/divergences [get]
func (h *DivergenceHandler) List(c echo.Context) error {
	ctx := c.Request().Context()

	actions := c.QueryParams()["action"]
	dcFilter := c.QueryParam("dc")

	entryQuery := h.db.DivergenceEntry.Query().Order(ent.Desc(divergenceentry.FieldLastSeenAt))
	if dcFilter != "" {
		entryQuery = entryQuery.Where(divergenceentry.DcOrbID(dcFilter))
	}
	entries, err := entryQuery.All(ctx)
	if err != nil {
		return fmt.Errorf("list divergence entries: %w", err)
	}

	// Resolution-level filters are applied after the entry fetch since the
	// resolution lives in a separate table linked by (entry_orb_id, field).
	// Cheap: typical divergence counts are 10s to low 100s.
	wantAction := map[divergenceresolution.Action]bool{}
	for _, a := range actions {
		parsed, perr := parseAction(a)
		if perr != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("bad action filter: %v", perr))
		}
		wantAction[parsed] = true
	}

	// Per-field staleness check for the action-filtered (bundler) query shape.
	// Compares the field's current DGraph value to what the admin's resolution
	// expects: for Reject, intent should still equal entry.intended_value (the
	// admin decided to keep that intent); for Accept, intent should now equal
	// entry.override_value (the admin's mutation adopted the override). If the
	// current value differs, another admin's edit has moved this specific field
	// since the decision — surface as stale, exclude from the bundler list.
	//
	// Per-FIELD (not per-ConfigItem) because batched decisions on sibling
	// fields of the same ConfigItem (e.g., Accept ipmiEnabled + Reject
	// sshEnabled on the same IdracSettings) must not invalidate each other.
	// The version field on ConfigItem bumps for any sibling edit, which would
	// false-positive every batch resolution that touches more than one field.
	type cachedValMatches struct{ match bool; have bool }
	currentValueMatchCache := map[string]cachedValMatches{}
	currentValueMatchesExpected := func(typeName, orbID, field string, expected json.RawMessage) bool {
		if typeName == "" || orbID == "" || field == "" || len(expected) == 0 {
			return true // no anchor → can't check; allow through (degraded MVCC)
		}
		key := typeName + "|" + orbID + "|" + field + "|" + string(expected)
		if c, ok := currentValueMatchCache[key]; ok && c.have {
			return c.match
		}
		match, err := h.currentValueMatches(ctx, typeName, orbID, field, expected)
		if err != nil {
			h.logger.Warn("fetch current value for resolution staleness check failed",
				"orbId", orbID, "type", typeName, "field", field, "err", err)
			currentValueMatchCache[key] = cachedValMatches{match: true, have: true} // degrade to allow
			return true
		}
		currentValueMatchCache[key] = cachedValMatches{match: match, have: true}
		return match
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
		res, err := h.db.DivergenceResolution.Query().
			Where(
				divergenceresolution.EntryOrbID(e.EntryOrbID),
				divergenceresolution.Field(e.Field),
			).
			Only(ctx)
		if err == nil {
			item.Resolution = &resolutionItem{
				ID:        res.ID.String(),
				Action:    string(res.Action),
				Actor:     res.Actor,
				DecidedAt: res.DecidedAt.UTC().Format(time.RFC3339),
			}
		} else if !ent.IsNotFound(err) {
			h.logger.Warn("query resolution failed", "orbId", e.EntryOrbID, "field", e.Field, "err", err)
		}

		// Action filter requires a resolution and matching action.
		if len(wantAction) > 0 {
			if item.Resolution == nil {
				continue
			}
			if !wantAction[divergenceresolution.Action(item.Resolution.Action)] {
				continue
			}
			// MVCC: refuse to surface accept/reject resolutions whose field
			// intent has been superseded by another cloud admin's edit since
			// the decision. Reject expects DGraph current == entry.intended
			// (admin chose to keep that intent); Accept expects DGraph current
			// == entry.override (admin's mutation adopted the override). If
			// current diverges from the expected post-decision value, surface
			// as stale and exclude from the bundler list. Ignore is exempt —
			// it's a standing instruction, not a one-shot enforcement.
			if res != nil &&
				(res.Action == divergenceresolution.ActionAccept || res.Action == divergenceresolution.ActionReject) {
				var expected json.RawMessage
				if res.Action == divergenceresolution.ActionReject {
					expected = e.IntendedValue
				} else {
					expected = e.OverrideValue
				}
				if !currentValueMatchesExpected(e.TypeName, e.EntryOrbID, e.Field, expected) {
					h.logger.Info("excluding stale resolution from action-filtered list",
						"orbId", e.EntryOrbID, "field", e.Field, "action", res.Action)
					continue
				}
			}
		}
		out = append(out, item)
	}
	return c.JSON(http.StatusOK, out)
}

// Get handles GET /api/v1/divergences/:id — returns one entry by UUID.
//
// @Summary  Get one divergence by ID
// @Tags     divergence
// @Produce  json
// @Param    id path string true "Divergence entry UUID"
// @Success  200 {object} entryItem
// @Failure  400 {object} map[string]string
// @Failure  404 {object} map[string]string
// @Router   /api/v1/divergences/{id} [get]
func (h *DivergenceHandler) Get(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	entry, err := h.db.DivergenceEntry.Get(ctx, id)
	if ent.IsNotFound(err) {
		return echo.NewHTTPError(http.StatusNotFound, "divergence not found")
	}
	if err != nil {
		return fmt.Errorf("get divergence: %w", err)
	}
	item := entryItem{
		ID:            entry.ID.String(),
		DCOrbID:       entry.DcOrbID,
		EntryOrbID:    entry.EntryOrbID,
		Field:         entry.Field,
		IntendedValue: entry.IntendedValue,
		OverrideValue: entry.OverrideValue,
		Who:           entry.Who,
		FirstSeenAt:   entry.FirstSeenAt.UTC().Format(time.RFC3339),
		LastSeenAt:    entry.LastSeenAt.UTC().Format(time.RFC3339),
	}
	res, err := h.db.DivergenceResolution.Query().
		Where(
			divergenceresolution.EntryOrbID(entry.EntryOrbID),
			divergenceresolution.Field(entry.Field),
		).
		Only(ctx)
	if err == nil {
		item.Resolution = &resolutionItem{
			ID:        res.ID.String(),
			Action:    string(res.Action),
			Actor:     res.Actor,
			DecidedAt: res.DecidedAt.UTC().Format(time.RFC3339),
		}
	} else if !ent.IsNotFound(err) {
		h.logger.Warn("query resolution failed", "orbId", entry.EntryOrbID, "field", entry.Field, "err", err)
	}
	return c.JSON(http.StatusOK, item)
}

// putResolutionBody is the JSON body for PUT /api/v1/divergences/:id/resolution.
type putResolutionBody struct {
	Action string `json:"action"` // "accept" | "reject" | "ignore"
}

// PutResolution handles PUT /api/v1/divergences/:id/resolution.
//
// Upserts the cloud admin's decision on a divergence. Body: {"action":"..."}.
// For accept, this also dispatches the GraphQL mutation that updates intent.
// Idempotent at the resolution-row level: re-PUTting the same decision
// REPLACES the prior row (same orbId+field unique key).
//
// Returns 200 with the resolution. 409 on MVCC conflict (intent has moved
// since the divergence was reported — admin re-reviews).
//
// @Summary  Record a divergence resolution
// @Tags     divergence
// @Accept   json
// @Produce  json
// @Param    id   path string            true "Divergence entry UUID"
// @Param    body body putResolutionBody true "Decision payload"
// @Success  200  {object} resolutionItem
// @Failure  400  {object} map[string]string
// @Failure  401  {object} map[string]string
// @Failure  404  {object} map[string]string
// @Failure  409  {object} map[string]string  "MVCC conflict — intent changed since report"
// @Router   /api/v1/divergences/{id}/resolution [put]
func (h *DivergenceHandler) PutResolution(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	var body putResolutionBody
	if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body: "+err.Error())
	}
	action, err := parseAction(body.Action)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	actor := actorFromContext(c)
	if actor == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "actor required")
	}
	res, err := h.applyResolution(c.Request().Context(), id, action, actor)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, *res)
}

// applyResolution is the side-effect-bearing core of the resolution pipeline.
// Finds the DivergenceEntry by ID, dispatches the side-effect (Accept: GraphQL
// mutation that updates intent), then upserts the DivergenceResolution row
// (REPLACE semantics: re-deciding overwrites the previous decision). If the
// side-effect fails, NO resolution row is written so the entry stays pending.
//
// Used by both per-row endpoints (singleResolution) and the batch endpoint
// (ResolveBatch). Returns an HTTPError on validation/dispatch failure suitable
// for echo to render, or a generic error on storage failures.
func (h *DivergenceHandler) applyResolution(ctx context.Context, id uuid.UUID, action divergenceresolution.Action, actor string) (*resolutionItem, error) {
	entry, err := h.db.DivergenceEntry.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, echo.NewHTTPError(http.StatusNotFound, "divergence entry not found")
	}
	if err != nil {
		return nil, fmt.Errorf("get entry: %w", err)
	}

	// Accept dispatches a mutation BEFORE recording the resolution. On failure,
	// the resolution is not written so the entry stays visible as pending.
	if action == divergenceresolution.ActionAccept {
		if err := h.dispatchAcceptMutation(ctx, entry, actor); err != nil {
			return nil, err
		}
	}

	// REPLACE: delete any existing resolution for this (orbId, field), insert new.
	if _, err := h.db.DivergenceResolution.Delete().
		Where(
			divergenceresolution.EntryOrbID(entry.EntryOrbID),
			divergenceresolution.Field(entry.Field),
		).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("delete prior resolution: %w", err)
	}

	// Pin the DGraph intent version this decision was made against. The List
	// handler later compares this against DGraph's current version to refuse
	// surfacing the resolution if intent has moved (e.g., another cloud admin
	// edited the field between this decision and the next bundle build).
	// Accept's mutation already incremented intent by 1, so we pin the
	// post-mutation version; Reject/Ignore leave intent untouched.
	create := h.db.DivergenceResolution.Create().
		SetEntryOrbID(entry.EntryOrbID).
		SetField(entry.Field).
		SetAction(action).
		SetActor(actor).
		SetDecidedAt(time.Now().UTC())
	if entry.IntendedAtVersion != nil {
		v := *entry.IntendedAtVersion
		if action == divergenceresolution.ActionAccept {
			v++
		}
		create = create.SetIntendedAtVersion(v)
	}
	res, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create resolution: %w", err)
	}

	// verbNoun camelCase per AUDIT.md convention. The action verb alone
	// ("accept") is too ambiguous in the global audit log — `acceptDivergence`
	// reads correctly out of context. Action enum stays as the raw verb;
	// only the audit-facing operation name gets the noun.
	//
	// intendedValue/overrideValue carry the actual data: an `accept` event
	// records the value orbital is now adopting; a `reject`/`ignore` records
	// the value that was offered and refused. Without these, audit shows the
	// metadata of the decision but loses the substance of what changed.
	var intendedVal, overrideVal any
	_ = json.Unmarshal(entry.IntendedValue, &intendedVal)
	_ = json.Unmarshal(entry.OverrideValue, &overrideVal)
	writeAuditEvent(h.db, h.logger, "management", actor, "resolveDivergence",
		[]string{string(action) + "Divergence"},
		[]string{"DivergenceEntry"},
		[]string{entry.EntryOrbID + ":" + entry.Field},
		map[string]any{
			"entryId":       entry.ID.String(),
			"action":        string(action),
			"orbId":         entry.EntryOrbID,
			"field":         entry.Field,
			"dcOrbId":       entry.DcOrbID,
			"typeName":      entry.TypeName,
			"intendedValue": intendedVal,
			"overrideValue": overrideVal,
		},
	)

	return &resolutionItem{
		ID:        res.ID.String(),
		Action:    string(res.Action),
		Actor:     res.Actor,
		DecidedAt: res.DecidedAt.UTC().Format(time.RFC3339),
	}, nil
}

// currentValueMatches asks DGraph for the current value of one field on a
// ConfigItem and compares it (as raw JSON bytes) against the supplied
// expected JSON. Used as the value-based fallback in dispatchAcceptMutation
// when intended_at_version is nil. Returns false if values disagree, true
// if they match.
//
// Returns (false, err) on transport errors; caller should treat error as
// "couldn't verify, proceed with logged warning" rather than blocking — same
// fallback semantics as a missing version anchor.
func (h *DivergenceHandler) currentValueMatches(ctx context.Context, typeName, orbID, field string, expected json.RawMessage) (bool, error) {
	query := fmt.Sprintf(
		`query CurrentValue($orbId: String!) { get%s(orbId: $orbId) { %s } }`,
		typeName, field,
	)
	body, _ := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"orbId": orbID},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.gql.DGraphURL(), bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("build query: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("dgraph fetch: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("dgraph returned %d", resp.StatusCode)
	}
	var result struct {
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decode: %w", err)
	}
	entity, ok := result.Data["get"+typeName]
	if !ok || entity == nil {
		return false, fmt.Errorf("entity not found")
	}
	currentRaw, ok := entity[field]
	if !ok {
		return false, fmt.Errorf("field %q absent in response", field)
	}
	return bytes.Equal(bytes.TrimSpace(currentRaw), bytes.TrimSpace(expected)), nil
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

	// Stale-detection. Two layers:
	//   1. Version-based (primary): if we captured intended_at_version at
	//      intake, compare to current DGraph version. Catches ANY intervening
	//      write (other admin's edit, prior accept, etc.).
	//   2. Value-based (fallback when version anchor is nil): compare report's
	//      stored intended_value to current DGraph value for the field. Less
	//      precise — misses edit-then-revert cycles where value matches but
	//      version moved — but catches the common case.
	// Either failure mode surfaces as 409 so the admin re-reviews.
	if entry.IntendedAtVersion != nil {
		currentVersion, err := divergence.FetchCurrentVersion(ctx, h.gql.DGraphURL(), entry.TypeName, entry.EntryOrbID)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("could not verify intent version: %v", err))
		}
		if currentVersion != nil && *currentVersion != *entry.IntendedAtVersion {
			h.logger.Info("accept-divergence stale — version moved since report",
				"orbId", entry.EntryOrbID, "field", entry.Field,
				"reportedAt", *entry.IntendedAtVersion, "current", *currentVersion)
			return echo.NewHTTPError(http.StatusConflict,
				"intent has changed since this divergence was reported — please re-review")
		}
	} else if entry.TypeName != "" {
		// Value-based fallback. Less precise but better than nothing.
		matches, err := h.currentValueMatches(ctx, entry.TypeName, entry.EntryOrbID, entry.Field, entry.IntendedValue)
		if err != nil {
			h.logger.Warn("accept-divergence value-fallback check failed; proceeding without stale check",
				"orbId", entry.EntryOrbID, "field", entry.Field, "err", err)
		} else if !matches {
			h.logger.Info("accept-divergence stale — current intent value differs from report",
				"orbId", entry.EntryOrbID, "field", entry.Field)
			return echo.NewHTTPError(http.StatusConflict,
				"intent has changed since this divergence was reported — please re-review")
		}
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
	// Step 4 of the MVCC plan: bump version in the mutation. Without this,
	// ConfigItem.version stays stale and the NEXT divergence accept against
	// an entry captured at the same value would silently succeed. Mirrors how
	// user-driven edits bump version (orbital.js:519).
	set := map[string]any{entry.Field: overrideVal}
	if entry.IntendedAtVersion != nil {
		set["version"] = *entry.IntendedAtVersion + 1
	}
	variables := map[string]any{
		"filter": map[string]any{"orbId": map[string]any{"eq": entry.EntryOrbID}},
		"set":    set,
	}

	// The divergence entry already carries the intended value at snapshot
	// time — that's exactly what "before" is for the audit diff. No DGraph
	// round-trip needed; we already paid for that read when ingesting.
	var intended any
	_ = json.Unmarshal(entry.IntendedValue, &intended)
	before := map[string]any{entry.Field: intended}

	if _, err := h.gql.DispatchMutation(ctx, actor, mutation, variables, before); err != nil {
		h.logger.Warn("accept-divergence mutation failed",
			"orbId", entry.EntryOrbID, "field", entry.Field, "type", entry.TypeName, "err", err)
		return echo.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("failed to update intent: %v", err))
	}
	return nil
}

// Dismiss handles DELETE /api/v1/divergences/:id.
//
// Hard-deletes a divergence entry that the cloud admin has determined is
// stale — meaning the report was made against an orbital DGraph version that
// no longer matches current state (because intent has been edited since).
// Allowed ONLY when the entry is currently stale; fresh entries cannot be
// dismissed (the admin should accept/reject/ignore them instead).
//
// Audit-logged as `dismissDivergence` so the admin action is traceable. The
// entry's resolution row (if any) is cascaded out.
//
// Stale-check is re-validated at request time to close the race where the
// page showed stale but a concurrent ingest refreshed the entry's anchor.
//
// @Summary  Dismiss a stale divergence
// @Tags     divergence
// @Param    id path string true "Divergence entry UUID"
// @Success  204
// @Failure  400 {object} map[string]string
// @Failure  404 {object} map[string]string
// @Failure  409 {object} map[string]string "Not stale — accept/reject/ignore instead"
// @Router   /api/v1/divergences/{id} [delete]
func (h *DivergenceHandler) Dismiss(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	entry, err := h.db.DivergenceEntry.Get(ctx, id)
	if ent.IsNotFound(err) {
		return echo.NewHTTPError(http.StatusNotFound, "divergence not found")
	}
	if err != nil {
		return fmt.Errorf("get entry: %w", err)
	}

	// Re-check staleness at the moment of dismissal. Don't trust the page's
	// view — a concurrent ingest may have refreshed the anchor.
	stale, err := h.isStale(ctx, entry)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("could not verify staleness: %v", err))
	}
	if !stale {
		return echo.NewHTTPError(http.StatusConflict,
			"this divergence is not stale; accept, reject, or ignore it instead")
	}

	actor := actorFromContext(c)
	if actor == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "actor required")
	}

	// Cascade: delete the resolution row first (if any), then the entry.
	if _, err := h.db.DivergenceResolution.Delete().
		Where(
			divergenceresolution.EntryOrbID(entry.EntryOrbID),
			divergenceresolution.Field(entry.Field),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete resolution: %w", err)
	}
	if err := h.db.DivergenceEntry.DeleteOne(entry).Exec(ctx); err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}

	writeAuditEvent(h.db, h.logger, "management", actor, "dismissDivergence",
		[]string{"dismissDivergence"},
		[]string{"DivergenceEntry"},
		[]string{entry.EntryOrbID + ":" + entry.Field},
		map[string]any{
			"entryId": entry.ID.String(),
			"orbId":   entry.EntryOrbID,
			"field":   entry.Field,
			"dcOrbId": entry.DcOrbID,
			"reason":  "stale — intent has changed since report",
		},
	)
	return c.NoContent(http.StatusNoContent)
}

// isStale checks whether the entry's `intended_at_version` differs from the
// current DGraph version for the target ConfigItem. Used by Dismiss to gate
// the action. Value-based fallback (when version is nil) compares stored
// intended_value to current DGraph value.
func (h *DivergenceHandler) isStale(ctx context.Context, entry *ent.DivergenceEntry) (bool, error) {
	if entry.TypeName == "" {
		// Without a type we can't query — caller treats as fresh.
		return false, nil
	}
	if entry.IntendedAtVersion != nil {
		current, err := divergence.FetchCurrentVersion(ctx, h.gql.DGraphURL(), entry.TypeName, entry.EntryOrbID)
		if err != nil {
			return false, err
		}
		if current == nil {
			return false, nil
		}
		return *current != *entry.IntendedAtVersion, nil
	}
	// Value-based fallback.
	matches, err := h.currentValueMatches(ctx, entry.TypeName, entry.EntryOrbID, entry.Field, entry.IntendedValue)
	if err != nil {
		return false, err
	}
	return !matches, nil
}

// DeleteResolution handles DELETE /api/v1/divergences/:id/resolution.
//
// Removes the cloud admin's decision on a divergence — the entry returns to
// "pending." Used by the UI's "Undo" affordance when an admin wants to
// reconsider before propagation completes.
//
// Idempotent: 204 even if no resolution existed.
//
// @Summary  Clear a divergence resolution
// @Tags     divergence
// @Param    id path string true "Divergence entry UUID"
// @Success  204
// @Failure  400 {object} map[string]string
// @Failure  404 {object} map[string]string
// @Router   /api/v1/divergences/{id}/resolution [delete]
func (h *DivergenceHandler) DeleteResolution(c echo.Context) error {
	ctx := c.Request().Context()
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid id")
	}
	entry, err := h.db.DivergenceEntry.Get(ctx, id)
	if ent.IsNotFound(err) {
		return echo.NewHTTPError(http.StatusNotFound, "divergence not found")
	}
	if err != nil {
		return fmt.Errorf("get entry: %w", err)
	}
	if _, err := h.db.DivergenceResolution.Delete().
		Where(
			divergenceresolution.EntryOrbID(entry.EntryOrbID),
			divergenceresolution.Field(entry.Field),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete resolution: %w", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// parseAction maps an external action string to the typed enum value.
func parseAction(s string) (divergenceresolution.Action, error) {
	switch s {
	case string(divergenceresolution.ActionAccept):
		return divergenceresolution.ActionAccept, nil
	case string(divergenceresolution.ActionReject):
		return divergenceresolution.ActionReject, nil
	case string(divergenceresolution.ActionIgnore):
		return divergenceresolution.ActionIgnore, nil
	default:
		return "", fmt.Errorf("invalid action %q (want accept | reject | ignore)", s)
	}
}
