package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// BackupSchedule stores the automatic backup schedule configuration.
// At most one row is expected; the in-process scheduler reads this on every tick.
type BackupSchedule struct {
	ent.Schema
}

func (BackupSchedule) Fields() []ent.Field {
	return []ent.Field{
		field.String("cron_spec"),                    // robfig/cron expression; e.g. "0 0 * * *" = daily at midnight
		field.String("timezone").Default("UTC"),      // IANA timezone applied to cron_spec; e.g. "America/Los_Angeles"
		field.Bool("enabled").Default(true),
		field.Time("last_triggered_at").Optional().Nillable(),
	}
}

func (BackupSchedule) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (BackupSchedule) Edges() []ent.Edge {
	return nil
}
