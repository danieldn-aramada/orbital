package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	entapproval "github.com/armada/orbital/ent/approval"
	"github.com/armada/orbital/ent/approvalpolicy"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/ent/user"
	"github.com/armada/orbital/internal/approval"
	"github.com/armada/orbital/internal/configitems"
	"github.com/armada/orbital/internal/graphdiff"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// ── Request DTOs ────────────────────────────────────────────────────────────

// createChangeRequestBody opens a change request.
type createChangeRequestBody struct {
	// Title is the one-line summary reviewers see in the queue.
	Title string `json:"title" validate:"required" example:"Enable SSH on the Anchorage iDRACs"`
	// Description is optional free text — why, and anything a reviewer needs.
	Description string `json:"description,omitempty" example:"Requested by field ops for the Nov maintenance window."`
	// Namespace scopes the whole request. Every orbId in changes must be in it.
	Namespace string `json:"namespace" validate:"required" example:"alaska-dot"`
	// Changes is the target end-state, one entry per entity.
	Changes []changeItemBody `json:"changes" validate:"required"`
}

// changeItemBody is one entity's proposed end-state.
type changeItemBody struct {
	// OrbID identifies the target. Globally unique, which is why Type is optional.
	OrbID string `json:"orbId" validate:"required" example:"alaska-dot:server-4FK8K44"`
	// Type is the ConfigItem type. Optional for an existing entity (orbital
	// resolves it from OrbID); REQUIRED when creating one that does not exist.
	Type string `json:"type,omitempty" example:"Server"`
	// Op is one of upsert, update, delete. Explicit, never inferred — a request
	// approved as an update must not silently become a create.
	Op string `json:"op" validate:"required" example:"update"`
	// Set is the fields to write. Edge fields carry only a reference:
	// {"dataCenter": {"orbId": "alaska-dot:dc-01"}}.
	Set map[string]any `json:"set,omitempty"`
	// Clear is the fields to unset.
	Clear []string `json:"clear,omitempty" example:"oobMAC"`
}

// amendChangeRequestBody patches an open change request. Omitted fields are
// left alone; supplying changes re-captures the base and therefore invalidates
// the approvals cast against the previous one.
type amendChangeRequestBody struct {
	Title       *string          `json:"title,omitempty" example:"Enable SSH on the Anchorage iDRACs"`
	Description *string          `json:"description,omitempty" example:"Rescheduled to the Dec window."`
	Namespace   string           `json:"namespace,omitempty" example:"alaska-dot"`
	Changes     []changeItemBody `json:"changes,omitempty"`
}

// decisionBody carries an optional reviewer comment.
type decisionBody struct {
	Comment string `json:"comment,omitempty" example:"Checked against the maintenance window."`
}

// approvalPolicyBody declares a protected class.
type approvalPolicyBody struct {
	// Namespace the policy governs.
	Namespace string `json:"namespace" validate:"required" example:"alaska-dot"`
	// AllTypes protects every type in the namespace, including ConfigItem types
	// added to the schema later. Mutually exclusive with Types.
	AllTypes *bool `json:"allTypes,omitempty" example:"true"`
	// Types are the ConfigItem types to protect. Mutually exclusive with
	// AllTypes: exactly one of the two must say what is covered, and the other
	// two combinations are refused (see the create endpoint).
	Types []string `json:"types,omitempty" example:"Server"`
	// RequiredApprovals is how many distinct reviewers must approve. Default 1.
	RequiredApprovals int `json:"requiredApprovals,omitempty" example:"1"`
	// BypassRoles may write this class directly, recorded as a privileged write.
	// Bypass is a property of the policy, not of a user.
	//
	// Omit the field to accept the default ["admin"]. Send an EMPTY array to
	// mean nobody bypasses — including admins. The two are distinct on purpose:
	// there is no other way to express a class that everyone must get reviewed.
	BypassRoles []string `json:"bypassRoles,omitempty" example:"admin"`
	// Enabled turns the policy off without deleting it. Default true.
	Enabled *bool `json:"enabled,omitempty" example:"true"`
}

// ── Response DTOs ───────────────────────────────────────────────────────────

// changeRequestResponse is one change request, rendered.
//
// Everything a view needs is computed server-side: Stale, Status and
// AvailableActions are derived per request per caller so no client
// re-implements orbital's eligibility rules. Orbital's own UI renders buttons
// straight from AvailableActions, exactly as an external client would.
type changeRequestResponse struct {
	// ID is the human identifier — the namespace, then its number within that
	// namespace. It is what every URL and every client uses; the surrogate
	// bigint behind it is never exposed. Per-namespace numbering follows Jira's
	// PROJ-42 model applied to orbital's natural partition, so an id pasted into
	// chat says which data center it is about.
	ID          string `json:"id" example:"colo-42"`
	ActionType  string `json:"actionType" example:"config.mutation"`
	Title       string `json:"title" example:"Enable SSH on the Anchorage iDRACs"`
	Description string `json:"description,omitempty"`
	// Status is the EFFECTIVE status: open, approved, rejected, merged, closed.
	// `approved` is derived from the valid-approval count, so it can revert to
	// `open` on its own when the base moves.
	Status    string `json:"status" example:"open"`
	Namespace string `json:"namespace" example:"alaska-dot"`
	Author    string `json:"author" example:"proposer@armada.ai"`
	// Stale means the intent this request was written against has changed since
	// it was opened. Derived on every read — never a stored column.
	Stale bool `json:"stale" example:"false"`
	// Approvals is how many currently-counting approvals exist, and Required is
	// how many the policy demands. Required 0 means nothing governs this change.
	Approvals int `json:"approvals" example:"0"`
	Required  int `json:"requiredApprovals" example:"1"`
	// AvailableActions is the caller-relative verdict: approve, reject, merge,
	// edit, close.
	AvailableActions []string `json:"availableActions"`
	// MissingTargets are entities that existed when this request was opened and
	// have since been deleted. A merge will fail with TARGET_MISSING.
	MissingTargets []string             `json:"missingTargets,omitempty"`
	Changes        []changeItemBody     `json:"changes"`
	Reviews        []approvalResponse   `json:"reviews,omitempty"`
	MergeAttempts  []mergeAttemptResult `json:"mergeAttempts,omitempty"`
	CreatedAt      time.Time            `json:"createdAt"`
	UpdatedAt      *time.Time           `json:"updatedAt,omitempty"`
	ExecutedAt     *time.Time           `json:"executedAt,omitempty"`
	ExecutedBy     string               `json:"executedBy,omitempty"`
}

// approvalResponse is one reviewer's decision.
type approvalResponse struct {
	Approver string    `json:"approver" example:"reviewer@armada.ai"`
	Decision string    `json:"decision" example:"approved"`
	Comment  string    `json:"comment,omitempty"`
	At       time.Time `json:"at"`
	// Current is false when the decision was cast against an earlier version of
	// the intent and no longer counts — surfaced rather than hidden so the UI
	// can say "approved an earlier version" instead of the approval vanishing.
	Current bool `json:"current" example:"true"`
}

// mergeAttemptResult is what one merge actually did, item by item.
type mergeAttemptResult struct {
	AttemptedBy string    `json:"attemptedBy" example:"merger@armada.ai"`
	AttemptedAt time.Time `json:"attemptedAt"`
	// Error is the attempt-level failure. Empty when every item applied.
	Error string `json:"error,omitempty"`
	// Results is the per-item outcome, in the order applied. A partial merge is
	// a real outcome, not an error state — what applied stays applied.
	Results []mergeItemResult `json:"results,omitempty"`
}

// mergeItemResult is one item's outcome within a merge attempt.
type mergeItemResult struct {
	OrbID   string `json:"orbId" example:"alaska-dot:server-4FK8K44"`
	Applied bool   `json:"applied" example:"true"`
	Error   string `json:"error,omitempty"`
}

// changeRequestListResponse is a page of change requests.
type changeRequestListResponse struct {
	// Total is the number of matching requests. Drives the "awaiting my review"
	// nav badge — there is no separate count endpoint.
	Total int                     `json:"total" example:"3"`
	Items []changeRequestResponse `json:"items"`
}

// changeRequestDiffResponse is the content diff between current intent and the
// request's target end-state.
type changeRequestDiffResponse struct {
	// Stale means the base moved; the diff below is against CURRENT intent, so
	// it already reflects that.
	Stale bool `json:"stale" example:"false"`
	// ContentHash is the hash of current intent over this request's scope.
	ContentHash string `json:"contentHash" example:"sha256:045b8a51a0aea59fa"`
	// BaseHash is the hash captured when the request was opened.
	BaseHash string            `json:"baseHash" example:"sha256:045b8a51a0aea59fa"`
	Summary  graphdiff.Summary `json:"summary"`
	// Changes is FLAT — one entry per changed entity, never a nested tree.
	Changes []*graphdiff.Change `json:"changes"`
}

// approvalPolicyResponse is one protected class.
type approvalPolicyResponse struct {
	ID                string   `json:"id" example:"7c2e1f88-1a2b-4c3d-8e9f-0a1b2c3d4e5f"`
	ActionType        string   `json:"actionType" example:"config.mutation"`
	Namespace         string   `json:"namespace" example:"alaska-dot"`
	AllTypes          bool     `json:"allTypes" example:"true"`
	Types             []string `json:"types,omitempty"`
	RequiredApprovals int      `json:"requiredApprovals" example:"1"`
	BypassRoles       []string `json:"bypassRoles"`
	// Enabled is the admin's switch: turn a policy off without deleting it.
	Enabled bool `json:"enabled" example:"true"`
}

// approvalPolicyResolveResponse answers "is this change gated for me?" so a
// client can label its save button Save or Propose without guessing.
type approvalPolicyResolveResponse struct {
	// Required reports whether a POLICY demands approval for this class. It
	// describes the policy, not what orbital will do — see Enforced.
	Required          bool     `json:"required" example:"true"`
	RequiredApprovals int      `json:"requiredApprovals" example:"1"`
	BypassRoles       []string `json:"bypassRoles"`
	// CallerMayBypass is the verdict for THIS caller, already computed.
	CallerMayBypass bool `json:"callerMayBypass" example:"false"`
}

// validationErrorResponse reports every problem with a proposed changeset at
// once, so a client fixes one round-trip's worth of mistakes at a time rather
// than discovering them one 400 at a time.
type validationErrorResponse struct {
	Error      string             `json:"error" example:"changeset is not valid"`
	Code       string             `json:"code" example:"BAD_USER_INPUT"`
	HTTPStatus int                `json:"httpStatus" example:"400"`
	Problems   []changesetProblem `json:"problems"`
}

// changesetProblem is one problem with one change item.
type changesetProblem struct {
	// Index is the item's position in changes[], so a client can point at the
	// offending row instead of making the user re-read the whole changeset.
	Index int    `json:"index" example:"0"`
	OrbID string `json:"orbId,omitempty" example:"alaska-dot:server-4FK8K44"`
	Field string `json:"field,omitempty" example:"hostnmae"`
	Msg   string `json:"message" example:"no such field on Server"`
	Hint  string `json:"hint,omitempty"`
}

// ── Handlers ────────────────────────────────────────────────────────────────

// CreateChangeRequest opens a change request.
//
// @Summary     Open a change request
// @Description Validates the changeset against the deployed schema and the current graph, captures the intent it is written against, and opens it for review. Every problem is reported at once.
// @Tags        change-requests
// @Accept      json
// @Produce     json
// @Param       body body createChangeRequestBody true "Change request"
// @Success     201 {object} changeRequestResponse
// @Failure     400 {object} validationErrorResponse
// @Failure     403 {object} errorResponse
// @Router      /api/v1/change-requests [post]
func (h *ChangeRequest) CreateChangeRequest(c echo.Context) error {
	var body createChangeRequestBody
	if err := c.Bind(&body); err != nil {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "invalid request body", "")
	}
	if strings.TrimSpace(body.Title) == "" {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "title is required", "")
	}

	cs := &approval.Changeset{Namespace: body.Namespace, Changes: toChangeItems(body.Changes)}
	actor := actorFromContext(c)

	cr, problems, err := h.Create(c.Request().Context(), actor, body.Title, body.Description, cs)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return writeChangesetProblems(c, problems)
	}
	return h.renderOne(c, cr, http.StatusCreated)
}

// ListChangeRequests lists change requests.
//
// @Summary     List change requests
// @Description Filters compose. `mine` and `awaiting_review` are caller-relative; `orbId` matches any request whose changeset touches that entity, at any position. `status=active` means not-terminal (open plus approved) — the filter to use for "does this entity have a change in flight", since `approved` is derived and `status=open` excludes it.
// @Description
// @Description `orbId` is **repeatable** and the values are OR-ed (max 32; more is refused, never truncated). A change to an owned child records the CHILD's orbId — a server-maintenance edit lands as `<ns>:server-maintenance-<serial>` — so "is anything in flight for this server" means passing the server's orbId AND the orbIds of everything it owns, exactly as `/api/v1/audit-log` does.
// @Tags        change-requests
// @Produce     json
// @Param       status query string false "open, approved, active (open+approved), rejected, merged or closed"
// @Param       namespace query string false "Namespace"
// @Param       author query string false "Author email"
// @Param       mine query boolean false "Only requests this caller authored"
// @Param       awaiting_review query boolean false "Only requests this caller can still review"
// @Param       orbId query []string false "Only requests touching this entity. Repeatable, max 128 — matches requests touching ANY of them. Over 128 the request is refused (400), not truncated."
// @Success     200 {object} changeRequestListResponse
// @Failure     400 {object} errorResponse
// @Router      /api/v1/change-requests [get]
func (h *ChangeRequest) ListChangeRequests(c echo.Context) error {
	// One request renders many rows, and every row's status derives from the
	// same policy. Without this each row queries for it again.
	ctx := withPolicyMemo(c.Request().Context())
	actor := actorFromContext(c)
	cr := resolveCallerRole(c, h.db)

	wantStatus := c.QueryParam("status")
	wantNamespace := c.QueryParam("namespace")
	awaiting := c.QueryParam("awaiting_review") == "true"

	// orbId is repeatable — ?orbId=server&orbId=idrac&orbId=maintenance — and
	// the values are OR-ed, matching /api/v1/audit-log. Reading it with
	// QueryParam took the FIRST value and silently answered about that one
	// alone, so a page asking about a server and its owned children got an
	// answer about the server only, and a pending change on a child read as
	// "nothing in flight".
	wantOrbIDs := make([]string, 0, len(c.QueryParams()["orbId"]))
	for _, id := range c.QueryParams()["orbId"] {
		// Drop empties so an attribute like data-related-orb-ids="" cannot
		// insert "" and match nothing while looking like a filter.
		if id = strings.TrimSpace(id); id != "" {
			wantOrbIDs = append(wantOrbIDs, id)
		}
	}
	if len(wantOrbIDs) > maxOrbIDFilter {
		// Refused, not truncated. A truncated filter answers a question the
		// caller did not ask and looks exactly like a correct answer — the same
		// silent-wrong-answer failure the repeatable form exists to fix.
		return writeError(c, http.StatusBadRequest, CodeBadUserInput,
			fmt.Sprintf("too many orbId filters: %d (max %d)", len(wantOrbIDs), maxOrbIDFilter),
			fmt.Sprintf("Query at most %d orbIds at a time, or drop orbId and filter by namespace instead.", maxOrbIDFilter))
	}

	q := h.db.ApprovalRequest.Query().WithApprovals().WithMergeAttempts()
	if v := c.QueryParam("author"); v != "" {
		q = q.Where(approvalrequest.AuthorEQ(v))
	}
	if c.QueryParam("mine") == "true" {
		q = q.Where(approvalrequest.AuthorEQ(actor))
	}

	// Everything that CAN be decided in SQL is decided in SQL, before any row
	// is rendered.
	//
	// Rendering is the expensive step: it derives staleness, which means a
	// subtree query and a content hash per request. Filtering afterwards means
	// paying that for rows that were never going to be returned — fine for a
	// queue page a human opens now and then, ruinous for the pending-change
	// badge, which fires on every detail view and almost always matches
	// nothing. Ordering, not caching, is what makes that query free.
	switch wantStatus {
	case approval.StatusRejected:
		q = q.Where(approvalrequest.StatusEQ(approvalrequest.StatusRejected))
	case approval.StatusMerged:
		q = q.Where(approvalrequest.StatusEQ(approvalrequest.StatusMerged))
	case approval.StatusClosed:
		q = q.Where(approvalrequest.StatusEQ(approvalrequest.StatusClosed))
	case approval.StatusOpen, approval.StatusApproved, statusActive:
		// All three live in the stored `open` state — `approved` is derived
		// from the valid-approval count (D17), so SQL narrows to non-terminal
		// and the exact split happens after rendering, where the count exists.
		q = q.Where(approvalrequest.StatusEQ(approvalrequest.StatusOpen))
	}
	if awaiting {
		q = q.Where(approvalrequest.StatusEQ(approvalrequest.StatusOpen))

		// Two of the three reasons a row gets discarded after rendering are
		// knowable in SQL, and this filter runs on EVERY page load — the nav
		// badge has no namespace or orbId to narrow by, so without this it
		// renders the entire open queue to produce one number.
		if !cr.NoAuthz && !RoleAtLeast(cr.Role, user.RoleDev) {
			// readonly can look but never approve, so nothing awaits them.
			return c.JSON(http.StatusOK, changeRequestListResponse{Total: 0, Items: []changeRequestResponse{}})
		}
		// You cannot approve your own request — unless a policy lets your role
		// bypass, which makes approve available on it after all. Asked once,
		// against every enabled policy, rather than per row: if no policy grants
		// your role bypass then no row can, and the filter is exact.
		mayBypassSomewhere, err := h.roleBypassesAnyPolicy(ctx, cr)
		if err != nil {
			return err
		}
		if !mayBypassSomewhere {
			q = q.Where(approvalrequest.AuthorNEQ(actor))
		}
	}
	if wantNamespace != "" {
		q = q.Where(payloadNamespaceEQ(wantNamespace))
	}
	if len(wantOrbIDs) > 0 {
		q = q.Where(payloadTouchesAnyOrbID(wantOrbIDs))
	}

	rows, err := q.Order(ent.Desc(approvalrequest.FieldCreatedAt)).All(ctx)
	if err != nil {
		return fmt.Errorf("list change requests: %w", err)
	}

	items := make([]changeRequestResponse, 0, len(rows))
	for _, row := range rows {
		view, err := h.render(ctx, row, actor, cr)
		if err != nil {
			return err
		}
		// The only filters left are the ones that need a rendered view, because
		// they depend on derived state SQL cannot see.
		if wantStatus == approval.StatusOpen || wantStatus == approval.StatusApproved {
			if view.Status != wantStatus {
				continue
			}
		}
		// "Awaiting MY review" is exactly "approve is one of the actions I can
		// take" — the same verdict the buttons render from, so the badge count
		// and the button state cannot disagree.
		if awaiting && !containsStr(view.AvailableActions, "approve") {
			continue
		}
		items = append(items, view)
	}
	return c.JSON(http.StatusOK, changeRequestListResponse{Total: len(items), Items: items})
}

// GetChangeRequest returns one change request.
//
// @Summary     Get a change request
// @Tags        change-requests
// @Produce     json
// @Param       id path string true "Change request ID"
// @Success     200 {object} changeRequestResponse
// @Failure     404 {object} errorResponse
// @Router      /api/v1/change-requests/{id} [get]
func (h *ChangeRequest) GetChangeRequest(c echo.Context) error {
	cr, err := h.load(c)
	if err != nil {
		return err
	}
	return h.renderOne(c, cr, http.StatusOK)
}

// GetChangeRequestDiff returns the content diff between current intent and the
// request's target end-state.
//
// @Summary     Diff a change request against current intent
// @Description Flat list of changed entities — one entry per orbId, never a nested tree. Recomputed against live intent on every call, so it already reflects anything that moved since the request was opened.
// @Tags        change-requests
// @Produce     json
// @Param       id path string true "Change request ID"
// @Success     200 {object} changeRequestDiffResponse
// @Failure     404 {object} errorResponse
// @Router      /api/v1/change-requests/{id}/diff [get]
func (h *ChangeRequest) GetChangeRequestDiff(c echo.Context) error {
	cr, err := h.load(c)
	if err != nil {
		return err
	}
	st, err := h.StateWithSnapshot(c.Request().Context(), cr)
	if err != nil {
		return err
	}

	target := applyChangesetTo(st.Snapshot, st.Changeset)
	res := graphdiff.Compare(st.Snapshot, target)
	return c.JSON(http.StatusOK, changeRequestDiffResponse{
		Stale:       st.Stale,
		ContentHash: st.CurrentHash,
		BaseHash:    cr.BaseHash,
		Summary:     res.Summary,
		Changes:     res.Changes,
	})
}

// AmendChangeRequest patches an open change request.
//
// @Summary     Amend an open change request
// @Description Changing the changeset re-captures the intent it is written against, which stops the existing approvals from counting.
// @Tags        change-requests
// @Accept      json
// @Produce     json
// @Param       id path string true "Change request ID"
// @Param       body body amendChangeRequestBody true "Fields to change"
// @Success     200 {object} changeRequestResponse
// @Failure     400 {object} validationErrorResponse
// @Failure     403 {object} errorResponse
// @Failure     409 {object} errorResponse
// @Router      /api/v1/change-requests/{id} [patch]
func (h *ChangeRequest) AmendChangeRequest(c echo.Context) error {
	id, err := h.parseCRID(c)
	if err != nil {
		return err
	}
	var body amendChangeRequestBody
	if err := c.Bind(&body); err != nil {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "invalid request body", "")
	}

	var cs *approval.Changeset
	if len(body.Changes) > 0 {
		if body.Namespace == "" {
			return writeError(c, http.StatusBadRequest, CodeBadUserInput,
				"namespace is required when changing the changeset", "")
		}
		cs = &approval.Changeset{Namespace: body.Namespace, Changes: toChangeItems(body.Changes)}
	}

	caller := resolveCallerRole(c, h.db)
	cr, problems, err := h.Amend(c.Request().Context(), id, actorFromContext(c), caller.Role,
		body.Title, body.Description, cs)
	if err != nil {
		return crError(c, err)
	}
	if len(problems) > 0 {
		return writeChangesetProblems(c, problems)
	}
	return h.renderOne(c, cr, http.StatusOK)
}

// ApproveChangeRequest records an approval.
//
// @Summary     Approve a change request
// @Description Stamped with the intent it was cast against. Approving a stale request is how you re-review after the base moved.
// @Tags        change-requests
// @Accept      json
// @Produce     json
// @Param       id path string true "Change request ID"
// @Param       body body decisionBody false "Reviewer comment"
// @Success     200 {object} changeRequestResponse
// @Failure     403 {object} errorResponse
// @Failure     409 {object} errorResponse
// @Router      /api/v1/change-requests/{id}/approve [post]
func (h *ChangeRequest) ApproveChangeRequest(c echo.Context) error {
	return h.decideHTTP(c, entapproval.DecisionApproved)
}

// RejectChangeRequest records a rejection, which is terminal.
//
// @Summary     Reject a change request
// @Tags        change-requests
// @Accept      json
// @Produce     json
// @Param       id path string true "Change request ID"
// @Param       body body decisionBody false "Reviewer comment"
// @Success     200 {object} changeRequestResponse
// @Failure     403 {object} errorResponse
// @Failure     409 {object} errorResponse
// @Router      /api/v1/change-requests/{id}/reject [post]
func (h *ChangeRequest) RejectChangeRequest(c echo.Context) error {
	return h.decideHTTP(c, entapproval.DecisionRejected)
}

func (h *ChangeRequest) decideHTTP(c echo.Context, decision entapproval.Decision) error {
	id, err := h.parseCRID(c)
	if err != nil {
		return err
	}
	var body decisionBody
	_ = c.Bind(&body) // comment is optional; a missing body is not an error

	caller := resolveCallerRole(c, h.db)
	actor := actorFromContext(c)

	var cr *ent.ApprovalRequest
	if decision == entapproval.DecisionApproved {
		cr, err = h.Approve(c.Request().Context(), id, actor, caller.Role, body.Comment)
	} else {
		cr, err = h.Reject(c.Request().Context(), id, actor, caller.Role, body.Comment)
	}
	if err != nil {
		return crError(c, err)
	}
	return h.renderOne(c, cr, http.StatusOK)
}

// MergeChangeRequest applies an approved change request to the graph.
//
// @Summary     Merge a change request
// @Description MVCC-guarded. Items apply one at a time; a partial merge leaves the request open with a recorded attempt, and the remainder is re-mergeable without re-approval unless someone else wrote to a covered entity.
// @Tags        change-requests
// @Produce     json
// @Param       id path string true "Change request ID"
// @Success     200 {object} changeRequestResponse
// @Failure     403 {object} errorResponse
// @Failure     409 {object} errorResponse
// @Router      /api/v1/change-requests/{id}/merge [post]
func (h *ChangeRequest) MergeChangeRequest(c echo.Context) error {
	id, err := h.parseCRID(c)
	if err != nil {
		return err
	}
	caller := resolveCallerRole(c, h.db)
	cr, err := h.Merge(c.Request().Context(), id, actorFromContext(c), caller.Role, caller.NoAuthz)
	if err != nil {
		return crError(c, err)
	}
	return h.renderOne(c, cr, http.StatusOK)
}

// CloseChangeRequest withdraws a change request.
//
// @Summary     Close a change request
// @Tags        change-requests
// @Produce     json
// @Param       id path string true "Change request ID"
// @Success     200 {object} changeRequestResponse
// @Failure     403 {object} errorResponse
// @Failure     409 {object} errorResponse
// @Router      /api/v1/change-requests/{id}/close [post]
func (h *ChangeRequest) CloseChangeRequest(c echo.Context) error {
	id, err := h.parseCRID(c)
	if err != nil {
		return err
	}
	caller := resolveCallerRole(c, h.db)
	cr, err := h.Close(c.Request().Context(), id, actorFromContext(c), caller.Role)
	if err != nil {
		return crError(c, err)
	}
	return h.renderOne(c, cr, http.StatusOK)
}

// ── Approval policies ───────────────────────────────────────────────────────

// ListApprovalPolicies lists protected classes.
//
// @Summary     List approval policies
// @Tags        approval-policies
// @Produce     json
// @Success     200 {array} approvalPolicyResponse
// @Router      /api/v1/approval-policies [get]
func (h *ChangeRequest) ListApprovalPolicies(c echo.Context) error {
	rows, err := h.db.ApprovalPolicy.Query().
		Order(ent.Asc(approvalpolicy.FieldNamespace)).
		All(c.Request().Context())
	if err != nil {
		return fmt.Errorf("list approval policies: %w", err)
	}
	out := make([]approvalPolicyResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, renderPolicy(p))
	}
	return c.JSON(http.StatusOK, out)
}

// CreateApprovalPolicy declares a protected class.
//
// @Summary     Create an approval policy
// @Description Opt-in: with no enabled policy, writes behave exactly as they do today.
// @Description
// @Description One policy per namespace. Scope is an either/or: send allTypes:true with no
// @Description types (covers every type, including ones added to the schema later), or
// @Description allTypes:false with a non-empty types list. The other two combinations are
// @Description refused with 400 — a policy that says both, or says nothing, cannot answer
// @Description "what does this protect?". A second policy for the same namespace is 409;
// @Description PATCH the existing one to change which types it covers.
// @Tags        approval-policies
// @Accept      json
// @Produce     json
// @Param       body body approvalPolicyBody true "Policy"
// @Success     201 {object} approvalPolicyResponse
// @Failure     400 {object} errorResponse
// @Failure     409 {object} errorResponse
// @Router      /api/v1/approval-policies [post]
func (h *ChangeRequest) CreateApprovalPolicy(c echo.Context) error {
	var body approvalPolicyBody
	if err := c.Bind(&body); err != nil {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "invalid request body", "")
	}
	if strings.TrimSpace(body.Namespace) == "" {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "namespace is required", "")
	}
	// The default is the whole namespace, so a body that says nothing about
	// scope gets allTypes. Sending only a type list is read as meaning it,
	// rather than refused for omitting a field the caller clearly implied.
	allTypes := body.AllTypes == nil || *body.AllTypes
	if len(body.Types) > 0 && body.AllTypes == nil {
		allTypes = false
	}
	// Store [] rather than JSON null: null is a scalar, and a scalar is not an
	// empty list to anything that asks the column for its length.
	if body.Types == nil {
		body.Types = []string{}
	}
	if err := h.validatePolicyScope(c.Request().Context(), body.Namespace, allTypes, body.Types); err != nil {
		var gerr *gatedError
		if errors.As(err, &gerr) {
			return writeError(c, gerr.Status, gerr.Code, gerr.Message, gerr.Hint)
		}
		return err
	}

	create := h.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(body.Namespace).
		SetAllTypes(allTypes).
		SetTypes(body.Types).
		SetCreatedBy(actorFromContext(c))
	if body.RequiredApprovals > 0 {
		create = create.SetRequiredApprovals(body.RequiredApprovals)
	}
	// nil means "not supplied, use the default"; an empty slice means "nobody
	// bypasses, including admins" — a deliberate and meaningful choice. Testing
	// len() > 0 collapses the two and silently restores the admin bypass an
	// operator just removed, which is a false assurance in the opposite
	// direction from an unenforced policy: the control looks stricter than it is.
	if body.BypassRoles != nil {
		create = create.SetBypassRoles(body.BypassRoles)
	}
	if body.Enabled != nil {
		create = create.SetEnabled(*body.Enabled)
	}

	p, err := create.Save(c.Request().Context())
	if err != nil {
		// Two different constraints can fire here and ent reports both as a
		// constraint error. Reporting the CHECK as "a policy already covers that
		// namespace" would send an operator to look for a policy that isn't
		// there, so the scope rule is named separately.
		if isScopeCheckViolation(err) {
			return writeError(c, http.StatusBadRequest, CodeBadUserInput,
				"a policy covers either all types or a list of types, never both and never neither",
				"Send allTypes:true with no types, or allTypes:false with the types to protect.")
		}
		if ent.IsConstraintError(err) {
			return writeError(c, http.StatusConflict, CodeConflict,
				"a policy already covers that namespace",
				"There is one policy per namespace — PATCH it to change which types it protects")
		}
		return fmt.Errorf("create approval policy: %w", err)
	}
	// Written only after Save succeeds. A refused policy leaves NO audit trail —
	// a record of something that never took effect is worse than none, because
	// whoever reads it believes the gate changed.
	h.auditPolicy(c, "createApprovalPolicy", p.Namespace, map[string]any{
		"policyId": p.ID.String(),
		"after":    policyFields(p),
	})
	return c.JSON(http.StatusCreated, renderPolicy(p))
}

// UpdateApprovalPolicy changes a protected class.
//
// @Summary     Update an approval policy
// @Tags        approval-policies
// @Accept      json
// @Produce     json
// @Param       id path string true "Policy ID"
// @Param       body body approvalPolicyBody true "Fields to change"
// @Success     200 {object} approvalPolicyResponse
// @Failure     404 {object} errorResponse
// @Router      /api/v1/approval-policies/{id} [patch]
func (h *ChangeRequest) UpdateApprovalPolicy(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "invalid policy id", "")
	}
	var body approvalPolicyBody
	if err := c.Bind(&body); err != nil {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "invalid request body", "")
	}

	// Read the row BEFORE changing it. "required_approvals is now 1" does not
	// answer "was the bar lowered?", and that is the question an audit of a
	// change-control system exists to answer.
	prev, err := h.db.ApprovalPolicy.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return writeError(c, http.StatusNotFound, CodeNotFound, "approval policy not found", "")
	}
	if err != nil {
		return fmt.Errorf("load approval policy: %w", err)
	}

	upd := h.db.ApprovalPolicy.UpdateOneID(id).
		SetUpdatedAt(time.Now()).
		SetUpdatedBy(actorFromContext(c))
	if body.RequiredApprovals > 0 {
		upd = upd.SetRequiredApprovals(body.RequiredApprovals)
	}
	// Scope is edited in place — "also protect Rack" is a change to this policy,
	// not a new one. Supplying either field means supplying the scope, so both
	// are validated together.
	if body.AllTypes != nil || body.Types != nil {
		allTypes := body.AllTypes != nil && *body.AllTypes
		if body.Types == nil {
			body.Types = []string{}
		}
		if err := h.validatePolicyScope(c.Request().Context(), "", allTypes, body.Types); err != nil {
			var gerr *gatedError
			if errors.As(err, &gerr) {
				return writeError(c, gerr.Status, gerr.Code, gerr.Message, gerr.Hint)
			}
			return err
		}
		upd = upd.SetAllTypes(allTypes).SetTypes(body.Types)
	}
	if body.BypassRoles != nil {
		upd = upd.SetBypassRoles(body.BypassRoles)
	}
	if body.Enabled != nil {
		upd = upd.SetEnabled(*body.Enabled)
	}

	p, err := upd.Save(c.Request().Context())
	if ent.IsNotFound(err) {
		return writeError(c, http.StatusNotFound, CodeNotFound, "approval policy not found", "")
	}
	if err != nil {
		return fmt.Errorf("update approval policy: %w", err)
	}
	h.auditPolicy(c, "updateApprovalPolicy", p.Namespace, map[string]any{
		"policyId": p.ID.String(),
		"before":   policyFields(prev),
		"after":    policyFields(p),
		// Called out separately because it is the one change that stops the gate
		// applying at all, and nobody scanning a diff of five fields should have
		// to spot it.
		"enforcementStopped": prev.Enabled && !p.Enabled,
		"enforcementStarted": !prev.Enabled && p.Enabled,
	})
	return c.JSON(http.StatusOK, renderPolicy(p))
}

// DeleteApprovalPolicy removes a protected class.
//
// @Summary     Delete an approval policy
// @Tags        approval-policies
// @Produce     json
// @Param       id path string true "Policy ID"
// @Success     204
// @Failure     404 {object} errorResponse
// @Router      /api/v1/approval-policies/{id} [delete]
func (h *ChangeRequest) DeleteApprovalPolicy(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "invalid policy id", "")
	}
	// Read it first: after the row is gone the audit event is the ONLY record
	// that the policy ever existed, so it has to carry enough to reconstruct it.
	prev, err := h.db.ApprovalPolicy.Get(c.Request().Context(), id)
	if ent.IsNotFound(err) {
		return writeError(c, http.StatusNotFound, CodeNotFound, "approval policy not found", "")
	}
	if err != nil {
		return fmt.Errorf("load approval policy: %w", err)
	}
	if err := h.db.ApprovalPolicy.DeleteOneID(id).Exec(c.Request().Context()); err != nil {
		if ent.IsNotFound(err) {
			return writeError(c, http.StatusNotFound, CodeNotFound, "approval policy not found", "")
		}
		return fmt.Errorf("delete approval policy: %w", err)
	}
	h.auditPolicy(c, "deleteApprovalPolicy", prev.Namespace, map[string]any{
		"policyId": prev.ID.String(),
		"before":   policyFields(prev),
	})
	return c.NoContent(http.StatusNoContent)
}

// auditPolicy records a change to a protected class.
//
// Policy administration is the MOST consequential act in change control — it
// decides what needs review at all — and until now it was the only part of the
// feature that left no trace. A bypassed write was audited; removing the policy
// that would have gated it was not, which is backwards.
//
// The namespace is attached as the resource so `?resource_id=<namespace>`
// surfaces every policy change for it. Category "management", matching
// updateUserRole — the closest analogue, an admin changing an
// authorization-relevant setting.
func (h *ChangeRequest) auditPolicy(c echo.Context, action, namespace string, details map[string]any) {
	details["namespace"] = namespace
	writeAuditEvent(h.db, h.logger, "management", actorFromContext(c), action,
		[]string{action},
		[]string{"ApprovalPolicy"},
		[]string{namespace},
		details,
	)
}

// policyFields is the audit-facing shape of a policy: everything that decides
// what it governs and who escapes it. Deliberately not renderPolicy — that one
// is a wire contract for clients and will change for presentation reasons; this
// one is a historical record and must not.
func policyFields(p *ent.ApprovalPolicy) map[string]any {
	types := p.Types
	if types == nil {
		types = []string{}
	}
	roles := p.BypassRoles
	if roles == nil {
		roles = []string{}
	}
	return map[string]any{
		"actionType":        p.ActionType,
		"namespace":         p.Namespace,
		"allTypes":          p.AllTypes,
		"types":             types,
		"requiredApprovals": p.RequiredApprovals,
		"bypassRoles":       roles,
		"enabled":           p.Enabled,
	}
}

// ResolveApprovalPolicy answers "is this change gated for me?".
//
// @Summary     Resolve the policy for a namespace and type
// @Description Lets a client label its save button Save or Propose without re-implementing policy resolution.
// @Tags        approval-policies
// @Produce     json
// @Param       namespace query string true "Namespace"
// @Param       type query string false "ConfigItem type"
// @Success     200 {object} approvalPolicyResolveResponse
// @Failure     400 {object} errorResponse
// @Router      /api/v1/approval-policies/resolve [get]
func (h *ChangeRequest) ResolveApprovalPolicy(c echo.Context) error {
	ns := c.QueryParam("namespace")
	if ns == "" {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput, "namespace is required", "")
	}
	cs := &approval.Changeset{Namespace: ns}
	if t := c.QueryParam("type"); t != "" {
		cs.Changes = []approval.ChangeItem{{Type: t}}
	}
	pol, err := h.resolvePolicy(c.Request().Context(), approval.ActionTypeConfigMutation, cs)
	if err != nil {
		return err
	}
	caller := resolveCallerRole(c, h.db)
	roles := pol.bypassRoles
	if roles == nil {
		roles = []string{} // a JSON null would read as "unknown" rather than "none"
	}
	resp := approvalPolicyResolveResponse{
		Required:          pol.required > 0,
		RequiredApprovals: pol.required,
		BypassRoles:       roles,
		CallerMayBypass:   caller.NoAuthz || roleIn(caller.Role, pol.bypassRoles),
	}
	return c.JSON(http.StatusOK, resp)
}

// ── Rendering ───────────────────────────────────────────────────────────────

func (h *ChangeRequest) load(c echo.Context) (*ent.ApprovalRequest, error) {
	id, err := h.parseCRID(c)
	if err != nil {
		return nil, err
	}
	cr, err := h.Get(c.Request().Context(), id)
	if errors.Is(err, errCRNotFound) {
		return nil, echo.NewHTTPError(http.StatusNotFound, "change request not found")
	}
	return cr, err
}

func (h *ChangeRequest) renderOne(c echo.Context, cr *ent.ApprovalRequest, status int) error {
	view, err := h.render(c.Request().Context(), cr, actorFromContext(c), resolveCallerRole(c, h.db))
	if err != nil {
		return err
	}
	return c.JSON(status, view)
}

func (h *ChangeRequest) render(ctx context.Context, cr *ent.ApprovalRequest, actor string, caller callerRole) (changeRequestResponse, error) {
	st, err := h.State(ctx, cr)
	if err != nil {
		return changeRequestResponse{}, err
	}

	out := changeRequestResponse{
		ID:               crHumanID(cr),
		ActionType:       cr.ActionType,
		Title:            cr.Title,
		Description:      cr.Description,
		Status:           st.Status,
		Namespace:        st.Changeset.Namespace,
		Author:           cr.Author,
		Stale:            st.Stale,
		Approvals:        st.Valid,
		Required:         st.Required,
		AvailableActions: availableActions(cr, st, actor, caller.Role, caller.NoAuthz),
		MissingTargets:   st.Missing,
		Changes:          fromChangeItems(st.Changeset.Changes),
		CreatedAt:        cr.CreatedAt,
		UpdatedAt:        cr.UpdatedAt,
		ExecutedAt:       cr.ExecutedAt,
		ExecutedBy:       cr.ExecutedBy,
	}
	if out.AvailableActions == nil {
		out.AvailableActions = []string{}
	}
	terminal := cr.Status != approvalrequest.StatusOpen
	for _, a := range st.Approvals {
		out.Reviews = append(out.Reviews, approvalResponse{
			Approver: a.Approver,
			Decision: string(a.Decision),
			Comment:  a.Comment,
			At:       a.CreatedAt,
			// On a terminal request every decision is a historical fact, not a
			// claim about the current state.
			Current: terminal || a.ApprovedAtHash == st.CurrentHash,
		})
	}
	for _, m := range cr.Edges.MergeAttempts {
		out.MergeAttempts = append(out.MergeAttempts, mergeAttemptResult{
			AttemptedBy: m.AttemptedBy,
			AttemptedAt: m.AttemptedAt,
			Error:       m.Error,
			Results:     decodeItemResults(m.Results),
		})
	}
	sort.Slice(out.MergeAttempts, func(i, j int) bool {
		return out.MergeAttempts[i].AttemptedAt.Before(out.MergeAttempts[j].AttemptedAt)
	})
	return out, nil
}

func renderPolicy(p *ent.ApprovalPolicy) approvalPolicyResponse {
	roles := p.BypassRoles
	if roles == nil {
		roles = []string{}
	}
	out := approvalPolicyResponse{
		ID:                p.ID.String(),
		ActionType:        p.ActionType,
		Namespace:         p.Namespace,
		AllTypes:          p.AllTypes,
		Types:             p.Types,
		RequiredApprovals: p.RequiredApprovals,
		BypassRoles:       roles,
		Enabled:           p.Enabled,
	}
	return out
}

// crError maps the engine's sentinel errors to orbital's error envelope. One
// place, so a new call site cannot invent a status or a code.
func crError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, errCRNotFound):
		return writeError(c, http.StatusNotFound, CodeNotFound, "change request not found", "")
	case errors.Is(err, errCRStale):
		return writeError(c, http.StatusConflict, CodeMVCCConflict, err.Error(),
			"Review the recomputed diff, approve again, then merge.")
	case errors.Is(err, errCRTargetMissing):
		return writeError(c, http.StatusConflict, CodeTargetMissing, err.Error(),
			"Close this request, PATCH it to drop that item, or recreate the entity and re-review.")
	case errors.Is(err, errCRSelfApproval):
		return writeError(c, http.StatusForbidden, CodeForbidden, err.Error(),
			"Ask a different reviewer to approve it.")
	case errors.Is(err, errCRForbidden):
		return writeError(c, http.StatusForbidden, CodeForbidden, err.Error(), "")
	case errors.Is(err, errCRNotOpen), errors.Is(err, errCRNotApproved):
		return writeError(c, http.StatusConflict, CodeConflict, err.Error(), "")
	}
	return err
}

// parseCRID resolves the `:id` path parameter to a row id.
//
// The parameter is the HUMAN identifier — `colo-42` — not the surrogate key.
// Callers never see the bigint, and that is deliberate: the id in a URL is the
// one people paste into chat, and it should say which data center it is about.
//
// Split on the LAST hyphen. Namespaces contain hyphens (`alaska-dot-cruiser`),
// but the number is always the final segment and always digits, so the split is
// unambiguous — `alaska-dot-cruiser-42` and even `dc-2-42` resolve correctly.
func (h *ChangeRequest) parseCRID(c echo.Context) (int64, error) {
	raw := c.Param("id")
	ns, num, ok := splitCRID(raw)
	if !ok {
		return 0, handled(writeError(c, http.StatusBadRequest, CodeBadUserInput,
			fmt.Sprintf("%q is not a change request id", raw),
			"Change request ids look like colo-42 — the namespace, then its number."))
	}
	cr, err := h.db.ApprovalRequest.Query().
		Where(approvalrequest.NamespaceEQ(ns), approvalrequest.NumberEQ(num)).
		Only(c.Request().Context())
	if ent.IsNotFound(err) {
		return 0, handled(writeError(c, http.StatusNotFound, CodeNotFound, "change request not found", ""))
	}
	if err != nil {
		return 0, fmt.Errorf("resolve change request %q: %w", raw, err)
	}
	return cr.ID, nil
}

// errResponseWritten marks an error whose response is already on the wire.
//
// writeError RETURNS NIL on success — it writes the envelope and reports that
// the write worked. That is fine when a handler does `return writeError(...)`,
// but in a helper returning (value, error) it means the caller's `if err != nil`
// does not fire and execution continues with a zero value. The previous
// parseCRID had exactly this shape and got away with it only because
// Get(uuid.Nil) happened to 404 afterwards — a second response the committed-
// response guard then swallowed.
//
// ErrorHandler no-ops on an already-committed response, so returning this
// upward is safe and the caller's error check behaves as written.
var errResponseWritten = errors.New("response already written")

func handled(writeErr error) error {
	if writeErr != nil {
		return writeErr // the write itself failed; that is a real error
	}
	return errResponseWritten
}

// splitCRID parses "<namespace>-<number>".
func splitCRID(raw string) (namespace string, number int, ok bool) {
	i := strings.LastIndex(raw, "-")
	if i <= 0 || i == len(raw)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(raw[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return raw[:i], n, true
}

// crHumanID renders the identifier people use. Kept next to the parser so the
// two can never drift into disagreeing about the format.
func crHumanID(cr *ent.ApprovalRequest) string {
	return fmt.Sprintf("%s-%d", cr.Namespace, cr.Number)
}

func toChangeItems(in []changeItemBody) []approval.ChangeItem {
	out := make([]approval.ChangeItem, 0, len(in))
	for _, b := range in {
		out = append(out, approval.ChangeItem{
			OrbID: b.OrbID,
			Type:  b.Type,
			Op:    approval.Op(b.Op),
			Set:   b.Set,
			Clear: b.Clear,
		})
	}
	return out
}

func fromChangeItems(in []approval.ChangeItem) []changeItemBody {
	out := make([]changeItemBody, 0, len(in))
	for _, ch := range in {
		out = append(out, changeItemBody{
			OrbID: ch.OrbID,
			Type:  ch.Type,
			Op:    string(ch.Op),
			Set:   ch.Set,
			Clear: ch.Clear,
		})
	}
	return out
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// decodeItemResults reads a merge attempt's per-item outcomes. A malformed blob
// renders as no items rather than failing the whole response — the attempt's
// top-level error is the part an operator acts on.
func decodeItemResults(raw json.RawMessage) []mergeItemResult {
	if len(raw) == 0 {
		return nil
	}
	var items []approval.ItemResult
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]mergeItemResult, 0, len(items))
	for _, r := range items {
		out = append(out, mergeItemResult{OrbID: r.OrbID, Applied: r.Applied, Error: r.Error})
	}
	return out
}

// writeChangesetProblems renders a rejected changeset. Separate from
// writeError because the envelope carries a LIST — a changeset can be wrong in
// several places at once and a single `error` string would hide all but one.
func writeChangesetProblems(c echo.Context, problems []approval.ValidationError) error {
	out := make([]changesetProblem, 0, len(problems))
	for _, p := range problems {
		out = append(out, changesetProblem{
			Index: p.Index, OrbID: p.OrbID, Field: p.Field, Msg: p.Msg, Hint: p.Hint,
		})
	}
	return c.JSON(http.StatusBadRequest, validationErrorResponse{
		Error:      "changeset is not valid",
		Code:       CodeBadUserInput,
		HTTPStatus: http.StatusBadRequest,
		Problems:   out,
	})
}

// isScopeCheckViolation reports whether err is the database refusing a policy
// whose scope says both "all types" and "these types" (or neither).
//
// Matched on the constraint name because that is the only part of a Postgres
// check violation that is stable — the message text is not.
func isScopeCheckViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "approval_policy_scope_exclusive")
}

// validatePolicyScope refuses a policy that could never govern anything, and
// the two scope shapes that contradict themselves.
//
// The database enforces the either/or too, via a CHECK constraint — that layer
// is the one no future code path can skip. This one exists to say WHICH rule was
// broken, which a constraint violation cannot.
//
// namespace may be empty to skip the namespace check (an update supplying only
// a new scope).
func (h *ChangeRequest) validatePolicyScope(ctx context.Context, namespace string, allTypes bool, types []string) error {
	// Both invalid shapes. Together with the two valid ones these make the pair
	// a proper either/or with no third state — and refusing beats "ignoring the
	// unused field", which leaves a stored row that says two things and a reader
	// who cannot tell which one was honoured.
	switch {
	case allTypes && len(types) > 0:
		return &gatedError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadUserInput,
			Message: "a policy covering all types must not also list types — the two say different things and the row would not describe what is protected",
			Hint:    "Send allTypes:true with no types, or allTypes:false with the types to protect.",
		}
	case !allTypes && len(types) == 0:
		return &gatedError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadUserInput,
			Message: "a policy must protect something: either allTypes:true, or a non-empty list of types",
			Hint:    "Send allTypes:true to cover every type in the namespace, including ones added later.",
		}
	}

	for _, t := range types {
		if _, ok := configitems.FindByName(t); !ok {
			return &gatedError{
				Status:  http.StatusBadRequest,
				Code:    CodeBadUserInput,
				Message: fmt.Sprintf("%q is not a ConfigItem type, so a policy naming it would govern nothing", t),
				Hint:    "Valid types: " + strings.Join(configitems.Names(), ", "),
			}
		}
	}

	if namespace == "" {
		return nil
	}
	exists, err := h.schema.NamespaceExists(ctx, namespace)
	if err != nil {
		return fmt.Errorf("validate policy namespace: %w", err)
	}
	if !exists {
		return &gatedError{
			Status:  http.StatusBadRequest,
			Code:    CodeBadUserInput,
			Message: fmt.Sprintf("namespace %q holds no configuration items, so a policy for it would report itself active while gating nothing", namespace),
			Hint:    "Check the spelling against the namespaces that exist — the UI offers them as a list.",
		}
	}
	return nil
}
