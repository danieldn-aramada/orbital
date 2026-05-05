package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
)

// AuditMixin adds created/updated tracking fields to any schema.
type AuditMixin struct {
	mixin.Schema
}

func (AuditMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Default(time.Now).Immutable(),
		field.String("created_by").Optional(),
		field.Time("updated_at").Optional().Nillable(),
		field.String("updated_by").Optional(),
	}
}
