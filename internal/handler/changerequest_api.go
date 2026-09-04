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
	// Title is the one-line summary reviewers see in the queue. Written by the
	// proposer — a diff says what changed, only a person says why. Max 255
	// characters; put longer context in description, which is unbounded.
	Title string `json:"title" validate:"required" maxLength:"255" example:"Enable SSH on the Anchorage iDRACs"`
	// Description is optional free text — why, and anything a reviewer needs.
	Description string `json:"description,omitempty" example:"Requested by field ops for the Nov maintenance window."`
	// Namespace scopes the whole request. Every orbId in changes must be in it.
	Namespace string `json:"namespace" validate:"required" example:"alaska-dot"`
	// Changes is the target end-state, one entry per entity.
	Changes []changeItemBody `json:"changes" validate:"required"`
}

// changeItemBody is one entity's proposed end-state.
// changeEffect is what a changeset would actually DO, at a glance.
//
// It exists because a list view has ONE LINE per request and the changeset is a
// nested array. Every client rendering a queue would otherwise write the same
// walk — flatten the items, count the fields, special-case the single-field
// row — which is the bespoke-client-logic smell the API-first rule names
// explicitly. Computing it here costs nothing: `render` already holds the
// changeset.
//
// It describes the request's SCOPE. Today that is derived from the changeset
// alone, which is the same thing as its effect for anything orbital's editor
// authored — `changedOnly` sends only edited fields. It is NOT the same for a
// client that posts a complete end-state, which the API accepts on purpose so a
// reconcile flow can assert one: that yields a wide scope and a narrow effect.
//
// The contract is worded as scope, not payload, deliberately. Closing the gap
// (compute the effect once at creation and store it beside `base_hash`, the way
// a saved Terraform plan carries its delta and its state anchor together) then
// improves this number without changing what callers were promised. See
// `docs/planning/debt.md`; `GET /{id}/diff` is the live authority meanwhile.
type changeEffect struct {
	// Entities is how many distinct orbIds the changeset touches.
	Entities int `json:"entities" example:"1"`
	// Fields is the total across every item, counting a cleared field once.
	Fields int `json:"fields" example:"1"`
	// OrbID and Type are present whenever the changeset touches exactly ONE
	// entity, whatever its field count — a row saying "3 fields" without saying
	// on what is a worse answer than the count alone.
	OrbID string `json:"orbId,omitempty" example:"colo:server-1W8Y2Z3"`
	Type  string `json:"type,omitempty" example:"Server"`
	// Field, Value and Cleared are present ONLY when Fields == 1 — the case a
	// row can state in full rather than count.
	Field string `json:"field,omitempty" example:"manufacturer"`
	// Before is the value being replaced. Only an EFFECT-derived summary can
	// know it — a payload states what a field becomes, never what it was — so it
	// is absent on rows created before base_effect existed, and a client must
	// render `→ after` when it is missing rather than assume null meant empty.
	Before any `json:"before,omitempty"`
	// Value is the proposed value, omitted when the field is being cleared.
	// Rendered as-is: a scalar for scalars, an object for an edge reference.
	// No `example` tag: swag cannot render one for an `any`, and the type is
	// deliberately open — a scalar for scalars, an object for an edge reference.
	Value any `json:"value,omitempty"`
	// Cleared distinguishes "set to nothing" from "set to a falsy value" —
	// `Value` alone cannot, since `false` and `0` are legitimate values.
	Cleared bool `json:"cleared,omitempty" example:"false"`
}

// summarize counts a changeset and, when exactly one field is in play, names it.
//
// Deletes count as an entity with no fields: `op: delete` carries no `set` or
// `clear`, and reporting "0 fields" for one would read as an empty request.
func effectFromChangeset(cs approval.Changeset) changeEffect {
	out := changeEffect{}
	seen := make(map[string]bool, len(cs.Changes))
	var only *approval.ChangeItem
	var onlyField string
	var onlyCleared bool
	for i := range cs.Changes {
		item := &cs.Changes[i]
		if item.OrbID != "" && !seen[item.OrbID] {
			seen[item.OrbID] = true
			out.Entities++
		}
		for name := range item.Set {
			out.Fields++
			only, onlyField, onlyCleared = item, name, false
		}
		for _, name := range item.Clear {
			out.Fields++
			only, onlyField, onlyCleared = item, name, true
		}
	}
	if out.Entities == 1 {
		for i := range cs.Changes {
			if cs.Changes[i].OrbID != "" {
				out.OrbID = cs.Changes[i].OrbID
				out.Type = cs.Changes[i].Type
				break
			}
		}
	}
	if out.Fields == 1 && only != nil {
		out.Field = onlyField
		out.Cleared = onlyCleared
		if !onlyCleared {
			out.Value = only.Set[onlyField]
		}
	}
	return out
}

// resolveEffect returns the stored effect when the request has one, and falls
// back to deriving one from the changeset.
//
// The fallback is not a degraded path for new rows — it is what every row
// written before `base_effect` existed will always use, and it is exactly what
// those rows have always reported. It also covers a creation where the effect
// could not be computed (see storedEffect): a summary is a convenience, and
// failing to produce one must never cost someone their proposal.
func resolveEffect(stored json.RawMessage, cs approval.Changeset) changeEffect {
	if len(stored) > 0 {
		var out changeEffect
		if err := json.Unmarshal(stored, &out); err == nil {
			return out
		}
		// Unreadable stored value: fall through rather than return zeros. A row
		// reading "0 fields" would be a lie; the scope count is at least true
		// about something.
	}
	return effectFromChangeset(cs)
}

// effectFromDiff turns a computed diff into the same shape
// effectFromChangeset produces, so one response field carries either and a
// client renders one thing.
//
// The counts are FIELD-level, not entity-level: `graphdiff.Summary` counts
// entities added/removed/modified, which answers a different question than a
// queue row asks ("how much of this am I approving").
func effectFromDiff(res *graphdiff.Result) changeEffect {
	out := changeEffect{}
	var only *graphdiff.Change
	var onlyField *graphdiff.FieldChange
	for _, ch := range res.Changes {
		if ch == nil {
			continue
		}
		out.Entities++
		for i := range ch.Fields {
			out.Fields++
			only, onlyField = ch, &ch.Fields[i]
		}
		// An added or removed ENTITY with no field-level detail still counts as
		// something happening — reporting "0 fields" for a delete would read as
		// an empty request.
		if len(ch.Fields) == 0 {
			out.Fields++
			only, onlyField = ch, nil
		}
	}
	if out.Entities == 1 && only != nil {
		out.OrbID = only.OrbID
		out.Type = only.Type
	}
	if out.Fields == 1 && onlyField != nil {
		// graphdiff qualifies field names by type (`Server.hostname`) because a
		// diff spans entities and the name alone would be ambiguous. A summary
		// already carries `type` as its own field, so qualifying again would
		// force every client to strip it — and would make this path disagree
		// with the payload-derived one, which yields a bare name. One shape,
		// whichever path produced it.
		out.Field = strings.TrimPrefix(onlyField.Field, only.Type+".")
		out.Before = onlyField.Before
		out.Value = onlyField.After
		// A field whose new value is nothing is being cleared. Reading it off the
		// diff rather than off the changeset means an `op: update` that happens to
		// null a field is described the same way an explicit `clear` is.
		out.Cleared = onlyField.After == nil
		if out.Cleared {
			out.Value = nil
		}
	}
	return out
}

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
	// IfVersion is the OLD spelling of Version, kept only to detect callers that
	// have not caught up. Renamed 2026-09-04: a precondition naming the node's
	// own field is what Kubernetes, GCP, Firestore, DynamoDB and plain SQL all
	// do; `ifVersion` was an HTTP-header idiom borrowed for a body field. Silence
	// would drop the guard without saying so, so it is refused by name.
	IfVersion *int `json:"ifVersion,omitempty" swaggerignore:"true"`
	// Before is REMOVED as a feature and kept only to detect callers still
	// sending it. Echo's binder ignores unknown fields, so without this a client
	// that still supplies `before` loses its per-field guarantee silently — the
	// failure mode the concurrency work exists to eliminate. Rejected with a 400
	// naming `version` instead. Hidden from the published spec.
	Before json.RawMessage `json:"before,omitempty" swaggerignore:"true"`
	// Version is the entity's `version` as you read it — the same concurrency
	// token `/graphql` mutations accept, meaning the same thing here. Orbital
	// refuses the request with 409 MVCC_CONFLICT if the entity has moved since,
	// naming the item and both versions. One per item, never per field: an
	// entity has one version. Omit for an unconditional item. Supplying it for
	// an entity that does not exist is refused — there is no version to match.
	Version *int `json:"version,omitempty" example:"7"`
}

// amendChangeRequestBody patches an open change request. Omitted fields are
// left alone; supplying changes re-captures the base and therefore invalidates
// the approvals cast against the previous one.
type amendChangeRequestBody struct {
	// Title renames the request. Sending it alone leaves the changeset, the base
	// anchor and every approval untouched — a rename is not a re-proposal.
	// Max 255 characters.
	Title       *string          `json:"title,omitempty" maxLength:"255" example:"Enable SSH on the Anchorage iDRACs"`
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
	// AllNamespaces governs EVERY namespace, including data centers onboarded
	// after the policy was written. Mutually exclusive with Namespace.
	//
	// It is a DEFAULT, not a floor: a namespace with its own policy is governed
	// by that one instead, even when it is weaker, and a namespace whose policy
	// is DISABLED is not gated at all.
	AllNamespaces *bool `json:"allNamespaces,omitempty" example:"false"`
	// Namespace the policy governs. Required unless allNamespaces is true.
	Namespace string `json:"namespace,omitempty" example:"alaska-dot"`
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
	MissingTargets []string `json:"missingTargets,omitempty"`
	// SubtreeChanged means the reviewed scope moved without any change object
	// going out of date — an edit to an owned child. Cleared by approving again;
	// blocks merge on its own. Distinct from Stale, which only the author can
	// clear by rebasing.
	SubtreeChanged bool `json:"subtreeChanged" example:"false"`
	// StaleEntities names WHY this request is stale: the entities whose version
	// moved since it was last reviewed, each with the version reviewed and the
	// version now. Present only when Stale is true, and empty for requests
	// opened before orbital recorded the base version vector.
	//
	// Server-computed on purpose. `stale` alone tells a reader that merge is
	// blocked but not what to look at, and a client that had to work it out
	// would need the base vector, the current vector and the scope-expansion
	// rules — three things orbital already has and no integrator should
	// reimplement.
	StaleEntities []staleEntity `json:"staleEntities,omitempty"`
	// Summary is what a QUEUE ROW needs: how wide this change is, and — when it
	// is a single field — what that field becomes. Derived server-side so a
	// list view never walks `changes` to build a label. Orbital's own queue
	// renders straight from it, and so can anyone else's.
	Effect  changeEffect     `json:"effect"`
	Changes []changeItemBody `json:"changes"`
	// Record is the per-entity account of what this request does, one entry per
	// change object, with each item's applied status folded in. Built from the
	// payload so it stays correct after a merge, when the live diff is empty.
	Record        []changeRecordEntry  `json:"record"`
	Reviews       []approvalResponse   `json:"reviews,omitempty"`
	MergeAttempts []mergeAttemptResult `json:"mergeAttempts,omitempty"`
	CreatedAt     time.Time            `json:"createdAt"`
	UpdatedAt     *time.Time           `json:"updatedAt,omitempty"`
	ExecutedAt    *time.Time           `json:"executedAt,omitempty"`
	ExecutedBy    string               `json:"executedBy,omitempty"`
}

// staleEntity is one entity that moved out from under a request.
type staleEntity struct {
	OrbID string `json:"orbId" example:"alaska-dot:server-4FK8K44"`
	// Reviewed is the version this request was last reviewed against; Current is
	// the version now. Current is omitted when the entity no longer exists.
	Reviewed int  `json:"reviewedVersion" example:"7"`
	Current  *int `json:"currentVersion,omitempty" example:"9"`
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

// changeRecordEntry is one change object rendered as a ROW: what it targets,
// what it does to each field, and whether it landed.
//
// It exists because `effect` cannot answer this. `effect` is a SUMMARY built for
// a queue row — counts, plus the single entity and field when there happens to
// be exactly one — so a two-entity request collapses to "2 entities / 2 fields"
// and the per-entity detail is simply not in the shape. That is fine for a list
// and useless for a detail view.
//
// Derived from the stored payload rather than from a live diff, which is what
// makes it work on a MERGED request: once the changeset is applied the diff
// against current intent is empty, and the record of what was done would vanish
// exactly when it becomes the only account of it. Deriving from the payload also
// makes this correct for every request already in the database, with no backfill.
//
// Server-side (not a client join of `changes` against `mergeAttempts`) per
// CLAUDE.md's API-first rule: orbital's UI renders it, and so can anyone else's.
type changeRecordEntry struct {
	OrbID string `json:"orbId" example:"colo:CWJHDX3-idrac"`
	Type  string `json:"type,omitempty" example:"IdracSettings"`
	Op    string `json:"op" example:"update"`
	// Fields is empty for an op with no field detail — a delete, or a create
	// whose values were not recorded. The op still says what happened.
	Fields []changeRecordField `json:"fields,omitempty"`
	// Applied is whether this item landed. Nil when no merge has been attempted,
	// which is different from false: a request nobody has merged has not failed.
	Applied *bool `json:"applied,omitempty" example:"true"`
}

// changeRecordField is one field's intended end state.
type changeRecordField struct {
	Field string `json:"field" example:"sshEnabled"`
	// Value is the intended value, absent when the field is being cleared.
	Value any `json:"value,omitempty"`
	// Before is the value at the time this was last reviewed, read from the
	// recorded ancestor. Absent for a create, and for requests opened before
	// orbital recorded one — the row then reads "\u2192 after", which is less
	// informative but still true.
	Before any `json:"before,omitempty"`
	// Cleared distinguishes "unset this" from "set this to null" — the two are
	// different mutations and a reader cannot tell them apart from a null Value.
	Cleared bool `json:"cleared,omitempty" example:"false"`
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
	// Satisfied is the part of the changeset that would do nothing: fields whose
	// current value already equals the proposed one, and deletes whose target is
	// already gone. Same flat shape as Changes, so a client renders it with the
	// same code.
	//
	// It exists because `changes` alone cannot answer "what does this request
	// propose". A field someone else already set drops out of the diff, so the
	// request appears to shrink — with no signal that it did, or why. Listing
	// them separately keeps `changes` meaning exactly "what would change" while
	// making the whole proposal visible.
	//
	// `before` and `after` are equal on every entry here, by definition.
	Satisfied []*graphdiff.Change `json:"satisfied,omitempty"`
	// Fields is the review table: one row per field the changeset writes, each
	// resolved to what a merge would DO with it.
	//
	// It supersedes walking `changes` and `satisfied` separately. Those two
	// answer "what would change" and "what would not", but neither can express
	// the third outcome — a field someone else moved to a different value, which
	// refuses the merge. Rendered from `changes` alone, a conflict is
	// indistinguishable from an ordinary change: both are two differing values.
	//
	// Computed by the SAME classifier the merge uses, so the preview and the
	// refusal cannot disagree.
	Fields []fieldOutcomeBody `json:"fields"`
}

// fieldOutcomeBody is one row of the review table.
type fieldOutcomeBody struct {
	OrbID string `json:"orbId" example:"colo:server-maintenance-CWJHDX3"`
	Type  string `json:"type" example:"ServerMaintenance"`
	// Field is the bare field name, type prefix stripped — what a table shows.
	Field string `json:"field" example:"enabled"`
	// Outcome is `applies`, `satisfied` or `conflict`.
	//
	//   applies   — the merge writes Proposed over Current.
	//   satisfied — Current already equals Proposed; the merge writes nothing.
	//   conflict  — someone changed this field since the request was reviewed;
	//               the merge REFUSES until it is re-reviewed or amended.
	Outcome string `json:"outcome" example:"applies"`
	// Reviewed is the value when the request was opened. Present only on a
	// conflict — on the other two it equals Current and would be noise.
	Reviewed any `json:"reviewed,omitempty" swaggertype:"string"`
	Current  any `json:"current" swaggertype:"string"`
	Proposed any `json:"proposed" swaggertype:"string"`
}

// approvalPolicyResponse is one protected class.
type approvalPolicyResponse struct {
	ID         string `json:"id" example:"7c2e1f88-1a2b-4c3d-8e9f-0a1b2c3d4e5f"`
	ActionType string `json:"actionType" example:"config.mutation"`
	// AllNamespaces means this policy is the fallback for every namespace that
	// has no policy of its own. Namespace is empty when it is set.
	AllNamespaces     bool     `json:"allNamespaces" example:"false"`
	Namespace         string   `json:"namespace,omitempty" example:"alaska-dot"`
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
	if ok, err := validateTitle(c, body.Title, true); !ok {
		return err
	}

	if problems := removedBeforeProblems(body.Changes); len(problems) > 0 {
		return writeChangesetProblems(c, problems)
	}
	cs := &approval.Changeset{Namespace: body.Namespace, Changes: toChangeItems(body.Changes)}
	actor := actorFromContext(c)

	cr, problems, err := h.Create(c.Request().Context(), actor, body.Title, body.Description, cs)
	if err != nil {
		var pf *preconditionFailed
		if errors.As(err, &pf) {
			return writePreconditionFailed(c, pf)
		}
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
// @Description `status` is **repeatable** and OR-ed, so "everything that has finished" is `status=merged&status=rejected&status=closed` — there is no aggregate keyword for it, because the three stored values already say it and a coined term would have to be learned. An unrecognised value is refused rather than ignored: a `status=Merged` typo used to match no filter at all and return the ENTIRE queue, which looks exactly like a correct answer.
// @Description
// @Description `orbId` is **repeatable** and the values are OR-ed (max 32; more is refused, never truncated). A change to an owned child records the CHILD's orbId — a server-maintenance edit lands as `<ns>:server-maintenance-<serial>` — so "is anything in flight for this server" means passing the server's orbId AND the orbIds of everything it owns, exactly as `/api/v1/audit-log` does.
// @Tags        change-requests
// @Produce     json
// @Param       status query []string false "open, approved, active (open+approved), rejected, merged or closed. Repeatable and OR-ed: status=merged&status=rejected&status=closed is every terminal state. An unrecognised value is refused (400), never ignored."
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

	// Repeatable, like orbId below, and for the same reason: QueryParam returns
	// only the FIRST value, so a tab asking for three terminal states would
	// silently answer about one.
	wantStatuses := make([]string, 0, len(c.QueryParams()["status"]))
	for _, v := range c.QueryParams()["status"] {
		if v = strings.TrimSpace(v); v != "" && !containsStr(wantStatuses, v) {
			wantStatuses = append(wantStatuses, v)
		}
	}
	for _, v := range wantStatuses {
		if !validStatusFilter(v) {
			return writeError(c, http.StatusBadRequest, CodeBadUserInput,
				fmt.Sprintf("unknown status filter: %q", v),
				"Use one or more of: open, approved, active, rejected, merged, closed.")
		}
	}
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
	if preds := storedStatePredicates(wantStatuses); len(preds) > 0 {
		q = q.Where(approvalrequest.Or(preds...))
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
		// they depend on derived state SQL cannot see: `open` and `approved`
		// share one stored value and are told apart by the valid-approval count.
		if !statusWanted(wantStatuses, view.Status) {
			continue
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
		Satisfied:   satisfiedItems(st.Snapshot, st.Changeset, res),
		Fields:      fieldOutcomeBodies(classifyChangeset(st.Snapshot, st.Changeset, cr.BaseValues)),
	})
}

// maxTitleLen matches the `title` column, which ent generates as varchar(255)
// because the field declares no Size (`description` declares one and gets text).
// Without this check a longer title reached Postgres and failed at INSERT, so a
// user error surfaced as a 500.
const maxTitleLen = 255

// allNamespacesResourceID is the audit resource id for a policy that governs
// every namespace. See auditPolicy for why it is a sentinel rather than "".
const allNamespacesResourceID = "*"

// validateTitle enforces what the column already enforced silently. `required`
// is false for callers that treat an empty title as "leave it alone".
//
// Returns (ok, err) rather than a bare error BECAUSE writeError returns c.JSON's
// nil on success: a helper that returned it directly would report "no problem"
// to its caller while having already written a 400, and the handler would carry
// on and append a second body to the same response. That is not hypothetical —
// it shipped in the first draft of this function and produced a 400 whose body
// held both the error and a created change request.
func validateTitle(c echo.Context, title string, required bool) (bool, error) {
	t := strings.TrimSpace(title)
	if required && t == "" {
		return false, writeError(c, http.StatusBadRequest, CodeBadUserInput, "title is required", "")
	}
	if len([]rune(t)) > maxTitleLen {
		return false, writeError(c, http.StatusBadRequest, CodeBadUserInput,
			fmt.Sprintf("title is too long: %d characters, limit is %d", len([]rune(t)), maxTitleLen),
			"Shorten the title. Long context belongs in the description, which is unbounded.")
	}
	return true, nil
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
		if problems := removedBeforeProblems(body.Changes); len(problems) > 0 {
			return writeChangesetProblems(c, problems)
		}
		cs = &approval.Changeset{Namespace: body.Namespace, Changes: toChangeItems(body.Changes)}
	}

	if body.Title != nil {
		if ok, err := validateTitle(c, *body.Title, true); !ok {
			return err
		}
	}

	caller := resolveCallerRole(c, h.db)
	cr, problems, err := h.Amend(c.Request().Context(), id, actorFromContext(c), caller.Role,
		body.Title, body.Description, cs)
	if err != nil {
		var pf *preconditionFailed
		if errors.As(err, &pf) {
			return writePreconditionFailed(c, pf)
		}
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
		var pf *preconditionFailed
		if errors.As(err, &pf) {
			return writePreconditionFailed(c, pf)
		}
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
	allNamespaces := body.AllNamespaces != nil && *body.AllNamespaces
	if err := validatePolicyNamespace(c, allNamespaces, body.Namespace); err != nil {
		return err
	}
	if allNamespaces {
		body.Namespace = ""
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
		SetAllNamespaces(allNamespaces).
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
		if isNamespaceCheckViolation(err) {
			return writeError(c, http.StatusBadRequest, CodeBadUserInput,
				"a policy covers either all namespaces or one namespace, never both and never neither",
				"Send allNamespaces:true with no namespace, or a namespace with allNamespaces omitted.")
		}
		if ent.IsConstraintError(err) {
			if allNamespaces {
				return writeError(c, http.StatusConflict, CodeConflict,
					"a policy already covers all namespaces",
					"There is one all-namespaces policy — PATCH it instead of creating a second")
			}
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

	// Moving a policy between "one namespace" and "all namespaces" is deleting
	// one policy and creating another — the same reason the UI disables the
	// namespace field while editing. Refused rather than silently ignored: a
	// dropped scope change leaves the caller believing the gate widened.
	if body.AllNamespaces != nil && *body.AllNamespaces != prev.AllNamespaces {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput,
			"a policy's namespace scope cannot be changed",
			"Delete this policy and create the one you want — moving the scope is not an edit to it.")
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
//
// A GLOBAL policy has no namespace, and an empty resource id would make the
// most consequential policy in the system unfindable by the very query this
// exists for. It is recorded under `*` — a filter key, deliberately not the
// prose label `policyLabel` produces, and deliberately NOT stored in the row's
// own `namespace` column, so the sentinel never reaches the data model or a
// `WHERE namespace = $1`.
func (h *ChangeRequest) auditPolicy(c echo.Context, action, namespace string, details map[string]any) {
	if namespace == "" {
		namespace = allNamespacesResourceID
	}
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
		"allNamespaces":     p.AllNamespaces,
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
		SubtreeChanged:   st.SubtreeChanged,
		StaleEntities:    staleEntities(cr, st),
		Effect:           resolveEffect(cr.BaseEffect, st.Changeset),
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
	out.Record = changeRecord(st.Changeset.Changes, out.MergeAttempts, cr.BaseValues)
	return out, nil
}

// changeRecord turns the stored changeset into per-entity rows, folding in what
// each merge attempt did.
//
// Applied in ANY attempt counts as applied: a retried merge re-attempts only the
// items that failed, so a later attempt's silence about an item that already
// landed must not read as a failure.
func changeRecord(changes []approval.ChangeItem, attempts []mergeAttemptResult, ancestor map[string]map[string]any) []changeRecordEntry {
	// nil means "never attempted", which is not the same as "did not apply".
	var applied map[string]bool
	for _, a := range attempts {
		for _, r := range a.Results {
			if applied == nil {
				applied = map[string]bool{}
			}
			applied[r.OrbID] = applied[r.OrbID] || r.Applied
		}
	}

	out := make([]changeRecordEntry, 0, len(changes))
	for _, ch := range changes {
		e := changeRecordEntry{OrbID: ch.OrbID, Type: ch.Type, Op: string(ch.Op)}

		// Sorted: `set` is a map, so insertion order is not stable across reads
		// and an unsorted render would reshuffle the rows on every refresh.
		keys := make([]string, 0, len(ch.Set))
		for k := range ch.Set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// The ancestor is keyed by DGraph predicate (`Server.hostname`), the
		// changeset by bare field name, so the lookup goes through the same
		// mapping that wrote it.
		was := ancestor[ch.OrbID]
		before := func(f string) any {
			if was == nil {
				return nil
			}
			return was[predicateFor(ch.Type, f)]
		}
		for _, k := range keys {
			e.Fields = append(e.Fields, changeRecordField{Field: k, Value: ch.Set[k], Before: before(k)})
		}
		cleared := append([]string(nil), ch.Clear...)
		sort.Strings(cleared)
		for _, k := range cleared {
			e.Fields = append(e.Fields, changeRecordField{Field: k, Cleared: true, Before: before(k)})
		}

		if applied != nil {
			v := applied[ch.OrbID]
			e.Applied = &v
		}
		out = append(out, e)
	}
	return out
}

func renderPolicy(p *ent.ApprovalPolicy) approvalPolicyResponse {
	roles := p.BypassRoles
	if roles == nil {
		roles = []string{}
	}
	out := approvalPolicyResponse{
		ID:                p.ID.String(),
		ActionType:        p.ActionType,
		AllNamespaces:     p.AllNamespaces,
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
	// Checked before the switch: it wraps errCRStale, so the switch would match
	// it and drop the per-entity detail on the floor.
	var sw *staleWithEntities
	if errors.As(err, &sw) {
		out := make([]changesetProblem, 0, len(sw.Problems))
		for _, p := range sw.Problems {
			out = append(out, changesetProblem{Index: p.Index, OrbID: p.OrbID, Field: p.Field, Msg: p.Msg, Hint: p.Hint})
		}
		return c.JSON(http.StatusConflict, validationErrorResponse{
			Error:      err.Error(),
			Code:       CodeMVCCConflict,
			HTTPStatus: http.StatusConflict,
			Problems:   out,
		})
	}
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

// removedBeforeProblems reports items still carrying the removed `before` field.
//
// A breaking change announced loudly. Silence would leave a client believing it
// has a field-level precondition it no longer has.
func removedBeforeProblems(in []changeItemBody) []approval.ValidationError {
	var out []approval.ValidationError
	for i, b := range in {
		if len(b.Before) > 0 {
			out = append(out, approval.ValidationError{
				Index: i, OrbID: b.OrbID,
				Msg:  "`before` was removed; it is no longer a precondition and would be ignored",
				Hint: "Send `version` — the entity's version as you read it — instead. Field-level protection still applies at merge, from the ancestor orbital records itself.",
			})
		}
		if b.IfVersion != nil {
			out = append(out, approval.ValidationError{
				Index: i, OrbID: b.OrbID,
				Msg:  "`ifVersion` was renamed to `version` and would be ignored",
				Hint: "Rename the field to `version`; the value and its meaning are unchanged.",
			})
		}
	}
	return out
}

// fieldOutcomeBodies renders the classifier's output for the wire, stripping the
// graphdiff type prefix from field names ("Server.hostname" → "hostname") and
// omitting `reviewed` where it would only repeat `current`.
func fieldOutcomeBodies(in []fieldOutcome) []fieldOutcomeBody {
	out := make([]fieldOutcomeBody, 0, len(in))
	for _, o := range in {
		field := o.Field
		row := fieldOutcomeBody{
			OrbID: o.OrbID, Type: o.Type,
			// Strip whatever type prefix graphdiff used, not just this entity's
			// own: fields declared on the ConfigItem INTERFACE come back as
			// "ConfigItem.name" on a DataCenter, so trimming o.Type+"." alone
			// left the prefix on every inherited field.
			Field:    field[strings.Index(field, ".")+1:],
			Outcome:  o.Outcome,
			Current:  o.Current,
			Proposed: o.Proposed,
		}
		if o.Outcome == "conflict" {
			row.Reviewed = o.Reviewed
		}
		out = append(out, row)
	}
	return out
}

func toChangeItems(in []changeItemBody) []approval.ChangeItem {
	out := make([]approval.ChangeItem, 0, len(in))
	for _, b := range in {
		out = append(out, approval.ChangeItem{
			OrbID:   b.OrbID,
			Type:    b.Type,
			Op:      approval.Op(b.Op),
			Set:     b.Set,
			Clear:   b.Clear,
			Version: b.Version,
		})
	}
	return out
}

func fromChangeItems(in []approval.ChangeItem) []changeItemBody {
	out := make([]changeItemBody, 0, len(in))
	for _, ch := range in {
		out = append(out, changeItemBody{
			OrbID:   ch.OrbID,
			Type:    ch.Type,
			Op:      string(ch.Op),
			Set:     ch.Set,
			Clear:   ch.Clear,
			Version: ch.Version,
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
// writePreconditionFailed renders a `before` mismatch. 409, not 400: nothing
// about the request is malformed — a value moved while the caller was composing
// it, which is the same class of failure as the guarded-apply MVCC conflict and
// carries the same code so a client branches on one thing.
func writePreconditionFailed(c echo.Context, e *preconditionFailed) error {
	out := make([]changesetProblem, 0, len(e.Problems))
	for _, p := range e.Problems {
		out = append(out, changesetProblem{
			Index: p.Index, OrbID: p.OrbID, Field: p.Field, Msg: p.Msg, Hint: p.Hint,
		})
	}
	return c.JSON(http.StatusConflict, validationErrorResponse{
		Error:      "state moved since you read it",
		Code:       CodeMVCCConflict,
		HTTPStatus: http.StatusConflict,
		Problems:   out,
	})
}

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

func isNamespaceCheckViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "approval_policy_namespace_exclusive")
}

// validatePolicyNamespace refuses the two shapes that contradict themselves on
// the namespace axis, mirroring validatePolicyScope on the type axis.
//
// The database enforces this too, via a CHECK. This layer exists to say WHICH
// rule was broken — a constraint violation cannot.
func validatePolicyNamespace(c echo.Context, allNamespaces bool, namespace string) error {
	switch {
	case allNamespaces && strings.TrimSpace(namespace) != "":
		return writeError(c, http.StatusBadRequest, CodeBadUserInput,
			"a policy covering all namespaces must not also name one — the two say different things and the row would not describe what it protects",
			"Send allNamespaces:true with no namespace, or a namespace with allNamespaces omitted.")
	case !allNamespaces && strings.TrimSpace(namespace) == "":
		return writeError(c, http.StatusBadRequest, CodeBadUserInput,
			"namespace is required",
			"Name the namespace to protect, or send allNamespaces:true to cover every namespace including ones onboarded later.")
	}
	return nil
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
