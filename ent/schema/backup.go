package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// Backup holds the schema definition for the Backup entity.
type Backup struct {
	ent.Schema
}

// Fields of the Backup.
func (Backup) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("bucket"),
		field.String("key"),
		field.String("endpoint"),
		field.Enum("status").Values("pending", "in_progress", "completed", "failed"),
		field.String("dgraph_instance").Optional(),
		field.String("checksum").Optional(),
		field.String("schema_version").Optional(),
		field.String("error").Optional(),
		field.Int64("size_bytes").Optional(),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (Backup) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

// Edges of the Backup.
func (Backup) Edges() []ent.Edge {
	return nil
}
