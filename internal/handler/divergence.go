package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/divergenceentry"
	"github.com/armada/orbital/ent/divergenceresolution"
	"github.com/armada/orbital/internal/approval"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// DivergenceHandler exposes orbital's divergence ingestion + resolution API.
// Orbital ingests reports from S3 (via internal/divergenceingest) and
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
	// cr opens a change request when an Accept is refused by the approval gate.
	// Optional in the same sense as ingester: nil in tests that never exercise a
	// gated Accept.
	cr       *ChangeRequest
	ingester DivergenceIngester // optional; nil when ingester subsystem isn't running
}

// DivergenceIngester is the subset of *divergenceingest.Ingester this handler
// needs. Declared as an interface to avoid an import cycle and to keep the
// handler testable without booting a real ingester.
type DivergenceIngester interface {
	ResetDC(ctx context.Context, dcID string) error
}

func NewDivergenceHandler(db *ent.Client, logger *slog.Logger, gql *GraphQL) *DivergenceHandler {
	return &DivergenceHandler{db: db, logger: logger, gql: gql}
}

// SetIngester wires the divergence ingester so ClearByDC can reset the
// in-memory idempotency tracker after wiping DB rows. No-op when nil — the
// handler still works (and tests don't need to mock an ingester).
func (h *DivergenceHandler) SetIngester(ing DivergenceIngester) {
	h.ingester = ing
}

// SetChangeRequests wires the approval engine so a gated Accept can open a
// change request instead of failing. Set from server wiring; a nil engine makes
// a gated Accept a 500, which is correct — silently mutating past the gate would
// be far worse than a loud misconfiguration.
func (h *DivergenceHandler) SetChangeRequests(cr *ChangeRequest) {
	h.cr = cr
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

// pendingApprovalItem is what an Accept returns when the field it would change
// belongs to a protected class and the caller cannot write it directly.
//
// The divergence is NOT resolved: intent has not moved, so recording a
// resolution would claim a decision that has not taken effect, and the entry
// must stay pending until the change request merges.
type pendingApprovalItem struct {
	// Status is always "pending_approval" — a discriminator so a client can
	// branch without inspecting which fields are present.
	Status string `json:"status" example:"pending_approval"`
	// ChangeRequestID is the request opened on the caller's behalf, pre-filled
	// from this divergence entry.
	ChangeRequestID string `json:"changeRequestId" example:"colo-42"`
	Message         string `json:"message" example:"Accepting this divergence needs approval; a change request was opened."`
}

// resolutionOutcome is one of two shapes: a recorded resolution, or a change
// request awaiting approval. Never both.
type resolutionOutcome struct {
	Resolution *resolutionItem
	Pending    *pendingApprovalItem
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

	// Per ADR 012: a resolution row IS the operator's decision. No version pin,
	// no value-based staleness check — those concepts are gone. Resolutions
	// are wiped at ingest time when orb publishes a content-differing report,
	// so anything still in the table is by definition current.
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

		if len(wantAction) > 0 {
			if item.Resolution == nil {
				continue
			}
			if !wantAction[divergenceresolution.Action(item.Resolution.Action)] {
				continue
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
// @Failure  400 {object} errorResponse
// @Failure  404 {object} errorResponse
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
// @Success  202  {object} pendingApprovalItem "Accept needs approval — a change request was opened and the entry stays pending"
// @Failure  400  {object} errorResponse
// @Failure  401  {object} errorResponse
// @Failure  404  {object} errorResponse
// @Failure  409  {object} errorResponse  "MVCC conflict — intent changed since report"
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
	out, err := h.applyResolution(c.Request().Context(), id, action, actor, resolveCallerRole(c, h.db))
	if err != nil {
		return err
	}
	if out.Pending != nil {
		// 202: the request was accepted, the work is not complete. Additive for
		// clients that branch on 2xx, and honest about the fact that nothing has
		// changed in the graph yet.
		return c.JSON(http.StatusAccepted, *out.Pending)
	}
	return c.JSON(http.StatusOK, *out.Resolution)
}

// applyResolution is the side-effect-bearing core of the resolution pipeline.
// Finds the DivergenceEntry by ID, dispatches the side-effect (Accept: GraphQL
// mutation that updates intent), then upserts the DivergenceResolution row
// (REPLACE semantics: re-deciding overwrites the previous decision). If the
// side-effect fails, NO resolution row is written so the entry stays pending.
//
// Returns an HTTPError on validation/dispatch failure suitable for echo to
// render, or a generic error on storage failures.
//
// Accept on a protected class does NOT mutate: it opens a change request and
// returns it as `Pending`, leaving the entry unresolved. Reject and Ignore are
// never gated because neither touches intent.
func (h *DivergenceHandler) applyResolution(ctx context.Context, id uuid.UUID, action divergenceresolution.Action, actor string, caller callerRole) (resolutionOutcome, error) {
	entry, err := h.db.DivergenceEntry.Get(ctx, id)
	if ent.IsNotFound(err) {
		return resolutionOutcome{}, echo.NewHTTPError(http.StatusNotFound, "divergence entry not found")
	}
	if err != nil {
		return resolutionOutcome{}, fmt.Errorf("get entry: %w", err)
	}

	// Accept dispatches a mutation BEFORE recording the resolution. On failure,
	// the resolution is not written so the entry stays visible as pending.
	if action == divergenceresolution.ActionAccept {
		pending, err := h.dispatchAcceptMutation(ctx, entry, actor, caller)
		if err != nil {
			return resolutionOutcome{}, err
		}
		if pending != nil {
			return resolutionOutcome{Pending: pending}, nil
		}
	}

	// REPLACE: delete any existing resolution for this (orbId, field), insert new.
	if _, err := h.db.DivergenceResolution.Delete().
		Where(
			divergenceresolution.EntryOrbID(entry.EntryOrbID),
			divergenceresolution.Field(entry.Field),
		).
		Exec(ctx); err != nil {
		return resolutionOutcome{}, fmt.Errorf("delete prior resolution: %w", err)
	}

	res, err := h.db.DivergenceResolution.Create().
		SetEntryOrbID(entry.EntryOrbID).
		SetField(entry.Field).
		SetAction(action).
		SetActor(actor).
		SetDecidedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return resolutionOutcome{}, fmt.Errorf("create resolution: %w", err)
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
	// resourceID is the underlying resource's orbId (e.g. the IdracSettings or
	// Server that the divergence is on) — NOT a synthetic <orbId>:<field>
	// compound. The orbId column must hold real, queryable orbIds; the field
	// name lives in details. This makes the event filterable by the resource's
	// audit panel (`?orbId=colo:CWJHDX3-idrac`) which is what operators expect.
	writeAuditEvent(h.db, h.logger, "management", actor, "resolveDivergence",
		[]string{string(action) + "Divergence"},
		[]string{"DivergenceEntry"},
		[]string{entry.EntryOrbID},
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

	return resolutionOutcome{Resolution: &resolutionItem{
		ID:        res.ID.String(),
		Action:    string(res.Action),
		Actor:     res.Actor,
		DecidedAt: res.DecidedAt.UTC().Format(time.RFC3339),
	}}, nil
}

// dispatchAcceptMutation issues `update{Type}(filter:{orbId},set:{field:value})`
// through the GraphQL handler so the mutation lands in audit alongside any
// user-driven mutation.
//
// Returns a non-nil pendingApprovalItem when the field belongs to a protected
// class the caller may not write directly: a change request is opened instead
// and NOTHING is mutated. Otherwise an HTTPError suitable to propagate verbatim.
func (h *DivergenceHandler) dispatchAcceptMutation(ctx context.Context, entry *ent.DivergenceEntry, actor string, caller callerRole) (*pendingApprovalItem, error) {
	if h.gql == nil {
		// Defensive: server.go must wire gql into DivergenceHandler.
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "graphql dispatcher not configured")
	}
	if len(entry.OverrideValue) == 0 {
		return nil, echo.NewHTTPError(http.StatusUnprocessableEntity, "override value is empty; cannot mutate intent")
	}

	typeName, err := h.resolveEntryType(ctx, entry)
	if err != nil {
		return nil, err
	}

	var overrideVal any
	if err := json.Unmarshal(entry.OverrideValue, &overrideVal); err != nil {
		return nil, echo.NewHTTPError(http.StatusUnprocessableEntity, fmt.Sprintf("invalid override value: %v", err))
	}

	// Per ADR 012: Accept is last-writer-wins against orbital DGraph. We do
	// not pre-check that intent hasn't moved since the report — if it has,
	// the post-Accept divergence-ingest cycle catches it via the supersede
	// path. Audit log records the dispatched mutation; any racing edit shows
	// up there too.
	//
	// The canonical update{Kind}($orbId, $set) shape, and it MUST stay that
	// shape. writeToDGraph resolves the row to stamp from the `orbId` variable;
	// the $filter-object form this used to send resolved to nothing, so an
	// Accept wrote intent with no version bump — which made it invisible to
	// change-request staleness, since base_hash only moves when a version does.
	// Fixed 2026-09-03; see docs/planning/debt.md Track B.
	mutation := fmt.Sprintf(
		`mutation AcceptDivergence($orbId: String!, $set: %sPatch!) { update%s(input: {filter: {orbId: {eq: $orbId}}, set: $set}) { numUids } }`,
		typeName, typeName,
	)
	variables := map[string]any{
		"orbId": entry.EntryOrbID,
		"set":   map[string]any{entry.Field: overrideVal},
	}

	// A FALLBACK for the audit diff, not the diff itself. writeToDGraph fetches
	// current state anyway (it cannot stamp the next version otherwise) and the
	// fetch wins field by field. This still earns its place: BeforeFields is a
	// curated per-type selection and entry.Field is resolved at runtime, so for
	// a field outside that list this is the only before-value there is.
	var intended any
	_ = json.Unmarshal(entry.IntendedValue, &intended)
	before := map[string]any{entry.Field: intended}

	// gateEnforce, deliberately. Accept mutates intent, so it is a config change
	// like any other and the chokepoint is the single authority on whether it is
	// allowed. Asking the policy here as well would be a second copy of the rule
	// to keep in sync — instead this REACTS to the gate's refusal.
	if _, err := h.gql.DispatchMutation(ctx, actor, caller, gateEnforce, mutation, variables, before); err != nil {
		var gerr *gatedError
		if errors.As(err, &gerr) && gerr.Code == CodeApprovalRequired {
			return h.openChangeRequestFor(ctx, entry, typeName, overrideVal, actor)
		}
		h.logger.Warn("accept-divergence mutation failed",
			"orbId", entry.EntryOrbID, "field", entry.Field, "type", typeName, "err", err)
		return nil, echo.NewHTTPError(http.StatusBadGateway, fmt.Sprintf("failed to update intent: %v", err))
	}
	return nil, nil
}

// resolveEntryType returns the ConfigItem type of the entry's target.
//
// The report's own type_name is a HINT, not a requirement. It comes from
// cb-bundler through orb, and older reports do not carry it — which used to make
// Accept fail with "update intent manually", advice that the approval gate turns
// circular for exactly the classes someone bothered to protect.
//
// orbId is @id on the ConfigItem interface, so it is globally unique across
// every type and orbital can simply look the type up. The remaining failure —
// the orbId is not in orbital's graph at all — is a genuinely different problem
// with a different remedy, so it says so rather than blaming missing type info.
func (h *DivergenceHandler) resolveEntryType(ctx context.Context, entry *ent.DivergenceEntry) (string, error) {
	if entry.TypeName != "" {
		return entry.TypeName, nil
	}
	src := approval.NewDGraphSchemaSource(h.gql.DGraphURL())
	refs, err := src.ResolveEntities(ctx, []string{entry.EntryOrbID})
	if err != nil {
		return "", echo.NewHTTPError(http.StatusBadGateway,
			fmt.Sprintf("could not resolve the type of %s: %v", entry.EntryOrbID, err))
	}
	ref, ok := refs[entry.EntryOrbID]
	if !ok {
		return "", echo.NewHTTPError(http.StatusUnprocessableEntity, fmt.Sprintf(
			"orbital has no entity with orbId %q, so there is no intent to update. The edge is reporting a resource orbital does not know about — check the data center's import, or dismiss this entry.",
			entry.EntryOrbID))
	}
	return ref.Type, nil
}

// openChangeRequestFor turns a refused Accept into a proposal.
//
// Deliberately does NOT record a resolution: intent has not moved, so claiming
// the divergence is resolved would be a lie that survives until someone notices
// the edge still diverging. The entry stays pending until the change request
// merges.
func (h *DivergenceHandler) openChangeRequestFor(ctx context.Context, entry *ent.DivergenceEntry, typeName string, overrideVal any, actor string) (*pendingApprovalItem, error) {
	if h.cr == nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "change request engine not configured")
	}
	ns, _, _ := strings.Cut(entry.EntryOrbID, ":")
	cs := &approval.Changeset{
		Namespace: ns,
		Changes: []approval.ChangeItem{{
			OrbID: entry.EntryOrbID,
			Type:  typeName,
			Op:    approval.OpUpdate,
			Set:   map[string]any{entry.Field: overrideVal},
		}},
	}
	title := fmt.Sprintf("Accept edge value for %s.%s", entry.EntryOrbID, entry.Field)
	desc := fmt.Sprintf("Opened from divergence entry %s. The edge reports %s=%v, set locally by %q. Accepting adopts that value as orbital's intent.",
		entry.ID, entry.Field, overrideVal, entry.Who)

	cr, problems, err := h.cr.Create(ctx, actor, title, desc, cs)
	if err != nil {
		return nil, fmt.Errorf("open change request for divergence: %w", err)
	}
	if len(problems) > 0 {
		return nil, echo.NewHTTPError(http.StatusUnprocessableEntity, fmt.Sprintf(
			"this divergence cannot be turned into a change request: %s", problems[0].Error()))
	}

	h.logger.Info("divergence accept requires approval — change request opened",
		"change_request", crHumanID(cr), "orbId", entry.EntryOrbID, "field", entry.Field, "actor", actor)

	return &pendingApprovalItem{
		Status:          "pending_approval",
		ChangeRequestID: crHumanID(cr),
		Message: fmt.Sprintf("Accepting this divergence needs approval. Change request %s was opened; the entry stays pending until it merges.",
			crHumanID(cr)),
	}, nil
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
// @Failure  400 {object} errorResponse
// @Failure  404 {object} errorResponse
// @Failure  409 {object} errorResponse "Not stale — accept/reject/ignore instead"
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

	actor := actorFromContext(c)
	if actor == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "actor required")
	}

	// Per ADR 012: Dismiss is a straight delete — no staleness gate. Operator
	// owns the call. The next supersede cycle would re-create this entry if
	// orb keeps reporting it, so Dismiss is more "I want this gone now" than
	// a permanent purge.
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
		[]string{entry.EntryOrbID},
		map[string]any{
			"entryId": entry.ID.String(),
			"orbId":   entry.EntryOrbID,
			"field":   entry.Field,
			"dcOrbId": entry.DcOrbID,
		},
	)
	return c.NoContent(http.StatusNoContent)
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
// @Failure  400 {object} errorResponse
// @Failure  404 {object} errorResponse
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

// ClearByDC handles DELETE /api/v1/divergences?dcOrbId=<dc>.
//
// Break-glass for operators: drops ALL DivergenceEntry and DivergenceResolution
// rows for a DC in one transaction, then resets the ingester's idempotency
// tracker so the next poll re-processes the latest S3 report fresh.
//
// Audit-logged as `deleteDivergenceReport` with entry count. Role-gated to Dev
// minimum by the api group middleware.
//
// @Summary  Clear all divergence state for a data center
// @Tags     divergence
// @Param    dcOrbId query string true "Data center orbId (e.g. colo:colo-galleon)"
// @Success  200 {object} map[string]any
// @Failure  400 {object} errorResponse
// @Failure  401 {object} errorResponse
// @Router   /api/v1/divergences [delete]
func (h *DivergenceHandler) ClearByDC(c echo.Context) error {
	dc := c.QueryParam("dcOrbId")
	if dc == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "dcOrbId query param required")
	}
	actor := actorFromContext(c)
	if actor == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "actor required")
	}

	ctx := c.Request().Context()

	entries, err := h.db.DivergenceEntry.Query().
		Where(divergenceentry.DcOrbID(dc)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query entries: %w", err)
	}

	tx, err := h.db.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	for _, e := range entries {
		if _, err := tx.DivergenceResolution.Delete().
			Where(
				divergenceresolution.EntryOrbID(e.EntryOrbID),
				divergenceresolution.Field(e.Field),
			).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("delete resolutions: %w", err)
		}
	}
	if _, err := tx.DivergenceEntry.Delete().
		Where(divergenceentry.DcOrbID(dc)).
		Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete entries: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Without this, the ingester's persisted cursor would skip the
	// (already-seen) latest S3 file and the operator would see an empty page
	// until orb publishes something genuinely new.
	if h.ingester != nil {
		if err := h.ingester.ResetDC(c.Request().Context(), dc); err != nil {
			h.logger.Warn("reset ingester cursor failed", "dc", dc, "err", err)
		}
	}

	writeAuditEvent(h.db, h.logger, "management", actor, "deleteDivergenceReport",
		[]string{"deleteDivergenceReport"},
		[]string{"DataCenter"},
		[]string{dc},
		map[string]any{
			"dcOrbId":        dc,
			"entriesDropped": len(entries),
		},
	)

	return c.JSON(http.StatusOK, map[string]any{
		"dcOrbId":        dc,
		"entriesDropped": len(entries),
	})
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
