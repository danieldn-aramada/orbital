package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ApprovalPolicy declares which changes require approval. Opt-in: with no
// matching enabled policy, writes behave exactly as they do today. Admin-managed.
//
// The selector is flattened into columns rather than a generic jsonb blob
// because v1 has exactly one action type and a queryable (namespace, type)
// pair is what resolution needs on every write. A second action type with a
// different selector shape adds its own columns or a jsonb selector then —
// not before there is a second one to design against.
type ApprovalPolicy struct {
	ent.Schema
}

func (ApprovalPolicy) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),

		// action_type matches ApprovalRequest.action_type (e.g. "config.mutation").
		field.String("action_type").NotEmpty(),

		// namespace + type_name are the config.mutation selector. type_name is
		// "" for "every type in this namespace" — an empty string rather than
		// NULL so the unique index below actually dedupes (Postgres treats NULLs
		// as distinct, so a nullable column would allow duplicate policies).
		//
		// Named type_name, not type, matching DivergenceEntry — `type` collides
		// with ent's generated identifiers.
		field.String("namespace").NotEmpty(),
		field.String("type_name").Optional().Default(""),

		field.Int("required_approvals").Default(1).Positive(),

		// bypass_roles are the roles that write straight through, recorded as a
		// privileged write. Bypass is a property of the POLICY, not a capability
		// on the user (D15): orbital's role model stays readonly < dev < admin
		// with no per-user flags, and "who may bypass" is a policy question the
		// admin answers per protected class.
		field.JSON("bypass_roles", []string{}).Default([]string{"admin"}),

		field.Bool("enabled").Default(true),
	}
}

func (ApprovalPolicy) Mixin() []ent.Mixin {
	return []ent.Mixin{
		AuditMixin{},
	}
}

func (ApprovalPolicy) Indexes() []ent.Index {
	return []ent.Index{
		// One policy per protected class.
		index.Fields("action_type", "namespace", "type_name").Unique(),
	}
}

func (ApprovalPolicy) Edges() []ent.Edge {
	return nil
}
