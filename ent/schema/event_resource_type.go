package schema

import (
	"github.com/google/uuid"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EventResourceType records a single DGraph type touched by a parent Event.
type EventResourceType struct {
	ent.Schema
}

func (EventResourceType) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("event_id", uuid.UUID{}),
		field.String("resource_type"),
	}
}

func (EventResourceType) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("resource_types").
			Field("event_id").
			Unique().
			Required(),
	}
}

func (EventResourceType) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("resource_type"),
		index.Fields("event_id", "resource_type").Unique(),
	}
}
