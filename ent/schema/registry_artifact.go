package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// RegistryArtifact tracks an OCI artifact published from a subgraph export.
type RegistryArtifact struct {
	ent.Schema
}

func (RegistryArtifact) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("export_job_id", uuid.UUID{}),
		field.String("datacenter_id"),
		field.String("datacenter_name").Default(""),
		field.String("registry"),
		field.String("repository"),
		field.String("tag"),
		field.String("digest").Optional().Nillable(),
		field.Int64("size_bytes").Optional().Nillable(),
		field.Bool("signed").Default(false),
		field.String("signing_key_fingerprint").Optional().Nillable(),
		field.Enum("status").Values("pending", "pushing", "completed", "failed"),
		field.Int("initiated_by").Optional().Nillable(),
		field.Time("initiated_at"),
		field.Time("completed_at").Optional().Nillable(),
		field.String("error").Optional().Nillable(),
		field.Bool("enriched").Default(false),                   // true if all enrichers ran and their layers are included
		field.String("enricher_error").Optional().Nillable(),   // set if any enricher failed (job will also be failed)
	}
}

func (RegistryArtifact) Mixin() []ent.Mixin {
	return nil
}

func (RegistryArtifact) Edges() []ent.Edge {
	return nil
}
