package schema

import (
	"encoding/json"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Event records a GraphQL mutation received by orbital.
type Event struct {
	ent.Schema
}

func (Event) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.JSON("operations", []string{}).Optional(),     // DGraph operation names found in query, e.g. ["updateServer"]
		field.JSON("resource_types", []string{}).Optional(), // all DGraph types touched, e.g. ["Server"]
		field.JSON("resource_ids", []string{}).Optional(),   // all orbIds touched, extracted from variables or inline filters
		field.String("actor"),                               // user name or email
		field.Time("timestamp").Default(time.Now),
		field.JSON("details", json.RawMessage{}).Optional(), // {operationName, query, variables}
	}
}

func (Event) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("timestamp"),
	}
}

func (Event) Edges() []ent.Edge {
	return nil
}
