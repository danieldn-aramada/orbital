package schema

import (
	"encoding/json"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Event records a mutation applied to a config item.
type Event struct {
	ent.Schema
}

func (Event) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable(),
		field.String("resource_type").Immutable(),              // GraphQL type name: Server, DataCenter, etc.
		field.String("resource_id").Immutable(),                // orbId of the affected entity
		field.String("resource_name").Immutable(),              // denormalized name at event time
		field.Enum("type").Values("create", "update", "delete").Immutable(),
		field.String("actor").Immutable(),                      // user name or email
		field.Time("timestamp").Default(time.Now).Immutable(),
		field.String("message").Optional().Immutable(),
		field.JSON("details", json.RawMessage{}).Optional().Immutable(), // {"before":{...},"after":{...}}
	}
}

func (Event) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("resource_id"),
		index.Fields("resource_type", "timestamp"),
		index.Fields("timestamp"),
	}
}

func (Event) Edges() []ent.Edge {
	return nil
}
