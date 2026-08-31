package schema

import (
	"encoding/json"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// MergeAttempt records what one execution of an ApprovalRequest actually did,
// item by item.
//
// This table exists because merge applies items ONE AT A TIME (D11) — DGraph
// gives no cross-root-field transaction, and nested owned children are links
// rather than deep writes, so a multi-entity changeset is inherently several
// mutations. A partial application therefore has to be representable.
//
// There is deliberately NO `merge_failed` status on the request. A partial
// merge is self-correcting, not a new state: the items that landed move the
// base, the request goes stale by construction, the recomputed diff shows
// exactly the remainder, and a retry is a no-op for what already applied.
// The attempt is the thing that failed — the request is still just `approved`.
type MergeAttempt struct {
	ent.Schema
}

func (MergeAttempt) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(newUUIDv7),
		// Follows ApprovalRequest's key, which is a bigint — the human
		// identifier there is namespace-number, not this.
		field.Int64("approval_request_id"),

		field.String("attempted_by").NotEmpty(),
		field.Time("attempted_at").Default(time.Now),

		// results is the per-item outcome: [{orbId, applied, error}, ...], in the
		// order the items were applied. Opaque jsonb for the same reason
		// ApprovalRequest.payload is — the shape belongs to the adapter.
		field.JSON("results", json.RawMessage{}).Optional(),

		// error is the attempt-level failure (empty when every item applied).
		field.Text("error").Optional().Default(""),
	}
}

func (MergeAttempt) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (MergeAttempt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("approval_request_id", "attempted_at"),
	}
}

func (MergeAttempt) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("request", ApprovalRequest.Type).
			Ref("merge_attempts").
			Field("approval_request_id").
			Unique().
			Required(),
	}
}
