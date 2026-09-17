package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
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

func (RestoreJob) Indexes() []ent.Index {
	return []ent.Index{
		// Conflict checks filter status IN (pending, running) on every job trigger.
		index.Fields("status"),
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
