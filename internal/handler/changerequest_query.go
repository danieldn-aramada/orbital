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
