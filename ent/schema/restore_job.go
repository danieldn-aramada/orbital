package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// RestoreJob tracks an async DGraph restore operation.
type RestoreJob struct {
	ent.Schema
}

func (RestoreJob) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Enum("status").Values("pending", "running", "completed", "failed"),
		field.UUID("backup_id", uuid.UUID{}).Optional().Nillable(),
		field.String("backup_key").Optional().Nillable(),
		field.String("log").Optional().Nillable(),
		field.String("error").Optional().Nillable(),
		field.Time("started_at").Optional().Nillable(),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (RestoreJob) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (RestoreJob) Edges() []ent.Edge {
	return nil
}
