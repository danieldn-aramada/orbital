package schema

import (
	"github.com/google/uuid"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EventResource records a single orbId touched by a parent Event.
type EventResource struct {
	ent.Schema
}

func (EventResource) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("event_id", uuid.UUID{}),
		field.String("orb_id"),
	}
}

func (EventResource) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("event", Event.Type).
			Ref("resources").
			Field("event_id").
			Unique().
			Required(),
	}
}

func (EventResource) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("orb_id"),
		index.Fields("event_id", "orb_id").Unique(),
	}
}
