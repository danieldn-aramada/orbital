package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/armada/orbital/ent"
	entapproval "github.com/armada/orbital/ent/approval"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/internal/approval"
	"github.com/labstack/echo/v4"
)

// proposedChangesEntry is everything in flight for ONE entity, indexed by field.
type proposedChangesEntry struct {
	// Type is the ConfigItem type, so a client can label the entity without a
	// second lookup.
	Type string `json:"type,omitempty" example:"ServerMaintenance"`
	// Fields maps a field name to what is proposed for it.
	Fields map[string]proposedField `json:"fields"`
}

// proposedField is every active proposal touching one field of one entity.
type proposedField struct {
	// Conflicting is true when two or more proposals set this field to
	// DIFFERENT values.
	//
	// Derived server-side on purpose. It is a comparison across proposals, and
	// leaving it to callers means every client re-implements it and gets it
	// subtly different — the same reason the field index itself is server-side.
	Conflicting bool               `json:"conflicting"`
	Proposals   []proposedByChange `json:"proposals"`
}

// proposedByChange is one change request's proposal for one field.
type proposedByChange struct {
	ChangeRequestID string `json:"changeRequestId" example:"colo-42"`
	Title           string `json:"title,omitempty" example:"Update server-CWJHDX3"`
	// Status is "open" or "approved", derived from the approval count against
	// the request's STORED base. See the endpoint's @Description for the
	// caveat — it is a display hint, not a merge verdict.
	Status string `json:"status" example:"open"`
	// Op is "update", "upsert" or "clear". A clear carries no Value.
	Op string `json:"op" example:"update"`
	// Value is the proposed value, unmodified. Not truncated and not
	// stringified: how to render a long or structured value is the client's
	// decision, and orbital's own UI is not privileged over anyone else's.
	Value any `json:"value,omitempty"`
	// Approvals and RequiredApprovals are the raw counts behind Status, so a
	// client can render "1 of 2" rather than inferring it.
	Approvals         int    `json:"approvals"`
	RequiredApprovals int    `json:"requiredApprovals"`
	Author            string `json:"author" example:"dev@armada.ai"`
	CreatedAt         string `json:"createdAt" example:"2026-08-30T22:14:03Z"`
}

// ProposedChanges returns what is proposed for a set of entities, indexed by
// entity and field.
//
// @Summary     What is proposed for these entities
// @Description The field-level projection of the open change requests touching a set of entities.
// @Description
// @Description **Why this exists separately from `/api/v1/change-requests`.** That endpoint answers
// @Description "list the requests"; this one answers "what is proposed about these entities". Getting
// @Description the second from the first means inverting it — every request, every change item, every
// @Description key in `set` — then grouping by (orbId, field) and comparing values to spot conflicts.
// @Description That walk would be re-implemented by every client, so orbital does it once.
// @Description
// @Description **Designed for the join.** The response is keyed by orbId, which is `@id` on the
// @Description ConfigItem interface and therefore globally unique across every type. So overlaying
// @Description proposals onto entities read from `/graphql` is a map lookup:
// @Description `proposals[node.orbId].fields[name]` — no traversal, no correlation logic. Issue both
// @Description calls in parallel; they have no dependency on each other.
// @Description
// @Description **Reads PostgreSQL only** — no DGraph round-trip, so it is safe on every page load.
// @Description One consequence: it cannot know the CURRENT value of a field, so it cannot tell that a
// @Description proposal has become a no-op (someone applied the same value directly). The client holds
// @Description both halves and should suppress a field mark when the proposed value already equals the
// @Description current one. `status` is likewise derived from the approval count against the request's
// @Description stored base rather than live intent — a display hint, not a merge verdict. Merge
// @Description re-checks against live state and can still refuse with `409 MVCC_CONFLICT`.
// @Description
// @Description Only `active` requests appear — open plus approved-not-yet-merged. Closed, merged and
// @Description rejected requests are absent, as are entities with nothing proposed.
// @Tags        change-requests
// @Produce     json
// @Param       orbId query []string true "Entities to report on. Repeatable, max 128 — over that the request is refused (400), not truncated."
// @Success     200 {object} map[string]proposedChangesEntry
// @Failure     400 {object} errorResponse
// @Router      /api/v1/proposed-changes [get]
func (h *ChangeRequest) ProposedChanges(c echo.Context) error {
	// Every request rendered resolves the same namespace's policy for its
	// approval count. Without the memo that is one PostgreSQL query per
	// request, on an endpoint whose whole justification is being cheap enough
	// to call on every page load.
	ctx := withPolicyMemo(c.Request().Context())

	wanted := make([]string, 0, len(c.QueryParams()["orbId"]))
	seen := map[string]bool{}
	for _, id := range c.QueryParams()["orbId"] {
		if id != "" && !seen[id] {
			seen[id] = true
			wanted = append(wanted, id)
		}
	}
	if len(wanted) == 0 {
		return writeError(c, http.StatusBadRequest, CodeBadUserInput,
			"at least one orbId is required",
			"Pass the entities you are rendering: ?orbId=<a>&orbId=<b>.")
	}
	if len(wanted) > maxOrbIDFilter {
		// Refused, not truncated — same rule as the sibling filters. A silently
		// narrowed answer here would mean a field mark that never appears.
		return writeError(c, http.StatusBadRequest, CodeBadUserInput,
			fmt.Sprintf("too many orbId filters: %d (max %d)", len(wanted), maxOrbIDFilter),
			fmt.Sprintf("Query at most %d orbIds at a time.", maxOrbIDFilter))
	}

	// WithApprovals so the approval count needs no second query per request.
	rows, err := h.db.ApprovalRequest.Query().
		WithApprovals().
		Where(
			approvalrequest.StatusEQ(approvalrequest.StatusOpen),
			payloadTouchesAnyOrbID(wanted),
		).
		Order(ent.Asc(approvalrequest.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("list proposed changes: %w", err)
	}

	out := map[string]*proposedChangesEntry{}
	for _, cr := range rows {
		var cs approval.Changeset
		if err := json.Unmarshal(cr.Payload, &cs); err != nil {
			// One unreadable payload must not blank the whole overlay for an
			// entity that has other, readable proposals.
			h.logger.Warn("proposed changes: undecodable payload, skipping",
				"change_request", crHumanID(cr), "err", err)
			continue
		}

		valid, required, err := h.approvalCounts(ctx, cr, &cs)
		if err != nil {
			return err
		}
		status := approval.StatusOpen
		if required > 0 && valid >= required {
			status = approval.StatusApproved
		}

		for _, item := range cs.Changes {
			if !seen[item.OrbID] {
				continue // in the changeset but not something the caller asked about
			}
			entry := out[item.OrbID]
			if entry == nil {
				entry = &proposedChangesEntry{Type: item.Type, Fields: map[string]proposedField{}}
				out[item.OrbID] = entry
			}
			if entry.Type == "" {
				entry.Type = item.Type
			}

			base := proposedByChange{
				ChangeRequestID:   crHumanID(cr),
				Title:             cr.Title,
				Status:            status,
				Approvals:         valid,
				RequiredApprovals: required,
				Author:            cr.Author,
				CreatedAt:         cr.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			}
			for field, value := range item.Set {
				p := base
				p.Op = string(item.Op)
				p.Value = value
				addProposal(entry, field, p)
			}
			for _, field := range item.Clear {
				p := base
				p.Op = "clear"
				addProposal(entry, field, p)
			}
		}
	}

	// Conflict is a property of the finished set, so it is decided after every
	// request has contributed — not incrementally, which would leave the first
	// proposal marked from a comparison it was not part of.
	for _, entry := range out {
		for name, f := range entry.Fields {
			f.Conflicting = valuesDisagree(f.Proposals)
			sort.Slice(f.Proposals, func(i, j int) bool {
				return f.Proposals[i].CreatedAt < f.Proposals[j].CreatedAt
			})
			entry.Fields[name] = f
		}
	}
	return c.JSON(http.StatusOK, out)
}

func addProposal(entry *proposedChangesEntry, field string, p proposedByChange) {
	f := entry.Fields[field]
	f.Proposals = append(f.Proposals, p)
	entry.Fields[field] = f
}

// valuesDisagree reports whether two or more proposals want different things
// for the same field.
//
// Compared as encoded JSON so structured values compare by content rather than
// by Go identity — two proposals setting the same nested object are agreement,
// not conflict. A clear and a set are always a disagreement: one wants a value
// and the other wants none.
func valuesDisagree(ps []proposedByChange) bool {
	if len(ps) < 2 {
		return false
	}
	first := ""
	for i, p := range ps {
		key := p.Op + "\x00"
		if p.Op != "clear" {
			b, err := json.Marshal(p.Value)
			if err != nil {
				return true // cannot prove agreement; say so rather than imply it
			}
			key = "set\x00" + string(b)
		}
		if i == 0 {
			first = key
			continue
		}
		if key != first {
			return true
		}
	}
	return false
}

// approvalCounts returns how many approvals currently count and how many the
// policy demands.
//
// Validity is judged against the request's STORED base_hash rather than live
// intent, which is what keeps this endpoint free of DGraph. The two agree
// unless the graph moved since the base was last anchored, in which case this
// can report `approved` for a request that merge would refuse as stale. That is
// the documented trade: this powers a display hint, and merge re-checks against
// live state before it applies anything.
func (h *ChangeRequest) approvalCounts(ctx context.Context, cr *ent.ApprovalRequest, cs *approval.Changeset) (valid, required int, err error) {
	pol, err := h.resolvePolicy(ctx, cr.ActionType, cs)
	if err != nil {
		return 0, 0, err
	}
	approvals := cr.Edges.Approvals
	if approvals == nil {
		approvals, err = cr.QueryApprovals().All(ctx)
		if err != nil {
			return 0, 0, fmt.Errorf("load approvals: %w", err)
		}
	}
	for _, a := range approvals {
		if a.Decision != entapproval.DecisionRejected && a.ApprovedAtHash == cr.BaseHash {
			valid++
		}
	}
	return valid, pol.required, nil
}
