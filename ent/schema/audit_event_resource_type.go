package schema

import (
	"github.com/google/uuid"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AuditEventResourceType records a single DGraph type touched by a parent AuditEvent.
type AuditEventResourceType struct {
	ent.Schema
}

func (AuditEventResourceType) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("audit_event_id", uuid.UUID{}),
		field.String("resource_type"),
	}
}

func (AuditEventResourceType) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("audit_event", AuditEvent.Type).
			Ref("resource_types").
			Field("audit_event_id").
			Unique().
			Required(),
	}
}

func (AuditEventResourceType) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("resource_type"),
		index.Fields("audit_event_id", "resource_type").Unique(),
	}
}
