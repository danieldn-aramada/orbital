package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// Orb holds the schema definition for the Orb entity.
type Orb struct {
	ent.Schema
}

// Fields of the Orb.
func (Orb) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("datacenter_id").NotEmpty(),
		field.Text("public_key").Optional(),
		field.Enum("status").Values("active", "revoked").Default("active"),
	}
}

func (Orb) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

// Edges of the Orb.
func (Orb) Edges() []ent.Edge {
	return nil
}
