package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/armada/orbital/ent"
	entapproval "github.com/armada/orbital/ent/approval"
	"github.com/armada/orbital/ent/approvalpolicy"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/internal/approval"
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
	// TypeName narrows the policy to one ConfigItem type. Empty means every type.
	TypeName string `json:"type,omitempty" example:"Server"`
	// RequiredApprovals is how many distinct reviewers must approve. Default 1.
	RequiredApprovals int `json:"requiredApprovals,omitempty" example:"1"`
	// BypassRoles may write this class directly, recorded as a privileged write.
	// Defaults to ["admin"]. Bypass is a property of the policy, not of a user.
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
	ID          string `json:"id" example:"4b1f0f7a-6f0c-4a1e-9a2e-2f1c7a0b1234"`
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
	TypeName          string   `json:"type,omitempty" example:"Server"`
	RequiredApprovals int      `json:"requiredApprovals" example:"1"`
	BypassRoles       []string `json:"bypassRoles"`
	// Enabled is the admin's switch: turn a policy off without deleting it.
	Enabled bool `json:"enabled" example:"true"`
	// Enforced reports whether orbital's write path actually REFUSES a
	// mutation this policy covers. Distinct from Enabled, which is only what
	// the admin asked for. Both must be true for the class to be protected.
	Enforced bool `json:"enforced" example:"false"`
	// Notice explains a policy that is recorded but not yet enforced. Absent
	// once enforcement is live.
	Notice string `json:"notice,omitempty"`
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
	// Enforced reports whether a direct mutation would actually be refused.
	// A client labelling its save button should read Required to decide what to
	// OFFER, and Enforced to know whether saving directly would still succeed.
	Enforced bool `json:"enforced" example:"false"`
	// Notice explains a policy that is recorded but not yet enforced. Absent
	// once enforcement is live.
	Notice string `json:"notice,omitempty"`
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
// @Tags        change-requests
// @Produce     json
// @Param       status query string false "open, approved, active (open+approved), rejected, merged or closed"
// @Param       namespace query string false "Namespace"
// @Param       author query string false "Author email"
// @Param       mine query boolean false "Only requests this caller authored"
// @Param       awaiting_review query boolean false "Only requests this caller can still review"
// @Param       orbId query string false "Only requests touching this entity"
// @Success     200 {object} changeRequestListResponse
// @Router      /api/v1/change-requests [get]
func (h *ChangeRequest) ListChangeRequests(c echo.Context) error {
	ctx := c.Request().Context()
	actor := actorFromContext(c)
	cr := resolveCallerRole(c, h.db)

	wantStatus := c.QueryParam("status")
	wantNamespace := c.QueryParam("namespace")
	wantOrbID := c.QueryParam("orbId")
	awaiting := c.QueryParam("awaiting_review") == "true"

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
	}
	if wantNamespace != "" {
		q = q.Where(payloadNamespaceEQ(wantNamespace))
	}
	if wantOrbID != "" {
		q = q.Where(payloadTouchesOrbID(wantOrbID))
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
	st, err := h.State(c.Request().Context(), cr)
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
	id, err := parseCRID(c)
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
	id, err := parseCRID(c)
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
	id, err := parseCRID(c)
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
	id, err := parseCRID(c)
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
		Order(ent.Asc(approvalpolicy.FieldNamespace), ent.Asc(approvalpolicy.FieldTypeName)).
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

	create := h.db.ApprovalPolicy.Create().
		SetActionType(approval.ActionTypeConfigMutation).
		SetNamespace(body.Namespace).
		SetTypeName(body.TypeName).
		SetCreatedBy(actorFromContext(c))
	if body.RequiredApprovals > 0 {
		create = create.SetRequiredApprovals(body.RequiredApprovals)
	}
	if len(body.BypassRoles) > 0 {
		create = create.SetBypassRoles(body.BypassRoles)
	}
	if body.Enabled != nil {
		create = create.SetEnabled(*body.Enabled)
	}

	p, err := create.Save(c.Request().Context())
	if err != nil {
		if ent.IsConstraintError(err) {
			return writeError(c, http.StatusConflict, CodeConflict,
				"a policy already covers that namespace and type",
				"PATCH the existing policy instead of creating a second one")
		}
		return fmt.Errorf("create approval policy: %w", err)
	}
	h.warnIfUnenforced(c, p)
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

	upd := h.db.ApprovalPolicy.UpdateOneID(id).
		SetUpdatedAt(time.Now()).
		SetUpdatedBy(actorFromContext(c))
	if body.RequiredApprovals > 0 {
		upd = upd.SetRequiredApprovals(body.RequiredApprovals)
	}
	if len(body.BypassRoles) > 0 {
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
	h.warnIfUnenforced(c, p)
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
	err = h.db.ApprovalPolicy.DeleteOneID(id).Exec(c.Request().Context())
	if ent.IsNotFound(err) {
		return writeError(c, http.StatusNotFound, CodeNotFound, "approval policy not found", "")
	}
	if err != nil {
		return fmt.Errorf("delete approval policy: %w", err)
	}
	return c.NoContent(http.StatusNoContent)
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
		Enforced:          approvalGateInstalled && pol.required > 0,
	}
	if resp.Required {
		resp.Notice = enforcementNotice()
	}
	return c.JSON(http.StatusOK, resp)
}

// ── Rendering ───────────────────────────────────────────────────────────────

func (h *ChangeRequest) load(c echo.Context) (*ent.ApprovalRequest, error) {
	id, err := parseCRID(c)
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
		ID:               cr.ID.String(),
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
		TypeName:          p.TypeName,
		RequiredApprovals: p.RequiredApprovals,
		BypassRoles:       roles,
		Enabled:           p.Enabled,
		Enforced:          approvalGateInstalled && p.Enabled,
	}
	if p.Enabled {
		out.Notice = enforcementNotice()
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

func parseCRID(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, writeError(c, http.StatusBadRequest, CodeBadUserInput, "invalid change request id", "")
	}
	return id, nil
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

// warnIfUnenforced logs when an admin declares a protected class that orbital
// will not actually protect. The response says so too, but an admin acting
// through orbctl or a script may never read the body — and "I turned approvals
// on" is exactly the belief that must not go unchallenged.
func (h *ChangeRequest) warnIfUnenforced(c echo.Context, p *ent.ApprovalPolicy) {
	if approvalGateInstalled || !p.Enabled {
		return
	}
	target := p.Namespace
	if p.TypeName != "" {
		target += "/" + p.TypeName
	}
	h.logger.Warn("approval policy recorded but NOT enforced — direct mutations on this class are still accepted",
		"policy", target,
		"actor", actorFromContext(c),
		"request.id", c.Response().Header().Get(echo.HeaderXRequestID))
}

// WarnUnenforcedPolicies logs once at startup if any enabled policy exists
// while the write gate is off. Called from server wiring so an operator who
// inherited a configured deployment learns it from the log, not from a
// mutation that should have been refused and was not.
func (h *ChangeRequest) WarnUnenforcedPolicies(ctx context.Context) {
	if approvalGateInstalled || h.db == nil {
		return
	}
	n, err := h.db.ApprovalPolicy.Query().Where(approvalpolicy.EnabledEQ(true)).Count(ctx)
	if err != nil {
		h.logger.Warn("could not check approval policies at startup", "err", err)
		return
	}
	if n == 0 {
		return
	}
	h.logger.Warn("APPROVAL POLICIES ARE NOT ENFORCED — enabled policies exist, but orbital does not yet refuse a direct mutation on a protected class. Change requests work; nothing forces their use.",
		"enabled_policies", n)
}
