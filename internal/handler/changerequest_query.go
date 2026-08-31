package handler

import (
	"encoding/json"

	"entgo.io/ent/dialect/sql"
	"github.com/armada/orbital/ent/approvalrequest"
	"github.com/armada/orbital/ent/predicate"
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

// maxOrbIDFilter caps the repeatable ?orbId= filter. Same number as the
// audit-log API's cap, for the same reason and against the same caller: a
// detail page hands over the orbIds of a ConfigItem and everything it owns, and
// a subtree is not an unbounded list. It defends the URL length and the OR-ed
// containment scan behind it.
// maxOrbIDFilter caps the repeatable ?orbId= filter on the endpoints that take
// a subtree: /api/v1/change-requests and /api/v1/audit-log.
//
// It is a guardrail against query-string bloat and an unbounded OR, not a
// design limit. 128 orbIds is roughly 4.5KB of query string — under nginx's 8KB
// header buffer — and 128 GIN index probes is nothing.
//
// Sized from measurement, not taste: the largest real owned subtree in the
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
// union into every consumer, and NOT server-side subtree expansion, which
// AUDIT.md rules out.
const maxOrbIDFilter = 128

// payloadTouchesAnyOrbID matches change requests whose changeset names ANY of
// these orbIds.
//
// Repeatable rather than single-valued because a change to an owned child
// records the CHILD's orbId and never the parent's — a server-maintenance edit
// lands as `<ns>:server-maintenance-<serial>`. Asking about the server alone
// therefore answers "nothing in flight" while a change to that server sits open,
// which is exactly what the caller wanted to know. The parent→child knowledge
// stays in the page composer that already pulled the subtree (see AUDIT.md's
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
	operand, err := json.Marshal(map[string]any{
		"changes": []any{map[string]any{"orbId": orbID}},
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
