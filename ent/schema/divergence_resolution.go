package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// DivergenceResolution records a cloud admin's decision on a divergence:
// Accept, Reject, or Ignore. The (entry_orb_id, field) pair uniquely identifies
// the divergence being resolved; re-deciding REPLACES the existing row.
//
// Resolution lifecycle is bound 1:1 to the active DivergenceEntry — and both
// belong to the lifetime of a specific report ingest. When orbital ingests a
// content-differing report from orb, all DC entries AND their resolutions are
// dropped together. Audit history of every decision lives in the Event log —
// not this table. See ADR 012 for the supersede semantics.
//
//   - Accept: cloud agrees with the edge override. Orbital intent is updated to
//     match. The deployment layer re-takes ownership of the field with the new
//     intent value.
//   - Reject: cloud disagrees with the edge override. Intent unchanged. The
//     deployment layer re-takes ownership of the field, resetting the edge
//     value back to intent.
//   - Ignore: cloud disengages from the field. Intent unchanged. The
//     deployment layer does NOT enforce intent for this field; local admin
//     keeps sole ownership.
//
// Orbital does NOT track edge propagation. If local admin re-overrides after
// a Reject, orb's next published report will contain that override; if its
// content differs from the previously-ingested set, orbital supersedes —
// surfacing it as a fresh pending entry for re-decision.
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
	}
}

func (DivergenceResolution) Edges() []ent.Edge {
	return nil
}
