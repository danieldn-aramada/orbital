package schema

import (
	"github.com/google/uuid"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuditEventResource records a single orbId touched by a parent AuditEvent.
type AuditEventResource struct {
	ent.Schema
}

func (AuditEventResource) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("audit_event_id", uuid.UUID{}),
		field.String("orb_id"),
	}
}

func (AuditEventResource) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("audit_event", AuditEvent.Type).
			Ref("resources").
			Field("audit_event_id").
			Unique().
			Required(),
	}
}

func (AuditEventResource) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("orb_id"),
		index.Fields("audit_event_id", "orb_id").Unique(),
	}
}
