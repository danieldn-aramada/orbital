package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// DivergenceResolution records a cloud admin's decision on a divergence:
// Accept, Reject, or Ignore. The (entry_orb_id, field) pair uniquely identifies
// the divergence being resolved; re-deciding REPLACES the existing row
// (audit trail lives in the Event log, not this table).
//
// See docs/reference/DIVERGENCE.md for the full semantic model.
//
//   - Accept: cloud agrees with the edge override. Orbital intent is updated to
//     match. The deployment layer should re-take ownership of the field (with
//     the new intent value).
//   - Reject: cloud disagrees with the edge override. Intent unchanged. The
//     deployment layer should re-take ownership of the field, resetting the
//     edge value back to intent. (Previously called "force"; renamed for
//     deployment-layer neutrality — see Settled Decisions in DIVERGENCE.md.)
//   - Ignore: cloud disengages from the field. Intent unchanged. The
//     deployment layer should NOT enforce intent for this field; local admin
//     keeps sole ownership.
//
// propagated_at is set by the divergence ingester when it observes that the
// corresponding divergence entry has disappeared from the latest snapshot
// (loop closure). Derived-from-observation, NOT signaled by the deployment
// layer — orbital's source of truth is what it sees, not what consumers
// assert. NULL = still pending; non-NULL = propagated as of that timestamp.
type DivergenceResolution struct {
	ent.Schema
}

func (DivergenceResolution) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),

		// entry_orb_id and field identify the divergence this resolution applies to.
		// Matches DivergenceEntry's (entry_orb_id, field) pair.
		field.String("entry_orb_id").NotEmpty(),
		field.String("field").NotEmpty(),

		field.Enum("action").Values("accept", "reject", "ignore"),

		// actor is the email of the cloud admin who decided (from actorFromContext).
		field.String("actor").NotEmpty(),

		field.Time("decided_at"),

		// propagated_at is set by the ingester when the divergence loop closes
		// (entry is swept because orb stopped reporting it). NULL means still
		// pending propagation. Replaces the prior cb_consumed (bool) +
		// cb_consumed_at (timestamp) pair which carried deployment-layer-
		// specific naming.
		field.Time("propagated_at").Optional().Nillable(),
	}
}

func (DivergenceResolution) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (DivergenceResolution) Indexes() []ent.Index {
	return []ent.Index{
		// One current decision per divergence. Re-deciding overwrites.
		index.Fields("entry_orb_id", "field").Unique(),
		// Used by /pending-propagation query: action IN (accept, reject) AND
		// propagated_at IS NULL.
		index.Fields("action", "propagated_at"),
	}
}

func (DivergenceResolution) Edges() []ent.Edge {
	return nil
}
