package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// PendingOverride is one field-level divergence orb is currently tracking.
// Snapshot semantics — the full set is replaced on each Store.Save() via
// DELETE + INSERT in one transaction.
type PendingOverride struct {
	ent.Schema
}

func (PendingOverride) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.String("type_name").Optional(),
		field.String("entry_orb_id"),
		field.String("field"),
		field.Text("intended_value").Optional(),
		field.Text("override_value").Optional(),
		field.String("who").Optional(),
		field.String("when_str").Optional(),
		field.Time("first_seen_at").Default(time.Now),
		field.Int("intended_at_version").Optional(),
	}
}

func (PendingOverride) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("entry_orb_id"),
	}
}

func (PendingOverride) Edges() []ent.Edge {
	return nil
}
