package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// DivergenceResolution records a cloud admin's decision on a divergence:
// Accept, Force, or Ignore. The (entry_orb_id, field) pair uniquely identifies
// the divergence being resolved; re-deciding REPLACES the existing row
// (audit trail lives in the Event log, not this table).
//
//   - Accept: redundant with the GraphQL mutation that updates intent —
//     recorded here so the UI can show "Accepted by X on Y." cb-bundler ignores
//     these rows when building the next bundle.
//   - Force:  cb-bundler queries un-consumed `force` rows when building a
//     bundle, emits them as `spec.takeover[]` in the ConfigBundle CR, then
//     POSTs to /api/v1/divergence/resolutions/:id/consumed to mark consumed.
//   - Ignore: no downstream effect; the UI tags the divergence entry as
//     "ignored" so admins don't see it as needing action.
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

		field.Enum("action").Values("accept", "force", "ignore"),

		// actor is the email of the cloud admin who decided (from actorFromContext).
		field.String("actor").NotEmpty(),

		field.Time("decided_at"),

		// cb_consumed is true once cb-bundler has read this row and emitted
		// the corresponding takeover entry in a built bundle. Only meaningful
		// for `action == force`; accept/ignore are no-ops for cb-bundler.
		field.Bool("cb_consumed").Default(false),
		field.Time("cb_consumed_at").Optional().Nillable(),
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
		// cb-bundler's query: "give me un-consumed force resolutions."
		index.Fields("action", "cb_consumed"),
	}
}

func (DivergenceResolution) Edges() []ent.Edge {
	return nil
}
