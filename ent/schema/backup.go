package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// Backup tracks an async DGraph backup operation.
type Backup struct {
	ent.Schema
}

func (Backup) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.Enum("status").Values("pending", "running", "completed", "skipped", "failed"),
		field.Enum("trigger").Values("manual", "scheduled").Default("manual"),
		field.String("s3_bucket").Optional(),
		field.String("s3_key").Optional(),      // object key within the bucket
		field.String("s3_endpoint").Optional(), // custom endpoint; empty = AWS S3
		field.String("checksum").Optional(),    // SHA-256 of json.gz; used for dedup
		field.String("schema_version").Optional(),
		field.String("binary_version").Optional(),
		field.Int64("size_bytes").Optional().Nillable(),
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

func (Backup) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (Backup) Edges() []ent.Edge {
	return nil
}
