package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// ExportJob tracks an async subgraph export operation.
type ExportJob struct {
	ent.Schema
}

func (ExportJob) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("datacenter_id"),              // DGraph internal ID
		field.String("datacenter_name"),             // for display
		field.Enum("status").Values("pending", "running", "completed", "failed"),
		field.String("artifact_path").Optional().Nillable(), // local zip path on completion
		field.String("error").Optional().Nillable(),
		field.Time("started_at").Optional().Nillable(),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (ExportJob) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (ExportJob) Edges() []ent.Edge {
	return nil
}
