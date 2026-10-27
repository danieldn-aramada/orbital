package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ApprovalDecision is one approve or reject, kept forever. APPEND-ONLY: the
// database refuses UPDATE and DELETE (internal/db/auditguard.go).
//
// Approval is the CURRENT decision per approver — what the merge gate counts —
// and is replaced when an approver re-decides. This table is the RECORD: every
// decision, including the ones Approval no longer holds. Change-request review
// is deliberately not in the audit log (docs/audit-model.md), so without this a
// replaced decision existed nowhere.
//
// The id is the id of the Approval row written alongside it, which is what makes
// the one-time backfill of pre-existing approvals idempotent.
type ApprovalDecision struct {
	ent.Schema
}

func (ApprovalDecision) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Immutable(),
		field.Int64("approval_request_id").Immutable(),
		field.String("approver").NotEmpty().Immutable(),
		field.Enum("decision").Values("approved", "rejected").Immutable(),
		field.Text("comment").Optional().Default("").Immutable(),
		field.String("approved_at_hash").NotEmpty().Immutable(),
		field.Int("approved_at_revision").Default(0).Immutable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (ApprovalDecision) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("request", ApprovalRequest.Type).
			Ref("decisions").
			Field("approval_request_id").
			Immutable().
			Unique().
			Required(),
	}
}

func (ApprovalDecision) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("approval_request_id", "created_at"),
	}
}
