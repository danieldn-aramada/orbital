package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/ent/predicate"
	"github.com/armada/orbital/internal/approval"
)

// statusActive is a filter value, not a stored status: everything that has not
// reached a terminal state — `open` plus the derived `approved`.
//
// It exists because "does this entity have a change in flight?" is the question
// the pending-change badge asks, and neither stored status answers it.
// `status=open` excludes an approved-but-unmerged request, because `approved`
// is derived from the valid-approval count rather than written down (D17).
// Making the client OR two queries together would put orbital's own lifecycle
// logic in the client, which is exactly what the API-first rule forbids.
const statusActive = "active"

// validStatusFilter says whether a ?status= value is one this API defines.
//
// An unrecognised value must be REFUSED, not ignored. The switch it replaced
// had no default, so `status=Merged` matched nothing, applied no predicate, and
// returned the entire queue — the same silent-wrong-answer shape as a
// truncated filter, and harder to notice because the response looks right.
func validStatusFilter(v string) bool {
	switch v {
	case approval.StatusOpen, approval.StatusApproved, statusActive,
		approval.StatusRejected, approval.StatusMerged, approval.StatusClosed:
		return true
	}
	return false
}

// storedStatePredicates maps requested statuses onto the STORED state column.
//
// `open` and `approved` both live in the stored `open` row: `approved` is
// derived from the valid-approval count (D17), so SQL can only narrow to
// non-terminal and the exact split happens after rendering, where the count
// exists. Duplicates collapse — asking for open+approved+active is one
// predicate, not three.
func storedStatePredicates(wanted []string) []predicate.ApprovalRequest {
	var out []predicate.ApprovalRequest
	nonTerminal := false
	for _, v := range wanted {
		switch v {
		case approval.StatusRejected:
			out = append(out, approvalrequest.StatusEQ(approvalrequest.StatusRejected))
		case approval.StatusMerged:
			out = append(out, approvalrequest.StatusEQ(approvalrequest.StatusMerged))
		case approval.StatusClosed:
			out = append(out, approvalrequest.StatusEQ(approvalrequest.StatusClosed))
		case approval.StatusOpen, approval.StatusApproved, statusActive:
			nonTerminal = true
		}
	}
	if nonTerminal {
		out = append(out, approvalrequest.StatusEQ(approvalrequest.StatusOpen))
	}
	return out
}

// statusWanted judges a RENDERED row against the filter, where `view.Status` is
// the derived status rather than the stored one.
//
// No filter means everything. `active` accepts either non-terminal status,
// which is the whole reason it exists as a filter value.
func statusWanted(wanted []string, derived string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, v := range wanted {
		if v == statusActive {
			if derived == approval.StatusOpen || derived == approval.StatusApproved {
				return true
			}
			continue
		}
		if v == derived {
			return true
		}
	}
	return false
}

// maxOrbIDFilter caps the repeatable ?orbId= filter. Same number as the
// audit-log API's cap, for the same reason and against the same caller: a
// detail page hands over the orbIds of a ConfigItem and its declared subgraph, and
// a subgraph is not an unbounded list. It defends the URL length and the OR-ed
// containment scan behind it.
// maxOrbIDFilter caps the repeatable ?orbId= filter on the endpoints that take
// a subgraph: /api/v1/change-requests and /api/v1/audit-log.
//
// It is a guardrail against query-string bloat and an unbounded OR, not a
// design limit. 128 orbIds is roughly 4.5KB of query string — under nginx's 8KB
// header buffer — and 128 GIN index probes is nothing.
//
// Sized from measurement, not taste: the largest real subgraph in the
// seeded colo namespace is 35 (a populated server, dominated by storage devices
// and network interfaces), so this is ~3.5x headroom. The previous value of 32
// sat BELOW that, which meant a real page hit it on ordinary data.
//
// What protects callers is not the number, it is that both endpoints REFUSE
// over it rather than truncating — a truncated filter answers a question nobody
// asked and is indistinguishable from a correct answer.
//
// If a legitimate caller ever needs more, the exit is a POST-with-body read
// (the shape Prometheus /api/v1/query and Elasticsearch _search use for queries
// too long for a URL) — NOT client-side chunking, which pushes an overlap-aware
// union into every consumer, and NOT server-side subgraph expansion, which
// AUDIT.md rules out.
const maxOrbIDFilter = 128

// payloadTouchesAnyOrbID matches change requests whose changeset names ANY of
// these orbIds.
//
// Repeatable rather than single-valued because a change to a subgraph member
// records the CHILD's orbId and never the parent's — a server-maintenance edit
// lands as `<ns>:server-maintenance-<serial>`. Asking about the server alone
// therefore answers "nothing in flight" while a change to that server sits open,
// which is exactly what the caller wanted to know. The parent→child knowledge
// stays in the page composer that already pulled the subgraph (see AUDIT.md's
// "REST audit-log API is node-specific" decision); this endpoint only ORs the
// list it is given.
func payloadTouchesAnyOrbID(orbIDs []string) predicate.ApprovalRequest {
	ps := make([]predicate.ApprovalRequest, 0, len(orbIDs))
	for _, id := range orbIDs {
		ps = append(ps, payloadTouchesOrbID(id))
	}
	if len(ps) == 1 {
		return ps[0]
	}
	return approvalrequest.Or(ps...)
}

// payloadTouchesOrbID matches change requests whose changeset names this orbId.
//
// jsonb containment, so it uses the GIN index on `payload` rather than scanning
// and deserialising every row. Postgres containment descends into arrays — the
// operand `{"changes":[{"orbId":"X"}]}` matches a payload whose `changes` array
// holds ANY element containing that orbId, which is what makes the match work
// on the second and later items of a changeset and not just the first.
//
// Built with sql.P + b.Arg rather than sql.ExprP with a `?` placeholder: ent
// does not substitute `?` inside ExprP, so that form ships the literal question
// mark to Postgres and 500s.
func payloadTouchesOrbID(orbID string) predicate.ApprovalRequest {
	return payloadChangeHas("orbId", orbID)
}

// payloadChangeHas matches change requests with at least one changeset item
// whose key equals value. See payloadTouchesOrbID for why containment.
func payloadChangeHas(key, value string) predicate.ApprovalRequest {
	operand, err := json.Marshal(map[string]any{
		"changes": []any{map[string]any{key: value}},
	})
	if err != nil {
		// Unreachable for a string map; a false predicate is the safe reading
		// of "we could not express the filter" — return nothing rather than
		// silently returning everything.
		return predicate.ApprovalRequest(func(s *sql.Selector) { s.Where(sql.False()) })
	}
	return predicate.ApprovalRequest(func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.Ident(s.C(approvalrequest.FieldPayload)).WriteString(" @> ").Arg(string(operand)).WriteString("::jsonb")
		}))
	})
}

// payloadInAnyNamespace matches change requests scoped to ANY of these
// namespaces. Repeatable for the same reason status and orbId are: a caller
// watching several namespaces otherwise has to make one request per namespace,
// and reading the filter with QueryParam would take the FIRST value and silently
// answer about that one alone. ORs the single-namespace primitive below.
func payloadInAnyNamespace(namespaces []string) predicate.ApprovalRequest {
	ps := make([]predicate.ApprovalRequest, 0, len(namespaces))
	for _, ns := range namespaces {
		ps = append(ps, payloadNamespaceEQ(ns))
	}
	if len(ps) == 1 {
		return ps[0]
	}
	return approvalrequest.Or(ps...)
}

// payloadNamespaceEQ matches change requests scoped to a namespace. Same
// containment mechanism and the same index; a changeset is single-namespace by
// construction, so this is an equality test expressed as containment.
func payloadNamespaceEQ(namespace string) predicate.ApprovalRequest {
	operand, err := json.Marshal(map[string]any{"namespace": namespace})
	if err != nil {
		return predicate.ApprovalRequest(func(s *sql.Selector) { s.Where(sql.False()) })
	}
	return predicate.ApprovalRequest(func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			b.Ident(s.C(approvalrequest.FieldPayload)).WriteString(" @> ").Arg(string(operand)).WriteString("::jsonb")
		}))
	})
}

// changeRequestTimeFields are the list's time filters: createdAt_gte and so on.
var changeRequestTimeFields = TimeFields{
	"createdAt":  approvalrequest.FieldCreatedAt,
	"updatedAt":  approvalrequest.FieldUpdatedAt,
	"executedAt": approvalrequest.FieldExecutedAt,
}

// payloadTouchesAnyType matches change requests with at least one item of ANY
// of these concrete types — on any item, not just the first. Validation stamps
// every item's concrete type before the payload is stored, so containment on
// `type` is exact.
func payloadTouchesAnyType(types []string) predicate.ApprovalRequest {
	ps := make([]predicate.ApprovalRequest, 0, len(types))
	for _, t := range types {
		ps = append(ps, payloadChangeHas("type", t))
	}
	if len(ps) == 1 {
		return ps[0]
	}
	return approvalrequest.Or(ps...)
}

// concreteTypes expands interface names to the concrete types that implement
// them. Stored item types are always concrete, so `type=KubernetesCluster`
// taken literally would match nothing, and an empty list reads as "no
// requests touch clusters".
func (h *ChangeRequest) concreteTypes(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	views, err := h.views(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve type filter: %w", err)
	}
	var out []string
	for _, n := range names {
		add := []string{n}
		if v := views.Of(n); v.IsInterface {
			add = append(add, v.Implementations...)
		}
		for _, t := range add {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out, nil
}
