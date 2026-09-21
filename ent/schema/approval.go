package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Approval is one reviewer's decision on an ApprovalRequest. Rows accumulate —
// N-of-M is counted, not overwritten — and a re-decision by the same approver
// replaces their row (the unique index below).
//
// Approvals are hash-stamped rather than dismissed (D13): each row records the
// base_hash it was cast against, and counts only while that still equals the
// request's current hash. This removes the "dismiss approvals" state mutation
// that someone would otherwise have to remember to perform, and lets the UI say
// "Alice approved an earlier version" instead of the approval silently
// vanishing — which is what GitHub shows.
type Approval struct {
	ent.Schema
}

func (Approval) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(newUUIDv7),
		// Follows ApprovalRequest's key, which is a bigint — the human
		// identifier there is namespace-number, not this.
		field.Int64("approval_request_id"),

		// approver is the reviewer's email, from actorFromContext.
		field.String("approver").NotEmpty(),

		field.Enum("decision").Values("approved", "rejected"),

		field.Text("comment").Optional().Default(""),

		// approved_at_hash is the request's base_hash at the moment this decision
		// was cast. An approval counts only while it matches the current hash.
		field.String("approved_at_hash").NotEmpty(),

		// approved_at_revision is the changeset revision this decision was cast
		// against. Zero for rows written before the column existed — treated as
		// matching, so historical approvals are not retroactively dismissed.
		field.Int("approved_at_revision").Default(0),
	}
}

func (Approval) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (Approval) Indexes() []ent.Index {
	return []ent.Index{
		// One current decision per (request, approver). Re-deciding overwrites.
		index.Fields("approval_request_id", "approver").Unique(),
	}
}

func (Approval) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("request", ApprovalRequest.Type).
			Ref("approvals").
			Field("approval_request_id").
			Unique().
			Required(),
	}
}
