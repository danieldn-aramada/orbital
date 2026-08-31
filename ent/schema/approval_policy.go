package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
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

		field.String("namespace").NotEmpty(),

		// AllTypes and Types are an either/or, and the CHECK constraint in
		// Annotations makes the other two combinations unrepresentable rather
		// than merely discouraged.
		//
		// A row that set both would say two contradictory things, and whichever
		// one the code chose to honour, the row itself would no longer describe
		// what is protected — unreadable from a backup, from psql during an
		// incident, or by the next person who writes `policy.Types` without
		// checking AllTypes. That is the same two-representations-of-one-fact
		// drift this feature has been bitten by repeatedly.
		//
		// AllTypes is not "every type that exists today": it matches ANY type,
		// so a ConfigItem added to the schema next month is covered the day it
		// lands. Enumerating today's types cannot express that.
		field.Bool("all_types").Default(true),
		field.JSON("types", []string{}).Optional(),

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
		// ONE policy per namespace. Types live in the row, so there is nothing
		// to compose and exactly one policy can ever govern a mutation — which
		// is what makes "why was this gated?" answerable with a single name.
		index.Fields("action_type", "namespace").Unique(),
	}
}

func (ApprovalPolicy) Edges() []ent.Edge {
	return nil
}

// Annotations puts the either/or in the DATABASE, not only in the API.
//
// The API validates it too, with a better message — but the API is one writer.
// A migration, a psql session, or a code path added next year are not, and a
// constraint here is the only layer none of them can skip. That is the
// difference between the rule holding today and holding in a year.
func (ApprovalPolicy) Annotations() []schema.Annotation {
	return []schema.Annotation{
		// jsonb null is a SCALAR, not an empty array, so jsonb_array_length()
		// raises rather than returning 0 on a row whose types was never set.
		// Treating any non-array as "no types" keeps the rule about scope
		// rather than about representation.
		entsql.Checks(map[string]string{
			"approval_policy_scope_exclusive": "(all_types AND (jsonb_typeof(types) <> 'array' OR jsonb_array_length(types) = 0)) OR ((NOT all_types) AND jsonb_typeof(types) = 'array' AND jsonb_array_length(types) > 0)",
		}),
	}
}
