package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// Namespace holds the schema definition for the Namespace entity.
// Namespace is a tenancy boundary — one per data center, enforced by orbital's app layer.
// This record is orbital's operational record of the namespace; the graph node lives in DGraph.
type Namespace struct {
	ent.Schema
}

// Fields of the Namespace.
func (Namespace) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("name").Unique().NotEmpty(),
		field.String("dgraph_id").Optional(),
	}
}

func (Namespace) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

// Edges of the Namespace.
func (Namespace) Edges() []ent.Edge {
	return nil
}
