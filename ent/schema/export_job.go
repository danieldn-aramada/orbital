package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
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
		field.String("datacenter_id"),                           // DGraph internal ID
		field.String("datacenter_name"),                         // for display
		field.String("datacenter_orb_id").Optional().Nillable(), // canonical orbId
		field.Enum("status").Values("pending", "running", "completed", "failed", "stale"),
		field.String("artifact_path").Optional().Nillable(), // local zip path on completion
		field.String("error").Optional().Nillable(),
		field.Time("started_at").Optional().Nillable(),
		field.Time("completed_at").Optional().Nillable(),
		// ── Job lease (HA) ────────────────────────────────────────────────
		// locked_by is the runner that claimed this job, formatted
		// <hostname>_<uuid> — controller-runtime's lease-holder format. NEVER a
		// bare hostname: a pod that restarts in place reuses its name, so a
		// zombie from the previous process would match and the fence would
		// silently stop fencing. heartbeat_at is refreshed while the job runs;
		// the reaper treats a stale heartbeat as death. Both are nil for jobs
		// that predate this, which the orphan grace handles.
		field.String("locked_by").Optional().Nillable(),
		field.Time("heartbeat_at").Optional().Nillable(),
	}
}

func (ExportJob) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (ExportJob) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("registry_artifacts", RegistryArtifact.Type),
	}
}
